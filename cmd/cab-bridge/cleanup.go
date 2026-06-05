package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/myAIPlugins/cli-agents-bridge/internal/cleanup"
)

// ErrConfirmRequired is returned by runCleanup when scope=global is invoked
// from a non-TTY stdin without --force. Mapped to exit 3 in main.go.
var ErrConfirmRequired = errors.New("global cleanup requires explicit confirmation (non-tty: pass --force)")

func runCleanup(args []string) error {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	scope := fs.String("scope", cleanup.ScopeMySession, "cleanup scope (my-session|global)")
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

	opts := cleanup.Options{
		DataDir:       cfg.DataDir,
		Scope:         *scope,
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
		fmt.Fprint(os.Stderr, "Confirm global cleanup of all stale sessions across all projects? [y/N]: ")
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
