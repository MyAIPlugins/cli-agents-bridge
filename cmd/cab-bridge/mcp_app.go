package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/mcp"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The state of one `cab-bridge mcp` process (F-72). Claude Desktop starts one per
// surface (chat, Cowork) at app launch, and each serves every project its val has
// opened: install once, no per-repository configuration.
//
// What is held here is deliberately small. Who may be read, and what a tool shows,
// is decided from the disk on every call (mcp_authorize.go); this keeps only what
// has to live as long as the process: the identities it joined, the lock that
// makes each of them exclusive, and the heartbeat that says the session is alive.

// The two surfaces Claude Desktop is measured to start a server for (F-72 spike).
const (
	surfaceChat   = "chat"
	surfaceCowork = "cowork"

	clientNameChat        = "claude-ai"
	clientPrefixCowork    = "local-agent-mode"
	virtualProjectDirName = ".cab-desktop"
	architectNamePrefix   = "ARC"
	scopeTagBytes         = 2 // hex-encoded: 4 characters
)

// surfaceOf names the surface from the client's own account of itself. An unknown
// client is refused instead of guessed: a surface is part of an identity, and a
// session registered under the wrong one is a session nobody can find again.
func surfaceOf(clientName string) (string, error) {
	switch {
	case clientName == clientNameChat:
		return surfaceChat, nil
	case strings.HasPrefix(clientName, clientPrefixCowork):
		return surfaceCowork, nil
	}
	return "", fmt.Errorf("this client calls itself %q, which this bridge does not know how to tell apart: it knows Claude Desktop's chat (%q) and Cowork (%q...)",
		clientName, clientNameChat, clientPrefixCowork)
}

// mcpProject is one identity this process speaks as: an architect session in one
// project, through one surface.
type mcpProject struct {
	scope   string
	surface string
	label   string
	sid     string
	name    string

	lock      *session.IdentityLock
	stopBeats context.CancelFunc
	beatsDone <-chan struct{}

	// mu orders ack and reply of THIS project against each other. It is never held
	// while waiting for anything (next holds nothing) and never across projects.
	mu      sync.Mutex
	guard   scanGuard
	planner *mcp.Planner
}

type mcpApp struct {
	ctx  context.Context // the life of the process: the heartbeats run on it
	cfg  config.Config
	mgr  *session.Manager
	exe  string // this binary, to run the sending verbs as a child
	home string
	logf func(format string, args ...any)

	// pollInterval is how often a waiting `next` looks at the inbox again.
	pollInterval time.Duration

	joinMu   sync.Mutex // one join at a time: the identity lock is per open file
	mu       sync.Mutex // guards projects
	projects map[string]*mcpProject
}

// nextPollInterval is the production polling period of a waiting `next`: short
// enough that mail is handed over within a second, long enough that a forty-second
// wait reads the inbox forty times and not four thousand.
const nextPollInterval = time.Second

func newMCPApp(ctx context.Context, cfg config.Config, mgr *session.Manager, exe, home string, logf func(string, ...any)) *mcpApp {
	return &mcpApp{ctx: ctx, cfg: cfg, mgr: mgr, exe: exe, home: home, logf: logf, pollInterval: nextPollInterval, projects: map[string]*mcpProject{}}
}

func projectKey(scope, surface string) string { return scope + "\x00" + surface }

func (a *mcpApp) lookup(key string) *mcpProject {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.projects[key]
}

func (a *mcpApp) joinedProjects() []*mcpProject {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]*mcpProject, 0, len(a.projects))
	for _, p := range a.projects {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label < out[j].label })
	return out
}

var errNoProjectJoined = errors.New("no project is joined in this conversation yet: call `join` first (without arguments it lists the projects that are open to you)")

// projectFor picks the project a tool call is about: the one named, or the only one
// joined. With several joined and none named it asks, instead of choosing.
func (a *mcpApp) projectFor(named string) (*mcpProject, error) {
	all := a.joinedProjects()
	if len(all) == 0 {
		return nil, errNoProjectJoined
	}
	if named == "" {
		if len(all) == 1 {
			return all[0], nil
		}
		labels := make([]string, len(all))
		for i, p := range all {
			labels[i] = p.label
		}
		return nil, fmt.Errorf("several projects are joined (%s): say which one with the `project` argument", strings.Join(labels, ", "))
	}
	for _, p := range all {
		if p.label == named || p.scope == named {
			return p, nil
		}
	}
	return nil, fmt.Errorf("project %q is not joined in this conversation: call `join` with it first", named)
}

