package fs

// RenameAtomic moves oldpath to newpath, and where newpath already exists it REPLACES it:
// this is not a "move without overwriting". On Unix it is os.Rename (rename_unix.go). On
// Windows it is the primitive of F-133 (rename_windows.go): POSIX rename semantics, and a
// bounded retry for the transient refusals of a file somebody else holds open.
//
// Both halves are exported here as one function so a caller outside this package that moves
// a file between two folders of the data dir gets the retry on Windows without owning a
// second copy of its budget. The first caller outside the package is the archive step of
// the session retirement (internal/cleanup), which until F-171 called os.Rename and threw
// the error away.
//
// WHAT A CALLER UNDER A LOCK MUST KNOW. On Windows a refusal that does not pass (a read-only
// attribute, an ACL, a handle that is never closed) is reported only after the whole budget
// has been asked for: six attempts and five waits, 310 ms of REQUESTED backoff, which is a
// lower bound on the time spent and not a deadline (see the block in rename_windows.go). A
// caller that holds a lock while it calls this pays that time for every error. On Unix there
// is no wait: rename(2) answers at once.
//
// There is no list of callers, and that is deliberate (the contrast is security.RemoveFile,
// which has one): the retry repeats the SAME atomic operation on an explicit pair of paths,
// so there is no name that can come to mean a newer file between two attempts.
func RenameAtomic(oldpath, newpath string) error {
	return renameAtomic(oldpath, newpath)
}
