package doctor

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

// The figures below are the real ones from the incident this check exists for.
// A stock AVD leaves disk.dataPartition.size at the emulator's 6 GiB default,
// so the emulator demanded 7372.80 MiB against 6634.49 MiB free and refused to
// create the partition. Anything that changes these numbers changes whether
// doctor and the emulator agree, which is the whole value of the check.
const (
	incidentDataPartition = "6442450944" // 6 GiB, the emulator default
	incidentRequired      = 7730941132   // 7372.80 MiB
	incidentAvailable     = 6956766986   // 6634.49 MiB
)

func diskDependencies(avd string, disk avdDiskInfo, available uint64) (deps dependencies, commands *[]string) {
	deps, commands = healthyDependencies()
	baseRun := deps.run
	deps.run = func(
		ctx context.Context,
		path string,
		args []string,
		stdin io.Reader,
		progress io.Writer,
	) (androidsdk.CommandResult, error) {
		if path == "/sdk/emulator" && strings.Join(args, " ") == "-list-avds" {
			return androidsdk.CommandResult{Stdout: avd + "\r\n"}, nil
		}
		return baseRun(ctx, path, args, stdin, progress)
	}
	deps.availableDiskBytes = func(string) (uint64, error) { return available, nil }
	deps.avdDisk = func(string, []string) (avdDiskInfo, bool, error) { return disk, true, nil }
	return deps, commands
}

func coldAVD(size string) avdDiskInfo {
	values := map[string]string{"AvdId": "expected_avd"}
	if size != "" {
		values["disk.dataPartition.size"] = size
	}
	return avdDiskInfo{Values: values, Directory: "/user/.android/avd/expected_avd.avd"}
}

