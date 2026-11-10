package main

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

// Structural guards for F-160. Each one fixes a PROPERTY of the source that a
// behavioural test cannot see:
//
//   - who may read the base directory (the old tree hangs off it);
//   - that the name of the layout directory lives in one package;
//   - the perimeter of reads that reach the old tree (the snapshot tests prove
//     nothing was WRITTEN there; they cannot prove nothing was READ);
//   - that no test goes back to assuming CAB_DATA_DIR is where sessions live.
//
// They parse the tree with go/parser, so a comment or a docstring can neither
// satisfy nor trip them.

type goSource struct {
	rel  string // slash-separated, relative to the repository root
	file *ast.File
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	return root
}

func parseRepo(t *testing.T, tests bool, dirs ...string) []goSource {
	t.Helper()
	root := repoRoot(t)
	var out []goSource
	fset := token.NewFileSet()
	for _, dir := range dirs {
		require.NoError(t, filepath.WalkDir(filepath.Join(root, dir), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") != tests {
				return nil
			}
			f, perr := parser.ParseFile(fset, p, nil, 0)
			require.NoError(t, perr, p)
			rel, rerr := filepath.Rel(root, p)
			require.NoError(t, rerr)
			out = append(out, goSource{rel: filepath.ToSlash(rel), file: f})
			return nil
		}))
	}
	return out
}

func stringLit(n ast.Node) (string, bool) {
	lit, ok := n.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}

func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Only these files of the production code read cfg.BaseDir. Anything else that
// needs the base is a new way to reach the old tree, and goes through review.
func TestStructure_BaseDirIsReadInFourFilesOnly(t *testing.T) {
	readers := map[string]bool{}
	for _, src := range parseRepo(t, false, "cmd", "internal") {
		if strings.HasPrefix(src.rel, "internal/config/") {
			continue
		}
		ast.Inspect(src.file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "BaseDir" {
				readers[src.rel] = true
			}
			return true
		})
	}
	assert.Equal(t, []string{
		"cmd/cab-bridge/common.go",      // the SC-7 check of the base, before config.json is read
		"cmd/cab-bridge/legacy_tree.go", // the two hints about the old tree
		"cmd/cab-bridge/mcp_send.go",    // the value a child is handed as CAB_DATA_DIR
		"cmd/cab-bridge/whoami.go",      // reports it, next to dataDir
	}, sortedKeys(readers))
}

// In the three files that are not legacy_tree.go, a function that reads BaseDir
// does not also name the old tree's children: building <base>/sessions or
// <base>/archive is the job of one file.
func TestStructure_OnlyLegacyTreeGoNamesTheOldChildren(t *testing.T) {
	for _, src := range parseRepo(t, false, "cmd", "internal") {
		if strings.HasPrefix(src.rel, "internal/config/") || src.rel == "cmd/cab-bridge/legacy_tree.go" {
			continue
		}
		for _, decl := range src.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			readsBase, namesChild := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "BaseDir" {
					readsBase = true
				}
				if v, ok := stringLit(n); ok && (v == "sessions" || v == "archive") {
					namesChild = true
				}
				return true
			})
			assert.False(t, readsBase && namesChild, "%s: %s reads BaseDir and names sessions/archive", src.rel, fn.Name.Name)
		}
	}
}

// The directory name has one home. Everything else asks internal/config.
func TestStructure_TheLayoutNameLivesInInternalConfigOnly(t *testing.T) {
	for _, src := range parseRepo(t, false, "cmd", "internal") {
		if strings.HasPrefix(src.rel, "internal/config/") {
			continue
		}
		ast.Inspect(src.file, func(n ast.Node) bool {
			if v, ok := stringLit(n); ok {
				assert.NotContains(t, v, "layout-2", "%s names the layout directory in a string", src.rel)
			}
			if sel, ok := n.(*ast.SelectorExpr); ok {
				assert.NotEqual(t, "LayoutDir", sel.Sel.Name, "%s derives the state dir by hand instead of reading cfg.DataDir", src.rel)
			}
			return true
		})
	}
}

// The read perimeter of the old tree. legacy_tree.go may call exactly two functions
// of package os, Lstat (to refuse anything that is not a real directory) and Open
// (to list entry names); no os.ReadFile, os.Stat, os.ReadDir, os.OpenFile or
// os.Readlink can appear, so no manifest, link target or message of the old tree
// is ever read. Its helpers are called from the two resolvers and nowhere else.
func TestStructure_TheOldTreeIsReadOnlyByLegacyTreeGo(t *testing.T) {
	osCalls := map[string]bool{}
	callers := map[string]bool{}
	helpers := map[string]bool{"legacySessionsRoot": true, "legacySessionHint": true, "legacyRecipientHint": true}
	for _, src := range parseRepo(t, false, "cmd") {
		isLegacy := src.rel == "cmd/cab-bridge/legacy_tree.go"
		ast.Inspect(src.file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && pkg.Name == "os" && isLegacy {
					osCalls[x.Sel.Name] = true
				}
			case *ast.Ident:
				if helpers[x.Name] && !isLegacy {
					callers[src.rel] = true
				}
			}
			return true
		})
	}
	assert.Equal(t, []string{"Lstat", "Open"}, sortedKeys(osCalls), "the os calls legacy_tree.go makes")
	assert.Equal(t, []string{"cmd/cab-bridge/common.go", "cmd/cab-bridge/verbs.go"}, sortedKeys(callers), "who calls the hints")
}

// CRI2 N4: a test file that sets CAB_DATA_DIR itself and names "sessions" must say
// config.LayoutDir. (Files that go through useBase do not set the variable by
// hand, so they are not in scope: the helper returns the state dir.) A path built from the base alone
// plants fixtures where this binary never looks, and every "nothing was
// delivered" assertion on it passes without proving anything.
func TestStructure_TestsNeverAssumeTheStateDirIsTheBase(t *testing.T) {
	whitelist := map[string]string{
		// TestMain only sets the fence that keeps the suite off the real home; the
		// "sessions" literals are the children bootstrapDataDir checks INSIDE a dir
		// it is handed, not a path under CAB_DATA_DIR.
		"cmd/cab-bridge/main_test.go": "TestMain fence and bootstrapDataDir child names",
		// Asserts what a child is HANDED as CAB_DATA_DIR; its "sessions" path is built
		// from e.dataDir, which mcpEnv defines as <baseDir>/layout-2 in one place.
		"cmd/cab-bridge/mcp_send_test.go": "child environment assertion, state dir from mcpEnv",
		// This file spells the literals it searches for.
		"cmd/cab-bridge/layout_structure_test.go": "the guard itself",
	}
	var offenders []string
	for _, src := range parseRepo(t, true, "cmd", "tests", "internal") {
		setsEnv, namesSessions, usesLayout := false, false, false
		ast.Inspect(src.file, func(n ast.Node) bool {
			if v, ok := stringLit(n); ok {
				switch {
				case v == "CAB_DATA_DIR" || strings.HasPrefix(v, "CAB_DATA_DIR="):
					setsEnv = true
				case v == "sessions":
					namesSessions = true
				}
			}
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "LayoutDir" {
				usesLayout = true
			}
			return true
		})
		if setsEnv && namesSessions && !usesLayout {
			if _, ok := whitelist[src.rel]; !ok {
				offenders = append(offenders, src.rel)
			}
		}
	}
	assert.Empty(t, offenders, "these test files set CAB_DATA_DIR by hand and name \"sessions\" without config.LayoutDir")
}
