package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-170, a structural guard. The grant file is replaced in place (an open writes it
// by rename over the old one), and on Windows a delete that crosses that replace or
// another delete is answered ACCESS_DENIED for a few milliseconds before it gets the
// true answer. security.RemoveFile asks again; the bare os.Remove does not, and
// putting it back would reopen the defect with every other test still green.
//
// It scans EVERY non-test file of this package, not a list of files: a removal added
// in a file nobody thought of is the realistic way to reopen the defect (CRI2, N1).
// Any reference to os.Remove or os.RemoveAll (a call, or the function taken as a
// value) that is not one of the named exceptions below fails the test, and an
// exception that no longer matches fails it too, so the list cannot go stale.
//
// What this guard fixes is a PROPERTY of the source that a behavioural test cannot
// see from here. It does not prove that the removal works or that the path is the
// right one: that is TestDesktopGrant_CloseIsIdempotent and its neighbours, the
// wiring test of RemoveFile in internal/security, and the probe that runs the real
// close against the real race on Windows. It is coupled to the source on purpose,
// like the layout guards in cmd/cab-bridge: it parses with go/parser, so a comment
// or a docstring can neither satisfy it nor trip it.
//
// What it does NOT see, declared rather than discovered later: a removal that does
// not go through the os package (syscall.Unlink, golang.org/x/sys), and the other
// packages of the module (internal/cleanup removes names too, under the session lock
// or by another protocol, and is out of this lot by name). The reverse question, who
// may call security.RemoveFile, is TestRemoveFile_OnlyTheListedFilesCallIt there.
//
// The exceptions are keyed by "file:function" and say how many references each keeps.
// Adding one is claiming that the retry is not wanted for that name, with the reason.
var bareRemovals = map[string]struct {
	count int
	why   string
}{
	"manager.go:Register": {2, "rolls back the session directory this very call created, on an error path, " +
		"and discards the error: nothing else has the name yet, and a leftover directory is the lesser evil"},
	"replytxn.go:RemoveReplyTxn": {1, "the reply journal: its name is reused by a different transaction, so the blind retry " +
		"of RemoveFile would delete the journal that transaction just created (the F-170 plan gate, with an executed interleaving)"},
}

func TestBareRemovalsInSessionAreNamedExceptions(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	got := map[string]int{}
	helperCalls := 0
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, perr, name)
		osName := importedAs(f, "os")
		secName := importedAs(f, "github.com/myAIPlugins/cli-agents-bridge/internal/security")

		for _, decl := range f.Decls {
			// The enclosing function, or a marker for a package-level declaration (a
			// var holding os.Remove is a reference too).
			where := "(package level)"
			if fn, ok := decl.(*ast.FuncDecl); ok {
				where = fn.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				switch {
				case osName != "" && pkg.Name == osName && (sel.Sel.Name == "Remove" || sel.Sel.Name == "RemoveAll"):
					got[name+":"+where]++
				case name == "desktopgrant.go" && secName != "" && pkg.Name == secName && sel.Sel.Name == "RemoveFile":
					helperCalls++
				}
				return true
			})
		}
	}

	var unexpected, stale []string
	for key, n := range got {
		if want, ok := bareRemovals[key]; !ok || want.count != n {
			unexpected = append(unexpected, key+" (found "+strconv.Itoa(n)+")")
		}
	}
	for key, want := range bareRemovals {
		if got[key] != want.count {
			stale = append(stale, key+" (expected "+strconv.Itoa(want.count)+", found "+strconv.Itoa(got[key])+")")
		}
	}
	sort.Strings(unexpected)
	sort.Strings(stale)

	assert.Empty(t, unexpected, "a bare os.Remove or os.RemoveAll in the session package that is not a named exception: "+
		"a name that is replaced in place must go through security.RemoveFile (F-170); if the retry is not wanted for this name, "+
		"name the exception in bareRemovals with the reason")
	assert.Empty(t, stale, "a named exception no longer matches the source: fix or remove it in bareRemovals")
	assert.GreaterOrEqual(t, helperCalls, 1, "desktopgrant.go must remove the grant through security.RemoveFile")
}

// importedAs returns the local name a file gives the package at path ("" when it
// does not import it): the last element of the path unless it is renamed.
func importedAs(f *ast.File, path string) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != path {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		for i := len(p) - 1; i >= 0; i-- {
			if p[i] == '/' {
				return p[i+1:]
			}
		}
		return p
	}
	return ""
}
