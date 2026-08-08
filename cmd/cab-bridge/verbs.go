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
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// The three sending verbs of the LOOP surface (DESIGN v0.8 §2.2):
//
//	ask <who> "..."    asking — expects an answer, stays open until replied to
//	tell <who> "..."   informing — fire and forget
//	reply "..."        answering whoever asked — archives their open asks
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
func resolveMessagePayload(arg string, hasArg bool, stdin io.Reader) (string, error) {
	if hasArg {
		if strings.TrimSpace(arg) == "" {
			return "", errors.New("empty message")
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
func resolveRecipientByName(cfg config.Config, mgr *session.Manager, name, selfSID string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve recipient: cwd: %w", err)
	}
	peers, _, err := collectPeers(mgr, cfg.DataDir, cfg.StaleSeconds, true, "", resolveScope(cwd))
	if err != nil {
		return "", fmt.Errorf("resolve recipient: %w", err)
	}

	var exact, live []peerSummary
	for _, p := range peers {
		if p.SessionID == selfSID || p.AgentName != name {
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
		return candidates[0].SessionID, nil
	case 0:
		known := knownAgentNames(peers, selfSID)
		if len(known) == 0 {
			return "", fmt.Errorf("no agent named %q in this scope, and no other agent is registered here", name)
		}
		return "", fmt.Errorf("no agent named %q in this scope — registered here: %s", name, strings.Join(known, ", "))
	default:
		var ids []string
		for _, c := range candidates {
			ids = append(ids, c.SessionID)
		}
		return "", fmt.Errorf("%d live agents are named %q (%s) — this is ambiguous, so nothing was sent; clean up the duplicates with `cab-bridge cleanup --session-id=<id>`",
			len(candidates), name, strings.Join(ids, ", "))
	}
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

// openAsk is one query still awaiting a reply.
type openAsk struct {
	id       string
	from     string
	fromName string
	path     string
	when     string
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
	entries, _, err := readMailbox(inboxDir, cfg.MaxMessageBytes)
	if err != nil {
		return nil, err
	}

	var out []openAsk
	for _, e := range entries {
		if e.msg.Type != message.TypeQuery || !cursor.IsNotified(e.msg.ID) {
			continue
		}
		out = append(out, openAsk{
			id:       e.msg.ID,
			from:     e.msg.From,
			fromName: e.msg.FromAgentName,
			path:     e.path,
			when:     e.msg.Timestamp,
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

// --- ask / tell -------------------------------------------------------------

func runAskVerb(args []string) error {
	return runSendVerb("ask", message.TypeQuery, args, os.Stdin, os.Stdout)
}

func runTell(args []string) error {
	return runSendVerb("tell", message.TypeNotify, args, os.Stdin, os.Stdout)
}

func runSendVerb(verb, msgType string, args []string, stdin io.Reader, stdout io.Writer) error {
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
		return fmt.Errorf("%s: %w", verb, err)
	}
	to, err := resolveRecipientByName(cfg, mgr, args[0], sid)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}

	msgID, err := sendMessage(cfg, mgr, sid, to, msgType, content, nil, false)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	fmt.Fprintf(stdout, "→ %s (%s)\n", args[0], msgID)
	return nil
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

	// Resume first: an in-flight journal outranks anything the arguments say,
	// because finishing a delivered reply must never depend on the retry being
	// spelled the same way.
	if txn, found, terr := mgr.ReadReplyTxn(sid); terr != nil {
		return fmt.Errorf("reply: %w", terr)
	} else if found {
		return finishReplyTxn(mgr, cfg, sid, txn, stdout, stderr)
	}

	asks, err := collectOpenAsks(mgr, cfg, sid)
	if err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	if len(asks) == 0 {
		return errors.New("reply: nothing to reply to — no ask of yours is open (a tell is fire-and-forget: answer it with tell or ask)")
	}

	target, content, err := resolveReplyTarget(args, asks, stdin)
	if err != nil {
		return fmt.Errorf("reply: %w", err)
	}

	var closeIDs []string
	for _, a := range asks {
		if a.from == target {
			closeIDs = append(closeIDs, a.id)
		}
	}

	txn := &session.ReplyTxn{
		ResponseID: session.DeterministicResponseID(sid, closeIDs[0]),
		To:         target,
		Anchor:     closeIDs[0],
		CloseIDs:   closeIDs,
		State:      session.ReplyTxnPending,
		Timestamp:  time.Now().UTC(),
		Content:    content,
	}
	// Freeze the set under the session lock. An ask that lands after this
	// snapshot stays open and will be closed by the NEXT reply.
	if err := mgr.WithSessionLock(sid, func() error { return mgr.WriteReplyTxn(sid, txn) }); err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	return finishReplyTxn(mgr, cfg, sid, txn, stdout, stderr)
}

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
func resolveReplyTarget(args []string, asks []openAsk, stdin io.Reader) (target, content string, err error) {
	senders := map[string]string{} // sessionID -> agent name
	byName := map[string]string{}  // agent name -> sessionID
	for _, a := range asks {
		senders[a.from] = a.fromName
		if a.fromName != "" {
			byName[a.fromName] = a.from
		}
	}

	switch len(args) {
	case 2:
		sid, ok := byName[args[0]]
		if !ok {
			return "", "", fmt.Errorf("%q has no open ask of yours — open asks are from: %s", args[0], strings.Join(sortedNames(senders), ", "))
		}
		content, err = resolveMessagePayload(args[1], true, stdin)
		return sid, content, err

	case 1:
		if sid, ok := byName[args[0]]; ok {
			content, err = resolveMessagePayload("", false, stdin)
			return sid, content, err
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
	fmt.Fprintf(stdout, "→ %s (%s, closed: %s)\n", name, txn.ResponseID, strings.Join(txn.CloseIDs, ", "))
	return nil
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
			ProcessingState: message.StatusPending,
		},
	}
	data, err := message.EncodeStrict(m, cfg.MaxMessageBytes)
	if err != nil {
		return false, err
	}

	targetInbox := filepath.Join(cfg.DataDir, "sessions", txn.To, "inbox")
	if err := os.MkdirAll(targetInbox, 0o700); err != nil {
		return false, fmt.Errorf("mkdir target inbox: %w", err)
	}
	created, err := transportfs.WriteIfAbsentBytes(filepath.Join(targetInbox, txn.ResponseID+".json"), data, 0o600)
	if err != nil {
		return false, fmt.Errorf("deliver response: %w", err)
	}

	senderOutbox := filepath.Join(cfg.DataDir, "sessions", sid, "outbox")
	if mkErr := os.MkdirAll(senderOutbox, 0o700); mkErr == nil {
		_, _ = transportfs.WriteIfAbsentBytes(filepath.Join(senderOutbox, txn.ResponseID+".json"), data, 0o600)
	}
	return created, nil
}
