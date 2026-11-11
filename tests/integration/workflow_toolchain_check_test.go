//go:build !windows

package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The toolchain check of the workflows (F-164) is a few lines of shell inside a
// YAML file, which is the kind of code nobody runs until the day it matters. This
// test takes the script FROM THE WORKFLOW, as written, and runs it with a go.mod
// and a stand-in `go` that reports whatever version the case needs.
//
// Unix only, and on purpose: it needs bash, a `#!/bin/sh` stand-in on PATH and
// awk, all of which the Windows leg of CI has in Git Bash but a test should not
// have to guess. The Windows leg proves the real thing by RUNNING the step.

// realisticGoMod has the shape of ours: the `go` line is the language minimum,
// the `toolchain` line is the compiler, and a require block follows.
const realisticGoMod = "module example.com/x\n\ngo 1.25.0\n\ntoolchain go1.27.1\n\nrequire (\n\tgithub.com/stretchr/testify v1.11.1\n)\n"

// The two ways GitHub starts bash: a job with `shell: bash` gets -eo pipefail, a
// job without it gets plain -e. The script must hold under both.
var stepShells = []struct {
	name  string
	flags []string
}{
	{"bash -e, the default of a job with no shell:", []string{"-e"}},
	{"bash -eo pipefail, what shell: bash runs", []string{"-eo", "pipefail"}},
}

// runScriptOf returns the script of a step's `run: |`, dedented the way GitHub
// hands it to bash.
func runScriptOf(t *testing.T, block string) string {
	t.Helper()
	lines := strings.Split(block, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "run: |" {
			return scriptLines(lines[i+1:], len(l)-len(strings.TrimLeft(l, " ")))
		}
	}
	require.FailNow(t, "no `run: |` in the step")
	return ""
}

func scriptLines(lines []string, runIndent int) string {
	var script []string
	scriptIndent := -1
	for _, l := range lines {
		indent := len(l) - len(strings.TrimLeft(l, " "))
		if strings.TrimSpace(l) != "" && indent <= runIndent {
			break
		}
		if strings.TrimSpace(l) == "" {
			script = append(script, "")
			continue
		}
		if scriptIndent < 0 {
			scriptIndent = indent
		}
		script = append(script, l[scriptIndent:])
	}
	return strings.Join(script, "\n") + "\n"
}

// runToolchainCheck runs script in a temp dir that holds goMod (when non-nil) and
// a `go` on PATH that answers `go env GOVERSION` with running.
func runToolchainCheck(t *testing.T, script string, shell []string, goMod *string, running string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	if goMod != nil {
		require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte(*goMod), 0o600))
	}
	bin := filepath.Join(dir, "bin")
	require.NoError(t, os.MkdirAll(bin, 0o700))
	fakeGo := "#!/bin/sh\n[ \"$1 $2\" = \"env GOVERSION\" ] || exit 2\necho " + running + "\n"
	// 0o700 on purpose: this file is the stand-in for the go binary and has to run.
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0o700))
	scriptPath := filepath.Join(dir, "step.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o600))

	cmd := exec.Command("bash", append(append([]string{}, shell...), scriptPath)...)
	cmd.Dir = dir
	cmd.Env = append(envWithout(os.Environ(), "PATH"), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "the script did not run at all: %v", err)
		code = exit.ExitCode()
	}
	return string(out), code
}

func TestToolchainCheck_ComparesTheRunningGoWithTheToolchainLine(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	script := runScriptOf(t, stepNamed(t, readWorkflow(t, "ci.yml"), toolchainCheckStep))
	withToolchain := realisticGoMod
	crlf := strings.ReplaceAll(realisticGoMod, "\n", "\r\n")
	noToolchain := strings.Replace(realisticGoMod, "toolchain go1.27.1\n\n", "", 1)
	require.NotEqual(t, realisticGoMod, noToolchain, "the fixture without a toolchain line must differ from the one with it")

	for _, sh := range stepShells {
		t.Run(sh.name, func(t *testing.T) {
			t.Run("the same version passes", func(t *testing.T) {
				out, code := runToolchainCheck(t, script, sh.flags, &withToolchain, "go1.27.1")
				assert.Equal(t, 0, code, out)
				assert.Contains(t, out, "go1.27.1 is the toolchain line of go.mod")
			})

			t.Run("another version fails and says both", func(t *testing.T) {
				out, code := runToolchainCheck(t, script, sh.flags, &withToolchain, "go1.25.0")
				assert.Equal(t, 1, code, out)
				assert.Contains(t, out, "::error")
				assert.Contains(t, out, "go.mod asks for 'go1.27.1'")
				assert.Contains(t, out, "this job runs 'go1.25.0'")
			})

			t.Run("a go.mod with no toolchain line fails", func(t *testing.T) {
				out, code := runToolchainCheck(t, script, sh.flags, &noToolchain, "go1.27.1")
				assert.Equal(t, 1, code, out)
				assert.Contains(t, out, "<no toolchain line>")
			})

			t.Run("a go.mod with CRLF line endings, as a Windows checkout may leave it, passes", func(t *testing.T) {
				out, code := runToolchainCheck(t, script, sh.flags, &crlf, "go1.27.1")
				assert.Equal(t, 0, code, out)
			})

			t.Run("no go.mod at all fails loudly", func(t *testing.T) {
				// Not a particular exit code: under pipefail awk's own status wins, without it
				// the empty toolchain line is what trips. Both must be a failure with a message
				// that names the file.
				out, code := runToolchainCheck(t, script, sh.flags, nil, "go1.27.1")
				assert.NotZero(t, code, out)
				assert.Contains(t, out, "go.mod")
			})
		})
	}
}
