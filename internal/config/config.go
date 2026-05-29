// Package config loads the cli-agents-bridge runtime configuration.
//
// Resolution order (last wins):
//  1. Hardcoded Go defaults (see DefaultConfig)
//  2. ~/.claude/cli-agents-bridge/config.json (if exists)
//  3. CAB_* environment variables (per-field override)
//
// The user-facing reference for default values lives in config/default.json
// at the repo root. The Go struct defaults below are the source of truth at
// runtime — keep them in sync with the doc file.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// Config holds all tunable runtime parameters. Values are populated in
// Resolution order (defaults → user file → env). All fields exposed for
// observability (cab status, cab inspect-config in later sprints).
type Config struct {
	// DataDir is the root of all bridge state.
	// Default: ~/.claude/cli-agents-bridge/
	// Env override: CAB_DATA_DIR
	DataDir string `json:"data_dir"`

	// StaleSeconds is how old lastHeartbeat must be before a peer is
	// considered stale by list-peers and cleanup.
	// Default: 300 (5 min).
	// Env override: CAB_STALE_SECONDS
	// Resolves BUG-8 (inconsistency between list-peers 300 and cleanup 1800).
	StaleSeconds int `json:"stale_seconds"`

	// PollIntervalMs is the filesystem polling interval (ms) for listen mode.
	// Default: 1000.
	// Env override: CAB_POLL_INTERVAL_MS
	PollIntervalMs int `json:"poll_interval_ms"`

	// MaxBlockingSeconds caps how long listen blocks before returning exit
	// 124 to allow the agent harness to re-run. Must stay below the agent
	// harness 10-min timeout.
	// Default: 540 (9 min).
	// Env override: CAB_MAX_BLOCKING_SECONDS
	MaxBlockingSeconds int `json:"max_blocking_seconds"`

	// MaxInboxSize is the maximum number of pending messages in an inbox
	// before send-message returns backpressure error.
	// Default: 100.
	// Env override: CAB_MAX_INBOX_SIZE
	MaxInboxSize int `json:"max_inbox_size"`

	// MaxMessageBytes is the per-message size limit (bytes).
	// Default: 65536 (64 KB).
	// Env override: CAB_MAX_MESSAGE_BYTES
	MaxMessageBytes int `json:"max_message_bytes"`

	// RetentionDays controls GDPR-1 data minimization. Messages older than
	// this are archived/purged by cleanup.
	// Default: 7.
	// Env override: CAB_RETENTION_DAYS
	RetentionDays int `json:"retention_days"`

	// HeartbeatTickMs is the interval at which the session manager updates
	// lastHeartbeat in the manifest. Must stay well below StaleSeconds*1000
	// to avoid false stale detection.
	// Default: 30000 (30s — gives 3x safety buffer vs <90s test threshold,
	// and 10x vs default 300s StaleSeconds).
	// Env override: CAB_HEARTBEAT_TICK_MS (use small values like 100 in
	// tests to compress wall-clock).
	HeartbeatTickMs int `json:"heartbeat_tick_ms"`

	// AutoGCHours is the age threshold (in hours) above which an orphan
	// session (owning PID dead AND heartbeat older than this) is swept by the
	// auto-gc pass that runs at register startup (v0.2.1, F10). The double
	// condition is deliberate: a just-registered session already has a dead
	// PID (the one-shot register process exited), so PID-death alone is not
	// enough — only a stale heartbeat confirms abandonment (LL-10).
	// Default: 24 (generous — only sweeps sessions abandoned for >24h).
	// Set to 0 to disable auto-gc entirely (manual `cab-bridge cleanup` only).
	// Env override: CAB_AUTO_GC_HOURS. NOTE: 0 disables, but a 0 in the user
	// config.json is ignored (applyUserFile skips zero-values) — disable via
	// the env var instead.
	AutoGCHours int `json:"auto_gc_hours"`
}

// DefaultConfig returns the source-of-truth default values. The DataDir is
// computed relative to the current user's home directory.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DataDir:            filepath.Join(home, ".claude", "cli-agents-bridge"),
		StaleSeconds:       300,
		PollIntervalMs:     1000,
		MaxBlockingSeconds: 540,
		MaxInboxSize:       100,
		MaxMessageBytes:    65536,
		RetentionDays:      7,
		HeartbeatTickMs:    30000,
		AutoGCHours:        24,
	}
}

