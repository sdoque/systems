package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Whatever the platform, the maitreD must be able to name the file behind a
// process of its own user — this test binary, asked about itself — and a pid
// nothing runs as must be refused rather than reported as a fault.
func TestResolveExecutableFindsThisProcess(t *testing.T) {
	path, err := resolveExecutable(os.Getpid())
	if err != nil {
		t.Fatalf("cannot resolve this very process: %v", err)
	}
	want, _ := os.Executable()
	if filepath.Clean(path) != filepath.Clean(want) {
		// macOS may answer through a different link than os.Executable; the
		// two must at least name the same file.
		a, _ := os.Stat(path)
		b, _ := os.Stat(want)
		if a == nil || b == nil || !os.SameFile(a, b) {
			t.Fatalf("resolved %q, this process is %q", path, want)
		}
	}

	_, err = resolveExecutable(2147483646)
	if err == nil {
		t.Fatal("a pid nothing runs as resolved to something")
	}
	if reason, refused := describeResolutionFailure(2147483646, err); !refused {
		t.Fatalf("a missing process was reported as maitreD's own fault: %s", reason)
	}
}
