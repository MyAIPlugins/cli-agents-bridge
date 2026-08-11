package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/routing"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// The three sending verbs of the LOOP surface (DESIGN v0.8 §2.2):
//
//	ask <who> "..."    asking — expects an answer, stays open until replied to
//	tell <who> "..."   informing — fire and forget
//	reply "..."        answering whoever asked — archives one delivery of theirs
//
// THE VERB CARRIES THE TYPE. No --type, no --in-reply-to, no id to transcribe:
// "am I asking or informing" is something the writer already knows, so it is
// language, not configuration.

// resolveMessagePayload implements the one payload rule: an argument IS the
// message; no argument means stdin, read to EOF.
//
// With an argument present stdin is NEVER read — deliberately not "detect both",
// because detecting would require reading stdin, which is the environment
// dependency this rule exists to remove. tty detection is also out: verified
// empirically that stdin is not a tty inside the harness, so every short
// `tell X "hi"` would block on an empty pipe.
//
// F-113: but an argument AND redirected content is not a rule to apply, it is a
// contradiction to report. `reply VAL-x < report.md` sent the five bytes of the
// name and threw the report away, closing the ask with exit 0 — and it is the
// complement of the F-105 fix, which made the argument win precisely so that a
// val called `OK` could be answered with the word `OK`. Two mutually exclusive
// readings of one input; the defect was never which one we picked, it was
// picking in SILENCE. So the ambiguous combination is refused and the two
// unambiguous forms are named. Nothing else changes.
func resolveMessagePayload(arg string, hasArg bool, stdin io.Reader) (string, error) {
	if hasArg {
		if stdinIsRedirected(stdin) {
			return "", fmt.Errorf("an argument AND redirected input: %q would be sent and the redirected content thrown away, so nothing was sent. "+
				"Use one form or the other: `<verb> [<agent>] \"the message\"`, or `<verb> [<agent>] < file.md`", previewContent(arg, 30))
		}
		if strings.TrimSpace(arg) == "" {
			// An empty ARGUMENT is exactly the shell-ate-the-text case (a stray
			// backtick, a $, an apostrophe), so this is where the guidance is
			// most needed — it used to be the one path without it.
			return "", errors.New("empty message: the shell may have swallowed it (backticks, $, quotes) — pipe it on stdin instead, e.g. `... < message.md`")
		}
		return arg, nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read message from stdin: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errors.New("empty message: pass it as an argument or pipe it on stdin")
	}
	return string(data), nil
}

// resolveRecipientByName maps an agent NAME to a session id within the caller's
// scope, fail-closed (§2.2): zero matches is an error, several LIVE matches is
// an error listing them. Never a silent pick.
//
// By name and not by id because the id an agent re-types every day is its own
// peer's, and that is the one it confabulates after a compact (LL-13/LL-14).
func resolveRecipientByName(cfg config.Config, mgr *session.Manager, token, selfSID string) (string, error) {
	rcpt, perr := parseRecipient(token)
	if perr != nil {
		return "", perr
	}
	name := rcpt.name

	// Scope and team come from MY OWN manifest, never from the cwd
	// (CRI diff-gate P1-3). Once resolveCurrentSession has decided who I am —
	// possibly from CAB_SESSION_ID — the directory stops participating: fixing
	// "who I am" without fixing "which world I live in" leaves the hole open,
	// and a peer of the same name in the cwd's scope would silently receive a
	// message carrying my other identity as sender.
	me, err := mgr.LoadManifest(selfSID)
	if err != nil {
		return "", fmt.Errorf("resolve recipient: load my own manifest %s: %w", selfSID, err)
	}

	// A QUALIFIED address drops both filters and searches the whole data dir
	// (F-116). Team included, deliberately: `peers --all-scopes` shows every
	// team, so keeping that filter here would leave a peer VISIBLE IN THE LIST
	// and unreachable — which is the defect this feature exists to close,
	// rebuilt on another axis. `--team` stays what DESIGN §2.6 says it is, a
	// discovery filter and not a security boundary.
	//
	// The UNqualified path is untouched: nobody pays for a feature they are not
	// using, and its behaviour is the one every existing test pins.
	myScope := session.EffectiveScope(me)

	// The scope filter is applied BELOW, on effective scopes, not here: an empty
	// one means NO FILTER to collectPeers, so a legacy session searched the whole
	// data dir and resolved a bare name in another repository — which is how a
	// plain `ask` left its project without anybody naming one (CRI final gate).
	teamFilter := me.TeamID
	if rcpt.qualified() {
		teamFilter = ""
	}
	peers, _, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, cfg.MaxMessageBytes, true, teamFilter, "")
	if err != nil {
		return "", fmt.Errorf("resolve recipient: %w", err)
	}

	scopeOf := mgr.EffectiveScopeCache()

	// `peers` now means THE WHOLE DATA DIR, because the scope filter moved down
	// into the loop — and the readers of the zero-match branch below were written
	// when it meant "my world". Left as it was, "registered here" listed ten
	// agents of other projects, every one of which fails identically when tried
	// unqualified: the circular diagnostic of F-2, rebuilt.
	//
	// So the local view is materialised here, once, and the zero-match branch
	// reads THAT. The qualified path keeps the global list on purpose —
	// knownScopes wants it.
	//
	// The shape is the one that has bitten four times today: changing what
	// something MEANS is finished when its readers have been enumerated. Three of
	// those were struct fields and a grep found them; this one is a local
	// variable, and only the question finds it.
	inScope := peers
	if !rcpt.qualified() {
		inScope = inScope[:0:0]
		for _, p := range peers {
			if session.SameProject(myScope, scopeOf(p.SessionID)) {
				inScope = append(inScope, p)
			}
		}
	}

	var exact, live []peerSummary
	for _, p := range peers {
		if p.SessionID == selfSID || p.AgentName != name {
			continue
		}
		theirScope := scopeOf(p.SessionID)
		if rcpt.qualified() {
			if !scopeMatchesHint(theirScope, rcpt.scope) {
				continue
			}
		} else if !session.SameProject(myScope, theirScope) {
			continue
		}
		exact = append(exact, p)
		if !p.Stale {
			live = append(live, p)
		}
	}

	candidates := live
	if len(candidates) == 0 {
		// Nobody live under that name: fall back to the full set so the error
		// can say "found, but stale" instead of the misleading "no such agent".
		candidates = exact
	}

	switch len(candidates) {
	case 1:
		// Compared on the SCOPES, not on the syntax: `VAL-x@my-own-repo` is not a
		// cross-project message just because it was written the long way, and the
		// first version refused it for that reason alone. And an UNKNOWN scope is
		// not a different one — see crossesScopes.
		theirScope := scopeOf(candidates[0].SessionID)
		if session.CrossesScopes(myScope, theirScope) {
			if err := allowedAcrossScopes(me.Role, candidates[0].Role, name, rcpt.scope); err != nil {
				return "", err
			}
		}
		return candidates[0].SessionID, nil
	case 0:
		// A rename is invisible to whoever was not watching, and a peer that was
		// OFFLINE while it happened could not have been notified at all. So the
		// answer is not an event — it is the old name, remembered on disk, which
		// reaches both. Without this the sender reads "no such agent" about someone
		// who is right there under a new label, and the obvious next move (delete
		// and re-register) is the one that costs a mailbox.
		if renamed, ok := findRenamed(mgr, inScope, name); ok {
			return "", fmt.Errorf("no agent named %q in this scope — %s answered to that name and is now %q",
				name, renamed.SessionID, renamed.AgentName)
		}
		if rcpt.qualified() {
			// The question here is "wrong name, or wrong project?", and it is not
			// answerable without the list of projects. Naming only the agents would
			// answer half of it.
			return "", fmt.Errorf("no agent named %q in project %q — projects with agents: %s",
				name, rcpt.scope, strings.Join(knownScopes(peers), ", "))
		}
		known := knownAgentNames(inScope, selfSID)
		if len(known) == 0 {
			// An EMPTY scope is the likeliest place to be looking for somebody who
			// works elsewhere, so this is the message that most needs the other
			// form — it was the one without it.
			return "", fmt.Errorf("no agent named %q in this scope, and no other agent is registered here (another project: %s%s<project>, from the SCOPE column of `peers --all-scopes`)",
				name, name, session.ScopeSeparator)
		}
		return "", fmt.Errorf("no agent named %q in this scope — registered here: %s (another project: %s%s<project>)",
			name, strings.Join(known, ", "), name, session.ScopeSeparator)
	default:
		// List WHAT distinguishes them (CRI2 P2-6). The old message named no
		// discriminator — "which of the two?" — while advising to destroy a
		// session the tool itself classifies as alive. With projectPath and
		// heartbeat age the choice becomes mechanical, and removal stays a last
		// resort rather than the first suggestion.
		// Two scopes sharing a basename: the ambiguity is in the ADDRESS, not in
		// the sessions, and it has an exact way out — the full path, which is what
		// `peers` prints on those very rows (scopeColumn). Saying "remove the
		// stale one" here would advise destroying a healthy session to fix a typo.
		if rcpt.qualified() {
			// SAME scope or DIFFERENT scopes are two different accidents with two
			// different ways out, and `soleSessionNamed` already told them apart —
			// this branch did not, and called everything "projects". With three
			// homonyms, two of them in one project, it announced "matches 2
			// projects" (false: two SESSIONS in one) and offered the same token
			// twice as the remedy: paste it and you get the identical error, with
			// no flag on the loop verbs to escape by (CRI2 F-2). Fail-closed, so
			// nothing was ever sent — but a diagnostic that loops is not a way out.
			scopes := map[string]bool{}
			for _, c := range candidates {
				scopes[c.Scope] = true
			}
			if len(scopes) == 1 {
				var lines []string
				for _, c := range candidates {
					lines = append(lines, fmt.Sprintf("\n    %s  last seen %s ago", c.SessionID, time.Since(c.LastHeartbeat).Round(time.Second)))
				}
				sort.Strings(lines)
				return "", fmt.Errorf("%d sessions in project %q answer to %q, so nothing was sent — they are duplicates, not different projects; remove the dead one with `cab-bridge cleanup --session-id=<id>`:%s",
					len(candidates), rcpt.scope, name, strings.Join(lines, ""))
			}
			var lines []string
			for _, c := range candidates {
				lines = append(lines, fmt.Sprintf("\n    %s%s%s  (%s)", name, session.ScopeSeparator, c.Scope, c.SessionID))
			}
			sort.Strings(lines)
			return "", fmt.Errorf("%q matches %d projects, so nothing was sent — address one by its full path:%s",
				rcpt.String(), len(scopes), strings.Join(lines, ""))
		}
		var lines []string
		for _, c := range candidates {
			lines = append(lines, fmt.Sprintf("\n    %s  project %s  last seen %s ago", c.SessionID, c.ProjectName, time.Since(c.LastHeartbeat).Round(time.Second)))
		}
		sort.Strings(lines)
		return "", fmt.Errorf("%d live agents are named %q, so nothing was sent:%s\n  they are different sessions: check which is the one you mean, and remove the stale one with `cab-bridge cleanup --session-id=<id>` only if it is genuinely dead",
			len(candidates), name, strings.Join(lines, ""))
	}
}

