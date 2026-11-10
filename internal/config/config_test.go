package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoad_RelativeDataDirResolvedToAbsolute covers FINDING-11: a relative
// data_dir (here via CAB_DATA_DIR) must be resolved to absolute and surface a
// warning, so downstream filepath.Join and the SC-7 boot check operate on the
// intended directory rather than something CWD-relative.
func TestLoad_RelativeDataDirResolvedToAbsolute(t *testing.T) {
	t.Setenv("CAB_DATA_DIR", "relative/data/dir")

	cfg, warnings, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// F-160 changed contract: CAB_DATA_DIR names the BASE; DataDir (the state dir)
	// is derived from it. The absolute check applies to the base, and the derived
	// dir must follow it.
	if !filepath.IsAbs(cfg.BaseDir) {
		t.Fatalf("BaseDir not absolute after Load: %q", cfg.BaseDir)
	}
	if !strings.HasSuffix(cfg.BaseDir, filepath.Join("relative", "data", "dir")) {
		t.Errorf("resolved BaseDir %q does not preserve the relative input suffix", cfg.BaseDir)
	}
	if want := filepath.Join(cfg.BaseDir, LayoutDir); cfg.DataDir != want {
		t.Errorf("DataDir = %q, want the layout dir under the resolved base %q", cfg.DataDir, want)
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "was relative") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a 'was relative' warning, got %v", warnings)
	}
}

// TestLoad_AbsoluteDataDirUnchanged ensures an already-absolute data_dir is
// left verbatim with no spurious relative warning.
func TestLoad_AbsoluteDataDirUnchanged(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "cab-data")
	t.Setenv("CAB_DATA_DIR", abs)

	cfg, warnings, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseDir != abs {
		t.Errorf("absolute BaseDir changed: got %q want %q", cfg.BaseDir, abs)
	}
	if want := filepath.Join(abs, LayoutDir); cfg.DataDir != want {
		t.Errorf("DataDir = %q, want %q (the state lives under the base, never in it)", cfg.DataDir, want)
	}
	for _, w := range warnings {
		if strings.Contains(w, "was relative") {
			t.Errorf("unexpected relative warning for absolute path: %q", w)
		}
	}
}

// TestDefaultConfig_AutoGCHours pins the v0.2.1 default: auto-gc ON at 24h.
func TestDefaultConfig_AutoGCHours(t *testing.T) {
	if got := DefaultConfig().AutoGCHours; got != 24 {
		t.Errorf("default AutoGCHours = %d, want 24", got)
	}
}

// TestLoad_AutoGCHoursEnvOverride covers the env override path, including the
// disable case (0). Unlike the user config.json (which ignores zero-values),
// CAB_AUTO_GC_HOURS=0 must take effect so users can turn auto-gc off.
func TestLoad_AutoGCHoursEnvOverride(t *testing.T) {
	t.Setenv("CAB_DATA_DIR", filepath.Join(t.TempDir(), "cab-data"))

	t.Setenv("CAB_AUTO_GC_HOURS", "48")
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AutoGCHours != 48 {
		t.Errorf("AutoGCHours = %d, want 48 (env override)", cfg.AutoGCHours)
	}

	t.Setenv("CAB_AUTO_GC_HOURS", "0")
	cfg, _, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AutoGCHours != 0 {
		t.Errorf("AutoGCHours = %d, want 0 (env must disable, overriding default 24)", cfg.AutoGCHours)
	}
}

// TestLoad_DefaultJSONIsCopyableAsUserConfig is the MUST-2/F-A regression: the
// repo's config/default.json (which carries a "_comment" key) must be copyable
// verbatim into ~/.claude/cli-agents-bridge/config.json. Before the fix the
// DisallowUnknownFields decoder rejected "_comment", so the documented "copy
// this file and edit it" path failed. Also exercises F-B: the file is read
// from CAB_DATA_DIR, not the compiled-in default.
func TestLoad_DefaultJSONIsCopyableAsUserConfig(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)

	src, err := os.ReadFile(filepath.Join("..", "..", "config", "default.json"))
	if err != nil {
		t.Fatalf("read repo default.json: %v", err)
	}
	if !strings.Contains(string(src), "_comment") {
		t.Fatal("precondition: default.json should contain a _comment key for this regression")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), src, 0o600); err != nil {
		t.Fatalf("write user config.json: %v", err)
	}

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load must accept default.json copied as config.json, got: %v", err)
	}
	// A value from the file proves it was actually parsed and applied (F-B:
	// read from CAB_DATA_DIR), not silently skipped.
	if cfg.StaleSeconds != 300 {
		t.Errorf("StaleSeconds = %d, want 300 from the copied default.json", cfg.StaleSeconds)
	}
	if cfg.AutoGCHours != 24 {
		t.Errorf("AutoGCHours = %d, want 24 from the copied default.json", cfg.AutoGCHours)
	}
}

// TestLoad_UserConfigReadFromDataDirEnv pins F-B directly: a config.json placed
// under CAB_DATA_DIR is read (the path used to be computed from the default dir
// before the env override applied, so a custom dir's file was ignored).
func TestLoad_UserConfigReadFromDataDirEnv(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"),
		[]byte(`{"stale_seconds": 999}`), 0o600); err != nil {
		t.Fatalf("write user config.json: %v", err)
	}

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StaleSeconds != 999 {
		t.Errorf("StaleSeconds = %d, want 999 — config.json under CAB_DATA_DIR must be read (F-B)", cfg.StaleSeconds)
	}
}

