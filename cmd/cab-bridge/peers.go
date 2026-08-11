package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	"github.com/myAIPlugins/cli-agents-bridge/internal/shellarg"
)

type peerSummary struct {
	SessionID   string `json:"sessionId"`
	Role        string `json:"role"`
	AgentName   string `json:"agentName"`
	ProjectName string `json:"projectName"`
	// PID is the LISTENER's process — the one that actually holds this session's
	// wait — and 0 when nobody is listening.
	//
	// It used to be the manifest's PID, i.e. whichever process last touched the
	// file. For a one-shot command that process is dead by definition the moment
	// it returns, so `kill -0` on that number reported a perfectly healthy agent
	// as gone: the column answered a third question nobody asks ("who wrote this
	// manifest last") with a number that misleads on the two they do ask, "is it
	// alive" and "what do I signal". The manifest PID is still in `inspect`,
	// where a forensic detail belongs and nobody reads it in a hurry.
	PID           int       `json:"pid"`
	LastHeartbeat time.Time `json:"lastHeartbeat"`
	Stale         bool      `json:"stale"`
	// Listening reports whether a waiter is alive on this session RIGHT NOW.
	// nil means it never listened at all, which is not the same as "no" — a val
	// that orchestrates legitimately has no waiter between two messages, and a
	// column that flattened the two would read as a fault on a normal state.
	//
	// Deliberately NOT folded into Stale (F-112). Stale means "abandoned" and has
	// four consumers with real consequences — cleanup's sweep, the stale-namesake
	// takeover, by-name routing, betterOccupant — plus the orchestrating
	// exemption of F-23a, which exists precisely to keep a val that is working a
	// gate from being marked dead. "Not listening right now" is a fresher,
	// narrower fact and gets its own column.
	//
	// Same source as overview's `listener:` line (ListenerOwner.Listening), which
	// is the whole point: two commands answering the same question independently
	// is what let them disagree in the same instant.
	Listening *bool `json:"listening,omitempty"`
	// InboxCount is how many messages this peer has NOT been shown yet — the
	// UNREAD count, not the file count.
	//
	// The two used to be the same number, back when being shown a message also
	// moved it out of inbox/. Under the mailbox model `next` leaves the file
	// where it is, so counting files answers "how much mail is sitting there",
	// while the question this column exists for is "does this peer have work
	// waiting?". Two already-read, already-answered messages read as two waiting,
	// which is the one direction an orchestrator must not be wrong in.
	//
	// The full file count stays available in `inbox --list`, which is inspection.
	InboxCount        int    `json:"inboxCount"`
	LastConsumedMsgID string `json:"lastConsumedMsgId,omitempty"`
	// TeamID is the F-5 isolation label. Empty (omitted) for sessions
	// registered without --team. peers --team=<x> filters on it.
	TeamID string `json:"teamId,omitempty"`
	// Scope is the F-17 auto-derived project-root path. peers filters on it by
	// default (the cwd's scope); empty (omitted) for legacy/pre-F-17 sessions,
	// which are therefore hidden by the default filter and shown only with
	// --all-scopes.
	Scope string `json:"scope,omitempty"`
	// State is the F-23a agent task-state (idle/working/done/orchestrating).
	// Empty (omitted) for legacy/pre-F-23 or never-set sessions. State
	// "orchestrating" makes Stale always false (session.IsStale).
	State string `json:"state,omitempty"`
}