// allowedAcrossScopes is the F-116 restriction for this release: a message that
// crosses projects is val→val only.
//
// Here it is an EARLY ERROR, not the guarantee — the guarantee is at the
// gateway (send.go), on the manifests actually used to compose the message.
// Between this lookup and that write sits `SetRole`, which F-110 put on the
// normal path: val→val could pass here, the role change, and the message leave
// for an `esc` in another repository. The same distinction between a warning
// and an invariant that unexported roleNames was about.
//
// It is not a second routing matrix.
// ValidateSendPair stays the only policy, and stays untouched — because the
// scope was in practice the barrier, and removing it for a qualified address
// opens every pair that matrix already accepts, which is a decision nobody took.
// Alan's requirement is orchestrator-to-orchestrator, that is the only real use
// today, and it says itself in half a line. Widening it needs a case, not a flag.
func allowedAcrossScopes(fromRole, toRole, name, scope string) error {
	if fromRole == session.RoleVal && toRole == session.RoleVal {
		return nil
	}
	return fmt.Errorf("across projects only a val writes to a val (you are %q, %s%s%s is %q) — route it through your own val, who can reach theirs",
		fromRole, name, session.ScopeSeparator, scope, toRole)
}

// knownScopes lists the projects that have agents, so "no agent named X in
// project Y" can be answered with "which projects exist, then".
func knownScopes(peers []peerSummary) []string {
	labels := scopeColumn(peers)
	seen := map[string]bool{}
	var out []string
	for _, p := range peers {
		if p.Scope == "" || seen[p.Scope] {
			continue
		}
		seen[p.Scope] = true
		// The shared rule again: a list that names two projects identically
		// would answer "which projects exist" with a token that reaches neither.
		out = append(out, labels[p.Scope])
	}
	sort.Strings(out)
	return out
}

func knownAgentNames(peers []peerSummary, selfSID string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range peers {
		if p.SessionID == selfSID || p.AgentName == "" || seen[p.AgentName] {
			continue
		}
		seen[p.AgentName] = true
		out = append(out, p.AgentName)
	}
	sort.Strings(out)
	return out
}

// whoIThoughtIWas labels an error with the session the command resolved itself
// to, e.g. "in session b3e07991 (ESC-bridge)".
//
// F-97: an id-free command that resolves from the cwd can silently become
// SOMEBODY ELSE when the caller is in the wrong directory — the match is exact,
// so no guardrail fires. The failure that follows ("no message received from
// b3e07991") is then unreadable, because it never says from whose point of view
// it was looking. Stating the assumed identity makes the absurd case
// self-evident: asking for ESC's messages from inside ESC's own session.
func whoIThoughtIWas(mgr *session.Manager, sid string) string {
	if mf, err := mgr.LoadManifest(sid); err == nil && mf.AgentName != "" {
		return fmt.Sprintf("in session %s (%s)", sid, mf.AgentName)
	}
	return fmt.Sprintf("in session %s", sid)
}

