package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

type migrateReport struct {
	BackupDir       string   `json:"backupDir"`
	DryRun          bool     `json:"dryRun"`
	Migrated        []string `json:"migrated"`
	SkippedExisting []string `json:"skippedExisting"`
	SkippedInvalid  []string `json:"skippedInvalid"`
	Errors          []string `json:"errors"`
}

// runMigrate implements migrate-from-patil per PLAN §6.2.
//
// Pipeline:
//  1. Determine source: ~/.claude/session-bridge/ (Patil canonical path).
//     Empty -> nothing to do, exit 0.
//  2. Backup source -> ~/.claude/cli-agents-bridge/migration-backup-<ts>/
//     (skip on --dry-run).
//  3. For each session in source/sessions/<id>/:
//     a. Read manifest.json. Reject if malformed.
//     b. SC-4 validate id + projectPath (RC-3 security: corrupted v1
//        manifests may contain ../ traversal in projectPath).
//     c. Transform to v2: schemaVersion=2, role=neutral, agentName=projectName,
//        pid=0 (no live PID inferable for legacy session).
//     d. If target exists and has .migrated marker → skip (idempotent).
//     e. Write target manifest with 0o600 (SC-5) + atomic write.
//     f. Copy inbox/*.json + outbox/*.json preserving names (0o600).
//     g. Drop .migrated marker.
//  4. Source is never modified.
func runMigrate(args []string) error {
	fs_ := flag.NewFlagSet("migrate-from-patil", flag.ContinueOnError)
	fs_.SetOutput(os.Stderr)
	dryRun := fs_.Bool("dry-run", false, "scan + report without writing target or backup")
	patilDir := fs_.String("patil-dir", "", "override source path (default: ~/.claude/session-bridge)")
	if err := fs_.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	cfg, err := loadConfigOrFail()
	if err != nil {
		return err
	}

	src := *patilDir
	if src == "" {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return fmt.Errorf("migrate: resolve $HOME: %w", herr)
		}
		src = filepath.Join(home, ".claude", "session-bridge")
	}

	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(os.Stderr, "migrate: source", src, "does not exist — nothing to do")
			return nil
		}
		return fmt.Errorf("migrate: stat source %q: %w", src, err)
	}

	rep := migrateReport{DryRun: *dryRun}

	if !*dryRun {
		ts := time.Now().UTC().Format("2006-01-02-150405")
		backup := filepath.Join(cfg.DataDir, "migration-backup-"+ts)
		if err := os.MkdirAll(filepath.Dir(backup), 0o700); err != nil {
			return fmt.Errorf("migrate: mkdir backup parent: %w", err)
		}
		if err := copyTree(src, backup); err != nil {
			return fmt.Errorf("migrate: backup %q -> %q: %w", src, backup, err)
		}
		rep.BackupDir = backup
	}

	sessionsRoot := filepath.Join(src, "sessions")
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			rep.Errors = append(rep.Errors, "no sessions/ subdir in source")
		} else {
			return fmt.Errorf("migrate: read source sessions: %w", err)
		}
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sid := e.Name()
		if err := security.ValidateSessionID(sid); err != nil {
			rep.SkippedInvalid = append(rep.SkippedInvalid, sid+" (invalid id)")
			continue
		}

		srcSessionDir := filepath.Join(sessionsRoot, sid)
		srcManifestPath := filepath.Join(srcSessionDir, "manifest.json")
		mf, err := loadPatilManifest(srcManifestPath)
		if err != nil {
			rep.SkippedInvalid = append(rep.SkippedInvalid, sid+" ("+err.Error()+")")
			continue
		}

		// FINDING-4 / NEW-1: the directory name (sid, SC-4 validated above) is
		// the real session identity. Reject any manifest whose internal
		// sessionId diverges from it (manual rename or hand-crafted import) and
		// re-validate the internal value defensively. Prevents writing an
		// incoherent manifest whose sessionId could later be propagated as a
		// path component by LongestPrefixLookup.
		if mf.SessionID != sid {
			rep.SkippedInvalid = append(rep.SkippedInvalid, sid+" (manifest sessionId "+mf.SessionID+" != dir name)")
			continue
		}
		if err := security.ValidateSessionID(mf.SessionID); err != nil {
			rep.SkippedInvalid = append(rep.SkippedInvalid, sid+" (manifest sessionId invalid)")
			continue
		}

		// RC-3 (security audit Sprint 0): legacy manifests may carry
		// hand-crafted projectPath. Reject anything that looks like a
		// traversal attempt. We accept absolute paths under $HOME but
		// reject literal "..".
		if strings.Contains(mf.ProjectPath, "..") {
			rep.SkippedInvalid = append(rep.SkippedInvalid, sid+" (projectPath contains '..')")
			continue
		}

		dstSessionDir := filepath.Join(cfg.DataDir, "sessions", sid)
		dstMarker := filepath.Join(dstSessionDir, ".migrated")
		if _, err := os.Stat(dstMarker); err == nil {
			rep.SkippedExisting = append(rep.SkippedExisting, sid)
			continue
		}

		if *dryRun {
			rep.Migrated = append(rep.Migrated, sid+" (dry-run)")
			continue
		}

		if err := migrateOne(srcSessionDir, dstSessionDir, mf); err != nil {
			rep.Errors = append(rep.Errors, sid+": "+err.Error())
			continue
		}
		rep.Migrated = append(rep.Migrated, sid)
	}

	out, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("migrate: marshal report: %w", err)
	}
	fmt.Println(string(out))
	return nil
}

