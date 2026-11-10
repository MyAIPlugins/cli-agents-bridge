package integration

// F-160, CRI diff-gate blocker: no writer brings a retired session's manifest back.
// The retirement rests on "the manifest is gone for good", and that holds only if no
// code writes manifest.json outside the session lock. These tests read the syntax
// tree of the production code (ignoring build tags, skipping test files, like the
// lock tests next to them) and fix the set of places that can write it:
//
//   - SaveManifest is called by Register, under the lock it has just created, and by
//     updateManifest, which takes the session lock first. Nothing else calls it.
//   - Nothing else names "manifest.json" and writes: the only other writer is the
//     migration from the Patil bridge, and it creates the folder of the destination
//     session exclusively (os.Mkdir, like Register), so it writes nothing into a folder
//     that already exists. That was a wish until round 2 of the F-160 diff-gate: it used
//     MkdirAll, and a re-import brought a retired session's manifest back. It is
//     fixed by migrate_exclusive_test.go, not by this file: this test reads which
//     functions write, and cannot see how a folder is created.
//   - manifestMu is taken in one function, updateManifest, after the session lock.
//     One direction, so the two locks cannot be held in opposite orders.
//
// Declared limit: a path to the manifest built without the literal "manifest.json"
// would escape the second check; the first one (SaveManifest) does not depend on it.

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

type fnCall struct {
	name string // "SaveManifest", "os.WriteFile", "manifestMu.Lock", ...
	pos  token.Pos
}

type fnInfo struct {
	where    string // "internal/session/manager.go:updateManifest"
	file     string
	calls    []fnCall
	literals map[string]bool
}

func (f fnInfo) firstCall(name string) (token.Pos, bool) {
	for _, c := range f.calls {
		if c.name == name {
			return c.pos, true
		}
	}
	return token.NoPos, false
}

// callNames names a call in every way the checks above look for it: a selector call
// is recorded by its method name alone (so a.mgr.SaveManifest and m.SaveManifest are
// both "SaveManifest"), and also qualified by the last name before it (os.WriteFile,
// manifestMu.Lock). A plain call is recorded by its name.
func callNames(call *ast.CallExpr) []string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return []string{fun.Name}
	case *ast.SelectorExpr:
		names := []string{fun.Sel.Name}
		switch x := fun.X.(type) {
		case *ast.Ident:
			names = append(names, x.Name+"."+fun.Sel.Name)
		case *ast.SelectorExpr:
			names = append(names, x.Sel.Name+"."+fun.Sel.Name)
		}
		return names
	}
	return nil
}

func scanFunctions(t *testing.T) []fnInfo {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	var out []fnInfo
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			require.NoError(t, perr, p)
			rel, rerr := filepath.Rel(root, p)
			require.NoError(t, rerr)
			rel = filepath.ToSlash(rel)
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				info := fnInfo{where: rel + ":" + fn.Name.Name, file: rel, literals: map[string]bool{}}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch v := n.(type) {
					case *ast.CallExpr:
						for _, name := range callNames(v) {
							info.calls = append(info.calls, fnCall{name: name, pos: v.Pos()})
						}
					case *ast.BasicLit:
						if v.Kind == token.STRING {
							if s, uerr := strconv.Unquote(v.Value); uerr == nil {
								info.literals[s] = true
							}
						}
					}
					return true
				})
				out = append(out, info)
			}
			return nil
		}))
	}
	return out
}

var manifestWritePrimitives = map[string]bool{
	"AtomicWriteJSON": true, "AtomicWriteBytes": true, "WriteIfAbsentBytes": true,
	"os.WriteFile": true, "os.OpenFile": true, "os.Create": true,
	"transportfs.AtomicWriteJSON": true, "transportfs.AtomicWriteBytes": true, "transportfs.WriteIfAbsentBytes": true,
}

func TestManifestWriters_OnlyRegisterAndUpdateManifestSaveAManifest(t *testing.T) {
	var savers, otherWriters []string
	for _, fn := range scanFunctions(t) {
		if _, ok := fn.firstCall("SaveManifest"); ok {
			savers = append(savers, fn.where)
		}
		if !fn.literals["manifest.json"] {
			continue
		}
		for name := range manifestWritePrimitives {
			if _, ok := fn.firstCall(name); ok {
				otherWriters = append(otherWriters, fn.where)
				break
			}
		}
	}
	sort.Strings(savers)
	sort.Strings(otherWriters)

	assert.Equal(t, []string{"internal/session/manager.go:Register", "internal/session/manager.go:updateManifest"}, savers,
		"SaveManifest is called under a session lock and nowhere else: a read-modify-write goes through updateManifest")
	assert.Equal(t, []string{"cmd/cab-bridge/migrate.go:migrateOne", "internal/session/manager.go:SaveManifest"}, otherWriters,
		"the places that name manifest.json and write: SaveManifest itself, and the migration that creates a destination session")
}

func TestManifestWriters_TheSessionLockIsTakenBeforeManifestMuAndNeverAfter(t *testing.T) {
	var takers []string
	for _, fn := range scanFunctions(t) {
		muPos, takesMu := fn.firstCall("manifestMu.Lock")
		if !takesMu {
			continue
		}
		takers = append(takers, fn.where)
		for _, lockCall := range []string{"AcquireSessionLock", "WithSessionLock", "AcquireLock", "acquire"} {
			for _, c := range fn.calls {
				if c.name == lockCall {
					assert.Less(t, c.pos, muPos, "%s takes the session lock (%s) after manifestMu: the order is session lock, then manifestMu", fn.where, lockCall)
				}
			}
		}
		if fn.where == "internal/session/manager.go:updateManifest" {
			lockPos, ok := fn.firstCall("AcquireSessionLock")
			require.True(t, ok, "updateManifest takes the session lock")
			savePos, ok := fn.firstCall("SaveManifest")
			require.True(t, ok)
			assert.Less(t, lockPos, muPos)
			assert.Less(t, muPos, savePos, "and writes while holding both")
		}
	}
	sort.Strings(takers)
	assert.Equal(t, []string{"internal/session/manager.go:updateManifest"}, takers, "manifestMu is taken in one place")
}