// openAsk is one query still awaiting a reply.
type openAsk struct {
	id       string
	from     string
	fromName string
	path     string
	when     string
	// page is the delivery this ask belongs to: the instant CommitWakeCursor
	// stamped on the whole page it went out in. Equality is the only thing read
	// from it — see WakeCursor.NotifiedAt.
	page    time.Time
	content string
	// scope is the sender's project, read from ITS MANIFEST — not from the
	// message (F-116, CRI design-gate P1-3).
	//
	// Two questions on two different instants, and one datum cannot answer both:
	// `fromScope` in the message says where the sender declared it was WHEN IT
	// WROTE, which is the honest thing to display; this field says where it is
	// NOW, which is the question when deciding where to deliver now. The message
	// field also does not exist on anything sent before it was added, i.e. on the
	// asks that have been open longest — exactly the ones needing disambiguation.
	scope string
}

// collectOpenAsks returns the asks (type=query) still sitting in inbox/ that
// have been delivered by a next, i.e. NOTIFIED, grouped-ready and sorted oldest
// first.
//
// Only NOTIFIED ones count: replying to something never shown would mean the
// tool's state and the agent's state disagree, which §2.2 rules out.
func collectOpenAsks(mgr *session.Manager, cfg config.Config, sid string) ([]openAsk, error) {
	cursor, _, err := mgr.ReadWakeCursor(sid)
	if err != nil {
		return nil, err
	}
	inboxDir := filepath.Join(cfg.DataDir, "sessions", sid, "inbox")
	entries, _, _, err := readMailbox(inboxDir, cfg.MaxMessageBytes)
	if err != nil {
		return nil, err
	}

	// One manifest read per distinct sender, not per ask: a batch from one peer
	// is the common shape, and this runs on the working loop.
	scopeOf := map[string]string{}
	senderScope := func(from string) string {
		if s, ok := scopeOf[from]; ok {
			return s
		}
		s := ""
		if mf, lerr := mgr.LoadManifest(from); lerr == nil {
			s = session.EffectiveScope(mf)
		}
		scopeOf[from] = s
		return s
	}

	var out []openAsk
	for _, e := range entries {
		if e.msg.Type != message.TypeQuery {
			continue
		}
		page, notified := cursor.NotifiedAt(e.msg.ID)
		if !notified {
			continue
		}
		out = append(out, openAsk{
			id:       e.msg.ID,
			from:     e.msg.From,
			fromName: e.msg.FromAgentName,
			path:     e.path,
			when:     e.msg.Timestamp,
			page:     page,
			content:  e.msg.Content,
			scope:    senderScope(e.msg.From),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].when == out[j].when {
			return out[i].id < out[j].id
		}
		return out[i].when < out[j].when
	})
	return out, nil
}

// oldestPage returns the ids `reply` closes: the OLDEST DELIVERY of open asks
// from target, and never more than one.
//
// F-109. `reply` used to close every NOTIFIED ask of that sender, and NOTIFIED
// means "a next emitted it" — never "the agent read it". An ask that lands while
// the answer is being written is handed over by the re-armed waiter and archived
// as answered: reproduced as a "stop, do NOT do A" closed by a "done A as asked".
//
// Nothing on disk records that the agent READ anything — there is no ACK — so
// any rule here is an ESTIMATE. This one estimates with the only grouping the
// system genuinely knows: a delivery. CommitWakeCursor stamps ONE instant across
// a whole page (wakecursor.go:130), so asks sharing `page` are exactly those one
// `next` put in front of the agent at once.
//
// What it does NOT promise: the oldest open page can BE the unread one — answer
// t1, B arrives as t2 while working, reply, and the only open page is t2. The
// blast radius drops from every page to one, not to zero. That residue is why
// the caller prints what stays open and the response carries `closes`: if it
// happens, both sides see it immediately.
//
// A join replay MERGES pages, and rightly so: it puts every open ask back to
// UNREAD and the following `next` re-delivers them in one page, so they were
// shown together. The unit grows to match what the agent actually saw
// (verified on the real binary, not inferred).
func oldestPage(asks []openAsk, target string) []string {
	var mine []openAsk
	for _, a := range asks {
		if a.from == target {
			mine = append(mine, a)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	// The minimum, not mine[0]: asks are ordered by the SENDER's timestamp, and
	// a message can be delivered in a later page than one sent after it.
	oldest := mine[0].page
	for _, a := range mine[1:] {
		if a.page.Before(oldest) {
			oldest = a.page
		}
	}
	var closeIDs []string
	for _, a := range mine {
		if a.page.Equal(oldest) {
			closeIDs = append(closeIDs, a.id)
		}
	}
	return closeIDs
}

// --- ask / tell -------------------------------------------------------------

func runAskVerb(args []string) error {
	return runSendVerb("ask", message.TypeQuery, args, os.Stdin, os.Stdout, os.Stderr)
}

func runTell(args []string) error {
	return runSendVerb("tell", message.TypeNotify, args, os.Stdin, os.Stdout, os.Stderr)
}

func runSendVerb(verb, msgType string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s: usage: cab-bridge %s <agent-name> [\"message\"]   (without the message, it is read from stdin)", verb, verb)
	}
	if strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("%s: takes no flags — the verb carries the type, and the recipient is an agent name: cab-bridge %s <agent-name> [\"message\"]", verb, verb)
	}
	if len(args) > 2 {
		return fmt.Errorf("%s: too many arguments — quote the message as one argument: cab-bridge %s %s \"...\"", verb, verb, args[0])
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)
	sid, err := resolveCurrentSession(mgr, verb, "")
	if err != nil {
		return err
	}

	content, err := resolveMessagePayload(argAt(args, 1), len(args) > 1, stdin)
	if err != nil {
		return fmt.Errorf("%s (%s): %w", verb, whoIThoughtIWas(mgr, sid), err)
	}
	to, err := resolveRecipientByName(cfg, mgr, args[0], sid)
	if err != nil {
		return fmt.Errorf("%s (%s): %w", verb, whoIThoughtIWas(mgr, sid), err)
	}

	// F-43, carried over from the old flag surface: ask and tell are NOT
	// idempotent (reply is, by construction), so a degraded agent re-invoking
	// one before the first send's stdout returns sends it twice. Warn — never
	// silently skip: the second send may well be deliberate.
	if cfg.DedupWindowSeconds > 0 {
		outbox := filepath.Join(cfg.DataDir, "sessions", sid, "outbox")
		now := time.Now().UTC()
		if dupID, dupAt, derr := findRecentDuplicateAt(outbox, to, msgType, content, cfg.DedupWindowSeconds, cfg.MaxMessageBytes, now); derr == nil && dupID != "" {
			// The AGE, not the window: printing DedupWindowSeconds said "10s ago"
			// one second after the fact — a statement of something that did not
			// happen, in the very message meant to help spot a double-invoke.
			fmt.Fprintf(stderr, "%s: warning: an identical message to %s went out %s ago as %s — sending anyway; if that was a double-invoke, the recipient now has two\n",
				verb, args[0], now.Sub(dupAt).Round(time.Second), dupID)
		}
	}

	msgID, err := sendMessage(cfg, mgr, sid, to, msgType, content, nil, false)
	if err != nil {
		// The routing layer suggests --allow-mesh, a flag the LOOP verbs reject:
		// following it lands on "takes no flags", i.e. a bounce between two
		// contradicting instructions — the same dead end as F-91 (CRI2 P1-2).
		// Say the route that actually exists instead.
		if errors.Is(err, routing.ErrEscToEscForbidden) {
			// NOT "architect": that one is reserved for Claude Desktop over MCP.
			// This line sits on the recovery path from a mistake — the moment
			// somebody is already confused — and it used to send a reviewer
			// straight into the reserved role.
			return fmt.Errorf("%s (%s): two executors cannot message each other on the loop — route it through the val, or join as role=%s if you are a reviewer (the loop verbs take no flags, so there is no override here)",
				verb, whoIThoughtIWas(mgr, sid), session.RoleCritic)
		}
		return fmt.Errorf("%s (%s): %w", verb, whoIThoughtIWas(mgr, sid), err)
	}
	fmt.Fprintln(stdout, formatSendEcho(myName(mgr, sid), args[0], msgID, content, verb))
	warnNotListening(mgr, sid, stderr)
	return nil
}