// loadPatilManifest reads a v1 (or v2) manifest into our struct, applying
// v1 defaults. Returns a friendly error on malformed JSON or missing
// required fields.
func loadPatilManifest(path string) (*session.Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var mf session.Manifest
	if err := json.Unmarshal(data, &mf); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if mf.SessionID == "" || mf.ProjectPath == "" {
		return nil, errors.New("manifest missing sessionId or projectPath")
	}
	if mf.SchemaVersion == 0 || mf.SchemaVersion == 1 {
		mf.SchemaVersion = session.SchemaVersionV2
		mf.ApplyV1Defaults()
	}
	if mf.Status == "" {
		mf.Status = session.StatusActive
	}
	if mf.Capabilities == nil {
		mf.Capabilities = []string{"query", "context-dump", "conversation"}
	}
	return &mf, nil
}

// migrateOne writes the upgraded manifest into dstSessionDir + copies any
// inbox/outbox files + drops the .migrated marker for idempotency.
func migrateOne(srcSessionDir, dstSessionDir string, mf *session.Manifest) error {
	if err := os.MkdirAll(dstSessionDir, 0o700); err != nil {
		return fmt.Errorf("mkdir target: %w", err)
	}
	for _, sub := range []string{"inbox", "outbox"} {
		if err := os.MkdirAll(filepath.Join(dstSessionDir, sub), 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}

	// NEW-2: replicate session.Manager.SaveManifest's fail-closed guarantee —
	// never write a manifest that does not pass Validate (migrateOne uses
	// AtomicWriteJSON directly to avoid a Manager dependency).
	if err := mf.Validate(); err != nil {
		return fmt.Errorf("manifest failed validation, refusing to write: %w", err)
	}
	if err := transportfs.AtomicWriteJSON(filepath.Join(dstSessionDir, "manifest.json"), mf); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	for _, sub := range []string{"inbox", "outbox"} {
		srcDir := filepath.Join(srcSessionDir, sub)
		dstDir := filepath.Join(dstSessionDir, sub)
		entries, err := os.ReadDir(srcDir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read %s: %w", sub, err)
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if err := copyFile(filepath.Join(srcDir, e.Name()), filepath.Join(dstDir, e.Name()), 0o600); err != nil {
				return fmt.Errorf("copy %s/%s: %w", sub, e.Name(), err)
			}
		}
	}

	// .migrated marker for idempotency (next dry-run / re-run skips this id)
	if err := os.WriteFile(filepath.Join(dstSessionDir, ".migrated"), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		return fmt.Errorf("write .migrated marker: %w", err)
	}
	return nil
}

// copyFile is a tiny helper that preserves content with the requested mode.
// Used by both migrateOne (per-message copy) and copyTree (backup pass).
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// copyTree recursively duplicates srcRoot into dstRoot preserving file modes
// at 0o600 / 0o700 (we re-tighten perms — migration target must not inherit
// loose source perms). Used for the backup step.
func copyTree(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dstRoot, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(path, target, 0o600)
	})
}
