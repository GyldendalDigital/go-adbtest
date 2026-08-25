package doctor

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

// The emulator refuses to create an AVD's userdata partition unless the
// filesystem holding the AVD content directory has 1.2x the configured data
// partition size free, and reports the shortfall as:
//
//	Not enough space to create userdata partition. Available: %.2f MB at %s, need %.2f MB.
//
// Those figures are MiB despite the "MB" label, and "Available" is the
// unprivileged-available figure (statfs f_bavail x f_frsize), not total free
// space. All of this was measured against emulator 36.6.11.0 by provoking the
// failure at two configured sizes: 600G reported 737280.00 and 500G reported
// 614400.00, both exactly 1.2x.
const (
	// emulatorDataPartitionFloor is the userdata partition size the emulator
	// uses when config.ini does not set one, and the minimum it raises smaller
	// values to. 6 GiB x 1.2 = 7372.80 MiB, which is character for character
	// the figure from the incident that motivated this check.
	emulatorDataPartitionFloor = 6 << 30
	// emulatorCachePartitionDefault is the cache partition size avdmanager
	// writes ("66MB"). It is preallocated in full.
	emulatorCachePartitionDefault = 66 << 20
	// emulatorSDCardDefault is the SD card size avdmanager writes ("512 MB").
	// It is preallocated in full: sdcard.img is exactly 536870912 bytes.
	emulatorSDCardDefault = 512 << 20
	// diskHeadroomSlop covers the smaller preallocated images (encryptionkey,
	// initrd) plus room for the first moments of userdata growth. The headroom
	// band is otherwise derived from the AVD's own configuration rather than
	// guessed, because the emulator's 1.2x already embeds a proportional margin
	// and a second flat constant on top of it would double-count.
	diskHeadroomSlop = 256 << 20
	// maxDataPartitionSize bounds a configured size before it is multiplied, so
	// a nonsensical config.ini cannot overflow the requirement arithmetic.
	maxDataPartitionSize = 1 << 40
)

// requiredBytes returns the free space the emulator demands before it will
// create a userdata partition of the given size: exactly 1.2x.
func requiredBytes(dataPartition uint64) uint64 {
	if dataPartition > maxDataPartitionSize {
		dataPartition = maxDataPartitionSize
	}
	return dataPartition * 6 / 5
}

// dataPartitionBytes reports the userdata partition size an AVD will be created
// with, and whether config.ini actually specified it. An absent, empty,
// unparseable or zero value means the emulator picks its own size, and the
// emulator raises anything below that size to it.
func dataPartitionBytes(values map[string]string) (size uint64, configured bool) {
	parsed, ok := parseEmulatorSize(values["disk.dataPartition.size"])
	if !ok || parsed == 0 {
		return emulatorDataPartitionFloor, false
	}
	if parsed < emulatorDataPartitionFloor {
		return emulatorDataPartitionFloor, false
	}
	if parsed > maxDataPartitionSize {
		return maxDataPartitionSize, true
	}
	return parsed, true
}

// headroomBytes reports the space an AVD needs beyond its userdata partition.
// The cache and SD card images are preallocated in full and do not count
// towards the emulator's own check, so they belong in the warning band rather
// than in the number compared against its message.
func headroomBytes(values map[string]string) uint64 {
	cache, ok := parseEmulatorSize(values["disk.cachePartition.size"])
	if !ok || cache == 0 {
		cache = emulatorCachePartitionDefault
	}
	var sdcard uint64
	switch {
	case strings.EqualFold(strings.TrimSpace(values["hw.sdCard"]), "no"):
		// No SD card is created at all.
	case strings.TrimSpace(values["sdcard.path"]) != "":
		// The image already exists elsewhere and is not allocated again.
	default:
		if size, ok := parseEmulatorSize(values["sdcard.size"]); ok && size > 0 {
			sdcard = size
		} else {
			sdcard = emulatorSDCardDefault
		}
	}
	return cache + sdcard + diskHeadroomSlop
}

