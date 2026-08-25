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
// unprivileged-available figure, which statfs reports as f_bavail x f_bsize and
// statvfs as f_bavail x f_frsize.
//
// Measured against emulator 36.6.11.0, both by provoking the failure and by
// reading the check in emulator/qemu/linux-x86_64/qemu-system-x86_64: it
// computes size * 1.2 * (1/1048576) > avail * (1/1048576), and the two
// constants decode to exactly 1.2 and 1/MiB. Provoked at 600G it reported
// 737280.00, at 10G 12288.00, at 7G 8601.60.
const (
	// emulatorDataPartitionFloor is the userdata partition size the emulator
	// uses when config.ini sets none, and the minimum it raises smaller values
	// to. 6 GiB x 1.2 = 7372.80 MiB, character for character the figure from
	// the incident that motivated this check.
	//
	// The clamp is a hardcoded signed max in the emulator binary, not derived
	// from the image or device. Its boundary was measured exactly: 6143M
	// reports 7372.80, 6144M reports 7372.80, and 6145M reports 7374.00. It
	// applies unconditionally from API 24; below that the emulator gates it
	// behind a feature flag, which this check does not model.
	emulatorDataPartitionFloor = 6 << 30
	// avdmanagerDataPartitionDefault is the size avdmanager writes for every
	// profile EnsureAVD can request, so an AVD this library provisions needs
	// 10 GiB x 1.2 = 12 GiB free. Confirmed against sdklib 22.0, where
	// EmulatedProperties.DEFAULT_INTERNAL_STORAGE is Storage(10, GiB).
	avdmanagerDataPartitionDefault = 10 << 30
	// diskHeadroom is the band above the emulator's own requirement below which
	// this check warns. It is deliberately small and flat.
	//
	// Nothing else in the AVD directory is preallocated: userdata-qemu.img is a
	// ~10 MiB stub rather than a preallocated full-size image, cache.img holds
	// 5.2 MiB of a 66 MiB apparent file,
	// and avdmanager AVDs never get an sdcard.img at all. What does grow is the
	// qcow2 overlay - 1.87 GB inside a minute of first boot - and the
	// emulator's own 1.2x is precisely the allowance for that, so a second
	// proportional margin here would double-count it. This covers only the
	// genuinely dense auxiliary images (encryptionkey.img at 18 MiB, initrd at
	// ~2 MiB) plus room to notice before the bar is reached.
	diskHeadroom = 256 << 20
	// maxDataPartitionSize is the largest configured size this check models. It
	// exists only so the 1.2x multiply cannot overflow; every realistic value
	// is far below it and is compared exactly as configured.
	maxDataPartitionSize = math.MaxUint64 / 6
)

// requiredBytes returns the free space the emulator demands before it will
// create a userdata partition of the given size: exactly 1.2x.
func requiredBytes(dataPartition uint64) uint64 {
	if dataPartition > maxDataPartitionSize {
		dataPartition = maxDataPartitionSize
	}
	return dataPartition * 6 / 5
}

// sizeOrigin records where a userdata partition size came from, so the report
// can state an assumption it made rather than presenting it as the AVD's own
// configuration.
type sizeOrigin int

const (
	// sizeConfigured means config.ini set a usable size, used as-is.
	sizeConfigured sizeOrigin = iota
	// sizeDefaulted means config.ini sets no size at all.
	sizeDefaulted
	// sizeUnparseable means config.ini sets a size the emulator will not accept.
	sizeUnparseable
	// sizeRaised means config.ini sets a size below the emulator's minimum,
	// which the emulator silently raises and writes back.
	sizeRaised
	// sizeCapped means config.ini sets a size too large for this check to model.
	sizeCapped
)

// dataPartitionBytes reports the userdata partition size an AVD will be created
// with, and where that number came from.
func dataPartitionBytes(values map[string]string) (size uint64, origin sizeOrigin) {
	raw, present := values["disk.dataPartition.size"]
	if !present || strings.TrimSpace(raw) == "" {
		return emulatorDataPartitionFloor, sizeDefaulted
	}
	parsed, ok := parseEmulatorSize(raw)
	if !ok {
		// The emulator rejects it too and falls back to its own size.
		return emulatorDataPartitionFloor, sizeUnparseable
	}
	if parsed < emulatorDataPartitionFloor {
		return emulatorDataPartitionFloor, sizeRaised
	}
	if parsed > maxDataPartitionSize {
		return maxDataPartitionSize, sizeCapped
	}
	return parsed, sizeConfigured
}

