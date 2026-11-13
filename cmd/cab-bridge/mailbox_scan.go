package main

// What one pass over an inbox found, and how the findings are told to a reader.
//
// readMailbox lives in next.go (it is older than this file and next.go is already over the
// 600-line cap of dev-conventions.md, so what is new goes here and not there).

// mailboxScan is what one pass over an inbox found, in separate buckets because they are
// different facts, and a caller that wants one must not be handed another. A scanner
// reports; the command decides.
type mailboxScan struct {
	// entries are the decoded messages, legacy acks already dropped.
	entries []mailboxEntry
	// corrupt are files that could not be read or decoded: names, sorted.
	corrupt []string
	// foreign are files that do not belong to this user, or are links: names, sorted.
	foreign []string
}