func runPeers(args []string) error {
	fs := flag.NewFlagSet("peers", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON array on stdout (default: human tabwriter)")
	includeStale := fs.Bool("include-stale", true, "include sessions whose lastHeartbeat exceeds StaleSeconds")
	team := fs.String("team", "", "show only sessions whose teamId matches (F-5 isolation); default: all sessions")
	allScopes := fs.Bool("all-scopes", false, "show every scope (disable the F-17 default filter to the cwd's project root)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	// F-17 default isolation: filter to the cwd's project-root scope so a fresh
	// session sees only its own project's pair. --all-scopes and --team are
	// explicit cross-scope views, so either one disables the scope filter (H3):
	// --team keeps its existing global-by-teamId semantics, --all-scopes shows
	// everything. Scope detection failure is non-fatal (resolveScope logs and
	// returns "" -> show all).
	scopeFilter := ""
	if !*allScopes && *team == "" {
		cwd, werr := os.Getwd()
		if werr != nil {
			fmt.Fprintf(os.Stderr, "cab-bridge: cannot resolve cwd for scope filter (non-fatal): %v — showing all scopes\n", werr)
		} else {
			scopeFilter = resolveScope(cwd)
		}
	}

	peers, hiddenByScope, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, cfg.MaxMessageBytes, *includeStale, *team, scopeFilter)
	if err != nil {
		return err
	}

	// Anti-silent-cap (project no-silent-truncation discipline): when the default
	// scope filter hid sessions, say so on stderr so the user never mistakes a
	// filtered list for the whole picture. stdout (table/JSON) stays clean for
	// scripts.
	if hiddenByScope > 0 {
		fmt.Fprintf(os.Stderr, "cab-bridge: %d session(s) in other scopes hidden — use --all-scopes to show\n", hiddenByScope)
	}

	if *asJSON {
		out, err := json.MarshalIndent(peers, "", "  ")
		if err != nil {
			return fmt.Errorf("peers: marshal: %w", err)
		}
		fmt.Println(string(out))
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	// UNREAD, not INBOX: the header names what the number IS. "INBOX" over a
	// count that no longer means "files in the inbox" is a second way of saying
	// the wrong thing, and costs nothing to fix.
	fmt.Fprintln(tw, "SESSION_ID\tROLE\tSTATE\tAGENT_NAME\tPROJECT\tTEAM\tPID\tLISTENING\tHEARTBEAT_AGE\tSTALE\tUNREAD\tLAST_CONSUMED\tSCOPE")
	scopeLabels := scopeColumn(peers)
	now := time.Now().UTC()
	for _, p := range peers {
		age := now.Sub(p.LastHeartbeat).Truncate(time.Second)
		stale := "ok"
		if p.Stale {
			stale = "STALE"
		}
		lastConsumed := p.LastConsumedMsgID
		if lastConsumed == "" {
			lastConsumed = "-"
		}
		teamCol := p.TeamID
		if teamCol == "" {
			teamCol = "-"
		}
		// Quoted only where it must be: shellarg.Quote returns an ordinary label
		// untouched, so on every normal row this column reads exactly as before.
		// The one that would not survive a paste — a space, an apostrophe — comes
		// out ready to use instead of merely readable.
		//
		// The HUMAN column is also the place people copy from, which is the whole
		// of F-124; `peers --json` keeps `scope` raw, so anything parsing uses
		// that. The renderer belongs to display and remediation, never to
		// matching, JSON or storage.
		scopeCol := shellarg.Quote(scopeLabels[p.Scope])
		stateCol := p.State
		if stateCol == "" {
			stateCol = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			p.SessionID, p.Role, stateCol, p.AgentName, p.ProjectName, teamCol,
			listenerPIDCol(p.PID), listeningCol(p.Listening), age, stale, p.InboxCount, lastConsumed, scopeCol)
	}
	return tw.Flush()
}

// scopeColumn decides how to render each scope in the table: the BASENAME,
// which is the name of the repository and the only part anybody reads.
//
// F-116. `--all-scopes` listed sessions from other projects with a column full
// of absolute paths, so the boundary was there and unreadable — the list was
// wider than reachability and said so nowhere. The basename is what identifies
// the group; the full path is forensic and now lives in `inspect` and --json.
//
// An abbreviation that is AMBIGUOUS is worse than the long form, so two distinct
// scopes sharing a basename (two checkouts of one repo) both fall back to their
// full path. The shortening happens only where it cannot mislead — decided over
// the whole list, not per row, because a column that shortens some rows and not
// others is still readable, while one that shows the same label for two
// different places is not.
func scopeColumn(peers []peerSummary) map[string]string {
	scopes := make([]string, 0, len(peers))
	for _, p := range peers {
		scopes = append(scopes, p.Scope)
	}
	labels := scopeLabels(scopes)
	labels[""] = "-"
	return labels
}

// scopeLabels is that rule on its own, so the one place that shortens a scope is
// the one place that decides when it may not.
//
// It exists because the FIRST version of the qualified-address errors wrote its
// own shortening — plain basenames — and handed back `VAL-same@twin` twice for
// two different projects: two tokens, neither of which resolves, offered as the
// way out of an ambiguity (CRI diff-gate P1-2). The remediation was the trap,
// again, three lines under a comment saying it must not be.
func scopeLabels(scopes []string) map[string]string {
	byBase := map[string]map[string]bool{}
	for _, s := range scopes {
		if s == "" {
			continue
		}
		base := filepath.Base(s)
		if byBase[base] == nil {
			byBase[base] = map[string]bool{}
		}
		byBase[base][s] = true
	}

	labels := map[string]string{}
	for _, s := range scopes {
		if s == "" {
			continue
		}
		base := filepath.Base(s)
		if len(byBase[base]) > 1 {
			labels[s] = s // ambiguous within this set: only the whole path resolves
			continue
		}
		labels[s] = base
	}
	return labels
}

// listeningCol renders the three answers the column can give. `-` and `no` are
// kept apart on purpose: `no` on a val that orchestrates would read as a fault
// on a state that is normal by design.
func listeningCol(listening *bool) string {
	switch {
	case listening == nil:
		return "-"
	case *listening:
		return "yes"
	default:
		return "no"
	}
}

// listenerPIDCol prints the PID only when there is a process to signal. A zero
// rendered as "0" is a number somebody would try to kill.
func listenerPIDCol(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return strconv.Itoa(pid)
}

// collectPeers lists peer sessions. teamFilter, when non-empty, restricts the
// result to sessions whose teamId matches exactly (F-5); sessions without a team
// are therefore excluded by it. scopeFilter, when non-empty, restricts the
// result to sessions whose scope matches exactly (F-17); legacy/pre-F-17
// sessions have an empty scope and are excluded by it. Either empty filter is a
// no-op (the unchanged global default for that axis).
//
// The second return value is the number of sessions that passed the team and
// stale checks but were excluded SOLELY by scopeFilter — i.e. how many more the
// caller would see with --all-scopes. The caller uses it for the anti-silent-cap
// stderr hint. It is always 0 when scopeFilter is empty.
func collectPeers(mgr *session.Manager, dataDir string, staleSeconds, maxContentBytes int, includeStale bool, teamFilter, scopeFilter string) ([]peerSummary, int, error) {
	sessionsRoot := filepath.Join(dataDir, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []peerSummary{}, 0, nil // BUG-B: empty, not nil, for JSON []
		}
		return nil, 0, fmt.Errorf("peers: read sessions root: %w", err)
	}

	now := time.Now().UTC()
	out := []peerSummary{} // BUG-B: empty, not nil, so peers --json emits [] not null
	hiddenByScope := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mf, err := mgr.LoadManifest(e.Name())
		if err != nil {
			// A manifest owned by ANOTHER uid is excluded rather than fatal, and
			// said out loud. Enumeration is how an operator finds out what is
			// going on: a `peers` that dies on the anomaly it should be showing is
			// useless exactly when it is needed. Silence would be worse still —
			// the session would simply not be there, and nobody would ask why.
			//
			// Corrupt manifests keep their existing silent skip: unreadable JSON
			// is a mess, not a claim about identity.
			if errors.Is(err, security.ErrOwnershipMismatch) {
				fmt.Fprintf(os.Stderr, "cab-bridge: skipping session %s — its manifest belongs to another user: %v\n", e.Name(), err)
			}
			continue
		}
		if teamFilter != "" && mf.TeamID != teamFilter {
			continue
		}
		// The EFFECTIVE scope, computed once here so that everything downstream —
		// the filter, the column, the addressing token, the error messages — reads
		// a value that already carries its meaning. Filling this field with the
		// raw one is what left eight readers deciding for themselves what an empty
		// string meant.
		effScope := session.EffectiveScope(mf)

		// F-23a: staleness via the single shared definition (orchestrating is
		// heartbeat-exempt). Same source of truth as status + cleanup globalSweep.
		stale := session.IsStale(mf, staleSeconds, now)
		if stale && !includeStale {
			continue
		}
		// Scope filter last, after team+stale, so hiddenByScope counts exactly
		// the sessions --all-scopes would reveal under the same other flags.
		// An empty FILTER still means "no filter" — that is this parameter's
		// contract and the callers rely on it. What changed is the SESSION side:
		// comparing effScope means a legacy session is matched by the project it
		// actually belongs to, instead of falling out of every filter and being
		// visible only to a caller who happened to pass none (CRI final gate).
		if scopeFilter != "" && effScope != scopeFilter {
			hiddenByScope++
			continue
		}
		// One extra file read per peer, on a command run often — said out loud
		// rather than hidden: it is the same order of magnitude as the manifest
		// read just above, and it is what makes this column answer from the SAME
		// fact overview answers from (F-112).
		var listening *bool
		listenerPID := 0
		if owner, ok, oerr := mgr.ReadListener(mf.SessionID); oerr == nil && ok {
			alive := owner.Listening()
			listening = &alive
			if alive {
				listenerPID = owner.PID
			}
		}

		out = append(out, peerSummary{
			SessionID:         mf.SessionID,
			Role:              mf.Role,
			AgentName:         mf.AgentName,
			ProjectName:       mf.ProjectName,
			PID:               listenerPID,
			LastHeartbeat:     mf.LastHeartbeat,
			Stale:             stale,
			Listening:         listening,
			InboxCount:        countUnread(mgr, e.Name(), filepath.Join(sessionsRoot, e.Name()), maxContentBytes),
			LastConsumedMsgID: mf.LastConsumedMsgID,
			TeamID:            mf.TeamID,
			Scope:             effScope,
			State:             mf.State,
		})
	}
	return out, hiddenByScope, nil
}
