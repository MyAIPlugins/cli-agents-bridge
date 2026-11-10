//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// These tests run the REAL binary: the server as Claude Desktop starts it (its own
// process, `/` as working directory, a handful of environment variables, stdin and
// stdout carrying the protocol) and the CLI as a val uses it, against one sandbox data
// directory. They exist because a test that drives the handlers in-process cannot say
// anything about what a process boundary changes: the environment of the child, the
// descriptors, the exit on end of input, the lock a killed process leaves behind.

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// sandbox is one home, one data directory and one repository, shared by the processes
// of a test.
type sandbox struct {
	t    *testing.T
	bin  string
	home string
	data string // the BASE: what CAB_DATA_DIR is set to
	repo string
}

// state is where the binary keeps its sessions under the base (F-160): the tests
// that look at the disk read it, not data.
func (sb *sandbox) state() string { return filepath.Join(sb.data, config.LayoutDir) }

// newSandbox makes the repository by creating its `.git` directory and NOT by running
// `git init` (F-158). The binary under test never runs git: to find the project it only
// asks whether `.git` exists and is a directory (session.gitMarkerRoot, reached from
// FindProjectRoot; findingsRoot adds one more Lstat of the same entry), so the marker is
// all the tests need. A real `git init` made them depend on the machine that runs them:
// on a Mac where `~/.gitconfig` sets `trace2.eventtarget` to the git-ai daemon, every git
// process, whichever binary, tells the daemon, which creates `.git/ai/logs` a few
// milliseconds later. The test lasts 0.13 s, so the folder appeared while t.TempDir was
// removing the tree and the cleanup failed with "unlinkat .../.git: directory not empty",
// about 3 times in 60 full suites. Starting git with another HOME would have made it
// right today; not starting it cannot go wrong tomorrow, whatever the next hook of the
// next machine is.
func newSandbox(t *testing.T, bin string) *sandbox {
	t.Helper()
	root := t.TempDir()
	sb := &sandbox{t: t, bin: bin, home: filepath.Join(root, "home"), data: filepath.Join(root, "data"), repo: filepath.Join(root, "home", "repo")}
	require.NoError(t, os.MkdirAll(filepath.Join(sb.repo, ".git"), 0o755))
	return sb
}

// The two halves of the choice made in newSandbox, fixed so that a comment is not the
// only thing keeping `git init` out: the marker is enough for the binary to find the
// project, and nothing in the sandbox has ever been touched by a git process (git init
// fills `.git` with HEAD, config, hooks, objects; an empty one means none ran).
func TestNewSandbox_TheMarkerIsEnoughAndGitNeverRan(t *testing.T) {
	t.Parallel()
	sb := newSandbox(t, "")

	entries, err := os.ReadDir(filepath.Join(sb.repo, ".git"))
	require.NoError(t, err)
	assert.Empty(t, entries, "a git process has been here: it would tell the git-ai daemon (F-158)")

	root, err := session.FindProjectRoot(sb.repo, sb.home)
	require.NoError(t, err)
	assert.Equal(t, sb.repo, root, "the project root found from the repository is the repository")
}

// env is what Claude Desktop gives a server: five variables, no CAB_ ones but the data
// directory, which is how a sandbox is told apart from the real bridge.
func (sb *sandbox) env() []string {
	return []string{"HOME=" + sb.home, "CAB_DATA_DIR=" + sb.data, "PATH=/usr/bin:/bin", "USER=sandbox", "LOGNAME=sandbox", "SHELL=/bin/sh"}
}

