package integration

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stepNamed returns the YAML block of the step whose `- name:` line contains
// name, so an assertion about "the staticcheck step" is about that step and not
// about whatever follows it in the file. See stepBlocks for where a block ends.
func stepNamed(t *testing.T, yaml, name string) string {
	t.Helper()
	for _, block := range stepBlocks(yaml) {
		first, _, _ := strings.Cut(block, "\n")
		if strings.HasPrefix(strings.TrimSpace(first), "- name: ") && strings.Contains(first, name) {
			return block
		}
	}
	require.FailNow(t, "step not found in the workflow", "no step named %q", name)
	return ""
}

// stepBlocks returns every step of every job, in document order. A step is a
// list item under a `steps:` key; it runs from its `- ` marker up to, and not
// including, the first following non-blank line indented no deeper than that
// marker. That line may be the next step, a comment written at step level (it
// belongs to what follows), the next job or the end of the list.
//
// The previous helper ended a step at the next `- name:` only. A step without
// a name (`- uses: ...`) was therefore swallowed into the block before it, and
// so was everything up to the next NAMED step, other jobs included. Lists that
// are not steps (a matrix `include:`) are never returned: only the items that
// sit under a `steps:` key count.
func stepBlocks(yaml string) []string {
	lines := strings.Split(yaml, "\n")
	var blocks []string
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "steps:" {
			continue
		}
		var job []string
		job, i = stepsOfJob(lines, i+1)
		blocks = append(blocks, job...)
	}
	return blocks
}

// stepsOfJob reads the list that starts at lines[from] and returns its steps
// and the index of the first line that is not part of it.
func stepsOfJob(lines []string, from int) ([]string, int) {
	var blocks []string
	itemIndent := -1
	i := from
	for i < len(lines) {
		text := strings.TrimSpace(lines[i])
		indent := len(lines[i]) - len(strings.TrimLeft(lines[i], " "))
		switch {
		case text == "" || strings.HasPrefix(text, "#"):
			i++
		case strings.HasPrefix(text, "- ") && (itemIndent < 0 || indent == itemIndent):
			itemIndent = indent
			end := stepEnd(lines, i, itemIndent)
			blocks = append(blocks, strings.TrimRight(strings.Join(lines[i:end], "\n"), "\n \r"))
			i = end
		default:
			return blocks, i
		}
	}
	return blocks, i
}

// stepEnd is the index of the first line after start that is not blank and is
// indented no deeper than the step's own `- ` marker.
func stepEnd(lines []string, start, itemIndent int) int {
	for j := start + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			continue
		}
		if len(lines[j])-len(strings.TrimLeft(lines[j], " ")) <= itemIndent {
			return j
		}
	}
	return len(lines)
}

// syntheticWorkflow has the shapes that made the first stepNamed lie: a step
// WITHOUT a name right after the one under test, a comment and a job header at
// job indentation, a matrix list that is not a list of steps, and a script with
// a blank line and a bash comment inside.
const syntheticWorkflow = `name: Synthetic
jobs:
  lint:
    steps:
      - uses: actions/checkout@aaaa # v1.0.0
      - name: staticcheck (every GOOS)
        # a comment that belongs to the step
        run: |
          # a bash comment inside the script

          echo staticcheck
      - uses: actions/setup-go@bbbb # v7.0.0
        with:
          cache: true

  # a comment between jobs, indented like a job key
  test:
    strategy:
      matrix:
        include:
          - goos: darwin
    steps:
      - name: last step
        run: echo done
`

// The regression for F-164 (the first finding of F-162): the helper said it
// stopped at the next step and stopped only at the next "- name:". A step
// without a name after the one under test was swallowed, and so was everything
// up to the following named step, other jobs included. Silent until somebody
// pinned a checkout with a "# v7.0.1" comment right after the staticcheck step:
// then the "no version literal in this step" test failed on a line that is not in it.
func TestStepNamed_StopsAtTheNextStepOfAnyKind(t *testing.T) {
	t.Run("a following step without a name is not part of the block", func(t *testing.T) {
		block := stepNamed(t, syntheticWorkflow, "staticcheck")
		assert.NotContains(t, block, "v7.0.0", "the unnamed setup-go step that FOLLOWS leaked into the block")
		assert.NotContains(t, block, "cache: true")
	})

	t.Run("the next job is not part of the block", func(t *testing.T) {
		block := stepNamed(t, syntheticWorkflow, "staticcheck")
		assert.NotContains(t, block, "test:")
		assert.NotContains(t, block, "a comment between jobs")
		assert.NotContains(t, block, "last step")
	})

	t.Run("the block keeps the comments, the env and the whole script of the step", func(t *testing.T) {
		block := stepNamed(t, syntheticWorkflow, "staticcheck")
		assert.Contains(t, block, "a comment that belongs to the step")
		assert.Contains(t, block, "a bash comment inside the script", "a comment INSIDE the script must not end the step")
		assert.Contains(t, block, "echo staticcheck", "a blank line inside the script must not end the step")
	})

	t.Run("the last step of the file runs to the end", func(t *testing.T) {
		block := stepNamed(t, syntheticWorkflow, "last step")
		assert.Equal(t, "      - name: last step\n        run: echo done", block)
	})

	t.Run("only the items under steps: are steps, in document order", func(t *testing.T) {
		blocks := stepBlocks(syntheticWorkflow)
		require.Len(t, blocks, 4, "checkout, staticcheck, setup-go, last step: the matrix item `- goos: darwin` is not a step")
		assert.Contains(t, blocks[0], "actions/checkout@aaaa")
		assert.Contains(t, blocks[1], "staticcheck")
		assert.Contains(t, blocks[2], "actions/setup-go@bbbb")
		assert.Contains(t, blocks[3], "last step")
		for _, b := range blocks {
			assert.NotContains(t, b, "goos: darwin")
		}
	})
}
