//go:build linux || darwin

// syscall.Statfs is present with compatible semantics on Linux and macOS only.
// This deliberately does not use the repo's //go:build unix convention as
// emulator/process_unix.go does: FreeBSD's Bavail is int64, and Solaris, illumos
// and AIX expose Statvfs rather than Statfs.

package doctor

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"syscall"
)

// availableDiskBytes reports unprivileged-available space on the filesystem
// holding path. It walks up to the nearest existing ancestor first, because a
// fresh runner has no AVD home until something creates one and that absence is
// the normal first-run state rather than an error.
func availableDiskBytes(path string) (uint64, error) {
	measured, err := nearestExistingAncestor(path)
	if err != nil {
		return 0, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(measured, &stat); err != nil {
		return 0, fmt.Errorf("statfs %q: %w", measured, err)
	}
	// Bsize is int64 on Linux and uint32 on macOS; Bavail is uint64 on both.
	// A filesystem reporting a non-positive block size is unmeasurable, which
	// must not be reported as zero free space: that would be a false failure on
	// a working host.
	blockSize := int64(stat.Bsize)
	if blockSize <= 0 {
		return 0, fmt.Errorf("statfs %q reported a non-positive block size", measured)
	}
	available := uint64(stat.Bavail)
	if available > math.MaxUint64/uint64(blockSize) {
		return math.MaxUint64, nil
	}
	return available * uint64(blockSize), nil
}

func nearestExistingAncestor(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		_, err := os.Stat(current)
		if err == nil {
			return current, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect %q: %w", current, err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing directory holds %q", path)
		}
		current = parent
	}
}
