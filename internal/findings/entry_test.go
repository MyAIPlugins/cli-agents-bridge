package findings

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validValues() Input {
	return Input{
		Lotto: "F-156", Gate: "piano", Critico: "CRI", Chiave: "lock-race", Finding: "the lock is advisory",
		Severita: "Major", Evidenza: "lock.go:12", Decisione: "PROPOSE-FIX", Esito: "aperto",
	}
}

func violationsFor(in Input, column string) []Violation {
	var out []Violation
	for _, v := range Validate(in, nil) {
		if v.Column == column {
			out = append(out, v)
		}
	}
	return out
}

func TestHeader_MatchesTheColumnOrder(t *testing.T) {
	assert.Equal(t, "ts\tlotto\tgate\tcritico\tchiave\tfirst_seen\tfinding\tseverita\tevidenza\tdecisione\tazione\tesito\tmodello\ttoken\tcosto\n", header)
	assert.Equal(t, numColumns, strings.Count(header, "\t")+1)
	// Every column the caller gives is a header column, in the same relative order.
	cols := strings.Split(strings.TrimSuffix(header, "\n"), "\t")
	pos := -1
	for _, spec := range inputColumns {
		i := indexOf(cols, spec.name)
		require.Greater(t, i, pos, spec.name)
		pos = i
	}
	assert.Len(t, inputColumns, numColumns-2, "ts and first_seen are the two the command writes")
}

func indexOf(list []string, v string) int {
	for i, item := range list {
		if item == v {
			return i
		}
	}
	return -1
}

func TestValidate_AcceptsAFullValidInput(t *testing.T) {
	in := validValues()
	in.Azione = "rename the lock file"
	in.Modello = "gpt-5.5"
	in.Token = json.Number("43085")
	in.Costo = json.Number("0.125")
	assert.Empty(t, Validate(in, nil))
	assert.Empty(t, Validate(validValues(), nil), "the optional columns may all be empty")
}

func TestValidate_RequiredColumnMissing(t *testing.T) {
	cleared := map[string]func(*Input){
		"lotto": func(in *Input) { in.Lotto = "" }, "gate": func(in *Input) { in.Gate = "" },
		"critico": func(in *Input) { in.Critico = "" }, "chiave": func(in *Input) { in.Chiave = "" },
		"finding": func(in *Input) { in.Finding = "" }, "severita": func(in *Input) { in.Severita = "" },
		"evidenza": func(in *Input) { in.Evidenza = "" }, "decisione": func(in *Input) { in.Decisione = "" },
		"esito": func(in *Input) { in.Esito = "" },
	}
	required := 0
	for _, spec := range inputColumns {
		if spec.required {
			required++
		}
	}
	require.Len(t, cleared, required, "one case per required column")

	for column, clear := range cleared {
		in := validValues()
		clear(&in)
		got := Validate(in, nil)
		require.Len(t, got, 1, column)
		assert.Equal(t, Violation{Column: column, Reason: "is required"}, got[0])
	}
}

func TestValidate_ValueOutsideEnum(t *testing.T) {
	cases := []struct {
		column string
		set    func(*Input, string)
		bad    []string
	}{
		{"gate", func(in *Input, v string) { in.Gate = v }, []string{"Piano", "code", "piano ", "diff2"}},
		{"severita", func(in *Input, v string) { in.Severita = v }, []string{"blocker", "P1", "minor", "Fuori-scala"}},
		{"decisione", func(in *Input, v string) { in.Decisione = v }, []string{"propose-fix", "ACCEPT", "contest"}},
		{"esito", func(in *Input, v string) { in.Esito = v }, []string{"Aperto", "open", "closed", "aperto "}},
	}
	for _, tc := range cases {
		for _, bad := range tc.bad {
			in := validValues()
			tc.set(&in, bad)
			got := violationsFor(in, tc.column)
			require.NotEmpty(t, got, "%s=%q must be refused", tc.column, bad)
			assert.Contains(t, got[0].Reason, "must be one of: ")
		}
	}
	assert.Equal(t, "must be one of: Blocker, Major, Nit, fuori-scala", violationsFor(func() Input { in := validValues(); in.Severita = "x"; return in }(), "severita")[0].Reason)
}

