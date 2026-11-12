package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-170, a structural guard on WHO calls RemoveFile. The doc of RemoveFile says what a
// caller must accept (the retry deletes by name, so it may remove the newer file a replace
// put there), but a doc does not fail: a file that calls it without reading the doc keeps
// every test green. This test enumerates every non-test caller in the repository and goes
// red on one that is not listed, so a new caller is a decision made in a diff, with the
// reason written next to the name. A listed file that stops calling it is red too: a stale
// entry would say that someone accepted a contract they no longer use.
//
// It parses with go/parser, so a comment or a docstring can neither satisfy it nor trip it.
// What it does NOT see, declared rather than discovered later: a call through a function
// value stored from another package (it sees the reference to the name, not what is done
// with it later), reflection, and a removal that never goes through this function (a bare
// os.Remove, syscall.Unlink or golang.org/x/sys); the last one is the half that
// TestBareRemovalsInSessionAreNamedExceptions covers for the session package.
var removeFileCallers = map[string]string{
	"internal/session/desktopgrant.go": "the desktop grant: an open and a close that overlap have two valid orders, and a revoke may drop the newer grant",
	"internal/session/replytxn.go": "the reply journal, ONLY under the exclusive session lock and AFTER the identity comparison in the same critical section " +
		"(CompleteReplyTxn): the name is reused by other transactions, so what makes the blind retry safe here is that protocol and not the helper (F-176)",
}

const securityImportPath = "github.com/myAIPlugins/cli-agents-bridge/internal/security"

func TestRemoveFile_OnlyTheListedFilesCallIt(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)

	found := map[string]int{}
	fset := token.NewFileSet()
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		name := d.Name()
		if d.IsDir() {
			// Hidden directories hold other checkouts and tooling (.git, .worktrees), and
			// those would count the same file twice; the rest hold no code of the module.
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
		if n := removeFileReferences(f); n > 0 {
			found[filepath.ToSlash(rel)] = n
		}
		return nil
	}))

	var unlisted, stale []string
	for file := range found {
		if _, ok := removeFileCallers[file]; !ok {
			unlisted = append(unlisted, file)
		}
	}
	for file := range removeFileCallers {
		if found[file] == 0 {
			stale = append(stale, file)
		}
	}
	sort.Strings(unlisted)
	sort.Strings(stale)

	assert.Empty(t, unlisted, "these files call security.RemoveFile and are not listed: its retry deletes by name and may remove "+
		"the newer file that a replace put there, so read the doc of RemoveFile, decide that losing it is a valid outcome for "+
		"your name, and list the file in removeFileCallers with the reason")
	assert.Empty(t, stale, "these listed files no longer call security.RemoveFile: remove them from removeFileCallers")
}

// removeFileReferences counts the references to security.RemoveFile in a file: through the
// import (whatever local name it has), or unqualified inside package security itself. A
// reference counts whether or not it is a call, so storing the function in a variable is seen
// too; the declaration of the function does not count.
func removeFileReferences(f *ast.File) int {
	local := ""
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != securityImportPath {
			continue
		}
		local = filepath.Base(p)
		if imp.Name != nil {
			local = imp.Name.Name
		}
	}
	inSecurity := f.Name.Name == "security"

	n := 0
	// skip holds the identifiers that are not a reference to the package-level function: its
	// own declaration, and the selected name of any x.RemoveFile (which Inspect would also
	// visit as a bare identifier). A parent is visited before its children, so it fills this
	// before the child is reached.
	skip := map[*ast.Ident]bool{}
	ast.Inspect(f, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.FuncDecl:
			skip[x.Name] = true
		case *ast.SelectorExpr:
			skip[x.Sel] = true
			if id, ok := x.X.(*ast.Ident); ok && local != "" && id.Name == local && x.Sel.Name == "RemoveFile" {
				n++
			}
		case *ast.Ident:
			if inSecurity && x.Name == "RemoveFile" && !skip[x] {
				n++
			}
		}
		return true
	})
	return n
}
