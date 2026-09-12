//go:build windows

package integration

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-136's oracle, and it is DELIBERATE rather than statistical — for a reason
// the measurement forced on us.
//
// The defect was found as a flake, and the obvious acceptance criterion was a
// rate: count failures before and after. That criterion DOES NOT WORK here. Ten
// runs of the whole suite produced ZERO failures on the same machine that had
// been failing about half the time earlier in the day, while a tight loop of the
// same fixture still failed 2 times in 15. The rate is a property of the machine
// at that moment — the load, the scanner, what else was compiling — and an
// "after" of zero would have proved nothing against a "before" of zero.
//
// So the failure is made CERTAIN instead of likely: hold a handle, and the
// removal cannot succeed until it is released. That turns "it stopped happening"
// into "it cannot happen", which is the only version worth a green.
//
// Windows only, and not for convenience: on Unix an open descriptor does not
// stop a file from being unlinked, so there is no defect here to reproduce.

func plantHeldTree(t *testing.T) (tree string, held *os.File) {
	t.Helper()
	tree = filepath.Join(t.TempDir(), "tree")
	require.NoError(t, os.MkdirAll(tree, 0o700))
	victim := filepath.Join(tree, "held.txt")
	require.NoError(t, os.WriteFile(victim, []byte("x"), 0o600))

	// os.Open grants FILE_SHARE_READ|WRITE and not DELETE — which is exactly what
	// a scanner or a just-exited process looks like from our side, and what the
	// measured failures were.
	f, err := os.Open(victim)
	require.NoError(t, err)
	return tree, f
}

// TestRemoveTreeWithPatience_WaitsOutATransientHandle is the property: a handle
// that lets go is survived.
func TestRemoveTreeWithPatience_WaitsOutATransientHandle(t *testing.T) {
	tree, held := plantHeldTree(t)

	released := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = held.Close()
		close(released)
	}()

	removeTreeWithPatience(t, tree)
	<-released

	assert.NoDirExists(t, tree, "the tree must be gone once the handle is released")
}

// TestRemoveTreeWithPatience_ThePlainRemoveAllControlFails keeps the test above
// honest.
//
// A green test proves nothing until it has been seen to go red for the right
// reason. This is the defect itself, on demand: os.RemoveAll against the same
// held tree. If it ever passes, Windows changed and the test above has stopped
// having a subject.
func TestRemoveTreeWithPatience_ThePlainRemoveAllControlFails(t *testing.T) {
	tree, held := plantHeldTree(t)
	defer func() { _ = held.Close() }()

	require.Error(t, os.RemoveAll(tree),
		"os.RemoveAll must still fail against a held file: this is F-136, reproduced on purpose")
}

// TestRemoveTreeWithPatience_GivesUpAndSaysSo covers the branch next door: a
// handle that never lets go.
//
// It must RETURN rather than fail the test — a cleanup that cannot finish is not
// what any of these tests measure, and failing there would put back exactly the
// kind of red this lot removes. Go's own TempDir cleanup still reports the leak
// afterwards, so nothing is hidden; what changes is that the report comes from
// the place that owns it.
func TestRemoveTreeWithPatience_GivesUpAndSaysSo(t *testing.T) {
	tree, held := plantHeldTree(t)
	defer func() { _ = held.Close() }()

	started := time.Now()
	removeTreeWithPatience(t, tree) // must not call t.Fatal
	elapsed := time.Since(started)

	assert.DirExists(t, tree, "it could not have removed a tree that is still held")
	assert.GreaterOrEqual(t, elapsed, treeRemovalPatience,
		"and it must have spent the patience before giving up, not returned at once")
}