func TestValidate_ShapesOfIdentifiersKeyAndNumbers(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	cases := []struct {
		column string
		set    func(*Input, string)
		good   []string
		bad    []string
	}{
		{"lotto", func(in *Input, v string) { in.Lotto = v },
			[]string{"F-156", "v0.11.0", "a", long(32)}, []string{"-x", ".x", "a b", "a/b", "a=b", "è", long(33)}},
		{"critico", func(in *Input, v string) { in.Critico = v },
			[]string{"CRI2", "SEC", long(32)}, []string{"_x", "CRI 2", long(33)}},
		{"chiave", func(in *Input, v string) { in.Chiave = v },
			[]string{"lock-race", "a", "a1-b2-c3", long(64)}, []string{"Lock-race", "lock_race", "-a", "a-", "a--b", "a b", "è", long(65)}},
		{"modello", func(in *Input, v string) { in.Modello = v },
			[]string{"gpt-5.5", "claude-sonnet-5-5", long(64)}, []string{"-x", "a b", long(65)}},
		{"token", func(in *Input, v string) { in.Token = json.Number(v) },
			[]string{"0", "43085", "999999999999"}, []string{"-1", "1.5", "1e5", "1234567890123", "abc", "+1"}},
		{"costo", func(in *Input, v string) { in.Costo = json.Number(v) },
			[]string{"0", "1", "0.5", "123456.123456", "1.0"}, []string{"1,5", "1234567", "1.1234567", "-1", ".5", "1.", "1e3"}},
	}
	for _, tc := range cases {
		for _, good := range tc.good {
			in := validValues()
			tc.set(&in, good)
			assert.Empty(t, violationsFor(in, tc.column), "%s=%q must pass", tc.column, good)
		}
		for _, bad := range tc.bad {
			in := validValues()
			tc.set(&in, bad)
			assert.NotEmpty(t, violationsFor(in, tc.column), "%s=%q must be refused", tc.column, bad)
		}
	}
}

func TestValidate_TextRules(t *testing.T) {
	set := func(v string) Input { in := validValues(); in.Finding = v; return in }

	for _, ok := range []string{
		"plain text", "punctuation, `backticks`, 'quotes' and $dollars", "accented è ò à", "emoji \U0001F600 ok",
		"a\u00a0b",               // NBSP is graphic: declared limit of D14, not a rule bypass
		strings.Repeat("è", 400), // 800 bytes
		strings.Repeat("a", 1000),
	} {
		assert.Empty(t, violationsFor(set(ok), "finding"), "%q must pass", ok)
	}

	for name, bad := range map[string]string{
		"tab": "a\tb", "newline": "a\nb", "carriage return": "a\rb", "nul": "a\x00b", "escape": "a\x1bb",
		"zero width space": "a\u200bb", "right-to-left override": "a\u202eb", "line separator": "a\u2028b",
		"replacement char": "a\ufffdb", "invalid utf-8": "a\xffb",
	} {
		got := violationsFor(set(bad), "finding")
		require.NotEmpty(t, got, name)
		assert.Equal(t, reasonCharacters, got[0].Reason, name)
	}

	assert.Equal(t, reasonEdgeSpace, violationsFor(set(" lead"), "finding")[0].Reason)
	assert.Equal(t, reasonEdgeSpace, violationsFor(set("trail "), "finding")[0].Reason)

	// The limit is in bytes: 501 two-byte characters are 1002 bytes.
	got := violationsFor(set(strings.Repeat("è", 501)), "finding")
	require.NotEmpty(t, got)
	assert.Equal(t, "is longer than 1000 bytes", got[0].Reason)
	assert.NotEmpty(t, violationsFor(set(strings.Repeat("a", 1001)), "finding"))

	ev := validValues()
	ev.Evidenza = strings.Repeat("a", 301)
	assert.Equal(t, "is longer than 300 bytes", violationsFor(ev, "evidenza")[0].Reason)
	az := validValues()
	az.Azione = strings.Repeat("a", 501)
	assert.Equal(t, "is longer than 500 bytes", violationsFor(az, "azione")[0].Reason)
}

func TestValidate_ContentRulesNameTheColumnAndTheRule(t *testing.T) {
	in := validValues()
	in.Finding = "password=" + "hunter22"
	in.Azione = "write to a@example.org"
	got := Validate(in, nil)
	assert.Equal(t, []Violation{
		{Column: "finding", Reason: "contains something that looks like secret-assignment"},
		{Column: "azione", Reason: "contains something that looks like email"},
	}, got)

	in = validValues()
	in.Evidenza = "ask Marta"
	assert.Empty(t, Validate(in, nil), "no names configured: rule 13 does not exist")
	assert.Equal(t, []Violation{{Column: "evidenza", Reason: "contains something that looks like person-name"}}, Validate(in, []string{"Marta"}))
}

