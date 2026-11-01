//go:build !windows

package integration

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcp.Refusef builds the text that reaches Claude Desktop's model, and an error given
// to it would put its own text there: a path of the machine, a session id. The
// signature is `...any` on purpose (go vet checks each format against its arguments
// only through it), so the compiler cannot refuse an error. This does, for every call
// in the module: each argument is a string or an integer, and none is, or is made
// from, an error (`err`, `err.Error()`, `fmt.Sprint(err)`). Reading a field of an
// error (`typeErr.Field`) is not using the error.
//
// Not covered, and said so: a string made from an error somewhere else and passed in
// through a variable; and a call in a file this GOOS does not compile, which the test
// does not look at but does find (it reads the excluded files for the name).

type listedPackage struct {
	ImportPath     string
	Dir            string
	GoFiles        []string
	IgnoredGoFiles []string
	Imports        []string
}

type refusefProblem struct {
	pos token.Pos
	msg string
}

func goListPackages(t *testing.T, repoRoot string) []listedPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.Output()
	require.NoError(t, err, "go list")
	dec := json.NewDecoder(strings.NewReader(string(out)))
	var pkgs []listedPackage
	for dec.More() {
		var p listedPackage
		require.NoError(t, dec.Decode(&p))
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// typeCheck parses and type-checks one package from its files. The imports are
// type-checked from source, with cgo off: the standard library has a pure-Go form of
// every package this module reaches, and no C compiler is needed.
func typeCheck(t *testing.T, fset *token.FileSet, imp types.Importer, path string, filenames []string) ([]*ast.File, *types.Info) {
	t.Helper()
	files := make([]*ast.File, 0, len(filenames))
	for _, name := range filenames {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err)
		files = append(files, f)
	}
	info := &types.Info{
		Types:      map[ast.Expr]types.TypeAndValue{},
		Uses:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	var typeErrs []error
	conf := types.Config{Importer: imp, Error: func(err error) { typeErrs = append(typeErrs, err) }}
	_, _ = conf.Check(path, fset, files, info)
	require.Empty(t, typeErrs, "type-checking %s", path)
	return files, info
}

func isRefusef(obj types.Object, mcpPath string) bool {
	fn, ok := obj.(*types.Func)
	return ok && fn.Name() == "Refusef" && fn.Pkg() != nil && fn.Pkg().Path() == mcpPath
}

// refusefProblems returns every way the calls of mcp.Refusef in the files break the
// rule, and how many calls there were.
func refusefProblems(files []*ast.File, info *types.Info, mcpPath string) ([]refusefProblem, int) {
	errIface := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	var problems []refusefProblem
	calls := 0
	callees := map[*ast.Ident]bool{}

	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			var id *ast.Ident
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				id = fun
			case *ast.SelectorExpr:
				id = fun.Sel
			}
			if id == nil || !isRefusef(info.Uses[id], mcpPath) {
				return true
			}
			callees[id] = true
			calls++
			problems = append(problems, argumentProblems(call, info, errIface)...)
			return true
		})
	}
	for id, obj := range info.Uses {
		if isRefusef(obj, mcpPath) && !callees[id] {
			problems = append(problems, refusefProblem{id.Pos(), "Refusef is used as a value, so its calls cannot be checked"})
		}
	}
	return problems, calls
}

func argumentProblems(call *ast.CallExpr, info *types.Info, errIface *types.Interface) []refusefProblem {
	var problems []refusefProblem
	add := func(at ast.Node, format string, a ...any) {
		problems = append(problems, refusefProblem{at.Pos(), fmt.Sprintf(format, a...)})
	}
	if len(call.Args) == 0 {
		add(call, "no format")
		return problems
	}
	if info.Types[call.Args[0]].Value == nil {
		add(call.Args[0], "the format is not a constant")
	}
	for i, arg := range call.Args {
		if i > 0 && !isPlainValue(info.TypeOf(arg)) {
			add(arg, "argument %d is %v: only a string or an integer may be given", i, info.TypeOf(arg))
		}
		if madeFromError(arg, info, errIface) {
			add(arg, "argument %d is, or is made from, an error", i)
		}
	}
	return problems
}

func isPlainValue(t types.Type) bool {
	b, ok := t.Underlying().(*types.Basic)
	return ok && b.Info()&(types.IsString|types.IsInteger) != 0
}

func implementsError(t types.Type, errIface *types.Interface) bool {
	return t != nil && types.Implements(t, errIface)
}

