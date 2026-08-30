//go:build windows

package main

import (
	"errors"
	"io/fs"
	"fmt"

	"golang.org/x/sys/windows"
)

// resolveExecutable returns the path of the image running as pid, from the
// kernel's own record of the process.
//
// Windows locks a running image, so the file at this path cannot be changed
// or replaced while the process lives: what is hashed is what is executing.
// That is a stronger guarantee than Linux's inode link and much stronger than
// macOS's path, and it is why a Windows host is an acceptable place for
// attestation and not merely a tolerated one.
//
// PROCESS_QUERY_LIMITED_INFORMATION is enough to ask, and is granted for a
// process of the same user without any privilege; an elevated process or
// another user's answers with access denied, as on Linux.
var resolveExecutable = func(pid int) (string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

// The generic fs errors are recognised too: a test substitutes a resolver
// that returns them, and the meaning is the same whatever produced it.
//
// describeResolutionFailure turns a failure to open the process into something
// an operator can act on. The same two cases as Linux, reached differently.
func describeResolutionFailure(pid int, err error) (reason string, refused bool) {
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED), errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("cannot open process %d: it belongs to another user or runs elevated, "+
			"so this maitreD cannot see what it is running. Start that system as the same user as maitreD, "+
			"without 'Run as administrator'", pid), true
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER), errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("no process %d: it exited before it could be attested", pid), true
	default:
		return fmt.Sprintf("cannot open process %d: %v", pid, err), false
	}
}
