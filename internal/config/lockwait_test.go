package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F-160: session_lock_wait_ms is how long a command that needs a session lock waits
// for it. 5 s is a default, not a maximum: the file and the environment change it,
// and a value of zero or less means "do not wait".

const wantLockWaitMs = 5000

func TestDefaultConfig_SessionLockWait(t *testing.T) {
	if got := DefaultConfig().SessionLockWaitMs; got != wantLockWaitMs {
		t.Errorf("default SessionLockWaitMs = %d, want %d", got, wantLockWaitMs)
	}
}

// The reference config/default.json carries the same number as the code, and a
// copy of it is accepted as a user config (the "copy this file and edit it" path).
func TestLoad_DefaultJSONCarriesTheLockWait(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "config", "default.json"))
	if err != nil {
		t.Fatalf("read repo default.json: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(src, &raw); err != nil {
		t.Fatalf("default.json is not JSON: %v", err)
	}
	got, ok := raw["session_lock_wait_ms"]
	if !ok {
		t.Fatal("config/default.json must carry session_lock_wait_ms")
	}
	if strings.TrimSpace(string(got)) != "5000" {
		t.Errorf("default.json session_lock_wait_ms = %s, want %d (the value in code)", got, wantLockWaitMs)
	}

	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), src, 0o600); err != nil {
		t.Fatalf("write user config.json: %v", err)
	}
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load must accept default.json copied as config.json, got: %v", err)
	}
	if cfg.SessionLockWaitMs != wantLockWaitMs {
		t.Errorf("SessionLockWaitMs = %d, want %d from the copied default.json", cfg.SessionLockWaitMs, wantLockWaitMs)
	}
}

// The three places a value can come from, in order: default, file, environment.
// The environment wins, and it is the only one that can say "do not wait" with a
// zero (the file ignores zero values, like every other key in it).
func TestLoad_SessionLockWaitSources(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	load := func() Config {
		t.Helper()
		cfg, _, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return cfg
	}
	writeUser := func(body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
	}

	if got := load().SessionLockWaitMs; got != wantLockWaitMs {
		t.Errorf("no file, no env: %d, want the default %d", got, wantLockWaitMs)
	}

	writeUser(`{"session_lock_wait_ms": 200}`)
	if got := load().SessionLockWaitMs; got != 200 {
		t.Errorf("from the file: %d, want 200", got)
	}

	writeUser(`{"session_lock_wait_ms": 0}`)
	if got := load().SessionLockWaitMs; got != wantLockWaitMs {
		t.Errorf("a zero in the file is ignored: %d, want the default %d", got, wantLockWaitMs)
	}

	writeUser(`{"session_lock_wait_ms": 200}`)
	t.Setenv("CAB_SESSION_LOCK_WAIT_MS", "3000")
	if got := load().SessionLockWaitMs; got != 3000 {
		t.Errorf("the environment wins over the file: %d, want 3000", got)
	}

	t.Setenv("CAB_SESSION_LOCK_WAIT_MS", "0")
	if got := load().SessionLockWaitMs; got != 0 {
		t.Errorf("CAB_SESSION_LOCK_WAIT_MS=0 must mean no wait: %d, want 0", got)
	}

	t.Setenv("CAB_SESSION_LOCK_WAIT_MS", "-1")
	if got := load().SessionLockWaitMs; got != -1 {
		t.Errorf("a negative value is passed through, and the command layer treats <= 0 as no wait: %d, want -1", got)
	}
}

// A value that is not a number is a warning and leaves the previous value alone;
// it does not turn into a silent zero (which would mean "do not wait").
func TestLoad_SessionLockWaitEnvThatIsNotANumberWarns(t *testing.T) {
	t.Setenv("CAB_DATA_DIR", t.TempDir())
	t.Setenv("CAB_SESSION_LOCK_WAIT_MS", "soon")

	cfg, warnings, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.SessionLockWaitMs != wantLockWaitMs {
		t.Errorf("SessionLockWaitMs = %d, want the default %d", cfg.SessionLockWaitMs, wantLockWaitMs)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "CAB_SESSION_LOCK_WAIT_MS") {
			found = true
		}
	}
	if !found {
		t.Errorf("no warning names CAB_SESSION_LOCK_WAIT_MS: %q", warnings)
	}
}
