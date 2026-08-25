//go:build linux || darwin

package doctor

import (
	"path/filepath"
	"testing"
)

// availableDiskBytes is a thin wrapper over a syscall, so a faked test proves
// nothing about whether Bavail and Bsize were read correctly. These assert
// against a real filesystem but never against an absolute amount of free
// space, which would be the flakiness vector.

func TestAvailableDiskBytesMeasuresARealFilesystem(t *testing.T) {
	got, measured, err := availableDiskBytes(t.TempDir())
	if err != nil {
		t.Fatalf("availableDiskBytes() error: %v", err)
	}
	if got == 0 {
		t.Fatal("availableDiskBytes() = 0 on a writable temporary directory")
	}
	if measured == "" {
		t.Fatal("availableDiskBytes() did not report the path it measured")
	}
}

func TestAvailableDiskBytesWalksUpToAnExistingAncestor(t *testing.T) {
	// A fresh runner has no AVD home until something creates one, so measuring
	// a path that does not exist yet is the normal first-run state.
	absent := filepath.Join(t.TempDir(), "android", "avd", "not_created_yet.avd")

	got, measured, err := availableDiskBytes(absent)
	if err != nil {
		t.Fatalf("availableDiskBytes(%q) error: %v", absent, err)
	}
	if got == 0 {
		t.Fatal("availableDiskBytes() = 0 for a path whose ancestor exists")
	}
	// The report must name the filesystem the number actually describes, not a
	// path that does not exist.
	if measured == absent {
		t.Fatalf("availableDiskBytes() reported %q, which does not exist", measured)
	}
}

func TestNearestExistingAncestorFindsTheDeepestExistingDirectory(t *testing.T) {
	directory := t.TempDir()

	got, err := nearestExistingAncestor(filepath.Join(directory, "a", "b", "c"))
	if err != nil {
		t.Fatalf("nearestExistingAncestor() error: %v", err)
	}
	if got != directory {
		t.Fatalf("nearestExistingAncestor() = %q, want %q", got, directory)
	}
}

func TestAvailableDiskBytesRejectsAnEmptyPath(t *testing.T) {
	if _, _, err := availableDiskBytes("  "); err == nil {
		t.Fatal("availableDiskBytes() measured the working directory for a blank path")
	}
}
