package integration

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The workflow standards of F-164, as properties of the two workflow files. They
// exist because nothing else enforces them: zizmor is run by hand, not in CI
// (that was left out of the lot on purpose), and `timeout-minutes` is not a
// thing any linter asks for. Each rule is a function from the workflow text to
// the list of lines that break it, so the same function runs on the real files
// and on a synthetic bad one: a check that cannot fail is not a check.

// toolchainCheckStep is the name of the step that follows every setup-go. The
// test below pins it, and workflow_toolchain_check_test.go runs its script.
const toolchainCheckStep = "The Go that runs is the toolchain line of go.mod"

var workflowFiles = []string{"ci.yml", "release.yml"}

var (
	reFullSHA       = regexp.MustCompile(`^[\w.-]+/[\w./-]+@[0-9a-f]{40}$`)
	reTagComment    = regexp.MustCompile(`^v\d+\.\d+\.\d+$`)
	rePersistFalse  = regexp.MustCompile(`(?m)^\s+persist-credentials:\s*false\b`)
	reJobKey        = regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	reEmptyTokenSet = regexp.MustCompile(`(?m)^permissions:\s*\{\}\s*(#.*)?$`)
)

func readWorkflow(t *testing.T, name string) string {
	t.Helper()
	return normalizeEOL(readRepoFile(t, mustAbs(t, "../.."), ".github/workflows/"+name))
}

// unpinnedUses lists every remote `uses:` that is not a full 40-character commit
// SHA followed by a comment holding exactly the tag it points to. Local actions
// (`./...`) are not versioned and are skipped. A line that does not fit is
// reported, never skipped: extra words after the tag are a failure too.
func unpinnedUses(yaml string) []string {
	var bad []string
	for n, line := range strings.Split(yaml, "\n") {
		item := strings.TrimPrefix(strings.TrimSpace(line), "- ")
		value, isUses := strings.CutPrefix(item, "uses:")
		if !isUses {
			continue
		}
		ref, comment, _ := strings.Cut(strings.TrimSpace(value), "#")
		ref, comment = strings.TrimSpace(ref), strings.TrimSpace(comment)
		if strings.HasPrefix(ref, "./") {
			continue
		}
		if !reFullSHA.MatchString(ref) || !reTagComment.MatchString(comment) {
			bad = append(bad, fmt.Sprintf("line %d: %s", n+1, strings.TrimSpace(line)))
		}
	}
	return bad
}

// checkoutsKeepingCredentials lists the checkout steps that do not say
// `persist-credentials: false` themselves. Not in a comment: the regexp wants the
// key at the start of a line.
func checkoutsKeepingCredentials(yaml string) []string {
	var bad []string
	for _, block := range stepBlocks(yaml) {
		if strings.Contains(block, "uses: actions/checkout@") && !rePersistFalse.MatchString(block) {
			first, _, _ := strings.Cut(block, "\n")
			bad = append(bad, strings.TrimSpace(first))
		}
	}
	return bad
}

type jobBody struct {
	name  string
	lines []string
}

// jobBodies splits the `jobs:` section of a workflow into one body per job. A
// job starts at a key with two spaces of indentation; whatever follows, comments
// included, belongs to it until the next job key.
func jobBodies(yaml string) []jobBody {
	var jobs []jobBody
	inJobs := false
	for _, line := range strings.Split(yaml, "\n") {
		switch {
		case strings.HasPrefix(line, "jobs:"):
			inJobs = true
		case !inJobs:
		case reJobKey.MatchString(line):
			jobs = append(jobs, jobBody{name: reJobKey.FindStringSubmatch(line)[1]})
		case len(jobs) > 0:
			jobs[len(jobs)-1].lines = append(jobs[len(jobs)-1].lines, line)
		}
	}
	return jobs
}

// jobsMissing lists "job X has no <key>" for every job of the workflow that does
// not declare each of the given keys at job level (four spaces of indentation).
func jobsMissing(yaml string, keys ...string) []string {
	var bad []string
	for _, job := range jobBodies(yaml) {
		bad = append(bad, missingKeys(job, keys)...)
	}
	return bad
}

func missingKeys(job jobBody, keys []string) []string {
	var bad []string
	for _, key := range keys {
		found := false
		for _, line := range job.lines {
			found = found || strings.HasPrefix(line, "    "+key+":")
		}
		if !found {
			bad = append(bad, fmt.Sprintf("job %q has no %s", job.name, key))
		}
	}
	return bad
}