// cli runs the command as a val would, from the repository, as session sid ("" for none).
func (sb *sandbox) cli(sid string, args ...string) (string, error) {
	sb.t.Helper()
	cmd := exec.Command(sb.bin, args...)
	cmd.Dir = sb.repo
	cmd.Env = sb.env()
	if sid != "" {
		cmd.Env = append(cmd.Env, "CAB_SESSION_ID="+sid)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

var joinedAs = regexp.MustCompile(`you are (\S+) \(([a-z0-9]{8})\)`)

// openVal joins a val to the repository and opens its bridge. It returns its session id.
func (sb *sandbox) openVal(name string) string {
	sb.t.Helper()
	out, err := sb.cli("", "join", "--role=val", "--agent-name="+name)
	require.NoError(sb.t, err, out)
	m := joinedAs.FindStringSubmatch(out)
	require.NotNil(sb.t, m, out)
	out, err = sb.cli(m[2], "desktop", "open")
	require.NoError(sb.t, err, out)
	return m[2]
}

// server is a running `cab-bridge mcp` and a client connected to it.
type server struct {
	cmd    *exec.Cmd
	client *mcpClient
	stderr *syncBuf
	done   chan struct{} // closed when the process has been waited for
	result error         // what Wait returned; valid once done is closed
}

func (sb *sandbox) startServer(clientName string) *server {
	sb.t.Helper()
	cmd := exec.Command(sb.bin, "mcp")
	cmd.Dir = "/" // as Desktop starts it
	cmd.Env = sb.env()
	stdin, err := cmd.StdinPipe()
	require.NoError(sb.t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(sb.t, err)
	errBuf := &syncBuf{}
	cmd.Stderr = errBuf
	require.NoError(sb.t, cmd.Start())

	s := &server{cmd: cmd, stderr: errBuf, done: make(chan struct{})}
	go func() {
		s.result = cmd.Wait()
		close(s.done)
	}()
	s.client = &mcpClient{t: sb.t, stdin: stdin, pending: map[int]chan rpcReply{}}
	go s.client.readLoop(stdout)
	sb.t.Cleanup(func() {
		_ = stdin.Close()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	s.client.initialize(clientName)
	return s
}

// waitExit waits for the process to end and returns its exit code.
func (s *server) waitExit(t *testing.T, within time.Duration) int {
	t.Helper()
	select {
	case <-s.done:
		if s.result == nil {
			return 0
		}
		var ee *exec.ExitError
		if assert.ErrorAs(t, s.result, &ee) {
			return ee.ExitCode()
		}
		return -1
	case <-time.After(within):
		t.Fatalf("the server did not exit within %s", within)
		return -1
	}
}

func (s *server) endInput() { _ = s.client.stdin.Close() }

// assertLogEventually waits for a line in the server's stderr: the pipe is copied by
// another goroutine, so the client can hold the response before the log line behind it
// has arrived in the buffer.
func assertLogEventually(t *testing.T, s *server, want string, msg string) {
	t.Helper()
	assert.Eventually(t, func() bool { return strings.Contains(s.stderr.String(), want) }, 3*time.Second, 20*time.Millisecond, msg)
}

var idInResult = regexp.MustCompile(`id (msg-[0-9a-f]{12}) \|`)

func firstID(t *testing.T, text string) string {
	t.Helper()
	m := idInResult.FindStringSubmatch(text)
	require.NotNil(t, m, "no message id in: %s", text)
	return m[1]
}

func peersJSON(t *testing.T, sb *sandbox) []map[string]any {
	t.Helper()
	out, err := sb.cli("", "peers", "--all-scopes", "--json")
	require.NoError(t, err, out)
	var peers []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &peers), out)
	return peers
}

func architectPeers(t *testing.T, sb *sandbox) []map[string]any {
	var out []map[string]any
	for _, p := range peersJSON(t, sb) {
		if p["role"] == "architect" {
			out = append(out, p)
		}
	}
	return out
}

// TestMCPWithTheRealBinary builds the binary once and runs the scenarios against it.
func TestMCPWithTheRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "cab-bridge")
	build := exec.Command("go", "build", "-o", bin, ".")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	t.Run("a whole conversation between a val and Desktop", func(t *testing.T) {
		sb := newSandbox(t, bin)
		valSID := sb.openVal("VAL-real")
		srv := sb.startServer("local-agent-mode-real")

		res := srv.client.call("join", map[string]any{"project": "repo"})
		require.False(t, res.IsError, res.Text())
		assert.Contains(t, res.Text(), "Joined repo as ARC-repo-cowork")

		// Desktop asks, the val answers from the CLI, Desktop reads the answer and acks it.
		res = srv.client.call("ask", map[string]any{"to": "VAL-real", "message": "QUESTION-ONE: is the plan sound?"})
		require.False(t, res.IsError, res.Text())
		assert.Contains(t, res.Text(), "open until they reply")
		out, err := sb.cli(valSID, "next")
		require.NoError(t, err, out)
		assert.Contains(t, out, "QUESTION-ONE")
		assert.Contains(t, out, "ARC-repo-cowork")
		out, err = sb.cli(valSID, "reply", "ANSWER-ONE: yes, with one change")
		require.NoError(t, err, out)

		res = srv.client.call("next", map[string]any{"wait_seconds": 2})
		require.False(t, res.IsError, res.Text())
		assert.Contains(t, res.Text(), "ANSWER-ONE")
		assert.Contains(t, res.Text(), "from VAL-real")
		responseID := firstID(t, res.Text())
		res = srv.client.call("ack", map[string]any{"ids": []string{responseID}})
		require.False(t, res.IsError, res.Text())
		assert.Contains(t, res.Text(), responseID+": archived")

		// The val asks, Desktop answers by id.
		out, err = sb.cli(valSID, "ask", "ARC-repo-cowork", "REVIEW-TWO: please look at the risks")
		require.NoError(t, err, out)
		res = srv.client.call("next", map[string]any{"wait_seconds": 3})
		require.False(t, res.IsError, res.Text())
		assert.Contains(t, res.Text(), "REVIEW-TWO")
		askID := firstID(t, res.Text())
		res = srv.client.call("reply", map[string]any{"ask_id": askID, "message": "RISKS-REPLY: R1 and R2 covered"})
		require.False(t, res.IsError, res.Text())
		assert.Contains(t, res.Text(), "Reply delivered to VAL-real")
		out, err = sb.cli(valSID, "next")
		require.NoError(t, err, out)
		assert.Contains(t, out, "RISKS-REPLY")
		assert.Contains(t, out, askID, "the response says which ask it closes")

		// The val is told, by the CLI's own account, that both asks were answered.
		out, err = sb.cli(valSID, "sent")
		require.NoError(t, err, out)
		rows := 0
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "msg-") {
				rows++
				assert.Contains(t, line, "archived", "everything the val sent was closed by Desktop: %s", line)
			}
		}
		assert.Equal(t, 2, rows)

		// What crossed the process boundary: only protocol on stdout, and no message
		// text in the log Desktop keeps.
		assert.Empty(t, srv.client.strayLines(), "stdout carries the protocol and nothing else")
		for _, text := range []string{"QUESTION-ONE", "ANSWER-ONE", "REVIEW-TWO", "RISKS-REPLY"} {
			assert.NotContains(t, srv.stderr.String(), text, "the server's log never carries a message")
		}

		// The end of the input is how Desktop quits: the server leaves at once and the
		// session says it is finished.
		srv.endInput()
		assert.Equal(t, 0, srv.waitExit(t, 3*time.Second))
		arcs := architectPeers(t, sb)
		require.Len(t, arcs, 1)
		assert.Equal(t, "done", arcs[0]["state"])
	})

	t.Run("what the command says on stderr stays on the machine", func(t *testing.T) {
		sb := newSandbox(t, bin)
		valSID := sb.openVal("VAL-real")
		srv := sb.startServer("local-agent-mode-real")
		require.False(t, srv.client.call("join", map[string]any{"project": "repo"}).IsError)

		// A success has no path in it: the echo carries names, an id and the start of the
		// text the caller wrote.
		res := srv.client.call("tell", map[string]any{"to": "VAL-real", "message": "a plain message"})
		require.False(t, res.IsError, res.Text())
		// The command does print "no next is listening" for this session, which has no
		// listener by design; it is on the stderr the server does not pass on.
		assert.NotContains(t, res.Text(), "no next is listening")
		assertLogEventually(t, srv, "stderr 1 lines", "it was there to be passed on: the note and nothing else, counted in the log")
		for _, machine := range []string{sb.data, sb.home, "/Users/"} {
			assert.NotContains(t, res.Raw, machine, "a successful result names nothing of this machine")
		}

		// The CRI2 reproduction: the command succeeds but complains, on stderr, about a
		// disk path (the sender's outbox is a file). The complaint must not arrive.
		arcs := architectPeers(t, sb)
		require.Len(t, arcs, 1)
		outbox := filepath.Join(sb.state(), "sessions", arcs[0]["sessionId"].(string), "outbox")
		require.NoError(t, os.RemoveAll(outbox))
		require.NoError(t, os.WriteFile(outbox, []byte("a file where a directory belongs"), 0o600))
		res = srv.client.call("tell", map[string]any{"to": "VAL-real", "message": "a second message, while the disk misbehaves"})
		require.False(t, res.IsError, res.Text())
		for _, leaked := range []string{sb.data, sb.home, "non-fatal", "outbox", "not a directory"} {
			assert.NotContains(t, res.Raw, leaked)
		}
		assertLogEventually(t, srv, "stderr 1 lines", "the log has the sizes")
		assert.NotContains(t, srv.stderr.String(), "non-fatal", "and not the command's sentence")

		// And the one warning a client needs is translated, from the real CLI.
		require.NoError(t, os.Remove(outbox))
		require.NoError(t, os.MkdirAll(outbox, 0o700))
		first := srv.client.call("ask", map[string]any{"to": "VAL-real", "message": "is anybody there?"})
		require.False(t, first.IsError, first.Text())
		assert.NotContains(t, first.Text(), duplicateNote)
		second := srv.client.call("ask", map[string]any{"to": "VAL-real", "message": "is anybody there?"})
		require.False(t, second.IsError, second.Text())
		assert.Contains(t, second.Text(), duplicateNote)
		assert.NotContains(t, second.Raw, "double-invoke", "the CLI's sentence is not forwarded, the server's note is")
		_ = valSID
	})

	t.Run("the server never reads as a listener", func(t *testing.T) {
		sb := newSandbox(t, bin)
		sb.openVal("VAL-real")
		srv := sb.startServer("claude-ai")
		require.False(t, srv.client.call("join", map[string]any{"project": "repo"}).IsError)
		require.False(t, srv.client.call("next", map[string]any{"wait_seconds": 0}).IsError)
		arcs := architectPeers(t, sb)
		require.Len(t, arcs, 1)
		assert.NotEqual(t, true, arcs[0]["listening"], "join and next claim no listener")
		assert.Equal(t, "ARC-repo-chat", arcs[0]["agentName"])
	})

	t.Run("a killed owner leaves the identity free and its session to be resumed", func(t *testing.T) {
		sb := newSandbox(t, bin)
		sb.openVal("VAL-real")
		first := sb.startServer("local-agent-mode-real")
		require.False(t, first.client.call("join", map[string]any{"project": "repo"}).IsError)
		before := architectPeers(t, sb)
		require.Len(t, before, 1)

		// A second process on the same identity is refused while the first lives.
		second := sb.startServer("local-agent-mode-real")
		res := second.client.call("join", map[string]any{"project": "repo"})
		require.True(t, res.IsError)
		assert.Contains(t, res.Text(), "already speaking as this identity")
		assert.Contains(t, res.Text(), "pid")

		require.NoError(t, first.cmd.Process.Signal(syscall.SIGKILL))
		first.waitExit(t, 3*time.Second)

		// The kernel dropped the lock with the process: the second one can now take
		// the identity, and it is the same session.
		res = second.client.call("join", map[string]any{"project": "repo"})
		require.False(t, res.IsError, res.Text())
		after := architectPeers(t, sb)
		require.Len(t, after, 1, "resumed, not a second session")
		assert.Equal(t, before[0]["sessionId"], after[0]["sessionId"])
		assert.Equal(t, before[0]["agentName"], after[0]["agentName"])
	})

	t.Run("a termination signal ends it tidily", func(t *testing.T) {
		sb := newSandbox(t, bin)
		sb.openVal("VAL-real")
		srv := sb.startServer("local-agent-mode-real")
		require.False(t, srv.client.call("join", map[string]any{"project": "repo"}).IsError)

		require.NoError(t, srv.cmd.Process.Signal(syscall.SIGTERM))
		assert.Equal(t, 0, srv.waitExit(t, 3*time.Second))
		arcs := architectPeers(t, sb)
		require.Len(t, arcs, 1)
		assert.Equal(t, "done", arcs[0]["state"])

		next := sb.startServer("local-agent-mode-real")
		assert.False(t, next.client.call("join", map[string]any{"project": "repo"}).IsError, "and the identity was given up")
	})

	t.Run("the val closing the bridge closes Desktop's doors at once", func(t *testing.T) {
		sb := newSandbox(t, bin)
		valSID := sb.openVal("VAL-real")
		srv := sb.startServer("local-agent-mode-real")
		require.False(t, srv.client.call("join", map[string]any{"project": "repo"}).IsError)
		out, err := sb.cli(valSID, "tell", "ARC-repo-cowork", "BEFORE-CLOSE: seen while open")
		require.NoError(t, err, out)
		res := srv.client.call("next", map[string]any{"wait_seconds": 2})
		require.Contains(t, res.Text(), "BEFORE-CLOSE")

		out, err = sb.cli(valSID, "desktop", "close")
		require.NoError(t, err, out)

		res = srv.client.call("next", map[string]any{"wait_seconds": 0})
		require.False(t, res.IsError, res.Text())
		assert.NotContains(t, res.Raw, "BEFORE-CLOSE", "not even the pointer to what it saw before")
		assert.Contains(t, res.Text(), "No val of this project has the bridge open")
		assert.Contains(t, res.Text(), "author-not-open")
		res = srv.client.call("tell", map[string]any{"to": "VAL-real", "message": "AFTER-CLOSE"})
		require.True(t, res.IsError, "and nothing can be sent")
		out, err = sb.cli(valSID, "inbox", "--list")
		require.NoError(t, err, out)
		assert.NotContains(t, out, "AFTER-CLOSE", "and what was refused was never sent")
	})

	t.Run("without a home directory the server refuses to start and leaves nothing behind", func(t *testing.T) {
		cwd := t.TempDir()
		cmd := exec.Command(bin, "mcp")
		cmd.Dir = cwd
		cmd.Env = []string{} // env -i
		var stderr syncBuf
		cmd.Stderr = &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		require.ErrorAs(t, err, &ee)
		assert.Equal(t, 1, ee.ExitCode())
		assert.Contains(t, stderr.String(), "no home directory")
		entries, rerr := os.ReadDir(cwd)
		require.NoError(t, rerr)
		assert.Empty(t, entries, "no private bridge was built in the working directory (F-145)")

		cmd = exec.Command(bin, "mcp")
		cmd.Dir = cwd
		cmd.Env = []string{"HOME=" + cwd, "CAB_DATA_DIR=relative/dir"}
		stderr = syncBuf{}
		cmd.Stderr = &stderr
		err = cmd.Run()
		require.ErrorAs(t, err, &ee)
		assert.Contains(t, stderr.String(), "not an absolute path")
		entries, rerr = os.ReadDir(cwd)
		require.NoError(t, rerr)
		assert.Empty(t, entries)
	})

	t.Run("a limit the relay cannot honour stops it before it serves", func(t *testing.T) {
		sb := newSandbox(t, bin)
		cmd := exec.Command(bin, "mcp")
		cmd.Dir = "/"
		cmd.Env = append(sb.env(), "CAB_MCP_NEXT_MAX_WAIT_SECONDS=100")
		var stderr syncBuf
		cmd.Stderr = &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		require.ErrorAs(t, err, &ee)
		assert.Equal(t, 1, ee.ExitCode())
		assert.Contains(t, stderr.String(), "not starting")
		assert.Contains(t, stderr.String(), "gives up on a tool call after 60 s")
	})

	t.Run("arguments are refused and the usage names the connector", func(t *testing.T) {
		out, err := exec.Command(bin, "mcp", "--bogus").CombinedOutput()
		require.Error(t, err)
		assert.Contains(t, string(out), "takes no arguments")
		help, _ := exec.Command(bin, "help").CombinedOutput()
		assert.True(t, strings.Contains(string(help), "mcp "), "the usage lists the subcommand")
	})
}
