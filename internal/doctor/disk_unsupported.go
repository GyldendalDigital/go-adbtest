//go:build !linux && !darwin

// The constraint is spelled out because _unsupported is not a GOOS suffix and
// carries no implicit build constraint of its own.

package doctor

import (
	"errors"
	"fmt"
)

// availableDiskBytes cannot measure free space on this host. checkHost already
// ends a doctor run before the disk check on every such host; this exists so
// the package builds everywhere, and reports ErrUnsupported so the check
// records an observation rather than a warning.
func availableDiskBytes(path string) (uint64, error) {
	return 0, fmt.Errorf("measuring free space on %q: %w", path, errors.ErrUnsupported)
}