// Load resolves the runtime config by applying user file then env overrides
// on top of DefaultConfig. Returns the resolved Config plus any non-fatal
// warnings (e.g. malformed env var ignored). A nil error means resolution
// succeeded; the user file is optional and absence is not an error.
func Load() (Config, []string, error) {
	cfg := DefaultConfig()
	var warnings []string

	userFile := filepath.Join(cfg.DataDir, "config.json")
	if err := applyUserFile(&cfg, userFile); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return cfg, warnings, fmt.Errorf("user config %q: %w", userFile, err)
		}
		// Missing user file is fine — defaults will be used.
	}

	warnings = append(warnings, applyEnv(&cfg)...)

	// FINDING-11: DataDir must be absolute. A relative data_dir (from a
	// relative CAB_DATA_DIR or config.json value) would make every
	// filepath.Join CWD-relative and would let the SC-7 boot check inspect
	// the wrong directory. We resolve rather than hard-fail to keep first-run
	// ergonomics, but a relative path is almost always a user error so we warn.
	if !filepath.IsAbs(cfg.DataDir) {
		abs, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			return cfg, warnings, fmt.Errorf("resolve data dir %q to absolute: %w", cfg.DataDir, err)
		}
		warnings = append(warnings, fmt.Sprintf("data_dir %q was relative, resolved to absolute %q", cfg.DataDir, abs))
		cfg.DataDir = abs
	}

	return cfg, warnings, nil
}

// applyUserFile reads path as JSON and merges non-zero fields into cfg. Zero
// values in the file are ignored (cannot reset a field to "0" via the user
// file — use env var if needed).
func applyUserFile(cfg *Config, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var override Config
	if err := dec.Decode(&override); err != nil {
		return fmt.Errorf("parse json: %w", err)
	}

	if override.DataDir != "" {
		cfg.DataDir = override.DataDir
	}
	if override.StaleSeconds != 0 {
		cfg.StaleSeconds = override.StaleSeconds
	}
	if override.PollIntervalMs != 0 {
		cfg.PollIntervalMs = override.PollIntervalMs
	}
	if override.MaxBlockingSeconds != 0 {
		cfg.MaxBlockingSeconds = override.MaxBlockingSeconds
	}
	if override.MaxInboxSize != 0 {
		cfg.MaxInboxSize = override.MaxInboxSize
	}
	if override.MaxMessageBytes != 0 {
		cfg.MaxMessageBytes = override.MaxMessageBytes
	}
	if override.RetentionDays != 0 {
		cfg.RetentionDays = override.RetentionDays
	}
	if override.HeartbeatTickMs != 0 {
		cfg.HeartbeatTickMs = override.HeartbeatTickMs
	}
	if override.AutoGCHours != 0 {
		cfg.AutoGCHours = override.AutoGCHours
	}
	return nil
}

// applyEnv reads CAB_* env vars and overrides cfg fields. Returns warnings
// for malformed numeric values (the bad var is ignored, the default kept).
func applyEnv(cfg *Config) []string {
	var warnings []string

	if v := os.Getenv("CAB_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if w := envInt("CAB_STALE_SECONDS", &cfg.StaleSeconds); w != "" {
		warnings = append(warnings, w)
	}
	if w := envInt("CAB_POLL_INTERVAL_MS", &cfg.PollIntervalMs); w != "" {
		warnings = append(warnings, w)
	}
	if w := envInt("CAB_MAX_BLOCKING_SECONDS", &cfg.MaxBlockingSeconds); w != "" {
		warnings = append(warnings, w)
	}
	if w := envInt("CAB_MAX_INBOX_SIZE", &cfg.MaxInboxSize); w != "" {
		warnings = append(warnings, w)
	}
	if w := envInt("CAB_MAX_MESSAGE_BYTES", &cfg.MaxMessageBytes); w != "" {
		warnings = append(warnings, w)
	}
	if w := envInt("CAB_RETENTION_DAYS", &cfg.RetentionDays); w != "" {
		warnings = append(warnings, w)
	}
	if w := envInt("CAB_HEARTBEAT_TICK_MS", &cfg.HeartbeatTickMs); w != "" {
		warnings = append(warnings, w)
	}
	if w := envInt("CAB_AUTO_GC_HOURS", &cfg.AutoGCHours); w != "" {
		warnings = append(warnings, w)
	}

	return warnings
}

// envInt parses the named env var as int and writes it to dst. If the var is
// unset, dst is unchanged. If parsing fails, dst is unchanged and a warning
// string is returned.
func envInt(name string, dst *int) string {
	raw := os.Getenv(name)
	if raw == "" {
		return ""
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Sprintf("env %s=%q is not a valid int, using default %d", name, raw, *dst)
	}
	*dst = n
	return ""
}