// parseEmulatorSize parses the grammar the emulator accepts for a partition
// size, reproducing its shape exactly:
//
//	[whitespace] ['+'] digits [ k|K|m|M|g|G <anything> ]
//
// The unit letter must follow the digits immediately, and everything after that
// single letter is ignored - the emulator reads 7Gi, 7Gxyz and 7168MiB as sizes,
// and 7168Mg as 7168 MiB. A bare integer is a count of BYTES, not megabytes;
// the emulator's -partition-size flag uses megabytes, which makes this easy to
// get backwards.
//
// Diverging in either direction under-reports the requirement. Parsing a value
// the emulator rejects makes doctor compare against a size the emulator would
// have replaced with its larger default; rejecting one the emulator accepts
// makes doctor fall back to that default when the real partition is bigger.
// The grammar must mirror the emulator, not merely err strict. Measured against
// emulator 36.6.11.0.
func parseEmulatorSize(raw string) (uint64, bool) {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "+")
	end := 0
	for end < len(value) && value[end] >= '0' && value[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	multiplier := uint64(1)
	if end < len(value) {
		switch value[end] {
		case 'k', 'K':
			multiplier = 1 << 10
		case 'm', 'M':
			multiplier = 1 << 20
		case 'g', 'G':
			multiplier = 1 << 30
		default:
			return 0, false
		}
	}
	digits, err := strconv.ParseUint(value[:end], 10, 64)
	if err != nil {
		return 0, false
	}
	if digits > math.MaxUint64/multiplier {
		return 0, false
	}
	return digits * multiplier, true
}

// availableFromStatfs converts a statfs result into a byte count. It lives here
// rather than beside the syscall so it can be tested on every host: a
// filesystem reporting a non-positive block size is unmeasurable, and must not
// be reported as zero free space, which would be a false failure on a working
// host.
func availableFromStatfs(blocksAvailable uint64, blockSize int64) (uint64, error) {
	if blockSize <= 0 {
		return 0, errors.New("filesystem reported a non-positive block size")
	}
	if blocksAvailable > math.MaxUint64/uint64(blockSize) {
		return math.MaxUint64, nil
	}
	return blocksAvailable * uint64(blockSize), nil
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
	// Values is the parsed config.ini, nil when it could not be read.
	Values map[string]string
	// Directory is the AVD content directory holding the partition images. It
	// is set even when Values is not, so a caller can still measure it.
	Directory string
	// Created reports that a userdata partition already exists, in which case
	// the emulator skips its space check entirely on the next boot.
	Created bool
}

// productionAVDDisk reads an AVD's configuration and reports whether its
// userdata partition has already been created.
func productionAVDDisk(name string, homes []string) (avdDiskInfo, bool, error) {
	metadata, found, err := androidsdk.AVDConfig(name, homes)
	if err != nil || !found {
		return avdDiskInfo{Directory: metadata.Directory}, found, err
	}
	// The raw image, not the qcow2 overlay: an AVD can carry the overlay alone,
	// and the emulator still runs its check in that state.
	info, statErr := os.Stat(filepath.Join(metadata.Directory, "userdata-qemu.img"))
	return avdDiskInfo{
		Values:    metadata.Values,
		Directory: metadata.Directory,
		Created:   statErr == nil && info.Mode().IsRegular(),
	}, true, nil
}

