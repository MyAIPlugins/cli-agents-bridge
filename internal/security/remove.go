package security

import (
	"errors"
	"io/fs"
	"os"
	"time"
)

// F-170: on Windows a delete that crosses another operation on the same name is
// sometimes answered "Access is denied", and a few milliseconds later the name
// gives the true answer. Two probes on a Windows runner (F-170 in ROADMAP.md) found
// two ways in, and they look the same from the caller:
//
//   - another delete of the same name is in flight (the name answers
//     STATUS_DELETE_PENDING until that deleter's handle closes);
//   - a replace of the name lands between DeleteFile's open and its disposition:
//     the disposition is refused on the old, already unlinked file, and the NEW
//     file is the one now standing under the name.
//
// RemoveFile asks again, briefly, with the budget F-154 already chose (see
// denied_retry.go): it adds no budget of its own. The rename has its own, F-133
// (rename_windows.go), and unifying the three is a design of its own. NotExist is
// the success it always was for a delete: the end state is the one that was asked
// for. Any other error, and an ACCESS_DENIED that outlasts the budget, comes back
// as the real error.
//
// THE CONTRACT A CALLER MUST ACCEPT. The retry is BLIND: it deletes by NAME, and
// it cannot tell the two ways in apart. In the second one it removes the newer
// file that the replace put there. So RemoveFile is for a name where dropping the
// newest content is a valid outcome of the caller's own operation. It is NOT for a
// name whose newer file must survive: a delete that has to leave a replacement
// alone needs to dispose of the file it opened (by handle), not of the name, and
// that primitive does not exist here. A caller that cannot accept losing the newer
// file must not use this function.
//
// The reply journal was examined and refused in the F-170 plan gate: the name is
// reused by a different transaction, and the retry would delete the journal that
// transaction just created (an executed interleaving, not a hypothesis). It is a
// caller since F-176, and only on two conditions that belong to the CALLER: the
// removal runs under the exclusive session lock between processes, and after the
// comparison of the journal on disk with the transaction being completed, in the
// same critical section. With both, no writer that follows the protocol can put
// another journal under the name between the check and the removal. The retry
// alone protects nothing: remove either condition and the interleaving above is
// back, so the guard of that caller is Manager.CompleteReplyTxn, not this helper.
//
// WHO MAY CALL IT is a list that fails: remove_callers_test.go enumerates every
// caller in the repository and goes red on a new one, so adding a caller is a
// decision made in a diff, with the reason written next to the name.
//
// Which errors are worth asking again about is decided per platform:
// transientRemoveDenial in perms_windows.go says ACCESS_DENIED, the one in
// perms_unix.go says none, so on Unix this is os.Remove with NotExist folded to nil.
func RemoveFile(path string) error {
	return removeFileSleeping(path, os.Remove, time.Sleep, transientRemoveDenial)
}

// removeFileSleeping is RemoveFile with the three things a test needs to
// control passed in: the delete itself, the wait between attempts, and which
// errors are retried. It is the seam, the way openNoFollowSleeping is for the
// open. Production passes os.Remove, time.Sleep and the platform predicate and
// nothing else.
func removeFileSleeping(path string, remove func(string) error,
	sleep func(time.Duration), retryable func(error) bool) error {
	return retryWhile(deniedAttempts, deniedFirstDelay, sleep, retryable, func() error {
		err := remove(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	})
}
