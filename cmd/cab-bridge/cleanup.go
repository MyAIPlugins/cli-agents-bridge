package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/myAIPlugins/cli-agents-bridge/internal/cleanup"
	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/shellarg"
)

// ErrConfirmRequired is returned by runCleanup when scope=global is invoked
// from a non-TTY stdin without --force. Mapped to exit 3 in main.go.
var ErrConfirmRequired = errors.New("global cleanup requires explicit confirmation (non-tty: pass --force)")

func runCleanup(args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	scope := fs.String("scope", cleanup.ScopeMySession, "cleanup scope (my-session|global); global sweeps the stale sessions of THIS project root")
	allScopes := fs.Bool("all-scopes", false, "with --scope=global: sweep every project sharing this data dir, other teams included (pre-v0.8 behaviour)")
	force := fs.Bool("force", false, "skip TTY confirmation for --scope=global")
	// -1, not 0, for "not specified": zero has to mean the same thing here as it
	// does in CAB_RETENTION_DAYS, which is "disable the purge". A flag where 0
	// meant "use the default" while the env var made it purge the entire archive
	// is two meanings for one number, in the one place where guessing wrong
	// deletes everything.
	retention := fs.Int("retention", -1, "override RetentionDays from config (0 disables the retention purge; -1 = use config)")
	sessionIDFlag := fs.String("session-id", "", "for scope=my-session: target session ID (default: longest-prefix lookup from cwd)")
	// The TARGET, stated by the caller. --force says "do it without asking"; it
	// never said WHERE, and where is the thing that was got wrong.
	dataDirFlag := fs.String("data-dir", "", "the data dir you intend to act on; required with --scope=global --force (paste the path the command prints)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}
	mgr := newSessionManager(cfg)

	// The caller's project root confines scope=global, unless --all-scopes says
	// otherwise. Resolved from the cwd like everywhere else; a failure leaves it
	// empty, which sweeps only unowned sessions — the conservative direction for
	// a destructive command that no longer knows where it is standing.
	callerScope := ""
	if cwd, werr := os.Getwd(); werr == nil {
		callerScope = resolveScope(cwd)
	}

	// ANNOUNCE THE TARGET BEFORE ACTING, and never suppressibly. A critic ran
	// `CAB_RETENTION_DAYS=1 cleanup --scope=global --force` from inside its own
	// sandbox, after a `cd`, and deleted thirteen archived sessions from the
	// PRODUCTION data dir. The binary did exactly what the command said; what the
	// command let it believe is the defect.
	//
	// The data dir comes from $HOME, never from the cwd — so a `cd` into a test
	// directory protects nothing, and nothing said so. The retention notice added
	// earlier today prints AFTER the deletion: it reads the damage, it does not
	// prevent it.
	fmt.Fprintf(os.Stderr, "cab-bridge: cleanup will act on %s\n", cfg.DataDir)
	if *scope == cleanup.ScopeGlobal || opts0RetentionActive(cfg, *retention) {
		fmt.Fprintf(os.Stderr, "cab-bridge: the retention purge spans EVERY project in that data dir, whatever --scope says\n")
	}

	// And with --scope=global --force, the caller must NAME that dir. --force
	// removes the question, not the aim: an unattended run cannot read a warning,
	// so the only thing that stops the wrong target is having had to type it.
	// Requiring it only here keeps the ordinary my-session cleanup one word long.
	if *scope == cleanup.ScopeGlobal && *force {
		if *dataDirFlag == "" {
			return fmt.Errorf("cleanup: --scope=global --force also needs --data-dir, naming the data dir you mean.\n"+
				"  This command acts on %s — which comes from $HOME, NOT from the directory you are in.\n"+
				"  A `cd` into a sandbox does not change it, and that is how thirteen archived sessions\n"+
				"  were deleted from a live data dir.\n"+
				"  If that is really the one you mean:\n"+
				"    cab-bridge cleanup --scope=global --force --data-dir=%s",
				cfg.DataDir, shellarg.Quote(cfg.DataDir))
		}
		if filepath.Clean(*dataDirFlag) != filepath.Clean(cfg.DataDir) {
			return fmt.Errorf("cleanup: --data-dir=%s does not match the data dir in effect (%s).\n"+
				"  Refusing rather than guessing which of the two you meant",
				*dataDirFlag, cfg.DataDir)
		}
	}

	opts := cleanup.Options{
		DataDir:       cfg.DataDir,
		Scope:         *scope,
		CallerScope:   callerScope,
		AllScopes:     *allScopes,
		StaleSeconds:  cfg.StaleSeconds,
		RetentionDays: cfg.RetentionDays,
	}
	if *retention >= 0 {
		opts.RetentionDays = *retention
	}

	if *scope == cleanup.ScopeMySession {
		sid, err := resolveCurrentSession(mgr, "cleanup", *sessionIDFlag)
		if err != nil {
			return err
		}
		opts.OwnSessionID = sid
	}

	if *scope == cleanup.ScopeGlobal && !*force && !isTTY(os.Stdin) {
		return ErrConfirmRequired
	}
	if *scope == cleanup.ScopeGlobal && !*force && isTTY(os.Stdin) {
		where := "this project"
		if *allScopes {
			where = "ALL projects sharing this data dir"
		}
		fmt.Fprintf(os.Stderr, "Confirm cleanup of stale sessions in %s? [y/N]: ", where)
		var answer string
		_, _ = fmt.Scanln(&answer)
		if answer != "y" && answer != "Y" {
			return errors.New("cleanup global: aborted by user")
		}
	}

	res, err := cleanup.Run(context.Background(), opts)
	if err != nil {
		return err
	}

	// WHOSE, on stderr, next to the what. stdout stays exactly the JSON a script
	// already parses; this line is for the person who has just deleted sessions
	// and cannot tell from a list of ids whether any of them belonged to another
	// team sharing this data dir.
	for sc, n := range res.RemovedByScope {
		fmt.Fprintf(os.Stderr, "cab-bridge: removed %d session(s) from scope %s\n", n, sc)
	}
	// The change announces itself. Before v0.8 this command swept every project
	// sharing the data dir; whoever had a script expecting that gets a narrower
	// sweep, and without this line they would get it with no error and no clue.
	// The retention sweep is not scoped and never was: it is a data-minimisation
	// policy (GDPR-1), not a tidy-up, and it runs on EVERY cleanup whatever its
	// scope. Saying so is the whole point — its reach was invisible, which is how
	// a `--scope=my-session` came to delete other teams' archived mail.
	if len(res.ArchivesPurged) > 0 {
		fmt.Fprintf(os.Stderr,
			"cab-bridge: retention purge removed %d archive day(s) holding %d archived session(s), across ALL projects in this data dir: %s\n",
			len(res.ArchivesPurged), res.PurgedSessionCount, strings.Join(res.ArchivesPurged, ", "))
	}
	if res.SkippedOtherScopes > 0 {
		fmt.Fprintf(os.Stderr,
			"cab-bridge: %d stale session(s) in other scopes were NOT touched — pass --all-scopes if you want them\n",
			res.SkippedOtherScopes)
	}

	out, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return fmt.Errorf("cleanup: marshal result: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

// opts0RetentionActive reports whether this run will purge archives at all — the
// retention sweep is data-dir-wide by design, so its reach deserves saying even
// when --scope=my-session suggests something narrow.
func opts0RetentionActive(cfg config.Config, retentionFlag int) bool {
	days := cfg.RetentionDays
	if retentionFlag >= 0 {
		days = retentionFlag
	}
	return days > 0
}

// isTTY returns true if f is a terminal. Uses os.Stat mode bits — sufficient
// for Unix (mode&ModeCharDevice signals a tty/char device). Avoids
// golang.org/x/term dependency.
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
