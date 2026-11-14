package security

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ONE LOOP FOR THE TRANSIENT REFUSAL. Before this guard the repository had two copies of the
// same retry (retryWhile here and the loop inside the atomic replace of F-133), which differed
// only in a budget and in a list of errors, and each was fixed on its own: the budget, the wait,
// the doubling and the message of one could drift from the other without any test noticing.
// RetryLoop is the one loop; what is per operation is the budget and the predicate, and both
// are named and pinned here to the place that uses them.
//
// A test of behaviour cannot hold this, because a second loop passes every test of the first.
// So the guard reads the syntax tree of every non-test file of the module (files of every build
// tag are parsed, so the Windows ones are seen from any system) and inventories five things.
// Each is listed below with its owner ("<file>::<function>", or "<file>::var|const <name>") and
// the reason. A site that is not listed is red; a listed site that is not found, or found a
// different number of times, is red too, because a stale entry would say that somebody decided
// something that is no longer true. A new place is a decision made in a diff, with the reason
// written next to it:
//
//   - doubling: a statement that doubles a value (x *= 2, x = x * 2, x <<= 1, x += x, also in a
//     for post statement). A second backoff has to double somewhere.
//   - loop-call: a call to RetryLoop or retryWhile, with the budget (first two arguments) and the
//     predicate (fourth) AS WRITTEN. A number or an expression in place of a name is a budget at
//     the call site, which is exactly what the named budgets exist to prevent.
//   - vocabulary: any reference to the names that make up a retry of this refusal: the three
//     predicates, the sleep of the rename, and the six names of the two budgets. A second loop
//     that reuses the existing predicate and budget (the case a count of doublings does not see)
//     has to name them, and is listed or red.
//   - errno: any reference to ERROR_ACCESS_DENIED or ERROR_SHARING_VIOLATION, the two refusals
//     that are retried. A new place that classifies them is a new predicate.
//   - sleep: any reference to time.Sleep. A new wait is a decision; the few that exist are listed.
//
// WHAT IT DOES NOT SEE, declared and not discovered later. It is a syntactic scan, and says
// nothing about whether a retry is right, only about WHERE retries are. It does not see: a loop
// that classifies the refusal another way (fs.ErrPermission, a number, the text of the error)
// and waits another way (time.After, a timer, a context, a ticker); a new predicate passed to
// an allowed entry through a parameter (removeFileSleeping takes it as one, and only its caller
// is pinned); generated code, cgo and files outside the module root; and a local identifier that
// shadows one of the names. The first of these is the real residual: the vocabulary and the
// errno rules close the way that REUSES what exists, not the way that starts again from nothing.

const (
	retryTimeImportPath = "time"
	retryVocabularyRule = "vocabulary"
)

var (
	loopNames = map[string]bool{"RetryLoop": true, "retryWhile": true}

	retryVocabulary = map[string]bool{
		"isAccessDenied": true, "isTransientReplaceError": true, "transientRemoveDenial": true,
		"renameSleep":    true,
		"renameAttempts": true, "renameFirstDelay": true, "deniedAttempts": true, "deniedFirstDelay": true,
		"RenameAttempts": true, "RenameFirstDelay": true,
	}

	refusalErrnos = map[string]bool{"ERROR_ACCESS_DENIED": true, "ERROR_SHARING_VIOLATION": true}
)

// retrySite is one place of the tree that a rule inventories.
type retrySite struct {
	rule  string // doubling, loop-call, loop-value, vocabulary, errno, sleep
	owner string // "<file>::<function>", "<file>::var <name>" or "<file>::const <name>"
	what  string
	args  string // loop-call only: the budget and the predicate as written
}

func (s retrySite) key() string {
	return strings.Join([]string{s.rule, s.owner, s.what, s.args}, " | ")
}

// allowedRetrySite is a listed site: n times, with the reason it is there.
type allowedRetrySite struct {
	retrySite
	n      int
	reason string
}

const (
	drFile  = "internal/security/denied_retry.go"
	pwFile  = "internal/security/perms_windows.go"
	rmFile  = "internal/security/remove.go"
	rwFile  = "internal/transport/fs/rename_windows.go"
	nwFile  = "cmd/cab-bridge/notify_watch_state.go"
	lkFile  = "internal/session/lock.go"
	fsFile  = "internal/findings/store.go"
	ltFile  = "internal/fslock/locktest/locktest.go"
	rrOwner = rwFile + "::renameRetrying"
)

func allow(rule, owner, what, args string, n int, reason string) allowedRetrySite {
	return allowedRetrySiteOf(retrySite{rule, owner, what, args}, n, reason)
}

