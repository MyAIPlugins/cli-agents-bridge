package main

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

// What one pass over an inbox found, and how the findings are told to a reader.
//
// readMailbox lives in next.go (it is older than this file and next.go is already over the
// 600-line cap of dev-conventions.md, so what is new goes here and not there).

const (
	// maxUnreadableListed bounds the files a TEXT names (the JSON of `next` lists every
	// one). It is half of maxHeldIDsListed, which sits in the same Desktop notice: a name
	// with its cause ("msg-bbbbbbbbbbbb.json": permission denied, about 45 bytes) is twice
	// as long as an id.
	maxUnreadableListed = 5
	// maxCauseDepth bounds the walk down a chain of errors. It is there against a cycle of
	// Unwrap, not to change the answer: past it the text found at that level is returned.
	maxCauseDepth = 16
)

// mailboxScan is what one pass over an inbox found, in four buckets because they are four
// different facts, and a caller that wants one must not be handed another. A scanner
// reports; the command decides.
type mailboxScan struct {
	// entries are the decoded messages, legacy acks already dropped.
	entries []mailboxEntry
	// corrupt are files that were READ and are not a valid message: names, sorted.
	corrupt []string
	// unreadable are files the system would not let us read (permission denied, an I/O
	// error): the name and what the system said, sorted by name. Those messages are not
	// delivered and somebody has to fix the cause (F-172).
	unreadable []unreadableFile
	// foreign are files that do not belong to this user, or are links: names, sorted.
	foreign []string
}

// unreadableFile is one file the system would not let us read.
type unreadableFile struct {
	// File is the file name, never the path.
	File string `json:"file"`
	// Cause is what the system said, in its shortest form: "permission denied".
	Cause string `json:"cause"`
}

// recordReadError files a file whose read failed under what the failure says it is.
//   - not ours (or a link): foreign, and the command refuses the inbox;
//   - gone: NOT filed. It was in the listing and a `reply` or a `tidy` moved it before the
//     read, which is the normal life of an inbox and says nothing about the file;
//   - anything else, the system refusing or failing: unreadable, with its cause.
//
// What was read and does not decode is not decided here: that is corrupt, and the caller
// that decodes files it did read files it under corrupt.
func (s *mailboxScan) recordReadError(name string, err error) {
	switch {
	case errors.Is(err, security.ErrOwnershipMismatch):
		s.foreign = append(s.foreign, name)
	case errors.Is(err, fs.ErrNotExist):
		return
	default:
		s.unreadable = append(s.unreadable, unreadableFile{File: name, Cause: causeOf(err)})
	}
}

// causeOf is what the system said, without the path: the leaf of the chain of errors.
// ReadOwnedFile wraps a *fs.PathError (path in the text) in a message that quotes the path
// again, and the leaf is the errno underneath. Three things about that choice:
//   - on Windows the leaf is windows.ERROR_ACCESS_DENIED and says "Access is denied.", not
//     "permission denied": the rule is the leaf, not a text;
//   - the leaf throws away the wrapper of F-154 (denied_retry.go, "still refused after 6
//     attempts, 155ms of backoff requested"). That is a choice: the cause is the answer of
//     the system, not the story of how long we asked;
//   - an error with `Unwrap() []error` stops errors.Unwrap on the composite node, whose text
//     may hold the path. Nothing in the chain of ReadOwnedFile has one today; if one day it
//     does, the test that the cause carries no path is the one that sees it.
//
// err must not be nil.
func causeOf(err error) string {
	cause := err
	for i := 0; i < maxCauseDepth; i++ {
		inner := errors.Unwrap(cause)
		if inner == nil {
			break
		}
		cause = inner
	}
	return cause.Error()
}

// listed joins at most maxUnreadableListed items and counts the rest.
func listed(items []string) string {
	shown := items
	if len(items) > maxUnreadableListed {
		shown = items[:maxUnreadableListed]
	}
	out := strings.Join(shown, "; ")
	if more := len(items) - len(shown); more > 0 {
		out += fmt.Sprintf(" and %d more", more)
	}
	return out
}

// quoted renders a name so that nothing in it can end the sentence it sits in.
func quoted(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = fmt.Sprintf("%q", n)
	}
	return out
}

// hintNothingDeliverable is the hint of a page with no message and at least one file that
// cannot be delivered. "Returns at once and does not wait" is a promise about `next`, and
// TestNext_AnInboxWithOnlyUnreadableFilesSaysNothingCanBeDeliveredAndReturnsAtOnce keeps
// it true: if `next` is ever made to wait on such an inbox, sentence and test change together.
const hintNothingDeliverable = "nothing can be delivered now: fix or move the file(s) named in warnings, then run next again. Until then next returns at once and does not wait"

// undeliverableWarnings are the warnings of `next` for the files in the inbox that will
// not be delivered, the ones that need a fix of the system first.
func undeliverableWarnings(scan mailboxScan) []string {
	var out []string
	if len(scan.unreadable) > 0 {
		items := make([]string, len(scan.unreadable))
		for i, f := range scan.unreadable {
			items[i] = fmt.Sprintf("%q: %s", f.File, f.Cause)
		}
		out = append(out, fmt.Sprintf("%d file(s) in the inbox cannot be read and are NOT delivered (%s). Fix the cause or move the file(s) out of the inbox: until then those messages will not arrive.",
			len(scan.unreadable), listed(items)))
	}
	if len(scan.corrupt) > 0 {
		out = append(out, fmt.Sprintf("%d file(s) in the inbox were read but are not valid messages, and are NOT delivered (%s). Move the file(s) out of the inbox and ask the sender to send again.",
			len(scan.corrupt), listed(quoted(scan.corrupt))))
	}
	return out
}

// unreadableNotice is what the Claude Desktop notice says about files the system would
// not let be read: ids, causes and counts, no path and no body.
func unreadableNotice(files []unreadableFile) string {
	items := make([]string, len(files))
	for i, f := range files {
		items[i] = fmt.Sprintf("%q (%s)", f.File, f.Cause)
	}
	return fmt.Sprintf("%d file(s) in the inbox cannot be read and are NOT shown or delivered (%s). A person has to fix the cause on the machine or move the file(s) out of the inbox; until then those messages will not arrive.",
		len(files), listed(items))
}

// corruptNotice is the same for files that were read and are not messages: a count, as
// before, because the notice policy is ids and reasons only.
func corruptNotice(n int) string {
	return fmt.Sprintf("%d file(s) in the inbox are not valid messages and are NOT shown or delivered. A person has to move them out of the inbox, and the sender has to send the message again.", n)
}
