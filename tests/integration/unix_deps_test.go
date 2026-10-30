package integration

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The binary for macOS and Linux imports nothing outside the standard library and
// this module. That is the "zero runtime dependencies" promise in its checkable
// form (docs/dev-conventions.md), and it is not decorative: building the binary in
// a throw-away HOME, which is what TestMakefile_InstallDevLinksWhatItJustBuilt
// does, downloads every module the binary needs into a module cache whose files
// are read-only, and a temp dir cannot remove that. The identity lock of F-72
// first imported golang.org/x/sys/unix for a flock the standard library already
// has, and the only thing that noticed was that unrelated test failing in its
// cleanup. This one states the property directly, so the next import of that kind
// fails HERE, with its name.
//
// golang.org/x/sys is still allowed in the Windows files: it is the only way to
// reach the APIs those need, and the test below looks only at the unix targets.
func TestUnixBinaryImportsNothingOutsideTheStandardLibraryAndThisModule(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not available")
	}
	repoRoot, err := filepath.Abs("../..")
	require.NoError(t, err)

	modOut, err := exec.Command("go", "list", "-m").CombinedOutput()
	require.NoError(t, err, "go list -m: %s", modOut)
	module := strings.TrimSpace(string(modOut))
	require.NotEmpty(t, module)

	for _, target := range []struct{ goos, goarch string }{{"darwin", "arm64"}, {"linux", "amd64"}} {
		t.Run(target.goos+"/"+target.goarch, func(t *testing.T) {
			cmd := exec.Command("go", "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./cmd/cab-bridge")
			cmd.Dir = repoRoot
			cmd.Env = append(os.Environ(), "GOOS="+target.goos, "GOARCH="+target.goarch, "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, "go list: %s", out)

			var outsiders []string
			for _, pkg := range strings.Fields(string(out)) {
				if pkg != module && !strings.HasPrefix(pkg, module+"/") {
					outsiders = append(outsiders, pkg)
				}
			}
			assert.Empty(t, outsiders, "the %s/%s binary must not import anything outside the standard library and %s", target.goos, target.goarch, module)
		})
	}
}