// warnNotListening says, on the one occasion when it is observable without
// asking, that nothing is waiting for mail on this session.
//
// F-114. Being out of earshot is invisible by construction: no command fails, no
// message bounces, and `overview` tells you only if you already suspect it. It
// happened twice in two days to a val, and the second time it was Alan who
// noticed, not him. Sending is the moment the gap matters — you have just spoken
// to someone — so it is where the fact belongs.
//
// A NOTE, never an error: writing to a peer who is not listening is legitimate,
// and so is being the peer who is not listening. Nothing is blocked.
//
// The wording states the MECHANISM, not a prediction, and that is what makes it
// true for the no-push peers too (Codex, Desktop): their notify-watch is a
// non-consuming poller, so their mail waits in the inbox until they run `next`
// exactly like everybody else's. A line saying "you will not receive the reply"
// would have been false for them — and their watcher leaves no liveness record
// to tell them apart, so a guess would have been a guess.
//
// No exemption for `orchestrating` (the state that is heartbeat-exempt): the
// re-arm discipline applies to a val too, and the val this was reported by was
// orchestrating when its own waiter died. Exempting the state would have
// silenced the exact case that produced the finding.
func warnNotListening(mgr *session.Manager, sid string, stderr io.Writer) {
	owner, ok, err := mgr.ReadListener(sid)
	if err != nil {
		// Pure observability: a damaged record is not worth a line here, and a
		// warning about a warning is noise on the path that has just succeeded.
		return
	}
	// NO record means never listened once — which is the worst case of all, not
	// a reason to stay quiet. (`peers` renders that same state as `-` rather than
	// `no`, because there the question is "is this peer faulty"; here it is "will
	// anything be waiting for the answer", and the answer is no either way.)
	if ok && owner.Listening() {
		return
	}
	fmt.Fprintln(stderr, "note: no next is listening on this session — anything sent back waits in your inbox until you run next")
}

// stdinIsRedirected reports whether the caller pointed real content at stdin.
//
// NOT tty detection, which this file already rules out for a measured reason:
// inside the harness stdin is a SOCKET — not a tty, not a pipe, not a file — so
// "is it a terminal" answers nothing here. The question that does have an answer
// is "did somebody redirect content into it", and that is visible without
// reading a byte:
//
//	harness, no redirection   socket        -> false
//	< report.md               regular, 10 B -> true
//	echo ... |                named pipe    -> true
//	< /dev/null               char device   -> false
//	< empty.md                regular, 0 B  -> false   (nothing to throw away)
//
// A pipe counts regardless of size: the byte count on a pipe is whatever
// happens to be buffered at this instant, so a slow producer would read as
// empty. A regular file's size is exact, so it is used.
//
// A non-*os.File reader — every injected stdin in the tests — is not a
// redirection and returns false, which keeps the guard off the unit tests and
// forces the ones that DO exercise it to use a real file, i.e. the real thing.
func stdinIsRedirected(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	mode := fi.Mode()
	if mode&os.ModeNamedPipe != 0 {
		return true
	}
	return mode.IsRegular() && fi.Size() > 0
}

