//go:build linux || darwin

// syscall.Statfs is present with compatible semantics on Linux and macOS only.
// This deliberately does not use the repo's //go:build unix convention as
// emulator/process_unix.go does: FreeBSD's Bavail is int64, and Solaris, illumos
// and AIX expose Statvfs rather than Statfs.

package doctor

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// availableDiskBytes reports unprivileged-available space on the filesystem
// holding path, and the path it actually measured. It walks up to the nearest
// existing ancestor first, because a fresh runner has no AVD home until
// something creates one and that absence is the normal first-run state rather
// than an error. The measured path is returned so a report can name the
// filesystem the number describes: an ancestor can sit on a different volume
// than the requested path would have.
func availableDiskBytes(path string) (available uint64, measured string, err error) {
	if strings.TrimSpace(path) == "" {
		return 0, "", errors.New("no path to measure")
	}
	measured, err = nearestExistingAncestor(path)
	if err != nil {
		return 0, path, err
	}
	var stat syscall.Statfs_t
	if err := syscall.Statfs(measured, &stat); err != nil {
		return 0, measured, fmt.Errorf("statfs %q: %w", measured, err)
	}
	// Bsize is int64 on Linux and uint32 on macOS; Bavail is uint64 on both.
	available, err = availableFromStatfs(uint64(stat.Bavail), int64(stat.Bsize))
	if err != nil {
		return 0, measured, fmt.Errorf("statfs %q: %w", measured, err)
	}
	return available, measured, nil
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
