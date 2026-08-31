//go:build windows

package main

import (
	"errors"

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
// process of the same user without any privilege — including one of higher
// integrity. Tested 31 August 2026: an unelevated maitreD attests a system
// started "Run as administrator". That is correct, not a gap: the whitelist is
// the control, a non-whitelisted binary fails the hash whatever its integrity,
// and elevation buys no cloud rights, which come from the certificate's name.
// The boundary that holds is the user: another user's process answers access
// denied without SeDebugPrivilege, so it is refused as a sudo-started one is
// on Linux.
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

// classifyResolutionError says which of the two refusals an OpenProcess error is.
func classifyResolutionError(err error) (permission, gone bool) {
	// A process that has exited but not yet been reaped — a parent or the
	// console still holds its object — opens, and the image query then fails
	// with ERROR_GEN_FAILURE: gone, not a fault of this maitreD's.
	return errors.Is(err, windows.ERROR_ACCESS_DENIED),
		errors.Is(err, windows.ERROR_INVALID_PARAMETER) || errors.Is(err, windows.ERROR_GEN_FAILURE)
}

// privilegeAdvice is the platform's way of saying "without extra privilege".
const privilegeAdvice = "without \"Run as administrator\""
