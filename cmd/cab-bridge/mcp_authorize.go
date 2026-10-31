package main

import (
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// What Claude Desktop is allowed to SEE (F-72, D3, C1).
//
// A message reaches the agent through ONE door: authorizeAuthor. It looks at the
// message's real author on disk, not at anything the message says about itself,
// and returns either a visibleMessage or a withheld notice. Every result the MCP
// tools build takes visibleMessage and nothing else; its fields are unexported
// and this file holds its only constructor. A message that was not authorized does
// not compile into a result, which is a stronger statement than "the tools were
// careful".
//
// Authorization is a fact about one instant. An authorizer is built for ONE pass
// (one poll, one final check before emitting), memoizes the manifests it read only
// inside that pass, and is thrown away: nothing about who is authorized survives
// into the next pass or the next call. A `next` that waits forty seconds builds a
// new one at every poll and again before it returns, because a val can close the
// bridge in the middle of the wait and the data about to be returned must not be
// judged by what was true when the wait began (C1).

// authDeps is what authorization reads. *session.Manager satisfies it.
type authDeps interface {
	LoadManifest(sessionID string) (*session.Manifest, error)
	ReadDesktopGrant(sessionID string) (*session.DesktopGrant, bool, error)
}

type withheldReason string

const (
	reasonNotVal       withheldReason = "author-not-val"
	reasonOtherScope   withheldReason = "author-other-scope"
	reasonNotOpen      withheldReason = "author-not-open"
	reasonUnresolvable withheldReason = "author-unresolvable"
)

// withheld is everything the server says about a message it holds back: that it
// exists and why. Deliberately NOTHING else, not the author's name, not a size, not
// a preview: the name of an executor must not reach Claude Desktop through the
// notice that its message was refused.
type withheld struct {
	ID     string         `json:"id"`
	Reason withheldReason `json:"reason"`
}

// visibleMessage is a message the author of which is authorized at the moment it
// was built. The author's name is the one on the author's manifest, never the one
// the message claims.
type visibleMessage struct {
	id         string
	typ        string
	from       string
	authorName string
	timestamp  string
	content    string
	bytes      int
}

func (v visibleMessage) ID() string         { return v.id }
func (v visibleMessage) Type() string       { return v.typ }
func (v visibleMessage) From() string       { return v.from }
func (v visibleMessage) AuthorName() string { return v.authorName }
func (v visibleMessage) Timestamp() string  { return v.timestamp }
func (v visibleMessage) Content() string    { return v.content }
func (v visibleMessage) Bytes() int         { return v.bytes }

// verdict is what one pass decided about one author.
type verdict struct {
	name   string
	reason withheldReason // empty: authorized
}

type authorizer struct {
	deps    authDeps
	scope   string
	now     time.Time
	judged  map[string]verdict
	reading int // manifests actually read, for the tests that check the memoization
}

// newAuthorizer builds the authorizer of ONE pass, for the project whose absolute
// scope is scope, judging the grants against now.
func newAuthorizer(deps authDeps, scope string, now time.Time) *authorizer {
	return &authorizer{deps: deps, scope: scope, now: now, judged: map[string]verdict{}}
}

// authorize returns the message as visible, or a notice that it is withheld.
func (a *authorizer) authorize(m *message.Message, size int) (visibleMessage, *withheld) {
	v := a.judge(m.From)
	if v.reason != "" {
		return visibleMessage{}, &withheld{ID: m.ID, Reason: v.reason}
	}
	return visibleMessage{
		id: m.ID, typ: m.Type, from: m.From, authorName: v.name,
		timestamp: m.Timestamp, content: m.Content, bytes: size,
	}, nil
}

// judge decides about an author once per pass.
func (a *authorizer) judge(from string) verdict {
	if v, ok := a.judged[from]; ok {
		return v
	}
	v := a.decide(from)
	a.judged[from] = v
	return v
}

func (a *authorizer) decide(from string) verdict {
	// The id names a directory: refuse anything that is not a session id BEFORE
	// it is used to build a path (SC-4).
	if err := security.ValidateSessionID(from); err != nil {
		return verdict{reason: reasonUnresolvable}
	}
	a.reading++
	mf, err := a.deps.LoadManifest(from)
	if err != nil {
		return verdict{reason: reasonUnresolvable}
	}
	if mf.Role != session.RoleVal {
		return verdict{reason: reasonNotVal}
	}
	if !session.SameProject(session.EffectiveScope(mf), a.scope) {
		return verdict{reason: reasonOtherScope}
	}
	g, found, err := a.deps.ReadDesktopGrant(from)
	// An unreadable grant counts as closed (ReadDesktopGrant says why, to whoever
	// asks it directly); a grant without an expiry is not open (ValidAt).
	if err != nil || !found || !g.ValidAt(a.now) {
		return verdict{reason: reasonNotOpen}
	}
	return verdict{name: mf.AgentName}
}

// allows says whether this pass lets the session `id` be spoken to or listened to: a
// val of this project whose bridge is open. It is the same judgement as for an author,
// asked about a recipient, so that what Desktop may read and whom it may write to cannot
// drift apart.
func (a *authorizer) allows(id string) bool { return a.judge(id).reason == "" }

// authorizeEntries splits a scan into what may be shown and what is held back, in
// the order given.
func (a *authorizer) authorizeEntries(entries []mailboxEntry) (visible []visibleMessage, held []withheld) {
	for _, e := range entries {
		v, w := a.authorize(e.msg, e.bytes)
		if w != nil {
			held = append(held, *w)
			continue
		}
		visible = append(visible, v)
	}
	return visible, held
}
