package integration

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