func argAt(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

// --- reply ------------------------------------------------------------------

func runReply(args []string) error { return replyRun(args, os.Stdin, os.Stdout, os.Stderr) }

func replyRun(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) > 0 && strings.HasPrefix(args[0], "-") {
		return errors.New("reply: takes no flags — cab-bridge reply [<agent-name>] [\"message\"]")
	}
	if len(args) > 2 {
		return errors.New("reply: too many arguments — quote the message as one argument")
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)
	sid, err := resolveCurrentSession(mgr, "reply", "")
	if err != nil {
		return err
	}

	// EVERYTHING up to and including the journal's creation happens inside ONE
	// critical section (CRI diff-gate P0-1).
	//
	// Reading the journal outside the lock and writing it with an overwrite let
	// two concurrent replies both see "no journal", build J1 and J2 for the same
	// anchor, and write them in sequence. If the first then delivered its
	// response and died before persisting SENT, recovery would read J2 while the
	// response id on disk carries J1's bytes: create-if-absent refuses it — as it
	// should — and every retry fails identically. The ask stays open forever and
	// exactly-once stops being recoverable.
	//
	// So a second initializer must RESUME J1, never replace it. The set is also
	// frozen in here, not before: a snapshot taken outside the lock could be
	// stale by the time it is persisted.
	var txn *session.ReplyTxn
	lockErr := mgr.WithSessionLock(sid, func() error {
		existing, found, terr := mgr.ReadReplyTxn(sid)
		if terr != nil {
			return terr
		}
		if found {
			// An in-flight journal outranks anything the arguments say: finishing
			// a delivered reply must not depend on the retry being spelled the
			// same way.
			txn = existing
			return nil
		}

		asks, aerr := collectOpenAsks(mgr, cfg, sid)
		if aerr != nil {
			return aerr
		}
		if len(asks) == 0 {
			// The state the agent is actually in matters here (CRI2 P1-3): with an
			// ask sitting UNREAD, "no ask of yours is open" is FALSE from where the
			// agent stands — it knows it was asked something — and the tool
			// contradicts its memory with no bridge across. Say which state it is.
			if n := countUnseenInbound(mgr, cfg, sid); n > 0 {
				return fmt.Errorf("%w: %d message(s) are waiting to be delivered — run next first, then reply", errUndeliveredWaiting, n)
			}
			return errNothingToReplyTo
		}
		// The names that could plausibly be the RECIPIENT of this reply: the
		// senders with an open ask, and nobody else.
		//
		// It used to be every peer in my scope, and the plan for F-116 proposed
		// widening it to the whole data dir — which would have made `reply GO`
		// stop working the day somebody in an unrelated project registered an
		// agent called `GO`: the meaning of a valid payload changing because of a
		// session that has nothing to do with this conversation.
		//
		// Narrowing is the correct move and it also fixes today: this list only
		// ever answers "did they mean to name a recipient, or is this the
		// message?", and only a sender with an open ask can be the recipient of a
		// reply. A typo aimed at a peer with no open ask is not a near-miss — it
		// is a message. (CRI design-gate P1-3.)
		known := senderNames(asks)
		target, content, rerr := resolveReplyTarget(args, asks, known, stdin)
		if rerr != nil {
			return rerr
		}

		closeIDs := oldestPage(asks, target)
		if len(closeIDs) == 0 {
			return fmt.Errorf("no open ask from %s to answer", target)
		}
		txn = &session.ReplyTxn{
			ResponseID: session.DeterministicResponseID(sid, closeIDs[0]),
			To:         target,
			Anchor:     closeIDs[0],
			CloseIDs:   closeIDs,
			State:      session.ReplyTxnPending,
			Timestamp:  time.Now().UTC(),
			Content:    content,
		}
		return mgr.WriteReplyTxn(sid, txn)
	})
	if lockErr != nil {
		if errors.Is(lockErr, errUndeliveredWaiting) {
			return fmt.Errorf("reply (%s): %w", whoIThoughtIWas(mgr, sid), lockErr)
		}
		if errors.Is(lockErr, errNothingToReplyTo) {
			return fmt.Errorf("reply (%s): nothing to reply to — no ask of yours is open (a tell is fire-and-forget: answer it with tell or ask)", whoIThoughtIWas(mgr, sid))
		}
		return fmt.Errorf("reply (%s): %w", whoIThoughtIWas(mgr, sid), lockErr)
	}

	return finishReplyTxn(mgr, cfg, sid, txn, stdout, stderr)
}

// errNothingToReplyTo is raised inside the locked section so the caller can
// phrase it with the identity label, without holding the lock to do so.
var errNothingToReplyTo = errors.New("no open ask")

// errUndeliveredWaiting separates "you have nothing" from "you have something
// you have not read yet" — two states that the same message would flatten.
var errUndeliveredWaiting = errors.New("nothing NOTIFIED to reply to")

// resolveReplyTarget decides who is being answered and with what.
//
// Argument shapes:
//
//	reply                      message from stdin, recipient inferred
//	reply "text"               message inline, recipient inferred
//	reply NAME                 recipient explicit, message from stdin
//	reply NAME "text"          both explicit
//
// The single-argument case is the only judgement call, and §2.2 does not cover
// it: "an argument IS the message" collides with `reply NAME < file.md`. It is
// resolved WITHOUT reading stdin — the argument is treated as a recipient only
// when it exactly matches the agent name of a sender with an open ask.
// Deterministic and inspectable; the residual collision (a message whose whole
// text equals a peer's name) is reported rather than guessed.
func resolveReplyTarget(args []string, asks []openAsk, known []string, stdin io.Reader) (target, content string, err error) {
	senders := map[string]string{}  // sessionID -> agent name
	byName := map[string][]string{} // agent NAME (never qualified) -> sessionIDs
	for _, a := range asks {
		senders[a.from] = a.fromName
		if a.fromName != "" {
			if !contains(byName[a.fromName], a.from) {
				byName[a.fromName] = append(byName[a.fromName], a.from)
			}
		}
	}
	// The argument may carry a project (`VAL-x@other-repo`), so recognising it as
	// a NAME has to look past the separator — the map is keyed on bare names.
	// A token that parses badly is not a recipient; it falls through to the
	// payload rule below, which is where a message containing an `@` belongs.
	asName := func(arg string) (string, bool) {
		r, perr := parseRecipient(arg)
		if perr != nil {
			return "", false
		}
		_, ok := byName[r.name]
		return r.name, ok
	}

	switch len(args) {
	case 2:
		sid, rerr := soleSessionNamed(args[0], asks, senders)
		if rerr != nil {
			return "", "", rerr
		}
		content, err = resolveMessagePayload(args[1], true, stdin)
		return sid, content, err

	case 1:
		// A single argument IS the message — that is the payload rule, without
		// exceptions — UNLESS the recipient is genuinely ambiguous. With one open
		// asker there is nothing to disambiguate, so reading the argument as a
		// name breaks the rule for nothing: a val called `OK`, answered with the
		// word `OK`, produced "empty message" and exit 1 while the message sat
		// right there in argv.
		//
		// The disambiguation earns its exception only when several agents have
		// open asks and the argument names one of them.
		if _, named := asName(args[0]); named && len(senders) > 1 {
			sid, rerr := soleSessionNamed(args[0], asks, senders)
			if rerr != nil {
				return "", "", rerr
			}
			content, err = resolveMessagePayload("", false, stdin)
			return sid, content, err
		}
		// The guardrail below is for a TYPO — an argument that is not a name and
		// resembles one. An EXACT match with the only open asker is not a typo:
		// there is nothing to protect from, and the payload rule applies (the same
		// rule stated three lines above). Without this the fix moved the defect
		// instead of removing it — `reply OK` to the only asker named `OK` fell
		// out of the recipient branch and straight into the lookalike branch,
		// which fired on the very case the fix had just freed.
		//
		// "e il ramo accanto?": the finding named one branch and the complement
		// went unexamined, by me writing it and by the val ratifying it.
		if _, isOpenAsker := asName(args[0]); !isOpenAsker {
			// A near-miss on a name must NOT quietly become the message (CRI2 P1-1).
			// `reply VAL-brige < report.md` used to send the string "VAL-brige" as the
			// answer, never read the report, close the ask and exit 0 — after which
			// the retry said "nothing to reply to", which from the agent's side is
			// incomprehensible. Case-insensitive, because that is how the near-miss
			// actually arrives.
			if match, ok := nameLookalike(known, args[0]); ok {
				// EXACT vs merely similar are two different mistakes and deserve
				// two different sentences. Saying "X ... looks like X" about an
				// exact match reads as a contradiction, and the reader then
				// distrusts the part that was right.
				if match == args[0] {
					return "", "", fmt.Errorf("%q is an agent here, but it has no open ask — so there is nothing to reply to it. "+
						"If %q is your message to whoever DID ask, pipe it on stdin instead", args[0], args[0])
				}
				return "", "", fmt.Errorf("%q is not one of the agents with an open ask, but it looks like %q — if you meant to answer them use `reply %s \"...\"`; if %q really is your message, pipe it on stdin instead",
					args[0], match, match, args[0])
			}
		}
		target, err = soleSender(senders)
		if err != nil {
			return "", "", err
		}
		content, err = resolveMessagePayload(args[0], true, stdin)
		return target, content, err

	default:
		target, err = soleSender(senders)
		if err != nil {
			return "", "", err
		}
		content, err = resolveMessagePayload("", false, stdin)
		return target, content, err
	}
}