//nolint:gocritic // The value-style dependency seam keeps tests isolated.
func runDiskCheck(t *testing.T, options Options, deps dependencies) Report {
	t.Helper()
	report, err := checkWithDependencies(context.Background(), options, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	return report
}

func TestCheckDiskSpaceFailsWhenAColdAVDCannotCreateItsUserdataPartition(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", coldAVD(incidentDataPartition), incidentAvailable)

	report := runDiskCheck(t, Options{AVD: "expected_avd"}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != Failure {
		t.Fatalf("disk space result = %+v, want Failure", result)
	}
	for _, want := range []string{"7372.80 MiB", "6634.49 MiB", "expected_avd"} {
		if !strings.Contains(result.Detail, want) {
			t.Fatalf("detail %q does not name %q", result.Detail, want)
		}
	}
	if !strings.Contains(result.Hint, "738.31 MiB") {
		t.Fatalf("hint %q does not quantify the deficit", result.Hint)
	}
	if strings.Contains(result.Hint, "rm ") || strings.Contains(result.Hint, "delete") {
		t.Fatalf("hint %q names a deletion an automated consumer could act on", result.Hint)
	}
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
}

func TestCheckDiskSpaceOnlyWarnsWhenTheUserdataPartitionAlreadyExists(t *testing.T) {
	disk := coldAVD(incidentDataPartition)
	disk.Created = true
	deps, _ := diskDependencies("expected_avd", disk, incidentAvailable)

	report := runDiskCheck(t, Options{AVD: "expected_avd"}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning for an already-created partition", result)
	}
	if !strings.Contains(result.Detail, "already created") {
		t.Fatalf("detail %q does not explain why this is not a failure", result.Detail)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0: the emulator will not recheck a warm AVD", report.ExitCode())
	}
}

func TestCheckDiskSpaceWarnsOnThinHeadroom(t *testing.T) {
	// Above the emulator's own requirement, below the cache and SD card images
	// the AVD still has to allocate.
	deps, _ := diskDependencies("expected_avd", coldAVD(incidentDataPartition), incidentRequired+(100<<20))

	report := runDiskCheck(t, Options{AVD: "expected_avd"}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning", result)
	}
	if !strings.Contains(result.Detail, "spare") {
		t.Fatalf("detail %q does not quantify what is left", result.Detail)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0", report.ExitCode())
	}
}

func TestCheckDiskSpaceAcceptsAmpleFreeSpace(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", coldAVD(incidentDataPartition), 512<<30)

	report := runDiskCheck(t, Options{AVD: "expected_avd"}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != OK {
		t.Fatalf("disk space result = %+v, want OK", result)
	}
	if result.Hint != "" {
		t.Fatalf("OK result carries a hint: %q", result.Hint)
	}
}

func TestCheckDiskSpaceAssumesTheEmulatorDefaultWhenTheAVDDoesNotSetASize(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", coldAVD(""), incidentAvailable)

	report := runDiskCheck(t, Options{AVD: "expected_avd"}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != Failure {
		t.Fatalf("disk space result = %+v, want Failure", result)
	}
	if !strings.Contains(result.Detail, "assuming the emulator's 6.0 GiB default") {
		t.Fatalf("detail %q does not state the assumption it made", result.Detail)
	}
	if !strings.Contains(result.Detail, "7372.80 MiB") {
		t.Fatalf("detail %q does not reproduce the emulator's own figure", result.Detail)
	}
}

func TestCheckDiskSpaceTreatsAZeroSizeAsUnset(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", coldAVD("0"), incidentAvailable)

	result := findResult(t, runDiskCheck(t, Options{AVD: "expected_avd"}, deps), "disk space")
	if result.Severity != Failure || !strings.Contains(result.Detail, "7372.80 MiB") {
		t.Fatalf("disk space result = %+v, want the default applied rather than a zero requirement", result)
	}
}

func TestCheckDiskSpaceReportsAnAbsentAVDWithoutFailing(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", avdDiskInfo{}, 512<<30)
	deps.avdDisk = func(string, []string) (avdDiskInfo, bool, error) { return avdDiskInfo{}, false, nil }

	result := findResult(t, runDiskCheck(t, Options{AVD: "expected_avd"}, deps), "disk space")
	if result.Severity != Information || !strings.Contains(result.Detail, "not checked") {
		t.Fatalf("disk space result = %+v, want an Information observation", result)
	}
}

func TestCheckDiskSpaceWarnsWhenTheAVDCannotBeRead(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", avdDiskInfo{}, 512<<30)
	deps.avdDisk = func(string, []string) (avdDiskInfo, bool, error) {
		return avdDiskInfo{}, false, errors.New("AVD config is not a regular file")
	}

	result := findResult(t, runDiskCheck(t, Options{AVD: "expected_avd"}, deps), "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning for unreadable AVD metadata", result)
	}
}

func TestCheckDiskSpaceWithoutAnAVDWarnsBelowTheSmallestPossibleRequirement(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(string) (uint64, error) { return incidentAvailable, nil }

	report := runDiskCheck(t, Options{}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning", result)
	}
	if !strings.Contains(result.Detail, "no AVD was selected") {
		t.Fatalf("detail %q does not lead with the limitation", result.Detail)
	}
	if !strings.Contains(result.Detail, "7372.80 MiB") {
		t.Fatalf("detail %q does not name the floor", result.Detail)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0 when no AVD was named", report.ExitCode())
	}
}

func TestCheckDiskSpaceWithoutAnAVDReportsFreeSpace(t *testing.T) {
	deps, commands := healthyDependencies()

	report := runDiskCheck(t, Options{}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != Information {
		t.Fatalf("disk space result = %+v, want Information", result)
	}
	if !strings.Contains(result.Detail, "/user/.android/avd") {
		t.Fatalf("detail %q does not name the measured path", result.Detail)
	}
	for _, command := range *commands {
		if strings.Contains(command, "df") || strings.Contains(command, "stat") {
			t.Fatalf("disk check shelled out: %q", command)
		}
	}
}

func TestCheckDiskSpaceReportsUnsupportedMeasurementAsInformation(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(string) (uint64, error) {
		return 0, errors.ErrUnsupported
	}

	result := findResult(t, runDiskCheck(t, Options{}, deps), "disk space")
	if result.Severity != Information {
		t.Fatalf("disk space result = %+v, want Information on an unsupported host", result)
	}
}

func TestCheckDiskSpaceWarnsWhenMeasurementFails(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(string) (uint64, error) {
		return 0, errors.New("statfs: permission denied")
	}

	report := runDiskCheck(t, Options{}, deps)
	result := findResult(t, report, "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning", result)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0: an unmeasurable disk is not a failed disk", report.ExitCode())
	}
}

func TestCheckDiskSpaceBoundsAHungMeasurement(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(string) (uint64, error) {
		<-release
		return 0, nil
	}

	result := findResult(t, runDiskCheck(t, Options{}, deps), "disk space")
	if result.Severity != Warning || !strings.Contains(result.Detail, "did not finish within") {
		t.Fatalf("disk space result = %+v, want a bounded measurement failure", result)
	}
}

func TestCheckDiskSpaceIsNotReportedOnUnsupportedHosts(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.goos = "windows"

	report := runDiskCheck(t, Options{}, deps)

	for _, result := range report.Results {
		if result.Name == "disk space" {
			t.Fatalf("disk space was checked on an unsupported host: %+v", result)
		}
	}
}

func TestCheckDiskSpaceIsInertWithoutAnAVDHome(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.avdHomes = func() ([]string, error) { return nil, errors.New("no AVD home") }
	deps.availableDiskBytes = func(string) (uint64, error) {
		t.Fatal("disk was measured without a resolved AVD home")
		return 0, nil
	}

	result := findResult(t, runDiskCheck(t, Options{}, deps), "disk space")
	if result.Severity != Information || !strings.Contains(result.Detail, "not checked") {
		t.Fatalf("disk space result = %+v, want an Information observation", result)
	}
}

func TestProductionDependenciesSatisfyValidation(t *testing.T) {
	if err := validateDependencies(productionDependencies()); err != nil {
		t.Fatalf("productionDependencies() does not satisfy validateDependencies(): %v", err)
	}
}

func TestValidateDependenciesRejectsMissingDiskSeams(t *testing.T) {
	deps, _ := healthyDependencies()
	withoutDisk := deps
	withoutDisk.availableDiskBytes = nil
	if err := validateDependencies(withoutDisk); err == nil {
		t.Fatal("validateDependencies() accepted a nil availableDiskBytes")
	}
	withoutAVD := deps
	withoutAVD.avdDisk = nil
	if err := validateDependencies(withoutAVD); err == nil {
		t.Fatal("validateDependencies() accepted a nil avdDisk")
	}
}

func TestRequiredBytesReproducesTheEmulatorArithmetic(t *testing.T) {
	tests := []struct {
		name          string
		dataPartition uint64
		wantMiB       string
	}{
		{"emulator default", 6 << 30, "7372.80 MiB"},
		{"avdmanager default", 10 << 30, "12288.00 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatMiB(requiredBytes(test.dataPartition)); got != test.wantMiB {
				t.Fatalf("requiredBytes(%d) = %s, want %s", test.dataPartition, got, test.wantMiB)
			}
		})
	}
}

func TestRequiredBytesCannotOverflow(t *testing.T) {
	if got := requiredBytes(math.MaxUint64); got != maxDataPartitionSize*6/5 {
		t.Fatalf("requiredBytes(MaxUint64) = %d, want the capped requirement", got)
	}
}

func TestDataPartitionBytesRaisesUnsetAndUndersizedValues(t *testing.T) {
	tests := []struct {
		name           string
		raw            string
		wantSize       uint64
		wantConfigured bool
	}{
		{"absent", "", emulatorDataPartitionFloor, false},
		{"zero", "0", emulatorDataPartitionFloor, false},
		{"unparseable", "banana", emulatorDataPartitionFloor, false},
		{"below the floor", "900000", emulatorDataPartitionFloor, false},
		{"at the floor", "6442450944", 6 << 30, true},
		{"avdmanager default", "10G", 10 << 30, true},
		{"absurd", "99999999G", maxDataPartitionSize, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{}
			if test.raw != "" {
				values["disk.dataPartition.size"] = test.raw
			}
			size, configured := dataPartitionBytes(values)
			if size != test.wantSize || configured != test.wantConfigured {
				t.Fatalf("dataPartitionBytes(%q) = %d, %v, want %d, %v",
					test.raw, size, configured, test.wantSize, test.wantConfigured)
			}
		})
	}
}

