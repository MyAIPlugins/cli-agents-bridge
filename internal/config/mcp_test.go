package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMCPLimits_DefaultsFileAndEnvironment(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("CAB_DATA_DIR", dataDir)
		cfg, _, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		got := [...]int{cfg.MCPNextMaxWaitSeconds, cfg.MCPNextDefaultWaitSeconds, cfg.MCPScanBudgetSeconds, cfg.MCPChildTimeoutSeconds, cfg.MCPMaxResultBytes, cfg.MCPGrantTTLMinutes}
		want := [...]int{40, 25, 5, 20, 24576, 480}
		if got != want {
			t.Fatalf("defaults = %v, want %v", got, want)
		}
	})

	t.Run("the user file overrides", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("CAB_DATA_DIR", dataDir)
		body := `{"mcp_next_max_wait_seconds": 30, "mcp_next_default_wait_seconds": 10, "mcp_scan_budget_seconds": 3, "mcp_child_timeout_seconds": 15, "mcp_max_result_bytes": 12288}`
		if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		got := [...]int{cfg.MCPNextMaxWaitSeconds, cfg.MCPNextDefaultWaitSeconds, cfg.MCPScanBudgetSeconds, cfg.MCPChildTimeoutSeconds, cfg.MCPMaxResultBytes}
		want := [...]int{30, 10, 3, 15, 12288}
		if got != want {
			t.Fatalf("from file = %v, want %v", got, want)
		}
	})

	t.Run("the environment wins over the file", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("CAB_DATA_DIR", dataDir)
		if err := os.WriteFile(filepath.Join(dataDir, "config.json"), []byte(`{"mcp_next_max_wait_seconds": 30}`), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CAB_MCP_NEXT_MAX_WAIT_SECONDS", "35")
		t.Setenv("CAB_MCP_NEXT_DEFAULT_WAIT_SECONDS", "12")
		t.Setenv("CAB_MCP_SCAN_BUDGET_SECONDS", "2")
		t.Setenv("CAB_MCP_CHILD_TIMEOUT_SECONDS", "9")
		t.Setenv("CAB_MCP_MAX_RESULT_BYTES", "8192")
		cfg, warnings, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		got := [...]int{cfg.MCPNextMaxWaitSeconds, cfg.MCPNextDefaultWaitSeconds, cfg.MCPScanBudgetSeconds, cfg.MCPChildTimeoutSeconds, cfg.MCPMaxResultBytes}
		want := [...]int{35, 12, 2, 9, 8192}
		if got != want {
			t.Fatalf("from env = %v, want %v (warnings %v)", got, want, warnings)
		}
	})

	t.Run("a value that is not a number is reported and the default kept", func(t *testing.T) {
		dataDir := t.TempDir()
		t.Setenv("CAB_DATA_DIR", dataDir)
		t.Setenv("CAB_MCP_MAX_RESULT_BYTES", "lots")
		cfg, warnings, err := Load()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MCPMaxResultBytes != 24576 {
			t.Fatalf("MCPMaxResultBytes = %d, want the default 24576", cfg.MCPMaxResultBytes)
		}
		if len(warnings) == 0 {
			t.Fatal("a malformed value must be reported, not silently ignored")
		}
	})
}