// checkDiskSpace reports whether the filesystem holding the AVD content
// directory can satisfy the emulator's userdata partition check. That check
// runs only when the emulator creates the partition, so an AVD that has already
// booted is reported as a warning at most: the emulator skips the check unless
// something recreates the partition, and failing a host that demonstrably boots
// would be worse than the bug this catches.
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
		c.addUnreadableAVD(disk, err)
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

	available, measured, err := c.measureAvailable(disk.Directory)
	if err != nil {
		c.addMeasurementFailure(disk.Directory, err)
		return
	}
	dataSize, origin := dataPartitionBytes(disk.Values)
	required := requiredBytes(dataSize)
	comfortable := addWithoutOverflow(required, diskHeadroom)
	// A shortfall is only worth failing on when the requirement behind it is
	// certain. Below API 24 the emulator gates its 6 GiB minimum behind a
	// feature flag, so a smaller configured partition may be honoured and this
	// requirement may be an overestimate.
	shortfall := Failure
	if (origin == sizeRaised || origin == sizeDefaulted) && derivedSizeIsUncertain(disk.Values) {
		shortfall = Warning
	}

	switch {
	case available >= comfortable:
		c.add(Result{
			Severity: OK,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q needs %s for its userdata partition%s; %s has %s free",
				c.options.AVD, formatGiB(required), describeOrigin(origin, formatGiB), measured, formatGiB(available)),
		})
	case disk.Created:
		c.add(Result{
			Severity: Warning,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q already has a userdata partition, so the emulator skips its space check; recreating it would need %s where %s has %s free",
				c.options.AVD, formatMiB(required), measured, formatMiB(available)),
			Hint: fmt.Sprintf("no action is needed while this AVD is reused; a wipe (emulator -wipe-data, or Config.WipeData) recreates the partition and would need %s free",
				formatMiB(required)),
		})
	case available < required:
		c.add(Result{
			Severity: shortfall,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q needs %s to create its userdata partition%s but %s has %s free",
				c.options.AVD, formatMiB(required), describeOrigin(origin, formatMiB), measured, formatMiB(available)),
			Hint: c.shortfallHint(required-available, available, dataSize, measured, origin),
		})
	default:
		c.add(Result{
			Severity: Warning,
			Name:     "disk space",
			Detail: fmt.Sprintf("AVD %q needs %s for its userdata partition%s; %s has %s free, leaving only %s spare",
				c.options.AVD, formatMiB(required), describeOrigin(origin, formatMiB), measured,
				formatMiB(available), formatMiB(available-required)),
			Hint: fmt.Sprintf("free another %s on the volume holding %s, or point ANDROID_AVD_HOME at a larger volume",
				formatMiB(comfortable-available), measured),
		})
	}
}

// shortfallHint offers only remedies that can actually work. Lowering
// disk.dataPartition.size is futile unless the AVD configures a size above the
// emulator's minimum, because the emulator raises anything smaller straight
// back - and a consumer acting on this output automatically would otherwise
// edit config.ini, rerun, and see an identical failure indefinitely.
func (c *checker) shortfallHint(deficit, available, dataSize uint64, measured string, origin sizeOrigin) string {
	hint := fmt.Sprintf("free at least %s on the volume holding %s, or point ANDROID_AVD_HOME at a larger volume",
		formatMiB(deficit), measured)
	// Offered only when lowering the size could close the gap on its own: the
	// emulator raises anything below 6G straight back, so at or under the floor
	// the suggestion is futile, and below the floor's own requirement there is
	// no size that would fit either.
	if (origin == sizeConfigured || origin == sizeCapped) &&
		dataSize > emulatorDataPartitionFloor &&
		available >= requiredBytes(emulatorDataPartitionFloor) {
		// 6G rather than a rendered size: this is a value to type into config.ini.
		hint += ", or lower disk.dataPartition.size in that AVD's config.ini to no less than 6G, the emulator's minimum"
	}
	return hint + ", then rerun " + doctorCommand(c.options.AVD)
}

// addUnreadableAVD reports metadata this check could not parse. The AVD content
// directory is still known, so free space is reported alongside it rather than
// discarded.
func (c *checker) addUnreadableAVD(disk avdDiskInfo, err error) {
	detail := fmt.Sprintf("AVD %q could not be read, so no userdata partition size was compared: %s",
		c.options.AVD, compact(err.Error()))
	if disk.Directory != "" {
		if available, measured, measureErr := c.measureAvailable(disk.Directory); measureErr == nil {
			detail += fmt.Sprintf("; %s has %s free", measured, formatGiB(available))
		}
	}
	c.add(Result{
		Severity: Warning,
		Name:     "disk space",
		Detail:   detail,
		Hint:     "fix that AVD's config.ini, or provision a replacement with adbtest.EnsureAVD under a new name, then rerun " + doctorCommand(c.options.AVD),
	})
}

