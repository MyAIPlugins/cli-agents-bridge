package session

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
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
// What this guard fixes is a PROPERTY of the source that a behavioural test cannot
// see from here: that the removal goes through the one function that asks again. It
// does not prove that the removal works or that the path is the right one: that is
// TestDesktopGrant_CloseIsIdempotent and its neighbours, and the probe that runs the
// real close against the real race on Windows. It is coupled to the source on
// purpose, like the layout guards in cmd/cab-bridge: it parses with go/parser, so a
// comment or a docstring can neither satisfy it nor trip it.
//
// Only the grant is in the list. The reply journal is NOT: it was examined for this
// and refused, because its name is reused by a different transaction and the retry
// would delete the journal that transaction just created. Adding a file here is
// claiming that dropping its newer content is a valid outcome (see RemoveFile).
var removedThroughTheHelper = []string{"desktopgrant.go"}

func TestReplacedInPlaceNamesAreRemovedThroughTheHelper(t *testing.T) {
	for _, name := range removedThroughTheHelper {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err, name)

		osName := importedAs(f, "os")
		secName := importedAs(f, "github.com/myAIPlugins/cli-agents-bridge/internal/security")
		require.NotEmpty(t, secName, "%s must import the security package to remove through it", name)

		var bare []string
		helper := 0
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case osName != "" && pkg.Name == osName && (sel.Sel.Name == "Remove" || sel.Sel.Name == "RemoveAll"):
				bare = append(bare, fset.Position(call.Pos()).String())
			case pkg.Name == secName && sel.Sel.Name == "RemoveFile":
				helper++
			}
			return true
		})

		assert.Empty(t, bare, "%s removes a name that is replaced in place with the bare call: use security.RemoveFile (F-170)", name)
		assert.GreaterOrEqual(t, helper, 1, "%s must remove through security.RemoveFile", name)
	}
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
