//go:build windows

package main

import "errors"

// errNotifyWatchUnsupported is what `cab-bridge notify-watch` answers on
// Windows. exitFromErr prints it and exits 1, like any other refusal.
//
// It REFUSES rather than disappearing, and that is the point: a subcommand that
// silently drops out of a binary is discovered by an agent whose hook never
// fires and who has no way to tell "it ran and found nothing" from "it was never
// there". Better a message than a blank.
var errNotifyWatchUnsupported = errors.New(
	"notify-watch is not supported on windows: the watcher runs its hook in its own process group " +
		"(setpgid + kill(-pgid)), which has no equivalent here — a Job Object rewrite is the port, not a flag. " +
		"Peers that would want it: Claude Code does not (it has native push); a Codex CLI agent is woken from outside with " +
		"`codex queue --thread <its CODEX_THREAD_ID> --message <text>`, which works on Windows, but you run it yourself " +
		"after sending the message, because nothing does it for you here")

// runNotifyWatch keeps the symbol main.go dispatches to. Everything else in
// notify_watch.go and notify_watch_state.go is tagged !windows, so this file is
// the entire surface on this platform.
//
// args is ignored deliberately: parsing flags to then refuse would suggest some
// combination of them might work.
func runNotifyWatch(args []string) error {
	return errNotifyWatchUnsupported
}
