package integration

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

// F-160, criterion 10: there is ONE copy of the kernel lock. The two that existed
// (findings and the identity lock) each carried their own EINTR loop and their own
// errno classification, and a third for the session lock would have been the
// version of it that drifted. The calls that take or release a kernel file lock
// appear only under internal/fslock, in every .go file, tests included: a test that
// calls syscall.Flock itself is a second opinion on what "busy" means.
//
// The scan is on the syntax tree and ignores build tags, so the Windows files are
// read on a Mac. A comment that names LockFileEx cannot trip it.
var kernelLockCalls = map[string]map[string]bool{
	"syscall": {"Flock": true, "FcntlFlock": true},
	"unix":    {"Flock": true, "FcntlFlock": true},
	"windows": {"LockFileEx": true, "UnlockFileEx": true, "LockFile": true, "UnlockFile": true},
}

func TestKernelLockCallsLiveOnlyInFslock(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)

	inside := map[string]bool{}
	var outside []string
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal", "tests"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			require.NoError(t, perr, p)
			rel, rerr := filepath.Rel(root, p)
			require.NoError(t, rerr)
			rel = filepath.ToSlash(rel)
			ast.Inspect(f, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || !kernelLockCalls[pkg.Name][sel.Sel.Name] {
					return true
				}
				if strings.HasPrefix(rel, "internal/fslock/") {
					inside[pkg.Name+"."+sel.Sel.Name] = true
				} else {
					outside = append(outside, rel+": "+pkg.Name+"."+sel.Sel.Name)
				}
				return true
			})
			return nil
		}))
	}

	var found []string
	for k := range inside {
		found = append(found, k)
	}
	sort.Strings(found)
	// A scan that finds nothing where the lock is would pass for the wrong reason.
	assert.Equal(t, []string{"syscall.Flock", "windows.LockFileEx", "windows.UnlockFileEx"}, found,
		"the calls internal/fslock makes: if this list changed, the scan and the package no longer agree")
	assert.Empty(t, outside, "a kernel lock taken outside internal/fslock is a second copy")
}

// F-160, criterion 10 (SEC): the session lock has ONE acquisition path. Everything
// that takes it goes through acquire() in internal/session/lock.go; nothing else
// opens a lock file, names it, or removes it. The guarantees of the lock (the file
// is created empty, never written, never replaced by a contender) are properties of
// that one function, and a second path next to it would not have them.
//
// Like the test above this reads the syntax tree, ignores build tags and skips test
// files: the tests are allowed to hold a lock on their own descriptor, because that
// is how they stand in for "another process".
type lockScan struct {
	// calls maps "what" to the files that contain a call to it.
	calls map[string]map[string]bool
	// literals maps a string literal to the files that contain it.
	literals map[string]map[string]bool
}

func (s *lockScan) addCall(what, file string) {
	if s.calls[what] == nil {
		s.calls[what] = map[string]bool{}
	}
	s.calls[what][file] = true
}

func (s *lockScan) addLiteral(lit, file string) {
	if s.literals[lit] == nil {
		s.literals[lit] = map[string]bool{}
	}
	s.literals[lit][file] = true
}

func (s *lockScan) filesOf(what string) []string {
	var out []string
	for f := range s.calls[what] {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func scanProductionCode(t *testing.T) *lockScan {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	scan := &lockScan{calls: map[string]map[string]bool{}, literals: map[string]map[string]bool{}}
	fset := token.NewFileSet()
	for _, dir := range []string{"cmd", "internal"} {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			require.NoError(t, perr, p)
			rel, rerr := filepath.Rel(root, p)
			require.NoError(t, rerr)
			rel = filepath.ToSlash(rel)
			ast.Inspect(f, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.BasicLit:
					if v.Kind == token.STRING {
						if s, uerr := strconv.Unquote(v.Value); uerr == nil {
							scan.addLiteral(s, rel)
						}
					}
				case *ast.CallExpr:
					switch fn := v.Fun.(type) {
					case *ast.Ident:
						scan.addCall(fn.Name, rel)
					case *ast.SelectorExpr:
						if pkg, ok := fn.X.(*ast.Ident); ok {
							scan.addCall(pkg.Name+"."+fn.Sel.Name, rel)
						}
					}
				}
				return true
			})
			return nil
		}))
	}
	return scan
}

func TestSessionLock_HasASingleAcquisitionPath(t *testing.T) {
	scan := scanProductionCode(t)
	const lockGo = "internal/session/lock.go"

	// The file is opened (and created) in one place.
	assert.Equal(t, []string{lockGo}, scan.filesOf("fslock.Open"), "only acquire() opens a session lock file")
	assert.Equal(t, []string{lockGo}, scan.filesOf("acquire"), "acquire() is called from lock.go alone: the public methods are thin wrappers over it")
	assert.Equal(t, []string{lockGo}, scan.filesOf("acquireOnce"))
	assert.Equal(t, []string{"internal/session/identitylock.go", lockGo}, scan.filesOf("fslock.Lock"),
		"fslock.Lock: the session lock, and the identity lock (a different lock, a different file)")

	// The bare primitive has one production caller, and it is Register.
	assert.Equal(t, []string{"internal/session/manager.go"}, scan.filesOf("AcquireLock"),
		"AcquireLock is the base-path primitive: only Register needs it, every other caller names a session")

	// The file name exists as a literal once, in the constant everyone else uses.
	assert.Equal(t, map[string]bool{lockGo: true}, scan.literals["lock"],
		"the literal \"lock\" appears only in lock.go (session.LockFileName); a second spelling is a second path")
}

// A contender never removes, replaces or writes the lock file. The whole of lock.go
// is held to it, not just the contender branch: nothing in the file has a reason to
// call any of these, and the day it has, this test says so before a successor loses
// the lock it holds (windows 2 and 4 of the BAR).
func TestSessionLock_TheLockFileCodeNeverRemovesOrWritesAFile(t *testing.T) {
	scan := scanProductionCode(t)
	const lockGo = "internal/session/lock.go"

	forbidden := []string{
		"os.Remove", "os.RemoveAll", "os.Rename", "os.WriteFile", "os.Truncate", "os.Chmod", "os.Link", "os.Symlink",
	}
	for _, what := range forbidden {
		assert.False(t, scan.calls[what][lockGo], "%s is called in %s", what, lockGo)
	}
	// The atomic writers are reached through an import alias, so a call list would
	// miss a renamed one: the file does not import their package at all.
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	imports, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, filepath.FromSlash(lockGo)), nil, parser.ImportsOnly)
	require.NoError(t, err)
	for _, imp := range imports.Imports {
		assert.NotContains(t, imp.Path.Value, "internal/transport/fs", "lock.go must not import the atomic writers")
	}
	// A scan that looked at an empty file would pass for the wrong reason.
	assert.True(t, scan.calls["fslock.Open"][lockGo], "premise: the scan reaches the calls of lock.go")
	assert.True(t, scan.calls["os.Mkdir"][lockGo], "premise: and sees the one directory it creates, the watcher's, non-recursively")
	assert.False(t, scan.calls["os.MkdirAll"][lockGo], "the watcher directory is never created with its parents: a retired session is not rebuilt")
}
