package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-176: the journal of `reply` is created once and then only changed by the operations that
// check it is still the caller's (UpdateReplyTxn, CompleteReplyTxn). WriteReplyTxn replaces
// whatever file is there, so who may call it is a list that fails: a new caller is a decision
// made in a diff, with the reason next to the name.
//
// The two permitted callers are the CREATION of a journal (beginReply, reply_core.go) and the
// write inside UpdateReplyTxn, after its check. Both run inside the session lock: beginReply
// inside the function it hands to WithSessionLock, UpdateReplyTxn inside the one it hands to
// the same method; a write that slipped outside would be a check followed by a gamble. That the
// check and the write share ONE section is fixed next to the code, in
// internal/session/replytxn_section_test.go.
var writeReplyTxnCallers = map[string]string{
	"cmd/cab-bridge/reply_core.go:beginReply":     "creates the journal of a new reply, under the session lock, after the selection",
	"internal/session/replytxn.go:UpdateReplyTxn": "records progress, under the session lock, after checking that the journal is still the caller's",
}

func TestWriteReplyTxn_OnlyTheListedFunctionsCallItAndInsideTheSessionLock(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	found := map[string]int{}
	unlocked := map[string]bool{}
	fset := token.NewFileSet()
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		name := d.Name()
		if d.IsDir() {
			// Hidden directories hold other checkouts and tooling (.git, .worktrees).
			if p != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, parser.SkipObjectResolution)
		require.NoError(t, perr, p)
		rel, rerr := filepath.Rel(root, p)
		require.NoError(t, rerr)
		rel = filepath.ToSlash(rel)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := rel + ":" + fn.Name.Name
			n, inLock := writeReplyTxnCalls(fn)
			if n == 0 {
				continue
			}
			found[key] += n
			if !inLock {
				unlocked[key] = true
			}
		}
		return nil
	}))

	var unlisted, stale, outside []string
	for key := range found {
		if _, ok := writeReplyTxnCallers[key]; !ok {
			unlisted = append(unlisted, key)
		}
	}
	for key := range writeReplyTxnCallers {
		if found[key] != 1 {
			stale = append(stale, key)
		}
	}
	for key := range unlocked {
		outside = append(outside, key)
	}
	sort.Strings(unlisted)
	sort.Strings(stale)
	sort.Strings(outside)

	assert.Empty(t, unlisted, "these functions call WriteReplyTxn and are not listed: it replaces whatever journal is on disk, "+
		"so a driver that already holds a transaction must use UpdateReplyTxn or CompleteReplyTxn (F-176)")
	assert.Empty(t, stale, "a listed caller must call WriteReplyTxn exactly once; fix the list or the code")
	assert.Empty(t, outside, "these callers write the journal outside the function they hand to WithSessionLock")
}

// writeReplyTxnCalls counts the calls to a method named WriteReplyTxn inside fn, and says
// whether every one of them is lexically inside a function literal that is an argument of a
// call to a method named WithSessionLock.
func writeReplyTxnCalls(fn *ast.FuncDecl) (calls int, allInLock bool) {
	allInLock = true
	var walk func(n ast.Node, inLock bool)
	walk = func(n ast.Node, inLock bool) {
		ast.Inspect(n, func(c ast.Node) bool {
			call, ok := c.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name == "WriteReplyTxn" {
				calls++
				if !inLock {
					allInLock = false
				}
			}
			if sel.Sel.Name == "WithSessionLock" {
				// Its function-literal arguments are walked as locked; the rest is walked
				// normally by the enclosing Inspect.
				for _, arg := range call.Args {
					if lit, isLit := arg.(*ast.FuncLit); isLit {
						walk(lit.Body, true)
					}
				}
				return false
			}
			return true
		})
	}
	walk(fn.Body, false)
	return calls, allInLock
}
