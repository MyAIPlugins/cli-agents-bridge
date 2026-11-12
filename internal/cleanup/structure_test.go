package cleanup

// Two properties of this package that no behavioural test can hold, because breaking them
// changes nothing until a Windows file is held open:
//
//   - it makes no wait of its own. F-171 sends the rename of the archive step through the
//     retry of F-133 and says "no budget of its own": a time.Sleep added here would be a
//     second budget that no test of the normal path would ever notice.
//   - it moves files only through transportfs.RenameAtomic. A bare os.Rename compiles, passes
//     every test on Unix (where the two are the same call) and goes back to the Windows
//     behaviour F-133 exists to replace.
//
// Both are read from the syntax tree of the non-test files, by what each name IS (the import
// path behind it) and not by how it is spelled. WHAT THIS DOES NOT SEE: a wait done in
// another package or with a channel, a rename done with syscall or x/sys directly (only
// syscall.Rename is listed), and a local variable that shadows an import name. It is a
// guard against the obvious regression, not a proof.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const transportFSImport = "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"

// reference is one use of "<import path>.<name>" in a non-test file.
type reference struct {
	key  string // import path + "." + selector, for example "time.Sleep"
	file string
	line int
}

func referencesInPackage(t *testing.T) []reference {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var refs []reference
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, perr, name)
		local := map[string]string{} // name used in this file -> import path
		for _, imp := range f.Imports {
			p, uerr := strconv.Unquote(imp.Path.Value)
			require.NoError(t, uerr)
			n := path.Base(p)
			if imp.Name != nil {
				n = imp.Name.Name
			}
			local[n] = p
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if p, imported := local[id.Name]; imported {
				refs = append(refs, reference{key: p + "." + sel.Sel.Name, file: name, line: fset.Position(sel.Pos()).Line})
			}
			return true
		})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].key < refs[j].key })
	return refs
}

func TestCleanup_HasNoWaitOfItsOwn(t *testing.T) {
	t.Parallel()
	forbidden := map[string]bool{
		"time.Sleep": true, "time.After": true, "time.AfterFunc": true,
		"time.Tick": true, "time.NewTimer": true, "time.NewTicker": true,
	}
	refs := referencesInPackage(t)
	require.NotEmpty(t, refs, "premise: the scan saw the package's references")
	sawTime := false
	for _, r := range refs {
		if strings.HasPrefix(r.key, "time.") {
			sawTime = true // the scan does resolve the time package here (time.Time, time.Duration)
		}
		assert.False(t, forbidden[r.key], "%s:%d uses %s: the retry of F-133 is the only wait of the archive step, and a second one is a second budget", r.file, r.line, r.key)
	}
	assert.True(t, sawTime, "premise: the scan resolved the time import, so an absence of Sleep means something")
}

func TestCleanup_MovesFilesOnlyThroughRenameAtomic(t *testing.T) {
	t.Parallel()
	forbidden := map[string]bool{"os.Rename": true, "syscall.Rename": true}
	renameAtomicUses := 0
	for _, r := range referencesInPackage(t) {
		assert.False(t, forbidden[r.key], "%s:%d uses %s: on Windows that is the call F-133 replaced, and the retry of the archive step is lost", r.file, r.line, r.key)
		if r.key == transportFSImport+".RenameAtomic" {
			renameAtomicUses++
		}
	}
	assert.Equal(t, 1, renameAtomicUses, "premise: the archive step names RenameAtomic once (archiveCalls), so the guard sees the call it protects")
}
