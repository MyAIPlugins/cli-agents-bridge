package config

import "path/filepath"

// LayoutDir is the directory, inside the base data dir, where THIS generation of
// the on-disk layout lives (F-160). A binary of this generation keeps all of its
// state under <base>/LayoutDir and never writes to <base>/sessions or
// <base>/archive, which belong to the binaries that came before.
//
// Why a directory per generation and not a migration: the session lock changed
// from a PID file to a kernel lock, and an old binary and a new one cannot be
// made to exclude each other on the same file (an old process that is paused, or
// lives 5 ms, leaves nothing a new one can see). Two trees with no file in
// common remove the question instead of answering it: criterion 8 of the F-160
// BAR holds by construction, whatever the old processes are doing.
//
// A change that breaks the on-disk invariants again opens layout-3 the same way.
const LayoutDir = "layout-2"

// layoutDir is the ONLY place that turns a base dir into this generation's state
// dir. Load and DefaultConfig call it; nothing else may build the path, and a
// structural test fixes that (cmd/cab-bridge/layout_structure_test.go).
func layoutDir(base string) string {
	return filepath.Join(base, LayoutDir)
}