// sessionManifests reads every manifest in the data dir. A session that cannot be
// read is skipped: this is discovery, and one damaged directory must not hide the
// rest. Authorization never goes through here; it reads the one session it judges.
func (a *mcpApp) sessionManifests() ([]*session.Manifest, error) {
	root := filepath.Join(a.cfg.DataDir, "sessions")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the sessions directory: %w", err)
	}
	var out []*session.Manifest
	for _, e := range entries {
		if !e.IsDir() || security.ValidateSessionID(e.Name()) != nil {
			continue
		}
		mf, lerr := a.mgr.LoadManifest(e.Name())
		if lerr != nil {
			continue
		}
		out = append(out, mf)
	}
	return out, nil
}

// openVal is a val whose bridge is open to Claude Desktop at this instant.
type openVal struct {
	sid     string
	name    string
	team    string
	expires time.Time
}

// openProject is a project with at least one open val.
type openProject struct {
	scope string
	label string
	vals  []openVal
}

// openProjects lists the projects Claude Desktop may join: those where some val holds
// a grant that is valid NOW. A project whose vals are all closed is not named at all,
// so the list does not tell Desktop what else lives in the data dir.
func (a *mcpApp) openProjects() ([]openProject, error) {
	manifests, err := a.sessionManifests()
	if err != nil {
		return nil, err
	}
	now := a.mgr.NowUTC()
	byScope := map[string]*openProject{}
	for _, mf := range manifests {
		if mf.Role != session.RoleVal {
			continue
		}
		g, found, gerr := a.mgr.ReadDesktopGrant(mf.SessionID)
		if gerr != nil || !found || !g.ValidAt(now) {
			continue
		}
		scope := session.EffectiveScope(mf)
		if scope == "" {
			continue
		}
		p := byScope[scope]
		if p == nil {
			p = &openProject{scope: scope}
			byScope[scope] = p
		}
		p.vals = append(p.vals, openVal{sid: mf.SessionID, name: mf.AgentName, team: mf.TeamID, expires: g.ExpiresAt})
	}
	scopes := make([]string, 0, len(byScope))
	for s := range byScope {
		scopes = append(scopes, s)
	}
	sort.Strings(scopes)
	labels := scopeLabels(scopes)
	out := make([]openProject, 0, len(scopes))
	for _, s := range scopes {
		p := byScope[s]
		p.label = labels[s]
		sort.Slice(p.vals, func(i, j int) bool { return p.vals[i].name < p.vals[j].name })
		out = append(out, *p)
	}
	return out, nil
}

// grantingVals is the closed list a message may be addressed to: the open vals of one
// project, judged now.
func (a *mcpApp) grantingVals(scope string) ([]openVal, error) {
	open, err := a.openProjects()
	if err != nil {
		return nil, err
	}
	for _, p := range open {
		if session.SameProject(p.scope, scope) {
			return p.vals, nil
		}
	}
	return nil, nil
}

// scopeTag is four hex characters that tell two projects of the same name apart.
func scopeTag(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:scopeTagBytes])
}

// close ends every identity this process holds: the sessions are marked done (only
// if they are still this process's), the heartbeats stop, and the locks are given
// up last, so nobody can take an identity over while it still looks alive.
func (a *mcpApp) close() {
	for _, p := range a.joinedProjects() {
		p.stopBeats()
		<-p.beatsDone
		if mf, err := a.mgr.LoadManifest(p.sid); err == nil && mf.PID == os.Getpid() {
			if serr := a.mgr.SetState(p.sid, session.StateDone); serr != nil {
				a.logf("could not mark session done on exit: %v", serr)
			}
		}
		if rerr := p.lock.Release(); rerr != nil {
			a.logf("could not release an identity lock on exit: %v", rerr)
		}
	}
}