// soleSessionNamed resolves an agent NAME to the single session behind it,
// fail-closed on duplicates.
//
// Two sessions can share a name — Register only blocks a second one on the same
// ProjectPath, and --force-new bypasses even that — and a map[name]sessionID
// silently kept the last writer. The worst shape was that bare `reply` correctly
// refused the ambiguity and then suggested `reply <name>`: the very form that
// picked one arbitrarily, sent it the response and CLOSED its asks. The
// remediation was the trap (CRI diff-gate P1-4). Same posture as
// resolveRecipientByName already had for ask/tell.
// nameLookalike reports whether s is a case-insensitive match of a known agent
// name. Deliberately NOT a fuzzy distance: a rule an agent cannot predict is
// worse than none, and case is the near-miss that actually happens.
// nameLookalike reports the known agent name that `s` was probably meant to be.
//
// It used to be strings.EqualFold, which catches a difference in CAPITALS and
// nothing else — while the comment above its call site cited `VAL-brige` as the
// motivating example. Executed, that exact line sent the typo itself as the
// answer, closed the ask and exited 0, with the report on stdin never read: word
// for word the damage the guardrail claims to prevent, still entirely there.
//
// A control that exists, is tested, is documented, and does not cover the case
// it was written for is worse than an absent one: the reader concludes they are
// protected and the user gets no error.
//
// Edit distance <= threshold, because that is how typos actually arrive —
// transposition, a missing letter, one letter too many. The threshold scales
// with length: on a short name almost everything is within two edits, and a
// guardrail that fires on unrelated words would push people to pipe everything
// through stdin, which is how a guardrail gets worked around instead of used.
func nameLookalike(known []string, s string) (string, bool) {
	threshold := 2
	if len(s) <= 4 {
		threshold = 1
	}
	best, bestDist := "", threshold+1
	for _, k := range known {
		if strings.EqualFold(k, s) {
			return k, true
		}
		if d := editDistance(strings.ToLower(k), strings.ToLower(s)); d <= threshold && d < bestDist {
			best, bestDist = k, d
		}
	}
	return best, best != ""
}

// editDistance is Levenshtein, iterative with a single row. Agent names are
// short and the candidate set is one scope, so the simple version is the right
// one — no dependency for twenty lines.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

func soleSessionNamed(name string, asks []openAsk, senders map[string]string) (string, error) {
	rcpt, perr := parseRecipient(name)
	if perr != nil {
		return "", perr
	}

	seen := map[string]openAsk{} // sessionID -> one of its asks, for the scope
	for _, a := range asks {
		if a.fromName != rcpt.name {
			continue
		}
		if rcpt.qualified() && !scopeMatchesHint(a.scope, rcpt.scope) {
			continue
		}
		seen[a.from] = a
	}

	switch len(seen) {
	case 1:
		for id := range seen {
			return id, nil
		}
	case 0:
		if rcpt.qualified() {
			return "", fmt.Errorf("%q has no open ask of yours — open asks are from: %s", rcpt.String(), strings.Join(sortedNames(senders), ", "))
		}
		return "", fmt.Errorf("%q has no open ask of yours — open asks are from: %s", rcpt.name, strings.Join(sortedNames(senders), ", "))
	}

	// Two senders share a name. Before F-116 this was a dead end: the message
	// said "clean up the duplicate", which means destroying a live session to
	// answer a question — and if the two are in DIFFERENT PROJECTS neither is a
	// duplicate and neither could be answered at all, id-free or otherwise.
	//
	// Now the qualified address is the way out, and it exists precisely because
	// `register` and `join --force-new` can put two sessions under one name
	// (verbs.go, soleSessionNamed's own note): a name is unique per scope only on
	// the `join` path. (CRI design-gate P1-2.)
	// The labels come from the SAME rule the peers column uses: basename when it
	// is unique in this set, full path when it is not. Writing a second
	// shortening here is exactly what handed back two identical tokens for two
	// different projects — a way out that did not work.
	scopes := make([]string, 0, len(seen))
	for _, a := range seen {
		scopes = append(scopes, a.scope)
	}
	labels := scopeLabels(scopes)

	var lines []string
	sameScope := true
	var firstScope string
	for id, a := range seen {
		if firstScope == "" {
			firstScope = a.scope
		} else if a.scope != firstScope {
			sameScope = false
		}
		lines = append(lines, fmt.Sprintf("\n    %s%s%s  (%s)", a.fromName, session.ScopeSeparator, scopeLabelOf(a.scope, labels), id))
	}
	sort.Strings(lines)
	if sameScope {
		return "", fmt.Errorf("%d sessions in one project have open asks under the name %q — this is ambiguous, so nothing was sent; clean up the duplicate with `cab-bridge cleanup --session-id=<id>`:%s",
			len(seen), rcpt.name, strings.Join(lines, ""))
	}
	return "", fmt.Errorf("%d agents named %q have open asks, in different projects — say which:%s",
		len(seen), rcpt.name, strings.Join(lines, ""))
}

// scopeLabelOf renders a scope for an error message, using the label the shared
// rule chose for this set — so what the message offers is always addressable.
func scopeLabelOf(scope string, labels map[string]string) string {
	if scope == "" {
		return "<no project>"
	}
	if l, ok := labels[scope]; ok {
		return l
	}
	return scope
}

