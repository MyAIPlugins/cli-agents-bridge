package config

import (
	"os"
	"path/filepath"
	"testing"
)

// F-160: DataDir (the state dir) is always <BaseDir>/layout-2, derived once, in
// one place. No input moves it independently of the base.

func TestDefaultConfig_StateDirIsTheLayoutDirUnderTheBase(t *testing.T) {
	cfg := DefaultConfig()
	if want := filepath.Join(cfg.BaseDir, LayoutDir); cfg.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, want)
	}
	if cfg.DataDir == cfg.BaseDir {
		t.Errorf("base and state are the same directory: %q", cfg.DataDir)
	}
}

func TestLoad_EnvMovesTheBaseAndTheStateFollows(t *testing.T) {
	base := filepath.Join(t.TempDir(), "elsewhere")
	t.Setenv("CAB_DATA_DIR", base)
	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseDir != base {
		t.Errorf("BaseDir = %q, want %q", cfg.BaseDir, base)
	}
	if want := filepath.Join(base, LayoutDir); cfg.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, want)
	}
}

// config.json's data_dir is the BASE too (it always was "where the bridge lives"):
// a file that moves it moves the state with it, and the key keeps its name, so a
// config.json written for an older binary still means the same place.
func TestLoad_ConfigFileDataDirIsTheBase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CAB_DATA_DIR", "")
	moved := filepath.Join(t.TempDir(), "moved")
	defaultBase := filepath.Join(home, ".claude", "cli-agents-bridge")
	if err := os.MkdirAll(defaultBase, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"data_dir": ` + quoteJSON(moved) + `}`)
	if err := os.WriteFile(filepath.Join(defaultBase, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BaseDir != moved {
		t.Errorf("BaseDir = %q, want the data_dir of config.json %q", cfg.BaseDir, moved)
	}
	if want := filepath.Join(moved, LayoutDir); cfg.DataDir != want {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, want)
	}
}

// The state dir is not a setting: the file cannot name it, because an old binary
// would then decode a key it does not know (DisallowUnknownFields) and refuse to
// start, and because a state dir that can differ from <base>/layout-2 is the
// split this change exists to remove.
func TestLoad_TheStateDirIsNotAConfigKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CAB_DATA_DIR", "")
	defaultBase := filepath.Join(home, ".claude", "cli-agents-bridge")
	if err := os.MkdirAll(defaultBase, 0o700); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"DataDir": "/somewhere/else"}`)
	if err := os.WriteFile(filepath.Join(defaultBase, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Load(); err == nil {
		t.Fatal("a config.json that names DataDir was accepted: the state dir became a setting")
	}
}

func quoteJSON(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		if r == '\\' || r == '"' {
			out = append(out, '\\')
		}
		out = append(out, string(r)...)
	}
	return string(append(out, '"'))
}
