// Package findings is the per-repository registry of critic findings
// (`.handover/findings.tsv`): one row per `add`, the file only ever grows, and
// `stale` lists what has stayed open for too long.
//
// The registry is "logically append-only": every `add` builds the whole new
// content in memory and publishes it with one atomic rename (store.go), so a
// reader or a crash sees the file before or after a row, never half of one.
package findings

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// header is the first line of every registry. Tabs are explicit: some viewers
// squash them. A registry whose first line differs is refused, not repaired.
const header = "ts\tlot\tgate\tcritic\tkey\tfirst_seen\tfinding\tseverity\tevidence\tdecision\taction\toutcome\tmodel\ttokens\tcost\n"

// numColumns is the width of the header and of every row.
const numColumns = 15

// timeLayout is the format of ts and first_seen: UTC, milliseconds. A second
// of resolution would make a shared first_seen indistinguishable from a
// correct one in the concurrent-writers test.
const timeLayout = "2006-01-02T15:04:05.000Z"

// Fixed enumerations. Values are part of the registry format.
var (
	gates      = []string{"plan", "diff"}
	severities = []string{"Blocker", "Major", "Nit", "out-of-scale"}
	decisions  = []string{"PROPOSE-FIX", "CONTEST", "DECLINE", "ESCALATE"}
	outcomes   = []string{outcomeOpen, "closed", "rejected"}
)

// outcomeOpen is the outcome of a finding that is still to be dealt with: the only
// one `stale` looks at.
const outcomeOpen = "open"

var (
	identPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	slugPattern  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	tokenPattern = regexp.MustCompile(`^[0-9]{1,12}$`)
	costPattern  = regexp.MustCompile(`^[0-9]{1,6}(\.[0-9]{1,6})?$`)
)

// Violation is one reason an input was refused. Reason is always a fixed text:
// it never contains the value, because the value may be exactly the secret the
// refusal is about.
type Violation struct {
	Column string
	Reason string
}

// RejectedError is returned when the input broke one or more rules. Nothing
// was written.
type RejectedError struct {
	Violations []Violation
}

func (e *RejectedError) Error() string {
	var b strings.Builder
	for _, v := range e.Violations {
		fmt.Fprintf(&b, "findings: column %s: %s\n", v.Column, v.Reason)
	}
	b.WriteString("findings: nothing was written")
	return b.String()
}

// Input is what the caller provides: the 13 columns that are not written by
// the command. Ts and FirstSeen exist only to be refused: the command owns them.
// They are json.RawMessage, not *string, because a JSON null leaves a *string
// nil, which reads as "not given", while a RawMessage keeps even the `null` text.
// So any value given under those keys is non-nil, and Validate refuses it.
type Input struct {
	Lot       string          `json:"lot"`
	Gate      string          `json:"gate"`
	Critic    string          `json:"critic"`
	Key       string          `json:"key"`
	Finding   string          `json:"finding"`
	Severity  string          `json:"severity"`
	Evidence  string          `json:"evidence"`
	Decision  string          `json:"decision"`
	Action    string          `json:"action"`
	Outcome   string          `json:"outcome"`
	Model     string          `json:"model"`
	Tokens    json.Number     `json:"tokens"`
	Cost      json.Number     `json:"cost"`
	Ts        json.RawMessage `json:"ts"`
	FirstSeen json.RawMessage `json:"first_seen"`
}

// Row is one registry line, all columns as text.
type Row struct {
	Ts, Lot, Gate, Critic, Key, FirstSeen, Finding, Severity string
	Evidence, Decision, Action, Outcome, Model, Tokens, Cost string
}

// fields returns the columns in registry order.
func (r Row) fields() [numColumns]string {
	return [numColumns]string{
		r.Ts, r.Lot, r.Gate, r.Critic, r.Key, r.FirstSeen, r.Finding, r.Severity,
		r.Evidence, r.Decision, r.Action, r.Outcome, r.Model, r.Tokens, r.Cost,
	}
}

// line is the registry representation: tab-separated, newline-terminated, no
// quoting and no escapes (Validate guarantees no value contains a tab or a newline).
func (r Row) line() string {
	f := r.fields()
	return strings.Join(f[:], "\t") + "\n"
}

func rowFromFields(f []string) Row {
	return Row{
		Ts: f[0], Lot: f[1], Gate: f[2], Critic: f[3], Key: f[4], FirstSeen: f[5],
		Finding: f[6], Severity: f[7], Evidence: f[8], Decision: f[9], Action: f[10],
		Outcome: f[11], Model: f[12], Tokens: f[13], Cost: f[14],
	}
}