// senderNames lists the agent names that have an open ask right now.
func senderNames(asks []openAsk) []string {
	seen := map[string]bool{}
	var out []string
	for _, a := range asks {
		if a.fromName == "" || seen[a.fromName] {
			continue
		}
		seen[a.fromName] = true
		out = append(out, a.fromName)
	}
	sort.Strings(out)
	return out
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// soleSender returns the only sender with open asks, or an error naming the
// candidates. Ambiguity is judged on senders with an OPEN ASK, not on the last
// batch: a batch holding one ask from VAL and one tell from CRI is NOT
// ambiguous — there is exactly one thing to close (§2.2).
func soleSender(senders map[string]string) (string, error) {
	if len(senders) == 1 {
		for sid := range senders {
			return sid, nil
		}
	}
	names := sortedNames(senders)
	return "", fmt.Errorf("%d agents have open asks (%s) — say who: cab-bridge reply %s \"...\"",
		len(senders), strings.Join(names, ", "), names[0])
}

func sortedNames(senders map[string]string) []string {
	var out []string
	for sid, name := range senders {
		if name == "" {
			name = sid
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// finishReplyTxn drives the journal to completion and is safe to re-enter.
//
// Order is obligatory — SEND, then ARCHIVE: the reverse would lose the
// actionable query if the send failed. The gap between them is what the
// journal covers.
func finishReplyTxn(mgr *session.Manager, cfg config.Config, sid string, txn *session.ReplyTxn, stdout, stderr io.Writer) error {
	// Entering already SENT means we are resuming after a crash in the gap.
	// Say so: completing a recovery in silence would look identical to having
	// just sent the text of THIS invocation, which is not what happened — the
	// delivered response is the one frozen in the journal.
	if txn.State == session.ReplyTxnSent {
		fmt.Fprintf(stderr, "reply: resuming an interrupted reply — response %s was already delivered, finishing the archiving without sending again\n", txn.ResponseID)
	}

	if txn.State == session.ReplyTxnPending {
		delivered, err := deliverResponse(cfg, mgr, sid, txn)
		if err != nil {
			return fmt.Errorf("reply: %w", err)
		}
		txn.State = session.ReplyTxnSent
		if err := mgr.WithSessionLock(sid, func() error { return mgr.WriteReplyTxn(sid, txn) }); err != nil {
			return fmt.Errorf("reply: %w", err)
		}
		if !delivered {
			fmt.Fprintf(stderr, "reply: response %s was already delivered — completing the archiving without sending again\n", txn.ResponseID)
		}
	}

	// Archive exactly and only the frozen closeIDs, resuming from the index. By
	// id, never by re-scanning the directory: a message that arrived after the
	// snapshot must not be swept away unseen (§2.3 invariant).
	processedDir := filepath.Join(cfg.DataDir, "sessions", sid, "processed")
	inboxDir := filepath.Join(cfg.DataDir, "sessions", sid, "inbox")
	for txn.ArchivedIndex < len(txn.CloseIDs) {
		id := txn.CloseIDs[txn.ArchivedIndex]
		src := filepath.Join(inboxDir, id+".json")
		if err := transportfs.MoveToProcessed(src, processedDir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("reply: archive %s: %w", id, err)
		}
		txn.ArchivedIndex++
		if err := mgr.WithSessionLock(sid, func() error { return mgr.WriteReplyTxn(sid, txn) }); err != nil {
			return fmt.Errorf("reply: %w", err)
		}
	}

	if err := mgr.RemoveReplyTxn(sid); err != nil {
		return fmt.Errorf("reply: %w", err)
	}

	name := txn.To
	if mf, err := mgr.LoadManifest(txn.To); err == nil && mf.AgentName != "" {
		name = mf.AgentName
	}
	fmt.Fprintln(stdout, formatSendEcho(myName(mgr, sid), name, txn.ResponseID, txn.Content, "reply"))
	// Each closed ask with a preview and its age (CRI2 P2): a list of opaque ids
	// tells you HOW MANY you closed, never WHICH — and "did you notice what you
	// just closed?" is the question §2.3/F-34 puts precisely on this echo.
	for _, line := range describeClosed(cfg, sid, txn.CloseIDs) {
		fmt.Fprintf(stdout, "  closed: %s\n", line)
	}

	// What this reply LEFT open, without which the page rule has no second half:
	// closing one delivery is only safe if the ones it did not close are stated.
	// Read back from the mailbox AFTER archiving rather than frozen in the
	// journal — the journal would need keeping in sync with a set that keeps
	// changing, and the mailbox is the set.
	//
	// It lists asks from OTHER senders too. They were never closed by a reply to
	// this one, before this fix either; the note is deliberately true of both
	// cases instead of guessing which one the reader is in.
	//
	// Counted BEFORE the re-queue below, which turns exactly these into UNREAD:
	// counting after would fold them into "arrived that you have not seen yet" —
	// the same asks announced twice, and the second time described wrongly.
	unseen := countUnseenInbound(mgr, cfg, sid)

	if left, lerr := collectOpenAsks(mgr, cfg, sid); lerr != nil {
		fmt.Fprintf(stderr, "reply: could not list what stayed open (%v) — check with `cab-bridge inbox --list`\n", lerr)
	} else if len(left) > 0 {
		ids := make([]string, 0, len(left))
		for _, a := range left {
			ids = append(ids, a.id)
		}
		// Put them back in line, or the fix only MOVES the defect: nothing is
		// archived unread any more, but a NOTIFIED ask is shown by no later
		// `next` — that command delivers UNREAD only, and hangs on an empty wait
		// otherwise (verified on the real binary). It would sit invisible until
		// a join. This is the same move join makes after a compact, for the same
		// reason, and the model is already at-least-once with a re-delivery
		// marker (WakeCursor.Replayed), so seeing one twice costs nothing.
		//
		// With the re-arm-before-working discipline there is already a `next`
		// waiting when this runs, so the re-delivery lands immediately.
		requeueErr := mgr.ForgetNotified(sid, ids)
		if requeueErr == nil {
			fmt.Fprintln(stdout, "  still open (they come back on your next `next`; `cab-bridge read <id>` to see one now):")
		} else {
			// The sentence follows the OUTCOME, never the intention. Promising a
			// re-delivery that did not happen is precisely the defect this commit
			// exists to remove, and it would be the ninth time in two days.
			fmt.Fprintf(stderr, "reply: could not put the open asks back in line (%v) — they stay delivered-but-unanswered\n", requeueErr)
			fmt.Fprintln(stdout, "  still open (NOT re-delivered — `cab-bridge read <id>` to see one, then reply):")
		}
		for _, line := range describeOpen(left) {
			fmt.Fprintf(stdout, "    %s\n", line)
		}
	}

	// F-34 in its v0.8 shape. Nothing unseen is ever CLOSED — collectOpenAsks
	// only considers NOTIFIED messages — but the agent may still be answering
	// without knowing something newer arrived. Say so: the original finding was
	// "I am replying without having read the last thing sent to me", and that
	// half survives even though the dangerous half does not.
	if unseen > 0 {
		fmt.Fprintf(stderr, "reply: note: %d message(s) arrived that you have not seen yet — none was closed by this reply; run next to read them\n", unseen)
	}
	return nil
}

// formatSendEcho is the success line, and it is doing four jobs at once
// (CRI2 R-1) — with no new option, because it is pure confirmation:
//
//   - it shows a PREVIEW of what was actually sent, so a mistyped recipient
//     that silently became the message is visible instead of invisible;
//   - it names the VERB's contract ("open until they reply" vs "no reply
//     expected"), so using the wrong one is self-evident at the one moment it
//     could be caught;
//   - it names the SENDER, which closes the half of F-97 that lives on the
//     success path: the dangerous CAB_SESSION_ID case is a silent success from
//     the wrong directory, and until now only errors said who you were;
//   - it gives the size, so a truncated payload is noticeable.
//
// It is §0 applied to the output an agent sees most often, which we had only
// ever hardened on the error path.
func formatSendEcho(from, to, msgID, content, verb string) string {
	suffix := ""
	switch verb {
	case "ask":
		suffix = " — open until they reply"
	case "tell":
		suffix = " — no reply expected"
	}
	return fmt.Sprintf("%s → %s (%s) %q [%s]%s", from, to, msgID, previewContent(content, 40), humanSize(len(content)), suffix)
}

// myName labels the sender by agent name, falling back to the id.
func myName(mgr *session.Manager, sid string) string {
	if mf, err := mgr.LoadManifest(sid); err == nil && mf.AgentName != "" {
		return mf.AgentName
	}
	return sid
}

func humanSize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

// describeClosed renders each archived ask as "id · preview · age", reading it
// back from processed/ where reply has just put it. An id that cannot be read
// degrades to the bare id rather than failing the echo: this runs AFTER a
// successful delivery, and losing the confirmation over a cosmetic lookup would
// be a poor trade.
func describeClosed(cfg config.Config, sid string, ids []string) []string {
	processedDir := filepath.Join(cfg.DataDir, "sessions", sid, "processed")
	// Only the ids we were asked about. Reading and decoding the WHOLE archive
	// to look up three messages is O(archive) on a command of the working loop,
	// which is the very shape the brief ruled out for `sent`.
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	byID := map[string]*message.Message{}
	if entries, err := os.ReadDir(processedDir); err == nil {
		for _, e := range entries {
			id := archivedID(e.Name())
			if id == "" || !want[id] {
				continue
			}
			data, rerr := security.ReadOwnedFile(filepath.Join(processedDir, e.Name()))
			if rerr != nil {
				_ = security.WarnNotOurs(filepath.Join(processedDir, e.Name()), rerr)
				continue
			}
			if m, derr := message.DecodeLenient(data, cfg.MaxMessageBytes); derr == nil {
				byID[id] = m
			}
		}
	}

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		m, ok := byID[id]
		if !ok {
			out = append(out, id)
			continue
		}
		age := ""
		if t, terr := time.Parse(time.RFC3339, m.Timestamp); terr == nil {
			age = " · " + time.Since(t).Round(time.Minute).String() + " old"
		}
		out = append(out, fmt.Sprintf("%s · %q%s", id, previewContent(m.Content, 40), age))
	}
	return out
}

// describeOpen renders the asks a reply left open as "id · from NAME · preview
// · age". It needs no directory lookup: collectOpenAsks already decoded them.
//
// The sender is named on every line because the two reasons for staying open —
// a later delivery from the same agent, another agent entirely — read
// identically otherwise.
func describeOpen(asks []openAsk) []string {
	out := make([]string, 0, len(asks))
	for _, a := range asks {
		from := a.fromName
		if from == "" {
			from = a.from
		}
		age := ""
		if t, terr := time.Parse(time.RFC3339, a.when); terr == nil {
			age = " · " + time.Since(t).Round(time.Minute).String() + " old"
		}
		out = append(out, fmt.Sprintf("%s · from %s · %q%s", a.id, from, previewContent(a.content, 40), age))
	}
	return out
}

// countUnseenInbound counts messages sitting in inbox/ that no next has emitted
// yet (UNREAD). They are untouched by reply; this is purely so the agent knows
// they exist.
func countUnseenInbound(mgr *session.Manager, cfg config.Config, sid string) int {
	cursor, _, err := mgr.ReadWakeCursor(sid)
	if err != nil {
		return 0
	}
	entries, _, _, err := readMailbox(filepath.Join(cfg.DataDir, "sessions", sid, "inbox"), cfg.MaxMessageBytes)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !cursor.IsNotified(e.msg.ID) {
			n++
		}
	}
	return n
}

// deliverResponse writes the response into the recipient's inbox under the
// deterministic id, create-if-absent. delivered=false means it was already
// there byte-identical, i.e. a retry after a crash in the gap.
func deliverResponse(cfg config.Config, mgr *session.Manager, sid string, txn *session.ReplyTxn) (bool, error) {
	senderManifest, err := mgr.LoadManifest(sid)
	if err != nil {
		return false, fmt.Errorf("load sender manifest: %w", err)
	}
	targetManifest, err := mgr.LoadManifest(txn.To)
	if err != nil {
		return false, fmt.Errorf("load target manifest %q: %w", txn.To, err)
	}

	anchor := txn.Anchor
	m := &message.Message{
		ID:            txn.ResponseID,
		SchemaVersion: message.SchemaVersionV2,
		From:          sid,
		FromRole:      senderManifest.Role,
		FromAgentName: senderManifest.AgentName,
		To:            txn.To,
		ToRole:        targetManifest.Role,
		Type:          message.TypeResponse,
		Timestamp:     txn.Timestamp.UTC().Format(time.RFC3339Nano),
		Status:        message.StatusPending,
		Content:       txn.Content,
		InReplyTo:     &anchor,
		Closes:        txn.CloseIDs,
		Metadata: message.Metadata{
			FromProject:     senderManifest.ProjectName,
			FromScope:       session.EffectiveScope(senderManifest),
			ProcessingState: message.StatusPending,
		},
	}
	data, err := message.EncodeStrict(m, cfg.MaxMessageBytes)
	if err != nil {
		return false, err
	}

	// The delivery runs under the TARGET's session lock, and re-validates that
	// the target still exists inside it (CRI diff-gate P0-2). cleanup now takes
	// the same lock for its whole decide-archive-remove, so the two can no
	// longer interleave into the case where a response is linked in and then
	// deleted by a RemoveAll that had already snapshotted the directory —
	// leaving the ask closed and the answer nowhere.
	//
	// Only ONE lock is held at a time: the responder's own was released before
	// this point, so there is no ordering to get wrong.
	targetInbox := filepath.Join(cfg.DataDir, "sessions", txn.To, "inbox")
	var created bool
	if err := mgr.WithSessionLock(txn.To, func() error {
		if _, rerr := mgr.LoadManifest(txn.To); rerr != nil {
			return fmt.Errorf("recipient %s disappeared before delivery: %w", txn.To, rerr)
		}
		if merr := os.MkdirAll(targetInbox, 0o700); merr != nil {
			return fmt.Errorf("mkdir target inbox: %w", merr)
		}
		var werr error
		created, werr = transportfs.WriteIfAbsentBytes(filepath.Join(targetInbox, txn.ResponseID+".json"), data, 0o600)
		return werr
	}); err != nil {
		return false, fmt.Errorf("deliver response: %w", err)
	}

	senderOutbox := filepath.Join(cfg.DataDir, "sessions", sid, "outbox")
	if mkErr := os.MkdirAll(senderOutbox, 0o700); mkErr == nil {
		_, _ = transportfs.WriteIfAbsentBytes(filepath.Join(senderOutbox, txn.ResponseID+".json"), data, 0o600)
	}
	return created, nil
}
