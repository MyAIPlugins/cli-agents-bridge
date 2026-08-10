package session

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ScopeSeparator splits a qualified recipient — `VAL-payload@alancurtisagency-payload`
// — into the agent name and the scope that disambiguates it (F-116).
//
// It has to be absent from agent names, or the grammar is not invertible: with a
// name like `VAL@home` there is no way to tell which `@` separates what, and the
// rule the whole addressing rests on — every token `peers` prints can be pasted
// into a command — stops being true.
const ScopeSeparator = "@"

// derivedNameReplacement is what a ScopeSeparator becomes when it arrives from a
// DIRECTORY NAME rather than from a person. `-` because it is what a name is
// already allowed to contain and it keeps the result readable.
const derivedNameReplacement = "-"

// ValidateAgentName rejects a name that cannot be addressed unambiguously.
//
// It refuses the name somebody TYPED. A name DERIVED from a directory takes
// SanitizeDerivedName instead: the two arrive by different routes and deserve
// different answers — one is a mistake to report, the other is a directory the
// caller never chose and must not be punished for (nobody typed anything wrong
// when their worktree happens to be called `feat@2`).
func ValidateAgentName(name string) error {
	if strings.Contains(name, ScopeSeparator) {
		return fmt.Errorf("agent name %q cannot contain %q: it is what separates a name from its project when addressing across repositories (e.g. VAL-other%sthe-other-repo)",
			name, ScopeSeparator, ScopeSeparator)
	}
	return nil
}

// derivedAgentName is the default agent name for a project path: its basename,
// made addressable. Used by Register when the caller passes no name.
func derivedAgentName(absProj string) string {
	name, _ := SanitizeDerivedName(filepath.Base(absProj))
	return name
}

// SanitizeDerivedName makes a directory-derived name addressable, reporting
// whether it had to change so the caller can say so out loud.
//
// The default agent name is filepath.Base of the project path (manager.go), so a
// directory called `foo@bar` would inject the separator with nobody having typed
// it — and a hard refusal there would fail a `join` for a choice the user never
// made.
func SanitizeDerivedName(base string) (string, bool) {
	if !strings.Contains(base, ScopeSeparator) {
		return base, false
	}
	return strings.ReplaceAll(base, ScopeSeparator, derivedNameReplacement), true
}
