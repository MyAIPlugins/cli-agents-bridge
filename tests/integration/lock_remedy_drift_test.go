package integration

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// filesAllowedToAdviseForceNew are the files where the flag is DECLARED, or
// where the caller's forceNew argument comes from it. Anywhere else, printing
// that advice sends the reader to a flag their command refuses.
var filesAllowedToAdviseForceNew = map[string]bool{
	"cmd/cab-bridge/register.go":     true, // declares --force-new
	"cmd/cab-bridge/join.go":         true, // declares --force-new
	"internal/session/manager.go":    true, // Register: the only AcquireLock caller fed by the flag
	"internal/session/reconnect.go":  true, // resume, whose way out is register --force-new
	"internal/cleanup/scope.go":      false,
	"internal/session/lock.go":       false, // the primitive: F-126 took it out of here
	"internal/session/listener.go":   false,
	"internal/session/wakecursor.go": false,
	"internal/session/replytxn.go":   false,
	"cmd/cab-bridge/notify_watch.go": false,
}

// TestLockRemedyDrift_ForceNewIsNamedOnlyWhereTheFlagExists is a DRIFT TRIPWIRE,
// and the classification is load-bearing rather than a hedge.
//
// WHAT IT CANNOT DO: say whether the advice is CORRECT where it is allowed. At
// Register it is (the holder is a live session, retrying never clears it); this
// test would be just as green if that sentence were nonsense.
//
// WHAT IT DOES DO: fail when a remedy naming `--force-new` appears in a file
// whose commands do not accept it. That is exactly how F-126 happened and why
// nothing caught it for a release: v0.8 removed every flag from the five loop
// verbs, the advice lived inside `AcquireLock` — one primitive, eleven callers —
// and no test looks at the words in an error. `cab-bridge ask --force-new`
// answers "takes no flags", so the loop was printing a remedy its own parser
// refuses.
//
// It reads STRING LITERALS via go/ast, not lines: a comment that explains the
// rule (lock.go is full of them now) must not trip it, and a grep could not tell
// the two apart.
func TestLockRemedyDrift_ForceNewIsNamedOnlyWhereTheFlagExists(t *testing.T) {
	t.Parallel()
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	var offenders, seen []string
	walkErr := filepath.WalkDir(repoRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", ".worktrees", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}

		file, perr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if perr != nil {
			return perr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, uerr := strconv.Unquote(lit.Value)
			if uerr != nil || !strings.Contains(s, "--force-new") {
				return true
			}
			seen = append(seen, rel)
			if !filesAllowedToAdviseForceNew[rel] {
				offenders = append(offenders, rel+": "+s)
			}
			return true
		})
		return nil
	})
	require.NoError(t, walkErr)

	// A tripwire that finds nothing to check is not passing, it is blind: if the
	// walk stops matching (a moved directory, a renamed package), zero offenders
	// would read as success forever.
	require.NotEmpty(t, seen, "no string mentions --force-new anywhere — the walk is not reaching the code")

	assert.Empty(t, offenders,
		"these files advise --force-new but their commands do not accept it (F-126):\n  %s",
		strings.Join(offenders, "\n  "))
}
