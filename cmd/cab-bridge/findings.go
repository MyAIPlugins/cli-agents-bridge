package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/findings"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// findings is the per-repository registry of critic findings (F-156): `add`
// appends one row, `stale` lists what has stayed open for too long. The rows live
// in <repository root>/.handover/findings.tsv, the root being the git-COMMON one,
// so every worktree of a repository shares one registry. The mechanism (snapshot
// plus one rename, kernel lock) is in internal/findings; this file is the command.
//
// findingsUsage is what an error prints; --help adds findingsGuide. Both are built
// from the package's own list of columns, so they cannot name a value Validate
// does not accept.
var findingsUsage = buildFindingsUsage()

func buildFindingsUsage() string {
	var required, optional []string
	for _, c := range findings.InputColumnInfo() {
		if c.Required {
			required = append(required, c.Name)
		} else {
			optional = append(optional, c.Name)
		}
	}
	return fmt.Sprintf(`usage:
  cab-bridge findings add                one JSON object on stdin, with the columns:
                                         %s (required);
                                         %s (optional)
  cab-bridge findings stale [--days=N]   open findings older than N days (default findings_stale_days), as TSV`,
		strings.Join(required, ", "), strings.Join(optional, ", "))
}

// findingsGuide is the rest of --help: the values a closed column accepts, how a
// finding is identified and closed, and what is checked and what is not. It is
// the only text that travels with the binary, so the limit of the check is here.
func findingsGuide() string { return guideClosing(findings.ClosingOutcomes()) }

// guideClosing builds the guide for a given list of outcomes that close a finding;
// findingsGuide passes the package's own, the tests pass another to see that the
// prose follows the list.
func guideClosing(closing []string) string {
	var b strings.Builder
	b.WriteString("\nallowed values:\n")
	for _, c := range findings.InputColumnInfo() {
		if len(c.Allowed) > 0 {
			fmt.Fprintf(&b, "  %-10s %s\n", c.Name, strings.Join(c.Allowed, " | "))
		}
	}
	fmt.Fprintf(&b, `
how it works:
  The registry is <repository root>/.handover/findings.tsv: one for the whole repository, shared by
  all its worktrees. Every add appends one row. A finding is identified by its key: the same
  key is the same finding, and its LAST row is its current state. To close one, add a row with
  the same key and outcome %s. ts and first_seen are written by the command;
  the age of a finding counts from the first row of its key. stale writes nothing in the
  repository; add creates .handover/ (with its own .gitignore) the first time.
  If .handover/ existed before, add and stale note that it has no .gitignore until it has one:
  a file named .gitignore in it is the whole check (git is not asked what it ignores).
`, closingOutcomesText(closing))
	fmt.Fprintf(&b, `
what is checked:
  A row is refused, and nothing is written, if a column matches one of these rules:
    %s
  (the last one needs the list findings_denied_names in config.json; without it names are not checked).
  This is NOT a general secret detector: a secret of a type that is not listed, split across the
  value or encoded passes. Write pointers (file:line, a commit, a message id), not values.
  The other way round, a name is refused although it is not a secret if it contains one of
    %s
  followed by 20 more characters, or sk- (the end of disk- or task-) followed by a digest of 48
  letters and digits, such as cache/task-<sha256>.bin. Cite the file with another pointer: a
  commit and a line.
`, strings.Join(findings.ContentRuleNames(), ", "), strings.Join(findings.PrefixedKeyPrefixes(), ", "))
	return b.String()
}

// closingOutcomesText is "closed (or rejected)": the first outcome that ends a
// finding, then the others, all from the package's list.
func closingOutcomesText(closing []string) string {
	if len(closing) < 2 {
		return strings.Join(closing, "")
	}
	return closing[0] + " (or " + strings.Join(closing[1:], ", ") + ")"
}

// The one format for what is informational on stderr; errors have no prefix of their own.
const findingsNotePrefix = "findings: note: "

const noNamesNote = "no person names configured (findings_denied_names in config.json): names are NOT checked"

const findingsDirName = ".handover"

// findingsDeps are the things the command takes from the outside, so a test can
// give it a repository, a clock and a configuration without a subprocess.
// TryLock and Publish stay nil in production: the Store uses its defaults.
type findingsDeps struct {
	Getwd      func() (string, error)
	Home       func() (string, error)
	LoadConfig func() (config.Config, error)
	Now        func() time.Time
	TryLock    findings.TryLockFunc
	Publish    findings.PublishFunc
}

func runFindings(args []string) error {
	return findingsRun(args, os.Stdin, os.Stdout, os.Stderr, findingsDeps{
		Getwd:      os.Getwd,
		Home:       os.UserHomeDir,
		LoadConfig: loadConfigOrFail,
		Now:        time.Now,
	})
}

func findingsRun(args []string, stdin io.Reader, stdout, stderr io.Writer, deps findingsDeps) error {
	if len(args) == 0 {
		return fmt.Errorf("findings: expected add or stale\n%s", findingsUsage)
	}
	switch args[0] {
	case "-h", "--help", "help":
		return printFindingsHelp(stderr)
	case "add":
		return findingsAdd(args[1:], stdin, stdout, stderr, deps)
	case "stale":
		return findingsStale(args[1:], stdout, stderr, deps)
	default:
		return fmt.Errorf("findings: unknown action %q\n%s", args[0], findingsUsage)
	}
}