func allowedRetrySiteOf(s retrySite, n int, reason string) allowedRetrySite {
	return allowedRetrySite{retrySite: s, n: n, reason: reason}
}

var retryAllowed = []allowedRetrySite{
	// ---- doubling
	allow("doubling", drFile+"::RetryLoop", "delay", "", 1, "the loop: the only backoff of a retry on a refusal"),
	allow("doubling", drFile+"::backoffTotal", "delay", "", 1, "the figure in the message of retryWhile, the same arithmetic as the loop, summed"),
	allow("doubling", nwFile+"::notifyRetryBackoff", "factor", "", 1,
		"the wait before retrying the HOOK of notify-watch for a message id: another cause, no syscall is being refused"),

	// ---- loop-call: who runs the loop, with which budget and which predicate
	allow("loop-call", drFile+"::retryWhile", "RetryLoop", "attempts, firstDelay, retryable", 1,
		"retryWhile is RetryLoop with the message of F-154 and F-170: it forwards its own parameters"),
	allow("loop-call", pwFile+"::openNoFollowSleeping", "retryWhile", "deniedAttempts, deniedFirstDelay, isAccessDenied", 1,
		"the read of a name being deleted (F-154): the budget of reads and removals, ACCESS_DENIED only"),
	allow("loop-call", rmFile+"::removeFileSleeping", "retryWhile", "deniedAttempts, deniedFirstDelay, retryable", 1,
		"the removal (F-170): the same budget; the predicate arrives as a parameter, and RemoveFile passes transientRemoveDenial"),
	allow("loop-call", rrOwner, "RetryLoop", "renameAttempts, renameFirstDelay, isTransientReplaceError", 1,
		"the atomic replace (F-133): its own budget (alias of security.Rename*) and its own predicate"),

	// ---- vocabulary: the names of a retry of this refusal
	allow("vocabulary", pwFile+"::openNoFollowSleeping", "deniedAttempts", "", 1, "the budget of the read, passed to the loop"),
	allow("vocabulary", pwFile+"::openNoFollowSleeping", "deniedFirstDelay", "", 1, "the budget of the read, passed to the loop"),
	allow("vocabulary", pwFile+"::openNoFollowSleeping", "isAccessDenied", "", 1, "the predicate of the read, passed to the loop"),
	allow("vocabulary", pwFile+"::transientRemoveDenial", "isAccessDenied", "", 1, "the predicate of the removal IS the one of the read, by definition"),
	allow("vocabulary", rmFile+"::removeFileSleeping", "deniedAttempts", "", 1, "the budget of the removal, passed to the loop"),
	allow("vocabulary", rmFile+"::removeFileSleeping", "deniedFirstDelay", "", 1, "the budget of the removal, passed to the loop"),
	allow("vocabulary", rmFile+"::RemoveFile", "transientRemoveDenial", "", 1, "the production wiring of the removal: the predicate handed to removeFileSleeping"),
	allow("vocabulary", rrOwner, "renameAttempts", "", 2, "the budget of the replace: passed to the loop and written in the message of the exhausted budget"),
	allow("vocabulary", rrOwner, "renameFirstDelay", "", 1, "the budget of the replace, passed to the loop"),
	allow("vocabulary", rrOwner, "isTransientReplaceError", "", 1, "the predicate of the replace, passed to the loop"),
	allow("vocabulary", rrOwner, "renameSleep", "", 1, "the wait of the replace, a variable so that a test can drive it"),
	allow("vocabulary", rwFile+"::const renameAttempts", "RenameAttempts", "", 1,
		"the name by which package fs refers to the budget; its value and origin are in denied_retry.go"),
	allow("vocabulary", rwFile+"::const renameFirstDelay", "RenameFirstDelay", "", 1,
		"the name by which package fs refers to the budget; its value and origin are in denied_retry.go"),

	// ---- errno: the only two places that say which refusals are worth asking again about
	allow("errno", pwFile+"::isAccessDenied", "ERROR_ACCESS_DENIED", "", 1, "the predicate of reads and removals: ACCESS_DENIED only"),
	allow("errno", rwFile+"::isTransientReplaceError", "ERROR_ACCESS_DENIED", "", 1, "the predicate of the replace"),
	allow("errno", rwFile+"::isTransientReplaceError", "ERROR_SHARING_VIOLATION", "", 1, "the predicate of the replace"),

	// ---- sleep: every wait of the production code
	allow("sleep", pwFile+"::openNoFollow", "time.Sleep", "", 1, "production passes the real clock to the read with retry (F-154)"),
	allow("sleep", rmFile+"::RemoveFile", "time.Sleep", "", 1, "production passes the real clock to the removal with retry (F-170)"),
	allow("sleep", rwFile+"::var renameSleep", "time.Sleep", "", 1, "the wait of the replace (F-133), a variable so that a test can drive it"),
	allow("sleep", lkFile+"::acquire", "time.Sleep", "", 1, "polling of the session lock while another process holds it (fslock.ErrBusy): not a refusal of this kind"),
	allow("sleep", fsFile+"::*Store.acquire", "time.Sleep", "", 1, "the wait of the findings lock while another process holds it (ErrBusy): not a refusal of this kind"),
	allow("sleep", ltFile+"::Take", "time.Sleep", "", 1, "support for the lock tests, compiled in a non-test file"),
}

