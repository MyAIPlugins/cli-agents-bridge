//go:build windows

package fs

import (
	"errors"

	"golang.org/x/sys/windows"
)

// isCrossDevice reports whether err is the system saying "these two paths are
// not on the same volume", which makes an atomic rename impossible.
//
// F-137 — AND THE POINT IS THAT `syscall.EXDEV` DOES NOT ANSWER IT HERE. On
// Windows the two constants are different numbers and neither maps to the
// other:
//
//	syscall.EXDEV          536871040   "invalid cross-device link"
//	ERROR_NOT_SAME_DEVICE         17   "cannot move the file to a different disk drive"
//
// Measured, not read: a real cross-volume rename returns the second, and
// errors.Is(err, syscall.EXDEV) against it is FALSE. The call-sites that used
// EXDEV therefore had a branch that could never be taken on this platform —
// while the comments above them announced that cross-device failures were
// surfaced explicitly and cited the project's no-silent-fallback rule. A dead
// branch is a defect; a dead branch declared alive is the one that costs
// somebody an afternoon.
//
// The Unix file is the same predicate with the other constant and nothing else
// in it. They stay two files on purpose: there is no single name for this
// condition, so a "unified" implementation would be a platform choice wearing
// the clothes of a simplification.
func isCrossDevice(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}