// setupGoNotFollowedByTheCheck lists the setup-go steps whose NEXT step is not the
// toolchain check, and returns the check steps it found, in document order.
func setupGoNotFollowedByTheCheck(yaml string) (bad, checks []string) {
	blocks := stepBlocks(yaml)
	for i, block := range blocks {
		if !strings.Contains(block, "uses: actions/setup-go@") {
			continue
		}
		if i+1 >= len(blocks) || !strings.HasPrefix(strings.TrimSpace(blocks[i+1]), "- name: "+toolchainCheckStep) {
			bad = append(bad, fmt.Sprintf("the setup-go step #%d has no toolchain check right after it", i+1))
			continue
		}
		checks = append(checks, blocks[i+1])
	}
	return bad, checks
}

const badWorkflow = `name: Bad
on: [push]
permissions:
  contents: read
jobs:
  fast:
    runs-on: ubuntu-latest
    timeout-minutes: 3
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e # v7.0.0 and some words
      - uses: ./local-action
  hung:
    runs-on: ubuntu-latest
    permissions: {}
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          # persist-credentials: false (a comment is not the key)
          fetch-depth: 0
`

func TestWorkflowStandards_TheCheckersCanFail(t *testing.T) {
	t.Run("pins: a tag, a SHA with extra words and a missing comment are reported, a local action is not", func(t *testing.T) {
		bad := unpinnedUses(badWorkflow)
		assert.Len(t, bad, 2, "checkout@v4 and the setup-go line with extra words; got %v", bad)
		assert.Empty(t, unpinnedUses("      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1\n      - uses: ./x\n"))
		assert.Len(t, unpinnedUses("      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n"), 1, "a SHA with no tag comment")
	})

	t.Run("checkout: only the key itself counts, a comment mentioning it does not", func(t *testing.T) {
		assert.Len(t, checkoutsKeepingCredentials(badWorkflow), 2)
	})

	t.Run("jobs: each one must declare its own timeout and permissions", func(t *testing.T) {
		bad := jobsMissing(badWorkflow, "timeout-minutes", "permissions")
		assert.Equal(t, []string{
			`job "fast" has no permissions`,
			`job "hung" has no timeout-minutes`,
		}, bad)
	})

	t.Run("workflow-level permissions: only the empty set passes", func(t *testing.T) {
		assert.False(t, reEmptyTokenSet.MatchString(badWorkflow), "contents: read at workflow level is not the empty set")
		assert.True(t, reEmptyTokenSet.MatchString("permissions: {}\njobs:\n"))
	})

	t.Run("toolchain check: a setup-go with something else after it is reported", func(t *testing.T) {
		bad, checks := setupGoNotFollowedByTheCheck(badWorkflow)
		assert.Len(t, bad, 1)
		assert.Empty(t, checks)
	})
}

func TestWorkflows_TheStandardsHold(t *testing.T) {
	for _, name := range workflowFiles {
		yaml := readWorkflow(t, name)
		t.Run(name, func(t *testing.T) {
			assert.Empty(t, unpinnedUses(yaml),
				"every remote action is pinned to a full commit SHA with its tag in a comment: a tag can be moved by its author, and Dependabot rewrites the comment with the SHA")
			assert.Empty(t, checkoutsKeepingCredentials(yaml),
				"every checkout says persist-credentials: false: nothing here pushes, so the token has no reason to stay in the workspace")
			assert.Empty(t, jobsMissing(yaml, "timeout-minutes", "permissions"),
				"every job has its own timeout (the default is 360 minutes) and its own permissions")
			assert.Regexp(t, reEmptyTokenSet, yaml,
				"the workflow starts from no permissions at all; each job asks for what it uses")
		})
	}
}

// Four copies of one step, and the property is that they are the SAME step: one
// edit that reaches three of them leaves the fourth checking something else,
// green. The count is derived from the files, not written here.
func TestWorkflows_EverySetupGoIsFollowedByTheSameToolchainCheck(t *testing.T) {
	var all []string
	setupGos := 0
	for _, name := range workflowFiles {
		yaml := readWorkflow(t, name)
		setupGos += strings.Count(yaml, "uses: actions/setup-go@")
		bad, checks := setupGoNotFollowedByTheCheck(yaml)
		assert.Empty(t, bad, "%s", name)
		all = append(all, checks...)
	}
	require.NotZero(t, setupGos, "no setup-go found: this check has no subject")
	require.Len(t, all, setupGos, "one toolchain check per setup-go")
	for i, c := range all {
		assert.Equal(t, all[0], c, "copy #%d differs from the first one", i+1)
	}
}
