package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/myAIPlugins/cli-agents-bridge/internal/cleanup"
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
	retention := fs.Int("retention", 0, "override RetentionDays from config (0 = use config default)")
	sessionIDFlag := fs.String("session-id", "", "for scope=my-session: target session ID (default: longest-prefix lookup from cwd)")
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

	opts := cleanup.Options{
		DataDir:       cfg.DataDir,
		Scope:         *scope,
		CallerScope:   callerScope,
		AllScopes:     *allScopes,
		StaleSeconds:  cfg.StaleSeconds,
		RetentionDays: cfg.RetentionDays,
	}
	if *retention > 0 {
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
