//go:build darwin

package main

/*
#include <libproc.h>
#include <errno.h>
*/
import "C"

import (
	"errors"
	"io/fs"
	"fmt"
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
	return string(buf[:n]), nil
}

// The generic fs errors are recognised too: a test substitutes a resolver
// that returns them, and the meaning is the same whatever produced it.
//
// describeResolutionFailure turns a proc_pidpath failure into something an
// operator can act on: the same two cases as Linux, by errno.
func describeResolutionFailure(pid int, err error) (reason string, refused bool) {
	switch {
	case errors.Is(err, syscall.EPERM), errors.Is(err, syscall.EACCES), errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("cannot read the path of process %d: it belongs to another user, "+
			"so this maitreD cannot see what it is running. Start that system as the same user as maitreD", pid), true
	case errors.Is(err, syscall.ESRCH), errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("no process %d: it exited before it could be attested", pid), true
	default:
		return fmt.Sprintf("cannot read the path of process %d: %v", pid, err), false
	}
}
