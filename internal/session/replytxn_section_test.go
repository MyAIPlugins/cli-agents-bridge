package session

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-176 (CRI2, N2): UpdateReplyTxn and CompleteReplyTxn are safe because the check that the
// journal on disk is still the caller's and the write or removal it guards run in ONE critical
// section. Two sections (check under one lock, act under another) are a check and then a gamble,
// and no behavioural test can tell them apart: a process that holds the lock for the whole call
// blocks both forms the same way. So the rule is fixed on the shape of the source, the way the
// repository fixes such rules (see also the callers' guard in cmd/cab-bridge, which says WHO may
// write a journal; this one says what the two operations that may must look like).

// journalOperations maps each operation that changes the journal of an existing reply to the
// call that does the change. Listing a new one is a decision made in a diff.
var journalOperations = map[string]string{
	"UpdateReplyTxn":   "WriteReplyTxn",
	"CompleteReplyTxn": "RemoveFile",
}

const journalCheck = "checkJournalIsMine"

// callsNamed returns the calls, anywhere under n, to a function or method called name.
func callsNamed(n ast.Node, name string) []*ast.CallExpr {
	var out []*ast.CallExpr
	ast.Inspect(n, func(c ast.Node) bool {
		call, ok := c.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel.Name == name {
				out = append(out, call)
			}
		case *ast.Ident:
			if fun.Name == name {
				out = append(out, call)
			}
		}
		return true
	})
	return out
}

// criticalSectionViolations says what is wrong, if anything, with how fn arranges the lock, the
// check and the action: one call to WithSessionLock, given a function literal, and inside that
// literal, and nowhere else, exactly one call to the check followed by exactly one to the action.
func criticalSectionViolations(fn *ast.FuncDecl, action string) []string {
	locks := callsNamed(fn.Body, "WithSessionLock")
	if len(locks) != 1 {
		return []string{fmt.Sprintf("%d calls to WithSessionLock, want exactly 1: the check and the %s must share one critical section", len(locks), action)}
	}
	var section *ast.FuncLit
	for _, arg := range locks[0].Args {
		if lit, ok := arg.(*ast.FuncLit); ok {
			section = lit
		}
	}
	if section == nil {
		return []string{"WithSessionLock is not given a function literal, so the section cannot be read: write the check and the " + action + " inline in it"}
	}

	var out []string
	var checkIn, actIn *ast.CallExpr
	for _, step := range []struct {
		name string
		slot **ast.CallExpr
	}{{journalCheck, &checkIn}, {action, &actIn}} {
		inside := callsNamed(section.Body, step.name)
		all := callsNamed(fn.Body, step.name)
		if len(inside) != 1 || len(all) != 1 {
			out = append(out, fmt.Sprintf("%s must be called exactly once and inside the function given to WithSessionLock (found %d inside, %d in all)", step.name, len(inside), len(all)))
			continue
		}
		*step.slot = inside[0]
	}
	if checkIn != nil && actIn != nil && actIn.Pos() < checkIn.Pos() {
		out = append(out, fmt.Sprintf("the %s comes before %s: the check must come first", action, journalCheck))
	}
	return out
}

func TestReplyTxnOperations_CheckAndActShareOneCriticalSection(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "replytxn.go", nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	decls := map[string]*ast.FuncDecl{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			decls[fn.Name.Name] = fn
		}
	}

	for op, action := range journalOperations {
		fn, ok := decls[op]
		require.True(t, ok, "%s is not in replytxn.go: fix journalOperations or the code", op)
		assert.Empty(t, criticalSectionViolations(fn, action), "%s", op)
	}

	// The branch next to it: a function of this file that writes or removes the journal and is
	// not in the table has no guard at all. WriteReplyTxn itself is the primitive; who calls it is
	// the other guard's business.
	var unlisted []string
	for name, fn := range decls {
		if _, listed := journalOperations[name]; listed || name == "WriteReplyTxn" {
			continue
		}
		for _, action := range journalOperations {
			if len(callsNamed(fn.Body, action)) > 0 {
				unlisted = append(unlisted, name+" calls "+action)
			}
		}
	}
	sort.Strings(unlisted)
	assert.Empty(t, unlisted, "these functions change the journal and are not in journalOperations: list them with their check, "+
		"or make them call UpdateReplyTxn or CompleteReplyTxn")
}

// The guard above is only worth anything if it can fail. These are the shapes it must refuse and
// the one it must accept, written as source so that a later edit of the guard cannot quietly
// make it accept everything.
func TestCriticalSectionViolations_RefuseEveryShapeThatSplitsTheCheckFromTheAction(t *testing.T) {
	cases := []struct {
		name string
		body string
		bad  bool
	}{
		{"one section, check then action", `return m.WithSessionLock(s, func() error {
			if err := m.checkJournalIsMine(s, x); err != nil { return err }
			return m.WriteReplyTxn(s, x)
		})`, false},
		{"two sections", `if err := m.WithSessionLock(s, func() error { return m.checkJournalIsMine(s, x) }); err != nil { return err }
		return m.WithSessionLock(s, func() error { return m.WriteReplyTxn(s, x) })`, true},
		{"check outside the lock", `if err := m.checkJournalIsMine(s, x); err != nil { return err }
		return m.WithSessionLock(s, func() error { return m.WriteReplyTxn(s, x) })`, true},
		{"check inside, action outside", `if err := m.WithSessionLock(s, func() error { return m.checkJournalIsMine(s, x) }); err != nil { return err }
		return m.WriteReplyTxn(s, x)`, true},
		{"no lock at all", `if err := m.checkJournalIsMine(s, x); err != nil { return err }
		return m.WriteReplyTxn(s, x)`, true},
		{"no check", `return m.WithSessionLock(s, func() error { return m.WriteReplyTxn(s, x) })`, true},
		{"action before check", `return m.WithSessionLock(s, func() error {
			if err := m.WriteReplyTxn(s, x); err != nil { return err }
			return m.checkJournalIsMine(s, x)
		})`, true},
		{"a named function instead of a literal", `work := func() error {
			if err := m.checkJournalIsMine(s, x); err != nil { return err }
			return m.WriteReplyTxn(s, x)
		}
		return m.WithSessionLock(s, work)`, true},
		{"the check twice", `return m.WithSessionLock(s, func() error {
			_ = m.checkJournalIsMine(s, x)
			if err := m.checkJournalIsMine(s, x); err != nil { return err }
			return m.WriteReplyTxn(s, x)
		})`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "package p\nfunc (m *M) Op(s string, x *T) error {\n" + c.body + "\n}\n"
			f, err := parser.ParseFile(token.NewFileSet(), "op.go", src, parser.SkipObjectResolution)
			require.NoError(t, err)
			fn, ok := f.Decls[0].(*ast.FuncDecl)
			require.True(t, ok)
			got := criticalSectionViolations(fn, "WriteReplyTxn")
			if c.bad {
				assert.NotEmpty(t, got, "this shape must be refused")
			} else {
				assert.Empty(t, got, "this shape must be accepted")
			}
		})
	}
}
