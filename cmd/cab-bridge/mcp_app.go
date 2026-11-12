package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	scopeTagChars         = 4  // hex characters of the scope's hash that tell two projects of one name apart
	maxScopeTagChars      = 64 // all of a sha256 in hex: the bound of the search for a tag that is long enough
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
	return "", mcp.Refusef("this client calls itself %q, which this bridge does not know how to tell apart: it knows Claude Desktop's chat (%q) and Cowork (%q...)",
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

	// pollInterval is how often a waiting `next` looks at the inbox again: the same
	// poll_interval_ms the CLI's own wait uses, so there is one setting for it.
	pollInterval time.Duration

	// afterBeginReply is a test seam: it runs in `reply` between the moment the call holds its
	// copy of the journal and the moment it drives it, which is where another driver can
	// overtake it (F-176). Nil in production.
	afterBeginReply func(*session.ReplyTxn)

	joinMu   sync.Mutex // one join at a time: the identity lock is per open file
	mu       sync.Mutex // guards projects
	projects map[string]*mcpProject
}

func newMCPApp(ctx context.Context, cfg config.Config, mgr *session.Manager, exe, home string, logf func(string, ...any)) *mcpApp {
	return &mcpApp{ctx: ctx, cfg: cfg, mgr: mgr, exe: exe, home: home, logf: logf, pollInterval: time.Duration(cfg.PollIntervalMs) * time.Millisecond, projects: map[string]*mcpProject{}}
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

// A join, the identity it takes and the inbox behind it belong to the SURFACE of Claude
// Desktop (chat, Cowork), not to a conversation: Desktop starts one server process per
// surface when the app launches and every conversation of that surface talks to it. The
// server cannot tell the conversations apart, so a text to the model never speaks of
// "this conversation" as if the server remembered anything of it.
const sharedBySurface = "Every conversation open on the same Claude Desktop surface (chat, or Cowork) shares the joined projects and the inbox: an `ack` or a `reply` in one removes the message for all of them."

var errNoProjectJoined = mcp.Refuse("no project is joined on this Claude Desktop surface yet (chat and Cowork each have their own, shared by all their conversations): call `join` first (without arguments it lists the projects that are open to you)")

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
		return nil, mcp.Refusef("several projects are joined (%s): say which one with the `project` argument", strings.Join(labels, ", "))
	}
	for _, p := range all {
		if p.label == named || p.scope == named {
			return p, nil
		}
	}
	if p, err := a.joinedUnderTheListsName(all, named); p != nil || err != nil {
		return p, err
	}
	return nil, mcp.Refusef("project %q is not joined on this Claude Desktop surface (a join is shared by all its conversations): call `join` with it first", named)
}

// joinedUnderTheListsName finds a joined project by the label `join` prints for it NOW,
// which is not always the one it was joined under: a second project of the same
// basename that has opened since turns "demo" into "demo-3fa2" in the list. The name
// the list prints must reach the project it names.
func (a *mcpApp) joinedUnderTheListsName(joined []*mcpProject, named string) (*mcpProject, error) {
	open, err := a.openProjects()
	if err != nil {
		return nil, err
	}
	op, ok := pickProject(open, named)
	if !ok {
		return nil, nil
	}
	for _, p := range joined {
		if session.SameProject(p.scope, op.scope) {
			return p, nil
		}
	}
	return nil, nil
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
	// The projects already joined take part in the naming even when their bridge is
	// closed now: a label must not be one that a joined project already answers to.
	named := append([]string(nil), scopes...)
	for _, p := range a.joinedProjects() {
		named = append(named, p.scope)
	}
	labels, err := projectLabels(named)
	if err != nil {
		return nil, err
	}
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
func scopeTag(scope string) string { return scopeTagOf(scope, scopeTagChars) }

// scopeTagOf is the first chars hex characters of the hash of the scope.
func scopeTagOf(scope string, chars int) string {
	sum := sha256.Sum256([]byte(scope))
	return hex.EncodeToString(sum[:])[:chars]
}

// projectLabels names each project for the model. A project is its basename; two that
// share one are the basename with a tag of the hash of the path (`demo-3fa2`), so the
// model never reads a path, which can hold a user's or a client's name. A name it is
// given is a name it can pass back, so the labels must be distinct: the tag grows until
// they are (two paths can share four hex characters, or a tagged label can be another
// project's basename), up to the whole hash.
func projectLabels(scopes []string) (map[string]string, error) {
	for chars := scopeTagChars; chars <= maxScopeTagChars; chars++ {
		labels := labelScopes(scopes, func(scope string) string {
			return filepath.Base(scope) + "-" + scopeTagOf(scope, chars)
		})
		if distinctLabels(labels) {
			return labels, nil
		}
	}
	return nil, fmt.Errorf("could not give %d projects distinct names", len(scopes))
}

func distinctLabels(labels map[string]string) bool {
	seen := make(map[string]bool, len(labels))
	for _, l := range labels {
		if seen[l] {
			return false
		}
		seen[l] = true
	}
	return true
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