// checkDiskSpaceWithoutAVD reports free space against the smallest requirement
// any AVD can have. Without a named AVD the check cannot know which one will be
// booted, so it never fails - but it must not stay silent either, because a
// runner that has not created its AVD yet is exactly the situation this check
// exists for.
func (c *checker) checkDiskSpaceWithoutAVD() {
	available, measured, err := c.measureAvailable(c.avdHomes[0])
	if err != nil {
		c.addMeasurementFailure(c.avdHomes[0], err)
		return
	}
	provisioned := requiredBytes(avdmanagerDataPartitionDefault)
	if floor := requiredBytes(emulatorDataPartitionFloor); available < floor {
		c.add(Result{
			Severity: Warning,
			Name:     "disk space",
			Detail: fmt.Sprintf("no AVD was selected, so no userdata partition size was compared, but %s has %s free, below the %s the smallest partition the emulator will create needs, so no AVD can be created here",
				measured, formatMiB(available), formatMiB(floor)),
			Hint: fmt.Sprintf("free space on the volume holding %s or point ANDROID_AVD_HOME at a larger volume; an AVD provisioned by EnsureAVD needs %s",
				measured, formatGiB(provisioned)),
		})
		return
	}
	c.add(Result{
		Severity: Information,
		Name:     "disk space",
		Detail: fmt.Sprintf("no AVD was selected, so no userdata partition size was compared; %s has %s free",
			measured, formatGiB(available)),
		Hint: fmt.Sprintf("an AVD provisioned by EnsureAVD needs %s; rerun with --avd NAME to compare against a specific AVD's disk.dataPartition.size",
			formatGiB(provisioned)),
	})
}

// measureAvailable bounds the measurement the way every other external step in
// this package is bounded. statfs does not observe a context, and a hung NFS or
// autofs home would otherwise make adbtest doctor unkillable. It returns the
// path actually measured, which differs from the requested one when the
// requested path does not exist yet.
func (c *checker) measureAvailable(path string) (available uint64, measured string, err error) {
	type measurement struct {
		bytes uint64
		path  string
		err   error
	}
	results := make(chan measurement, 1)
	go func() {
		bytes, measuredPath, measureErr := c.deps.availableDiskBytes(path)
		results <- measurement{bytes: bytes, path: measuredPath, err: measureErr}
	}()
	timer := time.NewTimer(c.deps.commandTimeout)
	defer timer.Stop()
	select {
	case result := <-results:
		return result.bytes, result.path, result.err
	case <-timer.C:
		return 0, path, fmt.Errorf("measuring free space on %q did not finish within %s", path, c.deps.commandTimeout)
	case <-c.ctx.Done():
		return 0, path, c.ctx.Err()
	}
}

func (c *checker) addMeasurementFailure(path string, err error) {
	if errors.Is(err, errors.ErrUnsupported) {
		c.add(Result{
			Severity: Information,
			Name:     "disk space",
			Detail:   fmt.Sprintf("free space on %s cannot be measured on this host", path),
		})
		return
	}
	command := "adbtest doctor"
	if c.options.AVD != "" {
		command = doctorCommand(c.options.AVD)
	}
	c.add(Result{
		Severity: Warning,
		Name:     "disk space",
		Detail:   fmt.Sprintf("free space on %s could not be measured: %s", path, compact(err.Error())),
		Hint:     fmt.Sprintf("check that %s is on a mounted, responsive filesystem, then rerun %s", path, command),
	})
}

// describeOrigin states an assumption the requirement rests on, in the same
// unit as the message carrying it.
func describeOrigin(origin sizeOrigin, format func(uint64) string) string {
	switch origin {
	case sizeDefaulted:
		return fmt.Sprintf(" (assuming the emulator's %s default, which that AVD does not set)", format(emulatorDataPartitionFloor))
	case sizeUnparseable:
		return fmt.Sprintf(" (that AVD's disk.dataPartition.size is not a size the emulator accepts, so it uses its %s default)", format(emulatorDataPartitionFloor))
	case sizeRaised:
		return fmt.Sprintf(" (the emulator raises that AVD's smaller configured size to its %s minimum)", format(emulatorDataPartitionFloor))
	case sizeCapped:
		return " (that AVD configures an implausibly large size; comparing against the largest this check models)"
	default: // sizeConfigured
		return ""
	}
}

// derivedSizeIsUncertain reports whether the AVD targets an API level where the
// emulator's 6 GiB minimum is feature-flagged rather than unconditional. Both a
// raised size and an absent one come out of that same clamp, so below API 24
// neither requirement can be relied on.
func derivedSizeIsUncertain(values map[string]string) bool {
	target := strings.TrimPrefix(strings.TrimSpace(values["target"]), "android-")
	level, err := strconv.Atoi(target)
	return err == nil && level < 24
}

func doctorCommand(avd string) string {
	return fmt.Sprintf("adbtest doctor --avd %s", avd)
}
