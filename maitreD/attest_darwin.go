//go:build darwin

package main

/*
#include <libproc.h>
#include <errno.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"unsafe"
)

// resolveExecutable returns the path of the executable running as pid, from
// proc_pidpath — the same source lsof uses.
//
// Read what this does not promise. It returns a path, not the running image:
// on macOS what is hashed is the file at that path now, and a binary replaced
// after it was started would attest as the replacement. Linux hashes the inode
// that is executing and Windows locks the image; this is the weakest of the
// three, and the reason a Mac is an administrative host and not a controller —
// see DEPLOYMENT.md. The platform's own answer would be code signing
// (SecCodeCheckValidity), which is a different attestation and not this one.
var resolveExecutable = func(pid int) (string, error) {
	buf := make([]byte, C.PROC_PIDPATHINFO_MAXSIZE)
	n, err := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf)))
	if n <= 0 {
		if err == nil {
			err = fmt.Errorf("proc_pidpath returned %d", n)
		}
		return "", err
	}
	path := string(buf[:n])
	// macOS lets any user read any process's path — proc_pidpath is what ps
	// uses — so the Linux rule, where another user's process simply cannot be
	// seen, does not exist here. The check the platform does allow is the
	// owner of the file at that path: a system started with sudo runs a file
	// this maitreD's user does not own, and is refused as it would be on Linux.
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return "", fmt.Errorf("%s is owned by uid %d and this maitreD runs as %d: %w", path, st.Uid, os.Getuid(), fs.ErrPermission)
	}
	return path, nil
}

// classifyResolutionError says which of the two refusals a proc_pidpath errno
// is. EPERM is rare here — see resolveExecutable — and ESRCH is the process
// having gone.
func classifyResolutionError(err error) (permission, gone bool) {
	return errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES), errors.Is(err, syscall.ESRCH)
}

// privilegeAdvice is the platform's way of saying "without extra privilege".
const privilegeAdvice = "as that user rather than sudo"
