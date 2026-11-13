package cleanup

// F-177: a retirement and `inbox --tidy` of the same session used to cross. Tidy moved a message
// out of the inbox in the middle of the retirement's archive step, between the listing and the
// rename, and the retirement then failed with "no such file" (or, on the other order, tidy
// recreated processed/ in a folder that was being removed). Both are the same missing thing: tidy
// did not take the lock of the session it moves files in.
//
// The retirement takes the session lock with no wait and keeps it to the end of its section
// (retire_contention_test.go), so a tidy that takes the same lock has to find it held. Here the
// tidy is the REAL one, in another process: it is run by the test binary of cmd/cab-bridge, built
// on the fly, which carries the child (TestHelperProcess_F177Tidy in cmd/cab-bridge). The two
// tests share the names of its environment, written out here because the packages cannot import
// each other's tests.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

const (
	f177Bound   = 120 * time.Second // compiling the binary of the child is the slow part
	f177Child   = 20 * time.Second
	f177WaitMs  = "300"
	f177Message = "msg-aaaaaaaaaaaa"
)

// buildTidyChild compiles the test binary of cmd/cab-bridge, the one that carries the child.
func buildTidyChild(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	helper := filepath.Join(t.TempDir(), "cabcmd.test")
	ctx, cancel := context.WithTimeout(context.Background(), f177Bound)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "test", "-c", "-o", helper, "./cmd/cab-bridge")
	build.Dir = root
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building the child binary: %s", out)
	return helper
}

func TestRetirement_ARealTidyOfTheSessionCannotCrossIt(t *testing.T) {
	helper := buildTidyChild(t)
	const sid = "aaaaaa0f"
	data := t.TempDir()
	plantStale(t, data, sid)
	m := &message.Message{
		ID: f177Message, SchemaVersion: message.SchemaVersionV2, From: "valpeer1", To: sid,
		FromRole: session.RoleVal, ToRole: session.RoleEsc, Type: message.TypeNotify,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Status: message.StatusPending, Content: "a shown tell",
	}
	body, err := message.EncodeStrict(m, 65536)
	require.NoError(t, err)
	inbox := sessionPath(data, sid, "inbox", f177Message+".json")
	writeFileIn(t, inbox, string(body))
	_, err = session.NewManager(data, time.Second).CommitWakeCursor(sid, []string{f177Message}, time.Now().UTC(), nil, nil)
	require.NoError(t, err)

	fired := false
	var childOut []byte
	rename := func(src, dst string) error {
		if !fired {
			// The first rename of the archive step: the retirement holds the lock and is in the
			// middle of moving the inbox. The tidy runs HERE, to its end, before the rename goes on.
			fired = true
			ctx, cancel := context.WithTimeout(context.Background(), f177Child)
			defer cancel()
			child := exec.CommandContext(ctx, helper, "-test.run=^TestHelperProcess_F177Tidy$", "-test.v")
			child.Env = append(os.Environ(),
				"CAB_F177_CHILD=tidy", "CAB_F177_DATA="+data, "CAB_F177_SID="+sid, "CAB_F177_WAIT_MS="+f177WaitMs)
			out, runErr := child.CombinedOutput()
			require.NoError(t, runErr, "the child did not run: %s", out)
			childOut = out
		}
		return transportfs.RenameAtomic(src, dst)
	}

	err = retireWithSeams(data, sid, fixedNow, retireSeams{rename: rename})

	require.True(t, fired, "premise: the retirement reached its first rename")
	assert.Contains(t, string(childOut), "F177CHILD moved=0 busy=true", "the tidy found the lock of the retiring session held: %s", childOut)
	require.NoError(t, err, "the retirement was not crossed by anything")
	assert.NoDirExists(t, sessionPath(data, sid), "the session is retired")
	archived := filepath.Join(data, "archive", fixedNow().UTC().Format("2006-01-02"), sid, "inbox", f177Message+".json")
	assert.FileExists(t, archived, "the message went where the retirement sends it, not where a crossing tidy would")
}
