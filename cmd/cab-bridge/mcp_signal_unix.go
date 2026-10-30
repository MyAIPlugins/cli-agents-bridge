//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"
)

// swallowSIGPIPE makes a write to a closed stdout return EPIPE. By default Go
// kills the process with SIGPIPE on such a write to fd 1 or 2, which would skip
// everything the server does on the way out (marking its sessions done, releasing
// its identity). The server treats the error as "the client is gone" and stops.
func swallowSIGPIPE() (stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGPIPE)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}
