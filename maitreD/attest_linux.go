//go:build linux

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// resolveExecutable returns the filesystem path of the executable running as
// pid, by reading /proc/<pid>/exe.
//
// That link resolves to the inode, so what is hashed is what is executing —
// even if the file at that path has since been replaced or deleted. Of the
// three platforms this is the strongest reading after Windows, which locks the
// image, and the reason the attestation was designed around it.
//
// A variable so a test can substitute an implementation without build tags.
var resolveExecutable = func(pid int) (string, error) {
	return os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
}

// describeResolutionFailure turns a failure to read /proc/<pid>/exe into
// something an operator can act on.
//
// The interesting case is permission. Linux lets a process read another's exe
// link only if it could trace it, so a maitreD running as one user cannot see a
// system started with sudo — which is how a system that needs GPIO is usually
// started. Every other system on the host attests and that one never does.
//
// The remedy given here is to drop the privilege rather than to raise maitreD's.
// A maitreD running as root to inspect everything is a larger thing to trust
// than the systems it is attesting, and requiring it would put root in the path
// of every deployment.
// It returns the explanation and whether maitreD is certain enough to refuse
// rather than report a fault of its own.
func describeResolutionFailure(pid int, err error) (reason string, refused bool) {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fmt.Sprintf("cannot read /proc/%d/exe: the process belongs to another user, "+
			"so this maitreD cannot see what it is running. Start that system as the same user as maitreD — "+
			"a system needing GPIO usually wants group membership (gpio, dialout) rather than sudo — "+
			"or run maitreD as the user that owns it", pid), true
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("no process %d: it exited before it could be attested", pid), true
	default:
		return fmt.Sprintf("cannot read /proc/%d/exe: %v", pid, err), false
	}
}