// judgeRetrySites compares what the scan found with what is listed.
func judgeRetrySites(found []retrySite, allowed []allowedRetrySite) (unlisted, stale []string) {
	have := map[string]int{}
	for _, s := range found {
		have[s.key()]++
	}
	want := map[string]int{}
	for _, a := range allowed {
		want[a.key()] += a.n
	}
	for k, n := range have {
		if w := want[k]; n > w {
			unlisted = append(unlisted, fmt.Sprintf("%s (found %d, listed %d)", k, n, w))
		}
	}
	for k, w := range want {
		if n := have[k]; n < w {
			stale = append(stale, fmt.Sprintf("%s (listed %d, found %d)", k, w, n))
		}
	}
	sort.Strings(unlisted)
	sort.Strings(stale)
	return unlisted, stale
}

// ---- the scan

type retryScan struct {
	rel        string
	secLocal   string // local name of the import of this package, "" if not imported
	timeLocal  string
	inSecurity bool
	skip       map[*ast.Ident]bool // declarations: not references
	callee     map[*ast.Ident]bool // the name being called, already counted as a loop-call
	sites      []retrySite
}

func scanRetrySources(t *testing.T, sources map[string]string) []retrySite {
	t.Helper()
	names := make([]string, 0, len(sources))
	for n := range sources {
		names = append(names, n)
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	var sites []retrySite
	for _, rel := range names {
		f, err := parser.ParseFile(fset, rel, sources[rel], parser.SkipObjectResolution)
		require.NoError(t, err, rel)
		sites = append(sites, scanRetryFile(rel, f)...)
	}
	return sites
}

func scanRetryFile(rel string, f *ast.File) []retrySite {
	sc := &retryScan{
		rel: rel, secLocal: importLocalName(f, securityImportPath), timeLocal: importLocalName(f, retryTimeImportPath),
		inSecurity: f.Name.Name == "security", skip: map[*ast.Ident]bool{}, callee: map[*ast.Ident]bool{},
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				sc.visit(rel+"::"+funcOwnerName(d), d.Body)
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR && d.Tok != token.CONST {
				continue
			}
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, n := range vs.Names {
					sc.skip[n] = true
				}
				sc.visit(rel+"::"+d.Tok.String()+" "+vs.Names[0].Name, vs)
			}
		}
	}
	return sc.sites
}

func (sc *retryScan) add(rule, owner, what, args string) {
	sc.sites = append(sc.sites, retrySite{rule: rule, owner: owner, what: what, args: args})
}

func (sc *retryScan) visit(owner string, root ast.Node) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.AssignStmt:
			for _, target := range doubledTargets(x) {
				sc.add("doubling", owner, target, "")
			}
		case *ast.CallExpr:
			sc.call(owner, x)
		case *ast.SelectorExpr:
			if id, ok := x.X.(*ast.Ident); ok && sc.timeLocal != "" && id.Name == sc.timeLocal && x.Sel.Name == "Sleep" {
				sc.add("sleep", owner, "time.Sleep", "")
			}
		case *ast.Ident:
			sc.ident(owner, x)
		}
		return true
	})
}

// call records a call of the loop with its budget and predicate as written.
func (sc *retryScan) call(owner string, c *ast.CallExpr) {
	id := sc.loopCallee(c.Fun)
	if id == nil {
		return
	}
	sc.callee[id] = true
	args := fmt.Sprintf("<malformed: %d arguments>", len(c.Args))
	if len(c.Args) == 5 {
		args = strings.Join([]string{describeArg(c.Args[0]), describeArg(c.Args[1]), describeArg(c.Args[3])}, ", ")
	}
	sc.add("loop-call", owner, id.Name, args)
}