// parseEmulatorSize parses the size grammar an AVD's config.ini uses. A bare
// integer is a count of BYTES, not megabytes - the emulator's -partition-size
// flag uses megabytes, which makes this easy to get backwards. K, M and G are
// binary multiples, may be lowercase, may carry a trailing B, and may be
// separated from the digits by spaces: avdmanager writes "512 MB" for
// sdcard.size and "66MB" for disk.cachePartition.size.
func parseEmulatorSize(raw string) (uint64, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, false
	}
	if last := value[len(value)-1]; last == 'B' || last == 'b' {
		value = strings.TrimSpace(value[:len(value)-1])
	}
	multiplier := uint64(1)
	if value != "" {
		switch value[len(value)-1] {
		case 'K', 'k':
			multiplier = 1 << 10
		case 'M', 'm':
			multiplier = 1 << 20
		case 'G', 'g':
			multiplier = 1 << 30
		}
		if multiplier > 1 {
			value = strings.TrimSpace(value[:len(value)-1])
		}
	}
	digits, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, false
	}
	if digits > math.MaxUint64/multiplier {
		return 0, false
	}
	return digits * multiplier, true
}

// addWithoutOverflow reports left+right, saturating rather than wrapping so a
// nonsensical requirement cannot fold back into a comfortable-looking number.
func addWithoutOverflow(left, right uint64) uint64 {
	if left > math.MaxUint64-right {
		return math.MaxUint64
	}
	return left + right
}

// formatMiB renders a byte count the way the emulator renders its own, so the
// two messages can be read against each other.
func formatMiB(bytes uint64) string {
	return fmt.Sprintf("%.2f MiB", float64(bytes)/(1<<20))
}

// formatGiB renders a byte count for the results where no comparison against
// the emulator's message is needed.
func formatGiB(bytes uint64) string {
	return fmt.Sprintf("%.1f GiB", float64(bytes)/(1<<30))
}

// avdDiskInfo describes what an AVD's own files say about the space it needs.
type avdDiskInfo struct {
	// Values is the parsed config.ini.
	Values map[string]string
	// Directory is the AVD content directory holding the partition images.
	Directory string
	// Created reports that a userdata partition already exists, in which case
	// the emulator will not run its space check on the next boot.
	Created bool
}

// productionAVDDisk reads an AVD's configuration and reports whether its
// userdata partition has already been created.
func productionAVDDisk(name string, homes []string) (avdDiskInfo, bool, error) {
	values, directory, found, err := androidsdk.AVDConfig(name, homes)
	if err != nil || !found {
		return avdDiskInfo{Directory: directory}, found, err
	}
	info, statErr := os.Stat(filepath.Join(directory, "userdata-qemu.img"))
	return avdDiskInfo{
		Values:    values,
		Directory: directory,
		Created:   statErr == nil && info.Mode().IsRegular(),
	}, true, nil
}

// checkDiskSpace reports whether the filesystem holding the AVD content
// directory can satisfy the emulator's userdata partition check. That check
// runs only when the emulator creates the partition, so an AVD that has already
// booted is reported as a warning at most: the emulator will not recheck, and
// failing a host that demonstrably boots would be worse than the bug this
// catches.
func (c *checker) checkDiskSpace() {
	if len(c.avdHomes) == 0 {
		c.add(Result{
			Severity: Information,
			Name:     "disk space",
			Detail:   "not checked because the AVD home is unavailable",
		})
		return
	}
	if c.options.AVD == "" {
		c.checkDiskSpaceWithoutAVD()
		return
	}

	disk, found, err := c.deps.avdDisk(c.options.AVD, c.avdHomes)
	if err != nil {
		c.add(Result{
			Severity: Warning,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q could not be read, so no userdata partition size was compared: %s",
				c.options.AVD, compact(err.Error())),
			Hint: "repair or recreate that AVD, then rerun " + doctorCommand(c.options.AVD),
		})
		return
	}
	if !found {
		c.add(Result{
			Severity: Information,
			Name:     "disk space",
			Detail:   fmt.Sprintf("not checked because AVD %q was not found", c.options.AVD),
			Hint:     "create it with adbtest.EnsureAVD, then rerun " + doctorCommand(c.options.AVD),
		})
		return
	}

	available, err := c.measureAvailable(disk.Directory)
	if err != nil {
		c.addMeasurementFailure(disk.Directory, err)
		return
	}
	dataSize, configured := dataPartitionBytes(disk.Values)
	required := requiredBytes(dataSize)
	comfortable := addWithoutOverflow(required, headroomBytes(disk.Values))

	switch {
	case available >= comfortable:
		c.add(Result{
			Severity: OK,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q needs %s for its userdata partition%s; %s has %s free",
				c.options.AVD, formatGiB(required), assumedDefault(configured), disk.Directory, formatGiB(available)),
		})
	case disk.Created:
		c.add(Result{
			Severity: Warning,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q has already created its userdata partition, so the emulator will not recheck space; %s has %s free against the %s a fresh partition would need",
				c.options.AVD, disk.Directory, formatMiB(available), formatMiB(required)),
			Hint: "free space before wiping or recreating that AVD, or point ANDROID_AVD_HOME at a larger volume",
		})
	case available < required:
		c.add(Result{
			Severity: Failure,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q needs %s to create its userdata partition%s but %s has %s free",
				c.options.AVD, formatMiB(required), assumedDefault(configured), disk.Directory, formatMiB(available)),
			Hint: fmt.Sprintf("free at least %s on that filesystem, point ANDROID_AVD_HOME at a larger volume, or lower disk.dataPartition.size in that AVD's config.ini, then rerun %s",
				formatMiB(required-available), doctorCommand(c.options.AVD)),
		})
	default:
		c.add(Result{
			Severity: Warning,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q needs %s and %s has %s free, leaving %s spare",
				c.options.AVD, formatMiB(required), disk.Directory, formatMiB(available), formatMiB(available-required)),
			Hint: fmt.Sprintf("free another %s so the AVD keeps room for its cache and SD card images, or point ANDROID_AVD_HOME at a larger volume",
				formatMiB(comfortable-available)),
		})
	}
}