// TestDefaultConfig_DedupWindowSeconds pins the F-43 default: dedup window 10s.
func TestDefaultConfig_DedupWindowSeconds(t *testing.T) {
	if got := DefaultConfig().DedupWindowSeconds; got != 10 {
		t.Errorf("default DedupWindowSeconds = %d, want 10", got)
	}
}

// TestLoad_DedupWindowSecondsEnvOverride covers the env override and the disable
// case (0). Like CAB_AUTO_GC_HOURS, a 0 via env must take effect (the user file
// ignores zero-values), so users can turn the duplicate check off.
func TestLoad_DedupWindowSecondsEnvOverride(t *testing.T) {
	t.Setenv("CAB_DATA_DIR", filepath.Join(t.TempDir(), "cab-data"))

	t.Setenv("CAB_DEDUP_WINDOW_SECONDS", "30")
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DedupWindowSeconds != 30 {
		t.Errorf("DedupWindowSeconds = %d, want 30 (env override)", cfg.DedupWindowSeconds)
	}

	t.Setenv("CAB_DEDUP_WINDOW_SECONDS", "0")
	cfg, _, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DedupWindowSeconds != 0 {
		t.Errorf("DedupWindowSeconds = %d, want 0 (env must disable, overriding default 10)", cfg.DedupWindowSeconds)
	}
}

// TestDefaultConfig_FindingsStaleDays pins the F-156 default: a finding is
// stale after 7 days, in code and in the reference config/default.json.
func TestDefaultConfig_FindingsStaleDays(t *testing.T) {
	if got := DefaultConfig().FindingsStaleDays; got != 7 {
		t.Errorf("default FindingsStaleDays = %d, want 7", got)
	}
	if got := DefaultConfig().FindingsDeniedNames; len(got) != 0 {
		t.Errorf("default FindingsDeniedNames = %q, want empty (names are not checked until configured)", got)
	}
}

// TestLoad_FindingsKeys covers where the two F-156 keys come from: the stale
// days from the file and from CAB_FINDINGS_STALE_DAYS (env wins, and a value
// below 1 is passed through so `findings stale` can refuse it by name), the
// denied names from config.json only.
func TestLoad_FindingsKeys(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FindingsStaleDays != 7 || len(cfg.FindingsDeniedNames) != 0 {
		t.Fatalf("no file: FindingsStaleDays = %d, FindingsDeniedNames = %q, want 7 and empty", cfg.FindingsStaleDays, cfg.FindingsDeniedNames)
	}

	if err := os.WriteFile(filepath.Join(dataDir, "config.json"),
		[]byte(`{"findings_stale_days": 3, "findings_denied_names": ["Marta", "Niccolò"]}`), 0o600); err != nil {
		t.Fatalf("write user config.json: %v", err)
	}
	cfg, _, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FindingsStaleDays != 3 {
		t.Errorf("FindingsStaleDays = %d, want 3 from config.json", cfg.FindingsStaleDays)
	}
	if len(cfg.FindingsDeniedNames) != 2 || cfg.FindingsDeniedNames[0] != "Marta" || cfg.FindingsDeniedNames[1] != "Niccolò" {
		t.Errorf("FindingsDeniedNames = %q, want [Marta Niccolò] from config.json", cfg.FindingsDeniedNames)
	}

	t.Setenv("CAB_FINDINGS_STALE_DAYS", "14")
	cfg, _, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FindingsStaleDays != 14 {
		t.Errorf("FindingsStaleDays = %d, want 14 (env overrides the file)", cfg.FindingsStaleDays)
	}

	t.Setenv("CAB_FINDINGS_STALE_DAYS", "0")
	cfg, _, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.FindingsStaleDays != 0 {
		t.Errorf("FindingsStaleDays = %d, want 0 passed through (the command refuses it, the loader does not hide it)", cfg.FindingsStaleDays)
	}
}

// TestLoad_FindingsDeniedNamesHaveNoEnvOverride: the list of names is not read
// from the environment, whatever the variable is called.
func TestLoad_FindingsDeniedNamesHaveNoEnvOverride(t *testing.T) {
	t.Setenv("CAB_DATA_DIR", t.TempDir())
	t.Setenv("CAB_FINDINGS_DENIED_NAMES", "Marta")
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.FindingsDeniedNames) != 0 {
		t.Errorf("FindingsDeniedNames = %q, want empty: no env override exists", cfg.FindingsDeniedNames)
	}
}

// TestLoad_DefaultJSONCarriesTheFindingsDefault: the reference file is what a
// user copies, so the F-156 default must be in it and must load.
func TestLoad_DefaultJSONCarriesTheFindingsDefault(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	src, err := os.ReadFile(filepath.Join("..", "..", "config", "default.json"))
	if err != nil {
		t.Fatalf("read repo default.json: %v", err)
	}
	if !strings.Contains(string(src), `"findings_stale_days": 7`) {
		t.Fatal("config/default.json must carry findings_stale_days: 7")
	}
	if err := os.WriteFile(filepath.Join(dataDir, "config.json"), src, 0o600); err != nil {
		t.Fatalf("write user config.json: %v", err)
	}
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load must accept default.json copied as config.json, got: %v", err)
	}
	if cfg.FindingsStaleDays != 7 {
		t.Errorf("FindingsStaleDays = %d, want 7 from the copied default.json", cfg.FindingsStaleDays)
	}
}