// madeFromError says whether any part of the expression is an error value. The one
// exception is the operand of a field selection: `typeErr.Field` reads a string out of
// an error struct, it does not carry the error.
func madeFromError(e ast.Expr, info *types.Info, errIface *types.Interface) bool {
	exempt := map[ast.Expr]bool{}
	ast.Inspect(e, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if s := info.Selections[sel]; s != nil && s.Kind() == types.FieldVal {
				exempt[sel.X] = true
			}
		}
		return true
	})
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		x, ok := n.(ast.Expr)
		if !ok || found {
			return !found
		}
		if !exempt[x] && implementsError(info.TypeOf(x), errIface) {
			found = true
		}
		return !found
	})
	return found
}

// control is a package the checker must judge exactly as marked: a line with `BAD` is a
// call that breaks the rule, any other call is allowed.
const controlSource = `package sample

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"%s/internal/mcp"
)

type named string

func fine(s string, n int, k named, typeErr *json.UnmarshalTypeError) {
	_ = mcp.Refusef("%%s, %%d, %%q", s, n, k)
	_ = mcp.Refusef("literal %%s", "words")
	_ = mcp.Refusef("a field of an error is not the error: %%q", typeErr.Field)
	_ = mcp.Refusef("a %%s", fmt.Sprintf("%%d", n))
	_ = mcp.Refusef("joined: %%s", strings.Join([]string{s}, ", "))
}

func broken(err error, typeErr *json.UnmarshalTypeError, anything any, list []string, format string) {
	_ = mcp.Refusef("%%v", err) // BAD
	_ = mcp.Refusef("%%s", err.Error()) // BAD
	_ = mcp.Refusef("%%s", fmt.Sprint(err)) // BAD
	_ = mcp.Refusef("%%s", typeErr.Error()) // BAD
	_ = mcp.Refusef("%%v", errors.Unwrap(err)) // BAD
	_ = mcp.Refusef("%%v", anything) // BAD
	_ = mcp.Refusef("%%v", list) // BAD
	_ = mcp.Refusef(format) // BAD
	_ = mcp.Refusef // BAD
}
`

func lineOf(fset *token.FileSet, pos token.Pos) int { return fset.Position(pos).Line }

func TestRefusefIsNeverGivenAnError(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)
	modOut, err := exec.Command("go", "list", "-m").Output()
	require.NoError(t, err)
	module := strings.TrimSpace(string(modOut))
	mcpPath := module + "/internal/mcp"

	cgo := build.Default.CgoEnabled
	build.Default.CgoEnabled = false
	t.Cleanup(func() { build.Default.CgoEnabled = cgo })
	fset := token.NewFileSet()
	imp := importer.ForCompiler(fset, "source", nil)

	t.Run("the checker flags what it should and nothing else", func(t *testing.T) {
		src := fmt.Sprintf(controlSource, module)
		dir := t.TempDir()
		name := filepath.Join(dir, "sample.go")
		require.NoError(t, os.WriteFile(name, []byte(src), 0o600))
		files, info := typeCheck(t, fset, imp, "example.test/sample", []string{name})

		problems, calls := refusefProblems(files, info, mcpPath)
		flagged := map[int]bool{}
		for _, p := range problems {
			flagged[lineOf(fset, p.pos)] = true
		}
		want := map[int]bool{}
		for i, line := range strings.Split(src, "\n") {
			if strings.Contains(line, "// BAD") {
				want[i+1] = true
			}
		}
		assert.Equal(t, want, flagged, "the lines flagged are the lines marked BAD")
		assert.Equal(t, 13, calls, "every call was seen, the fine ones too: 5 allowed and 8 broken (the BAD value that is not a call is not counted)")
	})

	t.Run("the module", func(t *testing.T) {
		var scanned []string
		for _, p := range goListPackages(t, repoRoot) {
			uses := p.ImportPath == mcpPath
			for _, imp := range p.Imports {
				uses = uses || imp == mcpPath
			}
			if !uses {
				continue
			}
			names := make([]string, len(p.GoFiles))
			for i, f := range p.GoFiles {
				names[i] = filepath.Join(p.Dir, f)
			}
			files, info := typeCheck(t, fset, imp, p.ImportPath, names)
			problems, calls := refusefProblems(files, info, mcpPath)
			for _, pr := range problems {
				t.Errorf("%s: %s", fset.Position(pr.pos), pr.msg)
			}
			for _, f := range p.IgnoredGoFiles {
				data, rerr := os.ReadFile(filepath.Join(p.Dir, f))
				require.NoError(t, rerr)
				assert.NotContains(t, string(data), "Refusef(", "%s is not compiled for this GOOS, so a call in it is not checked", f)
			}
			if calls > 0 {
				scanned = append(scanned, p.ImportPath)
			}
		}
		sort.Strings(scanned)
		assert.Contains(t, scanned, module+"/cmd/cab-bridge", "the premise: the consumer of Refusef was found, and its calls counted")
		assert.Contains(t, scanned, mcpPath, "and so was DecodeArgs's package")
	})
}
