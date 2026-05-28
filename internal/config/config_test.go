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
