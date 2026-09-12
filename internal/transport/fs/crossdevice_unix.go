//go:build !windows

package fs

import (
	"errors"
	"syscall"
)

// isCrossDevice reports whether err is the kernel saying "these two paths are
// not on the same filesystem", which makes an atomic rename impossible.
//
// On this platform that is EXDEV, and this file is nothing else. The Windows
// half answers the same question with a different constant, and the two are
// deliberately NOT unified: a shared implementation would have to pick one of
// the two names, and whichever it picked would be wrong somewhere. See
// crossdevice_windows.go for what the other one carries.
func isCrossDevice(err error) bool {
	return errors.Is(err, syscall.EXDEV)
}
