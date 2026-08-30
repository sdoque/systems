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

// classifyResolutionError says which of the two refusals a /proc error is.
func classifyResolutionError(err error) (permission, gone bool) {
	return errors.Is(err, fs.ErrPermission), errors.Is(err, fs.ErrNotExist)
}

// privilegeAdvice is the platform's way of saying "without extra privilege".
const privilegeAdvice = "a system needing GPIO usually wants group membership (gpio, dialout) rather than sudo"