// checkDiskSpaceWithoutAVD reports free space against the smallest requirement
// any AVD can have. Without a named AVD the check cannot know which one will be
// booted, so it never fails - but it must not stay silent either, because a
// runner that has not created its AVD yet is exactly the situation this check
// exists for.
func (c *checker) checkDiskSpaceWithoutAVD() {
	home := c.avdHomes[0]
	available, err := c.measureAvailable(home)
	if err != nil {
		c.addMeasurementFailure(home, err)
		return
	}
	if floor := requiredBytes(emulatorDataPartitionFloor); available < floor {
		c.add(Result{
			Severity: Warning,
			Name:     "disk space",
			Detail: fmt.Sprintf("no AVD was selected, so no userdata partition size was compared, but %s has %s free, below the %s the smallest possible AVD needs",
				home, formatMiB(available), formatMiB(floor)),
			Hint: "free space on that filesystem or point ANDROID_AVD_HOME at a larger volume, then rerun adbtest doctor --avd NAME to compare against a specific AVD",
		})
		return
	}
	c.add(Result{
		Severity: Information,
		Name:     "disk space",
		Detail: fmt.Sprintf("no AVD was selected, so no userdata partition size was compared; %s has %s free",
			home, formatGiB(available)),
		Hint: "rerun with --avd NAME to compare that free space against the AVD's disk.dataPartition.size",
	})
}

// measureAvailable bounds the measurement the way every other external step in
// this package is bounded. statfs does not observe a context, and a hung NFS or
// autofs home would otherwise make adbtest doctor unkillable.
func (c *checker) measureAvailable(path string) (uint64, error) {
	type measurement struct {
		bytes uint64
		err   error
	}
	results := make(chan measurement, 1)
	go func() {
		bytes, err := c.deps.availableDiskBytes(path)
		results <- measurement{bytes: bytes, err: err}
	}()
	timer := time.NewTimer(c.deps.commandTimeout)
	defer timer.Stop()
	select {
	case result := <-results:
		return result.bytes, result.err
	case <-timer.C:
		return 0, fmt.Errorf("measuring free space on %q did not finish within %s", path, c.deps.commandTimeout)
	case <-c.ctx.Done():
		return 0, c.ctx.Err()
	}
}

func (c *checker) addMeasurementFailure(path string, err error) {
	severity := Warning
	if errors.Is(err, errors.ErrUnsupported) {
		severity = Information
	}
	c.add(Result{
		Severity: severity,
		Name:     "disk space",
		Detail:   fmt.Sprintf("free space on %s could not be measured: %s", path, compact(err.Error())),
		Hint:     "check that the AVD home exists and is readable, then rerun adbtest doctor",
	})
}

func assumedDefault(configured bool) string {
	if configured {
		return ""
	}
	return fmt.Sprintf(" (assuming the emulator's %s default, which that AVD does not set)", formatGiB(emulatorDataPartitionFloor))
}

func doctorCommand(avd string) string {
	return fmt.Sprintf("adbtest doctor --avd %s", avd)
}