func TestHeadroomBytesFollowsTheAVDConfiguration(t *testing.T) {
	stock := headroomBytes(map[string]string{
		"disk.cachePartition.size": "66MB",
		"hw.sdCard":                "yes",
		"sdcard.size":              "512 MB",
	})
	if want := uint64(66<<20 + 512<<20 + diskHeadroomSlop); stock != want {
		t.Fatalf("headroomBytes(stock) = %d, want %d", stock, want)
	}

	withoutCard := headroomBytes(map[string]string{
		"disk.cachePartition.size": "66MB",
		"hw.sdCard":                "no",
		"sdcard.size":              "512 MB",
	})
	if want := uint64(66<<20 + diskHeadroomSlop); withoutCard != want {
		t.Fatalf("headroomBytes(no sd card) = %d, want %d: sdcard.size must not be counted", withoutCard, want)
	}

	external := headroomBytes(map[string]string{
		"hw.sdCard":   "yes",
		"sdcard.size": "512 MB",
		"sdcard.path": "/elsewhere/sdcard.img",
	})
	if want := uint64(emulatorCachePartitionDefault + diskHeadroomSlop); external != want {
		t.Fatalf("headroomBytes(external sd card) = %d, want %d: an existing image is not allocated again", external, want)
	}
}

func TestParseEmulatorSizeAcceptsEveryFormAVDToolsWrite(t *testing.T) {
	tests := []struct {
		raw  string
		want uint64
		ok   bool
	}{
		{"6442450944", 6 << 30, true},
		{"900000", 900000, true},
		{"512 MB", 512 << 20, true},
		{"66MB", 66 << 20, true},
		{"600g", 600 << 30, true},
		{"600GB", 600 << 30, true},
		{"10G", 10 << 30, true},
		{"2G", 2 << 30, true},
		{"512K", 512 << 10, true},
		{"7168M", 7168 << 20, true},
		{"0", 0, true},
		{"", 0, false},
		{"   ", 0, false},
		{"junk", 0, false},
		{"-1", 0, false},
		{"-2G", 0, false},
		{"2.5G", 0, false},
		// Spacing and a trailing B are both optional, so this is accepted.
		{"2 G B", 2 << 30, true},
		{"G", 0, false},
		{"B", 0, false},
		{"99999999999999999999", 0, false},
		{"18446744073709551615G", 0, false},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			got, ok := parseEmulatorSize(test.raw)
			if ok != test.ok || (ok && got != test.want) {
				t.Fatalf("parseEmulatorSize(%q) = %d, %v, want %d, %v", test.raw, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestAddWithoutOverflowSaturates(t *testing.T) {
	if got := addWithoutOverflow(math.MaxUint64, 1); got != math.MaxUint64 {
		t.Fatalf("addWithoutOverflow() = %d, want saturation rather than a wrapped value", got)
	}
}

func TestProductionAVDDiskDetectsAnExistingUserdataPartition(t *testing.T) {
	home := t.TempDir()
	directory := filepath.Join(home, "go_test.avd")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	config := "AvdId=go_test\ndisk.dataPartition.size = " + incidentDataPartition + "\n"
	if err := os.WriteFile(filepath.Join(directory, "config.ini"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "go_test.ini"), []byte("path="+directory+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	disk, found, err := productionAVDDisk("go_test", []string{home})
	if err != nil || !found {
		t.Fatalf("productionAVDDisk() = %v, %v", found, err)
	}
	if disk.Created {
		t.Fatal("productionAVDDisk() reported a userdata partition that does not exist")
	}
	if disk.Directory != directory || disk.Values["disk.dataPartition.size"] != incidentDataPartition {
		t.Fatalf("productionAVDDisk() = %+v", disk)
	}

	// Creating the image is what stops the emulator rechecking free space.
	if err := os.WriteFile(filepath.Join(directory, "userdata-qemu.img"), []byte("image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if disk, _, err = productionAVDDisk("go_test", []string{home}); err != nil || !disk.Created {
		t.Fatalf("productionAVDDisk() = %+v, %v, want the existing partition detected", disk, err)
	}
}

func TestProductionAVDDiskReportsAnAbsentAVD(t *testing.T) {
	disk, found, err := productionAVDDisk("go_test", []string{t.TempDir()})
	if err != nil || found || disk.Created {
		t.Fatalf("productionAVDDisk() = %+v, %v, %v", disk, found, err)
	}
}
