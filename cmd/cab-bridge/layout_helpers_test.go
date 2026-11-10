package main

import (
	"path/filepath"
	"testing"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
)

// useBase points the binary at base (CAB_DATA_DIR) and returns the directory it
// keeps its state in under it: <base>/layout-2 (F-160). A test then plants and
// reads sessions under the RETURNED directory; writing them under base would not
// fail, it would plant them where this binary never looks, and every negative
// assertion (NoFileExists, "nothing was delivered") would pass without proving
// anything. The structural test TestStructure_TestsNeverAssumeTheStateDirIsTheBase
// guards the files that still set the variable by hand.
func useBase(t *testing.T, base string) string {
	t.Helper()
	t.Setenv("CAB_DATA_DIR", base)
	return filepath.Join(base, config.LayoutDir)
}

// fixtureConfig is the Config of a test that does not go through Load. It names
// BOTH directories: stateDir is where sessions live (what tests plant under), and
// BaseDir is a fresh empty directory, so nothing in the test can read the old tree
// of the real home (the legacy hints probe <BaseDir>/sessions) and nothing can
// export the real home to a child. Assigning BaseDir = DataDir would be the same
// mistake in the other direction: it would put the old tree INSIDE the state dir.
func fixtureConfig(t *testing.T, stateDir string) config.Config {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.BaseDir = t.TempDir()
	cfg.DataDir = stateDir
	return cfg
}
