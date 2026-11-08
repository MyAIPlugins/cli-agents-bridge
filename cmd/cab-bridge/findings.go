package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
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
const findingsUsage = `usage:
  cab-bridge findings add             one JSON object on stdin, with the columns:
                                      lotto, gate, critico, chiave, finding, severita, evidenza,
                                      decisione, esito (required); azione, modello, token, costo (optional)
  cab-bridge findings stale [--days=N]   open findings older than N days (default findings_stale_days), as TSV`

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
		_, err := fmt.Fprintln(stderr, findingsUsage)
		return err
	case "add":
		return findingsAdd(args[1:], stdin, stdout, stderr, deps)
	case "stale":
		return findingsStale(args[1:], stdout, stderr, deps)
	default:
		return fmt.Errorf("findings: unknown action %q\n%s", args[0], findingsUsage)
	}
}

func findingsAdd(args []string, stdin io.Reader, stdout, stderr io.Writer, deps findingsDeps) error {
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
		_, err := fmt.Fprintln(stderr, findingsUsage)
		return err
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
	if res.Row.Chiave != "" {
		// Also when the lock failed to release after the row was recorded: the
		// error below says so, and this line says what was recorded.
		fmt.Fprintf(stdout, "recorded %s first_seen=%s -> %s\n", res.Row.Chiave, res.Row.FirstSeen, store.RegistryPath())
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
			return nil
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