// loopCallee returns the identifier naming RetryLoop or retryWhile in a call, resolved through
// the import (whatever local name it has) or unqualified inside this package.
func (sc *retryScan) loopCallee(fun ast.Expr) *ast.Ident {
	switch f := fun.(type) {
	case *ast.Ident:
		if sc.inSecurity && loopNames[f.Name] {
			return f
		}
	case *ast.SelectorExpr:
		if x, ok := f.X.(*ast.Ident); ok && sc.secLocal != "" && x.Name == sc.secLocal && loopNames[f.Sel.Name] {
			return f.Sel
		}
	}
	return nil
}

func (sc *retryScan) ident(owner string, id *ast.Ident) {
	if sc.skip[id] {
		return
	}
	switch {
	case retryVocabulary[id.Name]:
		sc.add(retryVocabularyRule, owner, id.Name, "")
	case refusalErrnos[id.Name]:
		sc.add("errno", owner, id.Name, "")
	case loopNames[id.Name] && !sc.callee[id]:
		// The loop used as a value (stored, passed on): it can be called from anywhere later.
		sc.add("loop-value", owner, id.Name, "")
	}
}

func importLocalName(f *ast.File, importPath string) string {
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil || p != importPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return filepath.Base(p)
	}
	return ""
}

func funcOwnerName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	return types.ExprString(d.Recv.List[0].Type) + "." + d.Name.Name
}

func describeArg(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		return types.ExprString(x)
	case *ast.BasicLit:
		return "<literal " + x.Value + ">"
	default:
		return "<expression " + types.ExprString(e) + ">"
	}
}

func isIntLit(e ast.Expr, v string) bool {
	b, ok := e.(*ast.BasicLit)
	return ok && b.Kind == token.INT && b.Value == v
}

// doubledTargets returns what an assignment doubles: x *= 2, x <<= 1, x += x, x = x * 2,
// x = 2 * x, x = x << 1, x = x + x, in a statement or in the post statement of a for.
func doubledTargets(a *ast.AssignStmt) []string {
	one := len(a.Lhs) == 1 && len(a.Rhs) == 1
	switch a.Tok {
	case token.MUL_ASSIGN:
		if one && isIntLit(a.Rhs[0], "2") {
			return []string{types.ExprString(a.Lhs[0])}
		}
	case token.SHL_ASSIGN:
		if one && isIntLit(a.Rhs[0], "1") {
			return []string{types.ExprString(a.Lhs[0])}
		}
	case token.ADD_ASSIGN:
		if one && types.ExprString(a.Lhs[0]) == types.ExprString(a.Rhs[0]) {
			return []string{types.ExprString(a.Lhs[0])}
		}
	case token.ASSIGN:
		var out []string
		if len(a.Lhs) != len(a.Rhs) {
			return nil
		}
		for i := range a.Lhs {
			if doubles(a.Lhs[i], a.Rhs[i]) {
				out = append(out, types.ExprString(a.Lhs[i]))
			}
		}
		return out
	}
	return nil
}

func doubles(lhs, rhs ast.Expr) bool {
	b, ok := rhs.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	l, x, y := types.ExprString(lhs), types.ExprString(b.X), types.ExprString(b.Y)
	switch b.Op {
	case token.MUL:
		return (x == l && isIntLit(b.Y, "2")) || (isIntLit(b.X, "2") && y == l)
	case token.SHL:
		return x == l && isIntLit(b.Y, "1")
	case token.ADD:
		return x == l && y == l
	}
	return false
}

// ---- the tree

// loadRepoSources reads every non-test Go file of the module, by path relative to its root.
func loadRepoSources(t *testing.T) map[string]string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	sources := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		name := d.Name()
		if d.IsDir() {
			// Hidden directories hold other checkouts and tooling (.git, .worktrees) and would
			// count the same file twice; the rest hold no code of the module.
			if p != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		require.NoError(t, rerr, p)
		rel, rerr := filepath.Rel(root, p)
		require.NoError(t, rerr)
		sources[filepath.ToSlash(rel)] = string(b)
		return nil
	}))
	return sources
}

// TestRetry_OnlyOneLoopExistsInTheRepository is the guard on the real tree.
func TestRetry_OnlyOneLoopExistsInTheRepository(t *testing.T) {
	sources := loadRepoSources(t)
	require.Contains(t, sources, rwFile, "the scan must read the Windows files too, whatever system it runs on")

	unlisted, stale := judgeRetrySites(scanRetrySources(t, sources), retryAllowed)

	assert.Empty(t, unlisted, "these places repeat an operation on a refusal, or name what a retry is made of, and are not "+
		"listed: use security.RetryLoop with a named budget (denied_retry.go, with its origin) and a predicate, and list the "+
		"new place in retryAllowed with the reason; if it is not a retry of this refusal, say so in the reason")
	assert.Empty(t, stale, "these listed sites are not there any more (or fewer times than listed): remove or correct the entry")
}