func TestValidate_ReportsEveryColumnInRegistryOrder(t *testing.T) {
	in := Input{Gate: "x", Chiave: "Bad Key", Esito: "y", Token: json.Number("-1")}
	var columns []string
	for _, v := range Validate(in, nil) {
		columns = append(columns, v.Column)
	}
	assert.Equal(t, []string{"lotto", "gate", "critico", "chiave", "finding", "severita", "evidenza", "decisione", "esito", "token"}, columns)
}

func TestValidate_RefusesTsAndFirstSeenForAProgrammaticCaller(t *testing.T) {
	in := validValues()
	ts := "2026-11-08T10:00:00.000Z"
	in.Ts, in.FirstSeen = &ts, &ts
	assert.Equal(t, []Violation{
		{Column: "ts", Reason: reasonCommandOwns},
		{Column: "first_seen", Reason: reasonCommandOwns},
	}, Validate(in, nil))
}

// TestValidate_ReasonsNeverContainTheValue is the premise of "the refusal does
// not echo the secret": every reason is a fixed text, so it cannot hold the value.
func TestValidate_ReasonsNeverContainTheValue(t *testing.T) {
	const marker = "ZQ9"
	in := Input{
		Lotto: "-" + marker, Gate: marker, Critico: marker + " x", Chiave: "Bad" + marker, Finding: "\t" + marker,
		Severita: marker, Evidenza: "password=" + marker, Decisione: marker, Azione: marker + "@example.org",
		Esito: marker, Modello: "-" + marker, Token: json.Number(marker), Costo: json.Number(marker),
	}
	got := Validate(in, []string{marker})
	require.NotEmpty(t, got)
	for _, v := range got {
		assert.NotContains(t, v.Reason, marker, v.Column)
		assert.NotContains(t, v.Reason, "Q", "the fixed texts carry no capital Q: the tests of the binary use Q as the marker of a value")
	}
	err := &RejectedError{Violations: got}
	assert.NotContains(t, err.Error(), marker)
}

func TestRejectedError_ListsOneLinePerViolationThenSaysNothingWasWritten(t *testing.T) {
	err := &RejectedError{Violations: []Violation{{Column: "gate", Reason: "is required"}, {Column: "finding", Reason: "is required"}}}
	assert.Equal(t, "findings: column gate: is required\nfindings: column finding: is required\nfindings: nothing was written", err.Error())
}

func TestRow_LineHasFifteenTabSeparatedFieldsAndANewline(t *testing.T) {
	in := validValues()
	in.Modello = "gpt-5.5"
	in.Token = json.Number("43085")
	row := newRow(in, "2026-11-08T10:00:00.000Z", "2026-11-01T09:00:00.000Z")
	assert.Equal(t,
		"2026-11-08T10:00:00.000Z\tF-156\tpiano\tCRI\tlock-race\t2026-11-01T09:00:00.000Z\tthe lock is advisory\tMajor\tlock.go:12\tPROPOSE-FIX\t\taperto\tgpt-5.5\t43085\t\n",
		row.line())
	assert.Equal(t, row, rowFromFields(strings.Split(strings.TrimSuffix(row.line(), "\n"), "\t")))
}

func TestTimestamp_RoundTripsOnlyTheCanonicalLayout(t *testing.T) {
	when := time.Date(2026, 11, 8, 10, 0, 0, 123_000_000, time.FixedZone("x", 3600))
	s := formatTimestamp(when)
	assert.Equal(t, "2026-11-08T09:00:00.123Z", s, "always UTC, milliseconds")
	back, err := parseTimestamp(s)
	require.NoError(t, err)
	assert.True(t, back.Equal(when))

	for _, bad := range []string{
		"", "2026-11-08", "2026-11-08T09:00:00Z", "2026-11-08T09:00:00.1Z", "2026-11-08T09:00:00.123456Z",
		"2026-11-08T09:00:00.123+01:00", "2026-11-08 09:00:00.123Z", "2026-13-08T09:00:00.123Z", "2026-02-30T09:00:00.123Z",
	} {
		_, err := parseTimestamp(bad)
		assert.Error(t, err, "%q", bad)
	}
}