// newRow builds the registry row for an input that already passed Validate.
func newRow(in Input, ts, firstSeen string) Row {
	return Row{
		Ts: ts, Lot: in.Lot, Gate: in.Gate, Critic: in.Critic, Key: in.Key, FirstSeen: firstSeen,
		Finding: in.Finding, Severity: in.Severity, Evidence: in.Evidence, Decision: in.Decision,
		Action: in.Action, Outcome: in.Outcome, Model: in.Model, Tokens: in.Tokens.String(), Cost: in.Cost.String(),
	}
}

type valueKind int

const (
	kindIdent valueKind = iota // lot, critic, model
	kindSlug                   // key
	kindEnum                   // gate, severity, decision, outcome
	kindText                   // finding, evidence, action
	kindToken                  // tokens
	kindCost                   // cost
)

type columnSpec struct {
	name     string
	required bool
	kind     valueKind
	maxBytes int      // 0 = no byte limit of its own (enums, tokens, cost have a shape)
	allowed  []string // kindEnum only
}

// inputColumns are the columns the caller gives, in registry order (ts and
// first_seen are not among them).
var inputColumns = []columnSpec{
	{name: "lot", required: true, kind: kindIdent, maxBytes: 32},
	{name: "gate", required: true, kind: kindEnum, allowed: gates},
	{name: "critic", required: true, kind: kindIdent, maxBytes: 32},
	{name: "key", required: true, kind: kindSlug, maxBytes: 64},
	{name: "finding", required: true, kind: kindText, maxBytes: 1000},
	{name: "severity", required: true, kind: kindEnum, allowed: severities},
	{name: "evidence", required: true, kind: kindText, maxBytes: 300},
	{name: "decision", required: true, kind: kindEnum, allowed: decisions},
	{name: "action", kind: kindText, maxBytes: 500},
	{name: "outcome", required: true, kind: kindEnum, allowed: outcomes},
	{name: "model", kind: kindIdent, maxBytes: 64},
	{name: "tokens", kind: kindToken},
	{name: "cost", kind: kindCost},
}

// ColumnInfo is what the command's help says about one input column.
type ColumnInfo struct {
	Name     string
	Required bool
	Allowed  []string // the closed list of values; empty for a free column
}

// InputColumnInfo describes the columns the caller gives, in registry order. The
// help is built from it, so the values it lists are the ones Validate accepts.
func InputColumnInfo() []ColumnInfo {
	out := make([]ColumnInfo, len(inputColumns))
	for i, c := range inputColumns {
		out[i] = ColumnInfo{Name: c.name, Required: c.required, Allowed: append([]string(nil), c.allowed...)}
	}
	return out
}

// Fixed reasons. None of them may contain a value from the input.
const (
	reasonRequired    = "is required"
	reasonCommandOwns = "is written by the command"
	reasonCharacters  = "contains a character that is not allowed (control, invisible or invalid)"
	reasonEdgeSpace   = "must not start or end with a space"
	reasonIdent       = "must start with a letter or digit and contain only letters, digits, '.', '_' and '-'"
	reasonSlug        = "must be lowercase words (a-z, 0-9) joined by single '-'"
	reasonToken       = "must be a non-negative integer of at most 12 digits"
	reasonCost        = "must be a decimal number in USD: up to 6 digits, optionally a '.' and up to 6 decimals"
	reasonContentFmt  = "contains something that looks like %s"
	reasonTooLongFmt  = "is longer than %d bytes"
	reasonEnumFmt     = "must be one of: %s"
)

func inputValues(in Input) []string {
	return []string{
		in.Lot, in.Gate, in.Critic, in.Key, in.Finding, in.Severity, in.Evidence,
		in.Decision, in.Action, in.Outcome, in.Model, in.Tokens.String(), in.Cost.String(),
	}
}

// Validate checks the caller's input and returns every violation, in column
// order. It is pure: no disk access, no clock. names is the list of person
// names to refuse (rule 13), already checked by CheckNames.
func Validate(in Input, names []string) []Violation {
	var out []Violation
	if in.Ts != nil {
		out = append(out, Violation{Column: "ts", Reason: reasonCommandOwns})
	}
	if in.FirstSeen != nil {
		out = append(out, Violation{Column: "first_seen", Reason: reasonCommandOwns})
	}
	for i, value := range inputValues(in) {
		out = append(out, checkValue(inputColumns[i], value, names)...)
	}
	return out
}

func checkValue(spec columnSpec, value string, names []string) []Violation {
	if value == "" {
		if spec.required {
			return []Violation{{Column: spec.name, Reason: reasonRequired}}
		}
		return nil
	}
	var out []Violation
	add := func(reason string) { out = append(out, Violation{Column: spec.name, Reason: reason}) }
	if spec.maxBytes > 0 && len(value) > spec.maxBytes {
		add(fmt.Sprintf(reasonTooLongFmt, spec.maxBytes))
	}
	if reason := shapeProblem(spec, value); reason != "" {
		add(reason)
	}
	if rule, hit := Scan(value, names); hit {
		add(fmt.Sprintf(reasonContentFmt, rule))
	}
	return out
}

