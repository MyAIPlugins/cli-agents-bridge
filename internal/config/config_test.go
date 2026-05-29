package config

import (
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
	if !filepath.IsAbs(cfg.DataDir) {
		t.Fatalf("DataDir not absolute after Load: %q", cfg.DataDir)
	}
	if !strings.HasSuffix(cfg.DataDir, filepath.Join("relative", "data", "dir")) {
		t.Errorf("resolved DataDir %q does not preserve the relative input suffix", cfg.DataDir)
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
	if cfg.DataDir != abs {
		t.Errorf("absolute DataDir changed: got %q want %q", cfg.DataDir, abs)
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