func printFindingsHelp(stderr io.Writer) error {
	_, err := fmt.Fprint(stderr, findingsUsage+"\n"+findingsGuide())
	return err
}

func findingsAdd(args []string, stdin io.Reader, stdout, stderr io.Writer, deps findingsDeps) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		return printFindingsHelp(stderr)
	}
	if len(args) != 0 {
		return fmt.Errorf("findings add: takes no arguments, the columns come as one JSON object on stdin\n%s", findingsUsage)
	}

	in, err := findings.DecodeStrict(stdin)
	if err != nil {
		return err
	}
	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	if len(cfg.FindingsDeniedNames) == 0 {
		fmt.Fprintln(stderr, findingsNotePrefix+noNamesNote)
	}
	store, err := findingsStore(cfg, deps)
	if err != nil {
		return err
	}

	res, addErr := store.Add(in)
	printNotes(stderr, res.Notes)
	if res.Row.Key != "" {
		// Also when the lock failed to release after the row was recorded: the
		// error below says so, and this line says what was recorded.
		fmt.Fprintf(stdout, "recorded %s first_seen=%s -> %s\n", res.Row.Key, res.Row.FirstSeen, store.RegistryPath())
	}
	return addErr
}

func findingsStale(args []string, stdout, stderr io.Writer, deps findingsDeps) error {
	flags := flag.NewFlagSet("findings stale", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprintln(stderr, findingsUsage) }
	daysFlag := flags.Int("days", 0, "findings open for more than this many days (default: findings_stale_days)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// The flag package has printed the short usage; the guide completes it.
			_, werr := fmt.Fprint(stderr, findingsGuide())
			return werr
		}
		return fmt.Errorf("findings stale: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("findings stale: unexpected argument\n%s", findingsUsage)
	}
	given := false
	flags.Visit(func(f *flag.Flag) { given = given || f.Name == "days" })

	cfg, err := deps.LoadConfig()
	if err != nil {
		return err
	}
	days, err := resolveStaleDays(given, *daysFlag, cfg.FindingsStaleDays)
	if err != nil {
		return err
	}
	store, err := findingsStore(cfg, deps)
	if err != nil {
		return err
	}

	res, err := store.Stale(days)
	if err != nil {
		return err
	}
	printNotes(stderr, res.Notes)
	_, err = io.WriteString(stdout, res.TSV())
	return err
}

// resolveStaleDays picks the threshold and names where a bad value came from:
// the flag if it was given, the configuration (file or environment) if not.
func resolveStaleDays(flagGiven bool, flagValue, configured int) (int, error) {
	source, value := "findings_stale_days", configured
	if flagGiven {
		source, value = "--days", flagValue
	}
	if value < 1 {
		return 0, fmt.Errorf("findings: %s must be >= 1", source)
	}
	if value > findings.MaxStaleDays {
		return 0, fmt.Errorf("findings: %s must be at most %d", source, findings.MaxStaleDays)
	}
	return value, nil
}

// findingsStore finds the registry of the repository the command runs in.
func findingsStore(cfg config.Config, deps findingsDeps) (*findings.Store, error) {
	cwd, err := deps.Getwd()
	if err != nil {
		return nil, fmt.Errorf("findings: cannot read the current directory: %w", err)
	}
	home, err := deps.Home()
	if err != nil {
		return nil, fmt.Errorf("findings: cannot find the home directory: %w", err)
	}
	root, err := findingsRoot(cwd, home)
	if err != nil {
		return nil, err
	}
	return &findings.Store{
		Dir:     filepath.Join(root, findingsDirName),
		Names:   cfg.FindingsDeniedNames,
		Now:     deps.Now,
		TryLock: deps.TryLock,
		Publish: deps.Publish,
	}, nil
}

// findingsRoot is D3: the git-common root of cwd, or an error. There is NO
// fallback to cwd: session.FindProjectRoot returns cwd when it finds no marker,
// and writing a registry there would create .handover/ in a folder that is not a
// repository (the home directory, /tmp). So the root must itself hold a .git, and
// must not be the home directory (a dotfiles repository there is not a project).
func findingsRoot(cwd, home string) (string, error) {
	root, err := session.FindProjectRoot(cwd, home)
	if err != nil {
		return "", fmt.Errorf("findings: %w", err)
	}
	root = session.CanonicalizePath(root)
	if _, err := os.Lstat(filepath.Join(root, ".git")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("findings: %s is not inside a git repository", cwd)
		}
		return "", fmt.Errorf("findings: %s: %w", root, err)
	}
	isHome, err := sameDirectory(root, home)
	if err != nil {
		return "", err
	}
	if isHome {
		return "", fmt.Errorf("findings: %s is the home directory, not a project: refusing to put a registry there", root)
	}
	return root, nil
}

func sameDirectory(a, b string) (bool, error) {
	infoA, err := os.Stat(a)
	if err != nil {
		return false, fmt.Errorf("findings: %s: %w", a, err)
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false, fmt.Errorf("findings: %s: %w", b, err)
	}
	return os.SameFile(infoA, infoB), nil
}

func printNotes(w io.Writer, notes []string) {
	for _, n := range notes {
		fmt.Fprintln(w, findingsNotePrefix+n)
	}
}
