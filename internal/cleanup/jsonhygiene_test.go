package cleanup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRun_EmptyResultSerializesAsEmptyArrays is the Sprint 6 BUG-B regression:
// an empty cleanup Result must serialize its slice fields as [] (not null), so
// JSON consumers like `jq '.sessionsRemoved | length'` do not break on null.
func TestRun_EmptyResultSerializesAsEmptyArrays(t *testing.T) {
	t.Parallel()

	res, err := Run(context.Background(), Options{
		DataDir:       t.TempDir(), // fresh: no sessions/ and no archive/ subdirs
		Scope:         ScopeGlobal,
		StaleSeconds:  300,
		RetentionDays: 7,
	})
	require.NoError(t, err)

	data, err := json.Marshal(res)
	require.NoError(t, err)
	js := string(data)
	assert.Contains(t, js, `"sessionsRemoved":[]`, "empty sessionsRemoved must serialize as [], not null")
	assert.Contains(t, js, `"archivesPurged":[]`, "empty archivesPurged must serialize as [], not null")
	assert.False(t, strings.Contains(js, "null"), "no slice field may serialize as null: %s", js)
}
