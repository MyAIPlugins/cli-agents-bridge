package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The shape F-163 asked of the test job of ci.yml, as properties of the file. The
// leg itself is one line of YAML; what keeps it a leg, rather than a name in a list
// that someone later weakens, is these checks: same steps everywhere, nothing that
// lets one OS opt out, a timeout for every OS, and a volume step whose test names
// still exist. Same rule as workflow_standards_test.go: each check is a function
// from text to the list of things that break it, so it also runs on a bad file.

// wantTestOS is the matrix the job must have, sorted. Adding an OS means adding it
// here on purpose: that is the point of pinning it.
var wantTestOS = []string{"macos-latest", "ubuntu-latest", "windows-latest"}

const volumeStepName = "The volume, and the tests that depend on it"

var (
	reMatrixOS    = regexp.MustCompile(`(?m)^\s+os:\s*\[([^\]]*)\]`)
	reFailFastOff = regexp.MustCompile(`(?m)^\s+fail-fast:\s*false\s*(#.*)?$`)
	reRunPattern  = regexp.MustCompile(`-run '\^\(([^)]*)\)\$'`)
)

func jobNamed(yaml, name string) (jobBody, bool) {
	for _, job := range jobBodies(yaml) {
		if job.name == name {
			return job, true
		}
	}
	return jobBody{}, false
}

// matrixOS reads the `os: [a, b, c]` line of the job, sorted. A matrix written in
// another shape yields nothing, and the caller reports that as a mismatch.
func matrixOS(body string) []string {
	m := reMatrixOS.FindStringSubmatch(body)
	if m == nil {
		return nil
	}
	var list []string
	for _, item := range strings.Split(m[1], ",") {
		list = append(list, strings.TrimSpace(item))
	}
	slices.Sort(list)
	return list
}

// testJobProblems lists what breaks the shape of the `test` job: a matrix other
// than wantTestOS, a `fail-fast` that is not off (one red leg would hide the other
// two), an OS without its own `timeout` in `include`, and any `if:` or
// `continue-on-error` in the job, which is how one leg stops running a step the
// others run, or turns red into green.
func testJobProblems(yaml string) []string {
	job, ok := jobNamed(yaml, "test")
	if !ok {
		return []string{"no job named test"}
	}
	body := strings.Join(job.lines, "\n")
	var bad []string
	if got := matrixOS(body); !slices.Equal(got, wantTestOS) {
		bad = append(bad, fmt.Sprintf("matrix os is %v, want %v", got, wantTestOS))
	}
	if !reFailFastOff.MatchString(body) {
		bad = append(bad, "fail-fast is not false: a red leg would cancel the others before they report")
	}
	for _, name := range wantTestOS {
		// The name must end where the name ends: a `\b` would let `macos-latest-x` pass
		// for `macos-latest`, since a hyphen is a word boundary.
		withTimeout := regexp.MustCompile(`(?m)^\s+- os:\s*` + regexp.QuoteMeta(name) + `[ \t]*(#[^\n]*)?\n\s+timeout:\s*[1-9]\d*\b`)
		if !withTimeout.MatchString(body) {
			bad = append(bad, "no timeout in include for "+name)
		}
	}
	for n, line := range job.lines {
		text := strings.TrimPrefix(strings.TrimSpace(line), "- ")
		if strings.HasPrefix(text, "if:") || strings.HasPrefix(text, "continue-on-error:") {
			bad = append(bad, fmt.Sprintf("job line %d: %s", n+1, text))
		}
	}
	return bad
}

// volumeTestNames returns the test names the volume step selects with -run.
func volumeTestNames(step string) []string {
	m := reRunPattern.FindStringSubmatch(step)
	if m == nil {
		return nil
	}
	return strings.Split(m[1], "|")
}

// testsMissingFrom lists the names that no *_test.go under the given directories
// declares as a function. A renamed test does not break `go test`: -run simply
// selects nothing, so this is the only place a rename shows up before CI.
func testsMissingFrom(root string, dirs, names []string) ([]string, error) {
	var source strings.Builder
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(root, dir, "*_test.go"))
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			text, err := os.ReadFile(file)
			if err != nil {
				return nil, err
			}
			source.Write(text)
		}
	}
	var missing []string
	for _, name := range names {
		if !strings.Contains(source.String(), "func "+name+"(") {
			missing = append(missing, name)
		}
	}
	return missing, nil
}

const badMatrixWorkflow = `name: Bad

jobs:
  test:
    runs-on: ${{ matrix.os }}
    strategy:
      matrix:
        os: [ubuntu-latest, windows-latest]
        include:
          - os: ubuntu-latest
            timeout: 6
          - os: windows-latest
    steps:
      - name: one
        if: runner.os != 'macOS'
        run: echo one
      - name: two
        continue-on-error: true
        run: echo two
`

func TestWorkflowMatrix_TheCheckersCanFail(t *testing.T) {
	t.Run("a test job that lost its guarantees reports each of them", func(t *testing.T) {
		got := strings.Join(testJobProblems(badMatrixWorkflow), "\n")
		for _, want := range []string{
			"matrix os is [ubuntu-latest windows-latest]",
			"fail-fast is not false",
			"no timeout in include for windows-latest",
			"no timeout in include for macos-latest",
			"if: runner.os != 'macOS'",
			"continue-on-error: true",
		} {
			assert.Contains(t, got, want)
		}
		assert.NotContains(t, got, "no timeout in include for ubuntu-latest")
	})
	t.Run("a workflow without a test job is reported", func(t *testing.T) {
		assert.Equal(t, []string{"no job named test"}, testJobProblems("jobs:\n  lint:\n    steps: []\n"))
	})
	t.Run("a -run pattern naming a test that does not exist is reported", func(t *testing.T) {
		step := "- name: x\n  run: go test -run '^(TestThatExists|TestRenamedAway)$' ./..."
		assert.Equal(t, []string{"TestThatExists", "TestRenamedAway"}, volumeTestNames(step))
		missing, err := testsMissingFrom(mustAbs(t, "../.."), []string{"tests/integration"},
			[]string{"TestWorkflowMatrix_TheCheckersCanFail", "TestRenamedAway"})
		require.NoError(t, err)
		assert.Equal(t, []string{"TestRenamedAway"}, missing)
	})
}

func TestWorkflows_TheTestJobRunsOnEverySystemTheSameWay(t *testing.T) {
	yaml := readWorkflow(t, "ci.yml")
	assert.Empty(t, testJobProblems(yaml))
}

func TestWorkflows_TheVolumeStepNamesTestsThatExist(t *testing.T) {
	job, ok := jobNamed(readWorkflow(t, "ci.yml"), "test")
	require.True(t, ok, "no job named test")
	step := stepNamed(t, strings.Join(job.lines, "\n"), volumeStepName)

	names := volumeTestNames(step)
	require.NotEmpty(t, names, "the volume step has no -run '^(A|B)$' pattern: this check has no subject")
	missing, err := testsMissingFrom(mustAbs(t, "../.."), []string{"internal/session", "tests/integration"}, names)
	require.NoError(t, err)
	assert.Empty(t, missing, "the volume step selects tests that no file declares: -run would select nothing for them")
}