// shapeProblem returns the fixed reason a value does not have the shape of its
// column, or "" if it does. Length is checked by the caller.
func shapeProblem(spec columnSpec, value string) string {
	switch spec.kind {
	case kindIdent:
		if !identPattern.MatchString(value) {
			return reasonIdent
		}
	case kindSlug:
		if !slugPattern.MatchString(value) {
			return reasonSlug
		}
	case kindEnum:
		if !contains(spec.allowed, value) {
			return fmt.Sprintf(reasonEnumFmt, strings.Join(spec.allowed, ", "))
		}
	case kindText:
		return textProblem(value)
	case kindToken:
		if !tokenPattern.MatchString(value) {
			return reasonToken
		}
	case kindCost:
		if !costPattern.MatchString(value) {
			return reasonCost
		}
	}
	return ""
}

// textProblem applies D14: graphic characters only (no tab, newline, control,
// zero-width or bidirectional-override characters), valid UTF-8, no leading or
// trailing space. U+FFFD is refused too: it is what the JSON decoder writes in
// place of invalid bytes, so accepting it would hide a corrupted input.
func textProblem(value string) string {
	for _, r := range value {
		if r == utf8.RuneError || !unicode.IsGraphic(r) {
			return reasonCharacters
		}
	}
	if value[0] == ' ' || value[len(value)-1] == ' ' {
		return reasonEdgeSpace
	}
	return ""
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func formatTimestamp(t time.Time) string {
	return t.UTC().Format(timeLayout)
}

// parseTimestamp accepts only the canonical layout: what formatTimestamp wrote.
func parseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, err
	}
	if formatTimestamp(t) != s {
		return time.Time{}, fmt.Errorf("not canonical")
	}
	return t, nil
}

// registryError locates a structural problem in an existing registry. The
// reason is a fixed text and never holds the content of the line.
type registryError struct {
	Line   int
	Reason string
}

func (e *registryError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
}

// parseRegistry checks an existing registry against D7 and returns its rows.
// Empty data is an empty registry. It checks structure only: header equal to the
// literal, a final newline, 15 columns per row, no carriage return, and the
// columns the reader relies on (the enums, the key, the two timestamps).
// Content rules (secrets, names) are not re-run: they passed at write time, and
// a rule added later must not make an old registry unreadable.
func parseRegistry(data []byte) ([]Row, error) {
	if len(data) == 0 {
		return nil, nil
	}
	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		return nil, &registryError{Line: strings.Count(text, "\n") + 1, Reason: "the last line does not end with a newline"}
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if lines[0]+"\n" != header {
		return nil, &registryError{Line: 1, Reason: "the header is not the expected one"}
	}
	rows := make([]Row, 0, len(lines)-1)
	for i, line := range lines[1:] {
		row, reason := parseRow(line)
		if reason != "" {
			return nil, &registryError{Line: i + 2, Reason: reason}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseRow(line string) (Row, string) {
	if strings.Contains(line, "\r") {
		return Row{}, "the line contains a carriage return"
	}
	fields := strings.Split(line, "\t")
	if len(fields) != numColumns {
		return Row{}, fmt.Sprintf("the line has %d columns, expected %d", len(fields), numColumns)
	}
	row := rowFromFields(fields)
	return row, structuralProblem(row)
}

// structuralProblem returns the fixed reason a stored row cannot be relied on,
// or "" if it can.
func structuralProblem(r Row) string {
	for _, c := range []struct {
		name string
		ok   bool
		what string
	}{
		{"ts", isTimestamp(r.Ts), "is not a UTC timestamp with milliseconds"},
		{"gate", contains(gates, r.Gate), "has a value outside its list"},
		{"key", slugPattern.MatchString(r.Key) && len(r.Key) <= 64, "is not a valid key"},
		{"first_seen", isTimestamp(r.FirstSeen), "is not a UTC timestamp with milliseconds"},
		{"severity", contains(severities, r.Severity), "has a value outside its list"},
		{"decision", contains(decisions, r.Decision), "has a value outside its list"},
		{"outcome", contains(outcomes, r.Outcome), "has a value outside its list"},
	} {
		if !c.ok {
			return fmt.Sprintf("column %s %s", c.name, c.what)
		}
	}
	return ""
}

func isTimestamp(s string) bool {
	_, err := parseTimestamp(s)
	return err == nil
}
