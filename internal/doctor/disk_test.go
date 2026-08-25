package doctor

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

// The figures below are the real ones from the incident this check exists for.
// A stock AVD leaves disk.dataPartition.size at the emulator's 6 GiB default,
// so the emulator demanded 7372.80 MiB against 6634.49 MiB free and refused to
// create the partition. Anything that changes these numbers changes whether
// doctor and the emulator agree, which is the whole value of the check.
const (
	incidentDataPartition = "6442450944" // 6 GiB, the emulator's default
	incidentRequired      = 7730941132   // 7372.80 MiB
	incidentAvailable     = 6956766986   // 6634.49 MiB
	incidentComfortable   = incidentRequired + diskHeadroom
	avdDirectory          = "/user/.android/avd/expected_avd.avd"
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
	deps.availableDiskBytes = func(path string) (uint64, string, error) { return available, path, nil }
	deps.avdDisk = func(string, []string) (avdDiskInfo, bool, error) { return disk, true, nil }
	return deps, commands
}

func coldAVD(size string) avdDiskInfo {
	values := map[string]string{"AvdId": "expected_avd"}
	if size != "" {
		values["disk.dataPartition.size"] = size
	}
	return avdDiskInfo{Values: values, Directory: avdDirectory}
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

func diskResult(t *testing.T, options Options, disk avdDiskInfo, available uint64) Result {
	t.Helper()
	deps, _ := diskDependencies("expected_avd", disk, available)
	return findResult(t, runDiskCheck(t, options, deps), "disk space")
}

func TestCheckDiskSpaceFailsWhenAColdAVDCannotCreateItsUserdataPartition(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", coldAVD(incidentDataPartition), incidentAvailable)

	report := runDiskCheck(t, Options{AVD: "expected_avd"}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != Failure {
		t.Fatalf("disk space result = %+v, want Failure", result)
	}
	for _, want := range []string{"7372.80 MiB", "6634.49 MiB", "expected_avd", avdDirectory} {
		if !strings.Contains(result.Detail, want) {
			t.Fatalf("detail %q does not name %q", result.Detail, want)
		}
	}
	if !strings.Contains(result.Hint, "738.31 MiB") {
		t.Fatalf("hint %q does not quantify the deficit", result.Hint)
	}
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
}

// The severity ladder is the part of this check a wrong edit would break most
// quietly, so each threshold is pinned from both sides.
func TestCheckDiskSpaceSeverityBoundaries(t *testing.T) {
	tests := []struct {
		name      string
		available uint64
		want      Severity
	}{
		{"one byte below the emulator's requirement", incidentRequired - 1, Failure},
		{"exactly the emulator's requirement", incidentRequired, Warning},
		{"one byte below the headroom band", incidentComfortable - 1, Warning},
		{"exactly the headroom band", incidentComfortable, OK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := diskResult(t, Options{AVD: "expected_avd"}, coldAVD(incidentDataPartition), test.available)
			if result.Severity != test.want {
				t.Fatalf("severity at %d = %v, want %v (%+v)", test.available, result.Severity, test.want, result)
			}
		})
	}
}

func TestCheckDiskSpaceQuantifiesTheHeadroomBandExactly(t *testing.T) {
	// 128 MiB into the 256 MiB band: 128 MiB spare, 128 MiB to free.
	result := diskResult(t, Options{AVD: "expected_avd"}, coldAVD(incidentDataPartition), incidentRequired+(128<<20))

	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning", result)
	}
	if !strings.Contains(result.Detail, "leaving only 128.00 MiB spare") {
		t.Fatalf("detail %q does not state the remaining space exactly", result.Detail)
	}
	if !strings.Contains(result.Hint, "free another 128.00 MiB") {
		t.Fatalf("hint %q does not state the shortfall exactly", result.Hint)
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
	if !strings.Contains(result.Detail, "already has a userdata partition") {
		t.Fatalf("detail %q does not explain why this is not a failure", result.Detail)
	}
	if !strings.Contains(result.Hint, "wipe") {
		t.Fatalf("hint %q does not mention that a wipe recreates the partition", result.Hint)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0: the emulator will not recheck a warm AVD", report.ExitCode())
	}
}

// A warm AVD is capped at Warning even inside the headroom band, where a cold
// one would also warn - but for a different reason and with different advice.
func TestCheckDiskSpaceCreatedBranchTakesPrecedenceOverTheHeadroomBand(t *testing.T) {
	disk := coldAVD(incidentDataPartition)
	disk.Created = true

	result := diskResult(t, Options{AVD: "expected_avd"}, disk, incidentRequired+(128<<20))
	if result.Severity != Warning || !strings.Contains(result.Detail, "already has a userdata partition") {
		t.Fatalf("disk space result = %+v, want the created-partition warning", result)
	}
}

func TestCheckDiskSpaceAcceptsAmpleFreeSpace(t *testing.T) {
	result := diskResult(t, Options{AVD: "expected_avd"}, coldAVD(incidentDataPartition), 512<<30)
	if result.Severity != OK {
		t.Fatalf("disk space result = %+v, want OK", result)
	}
	if result.Hint != "" {
		t.Fatalf("OK result carries a hint: %q", result.Hint)
	}
}

func TestCheckDiskSpaceStatesWhereTheRequirementCameFrom(t *testing.T) {
	tests := []struct {
		name string
		size string
		want string
	}{
		{"unset", "", "assuming the emulator's 6144.00 MiB default, which that AVD does not set"},
		{"below the minimum", "2G", "raises that AVD's smaller configured size"},
		{"not a size the emulator accepts", "7 G", "is not a size the emulator accepts"},
		{"implausibly large", "4000000000G", "implausibly large size"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := diskResult(t, Options{AVD: "expected_avd"}, coldAVD(test.size), 1<<20)
			if result.Severity != Failure {
				t.Fatalf("disk space result = %+v, want Failure", result)
			}
			if !strings.Contains(result.Detail, test.want) {
				t.Fatalf("detail %q does not contain %q", result.Detail, test.want)
			}
		})
	}
}

// Lowering disk.dataPartition.size only helps when the AVD configures a size
// above the emulator's minimum. Suggesting it anywhere else sends a consumer -
// especially an automated one - round a loop that can never terminate.
func TestCheckDiskSpaceOffersOnlyRemediesThatCanWork(t *testing.T) {
	futile := []string{"", "2G", "banana"}
	for _, size := range futile {
		t.Run("futile/"+size, func(t *testing.T) {
			result := diskResult(t, Options{AVD: "expected_avd"}, coldAVD(size), 1<<20)
			if strings.Contains(result.Hint, "lower disk.dataPartition.size") {
				t.Fatalf("hint %q tells the reader to lower a size the emulator would raise straight back", result.Hint)
			}
		})
	}
	result := diskResult(t, Options{AVD: "expected_avd"}, coldAVD("20G"), 1<<20)
	if !strings.Contains(result.Hint, "lower disk.dataPartition.size") {
		t.Fatalf("hint %q omits the one remedy that would work here", result.Hint)
	}
}

// This output is designed to be read by an automated disk-freeing step, so no
// hint may name an action that step could carry out destructively.
func TestDiskHintsNeverNameADestructiveAction(t *testing.T) {
	destructive := []string{"rm ", "rm -", "delete", "remove", "wiping", "recreating"}
	cases := []struct {
		name    string
		options Options
		disk    avdDiskInfo
		free    uint64
	}{
		{"cold shortfall", Options{AVD: "expected_avd"}, coldAVD(incidentDataPartition), incidentAvailable},
		{"cold shortfall, unset size", Options{AVD: "expected_avd"}, coldAVD(""), 1 << 20},
		{"headroom band", Options{AVD: "expected_avd"}, coldAVD(incidentDataPartition), incidentRequired + 1},
		{"ample", Options{AVD: "expected_avd"}, coldAVD(incidentDataPartition), 512 << 30},
		{"no AVD selected", Options{}, avdDiskInfo{}, incidentAvailable},
		{"no AVD selected, ample", Options{}, avdDiskInfo{}, 512 << 30},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			deps, _ := diskDependencies("expected_avd", test.disk, test.free)
			for _, result := range runDiskCheck(t, test.options, deps).Results {
				for _, verb := range destructive {
					if strings.Contains(strings.ToLower(result.Hint), verb) {
						t.Fatalf("hint %q names the destructive action %q", result.Hint, verb)
					}
				}
			}
		})
	}
	// The warm-AVD hint must mention a wipe to explain when the check reapplies,
	// but only as a condition, never as an instruction.
	warm := coldAVD(incidentDataPartition)
	warm.Created = true
	hint := diskResult(t, Options{AVD: "expected_avd"}, warm, incidentAvailable).Hint
	if !strings.Contains(hint, "no action is needed") {
		t.Fatalf("hint %q does not open by saying no action is needed", hint)
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

func TestCheckDiskSpaceReportsFreeSpaceEvenWhenTheAVDCannotBeRead(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", avdDiskInfo{}, 512<<30)
	deps.avdDisk = func(string, []string) (avdDiskInfo, bool, error) {
		return avdDiskInfo{Directory: avdDirectory}, false, errors.New("AVD config is not a regular file")
	}

	result := findResult(t, runDiskCheck(t, Options{AVD: "expected_avd"}, deps), "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning for unreadable AVD metadata", result)
	}
	if !strings.Contains(result.Detail, avdDirectory) || !strings.Contains(result.Detail, "free") {
		t.Fatalf("detail %q discards the directory the check could still measure", result.Detail)
	}
}

func TestCheckDiskSpaceWithoutAnAVDWarnsBelowTheSmallestPossibleRequirement(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(path string) (uint64, string, error) { return incidentAvailable, path, nil }

	report := runDiskCheck(t, Options{}, deps)

	result := findResult(t, report, "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning", result)
	}
	for _, want := range []string{"no AVD was selected", "7372.80 MiB", "no AVD can be created here"} {
		if !strings.Contains(result.Detail, want) {
			t.Fatalf("detail %q does not contain %q", result.Detail, want)
		}
	}
	if !strings.Contains(result.Hint, "12.0 GiB") {
		t.Fatalf("hint %q does not state what an EnsureAVD-provisioned AVD needs", result.Hint)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0 when no AVD was named", report.ExitCode())
	}
}

// A runner with more than the 7.2 GiB floor but less than the 12 GiB an
// EnsureAVD-provisioned AVD needs must still be told the larger number, or it
// reads a clean report and then fails to boot.
func TestCheckDiskSpaceWithoutAnAVDNamesTheProvisionedRequirement(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(path string) (uint64, string, error) { return 9 << 30, path, nil }

	result := findResult(t, runDiskCheck(t, Options{}, deps), "disk space")
	if result.Severity != Information {
		t.Fatalf("disk space result = %+v, want Information", result)
	}
	if !strings.Contains(result.Hint, "12.0 GiB") {
		t.Fatalf("hint %q leaves a host between the two requirements uninformed", result.Hint)
	}
}

func TestCheckDiskSpaceNamesThePathItActuallyMeasured(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(string) (uint64, string, error) {
		// The AVD home does not exist yet, so an ancestor was measured.
		return 512 << 30, "/user", nil
	}

	result := findResult(t, runDiskCheck(t, Options{}, deps), "disk space")
	if !strings.Contains(result.Detail, "/user has") {
		t.Fatalf("detail %q names a path other than the one measured", result.Detail)
	}
}

func TestCheckDiskSpaceReportsUnsupportedMeasurementAsInformation(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(path string) (uint64, string, error) {
		return 0, path, errors.ErrUnsupported
	}

	result := findResult(t, runDiskCheck(t, Options{}, deps), "disk space")
	if result.Severity != Information {
		t.Fatalf("disk space result = %+v, want Information on an unsupported host", result)
	}
}

func TestCheckDiskSpaceWarnsWhenMeasurementFails(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(path string) (uint64, string, error) {
		return 0, path, errors.New("statfs: permission denied")
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

func TestCheckDiskSpaceMeasurementFailureKeepsTheSelectedAVD(t *testing.T) {
	deps, _ := diskDependencies("expected_avd", coldAVD(incidentDataPartition), 0)
	deps.availableDiskBytes = func(path string) (uint64, string, error) {
		return 0, path, errors.New("statfs: input/output error")
	}

	result := findResult(t, runDiskCheck(t, Options{AVD: "expected_avd"}, deps), "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning", result)
	}
	if !strings.Contains(result.Hint, "--avd expected_avd") {
		t.Fatalf("hint %q tells a --avd user to rerun without it", result.Hint)
	}
}

func TestCheckDiskSpaceBoundsAHungMeasurement(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	deps, _ := healthyDependencies()
	deps.availableDiskBytes = func(path string) (uint64, string, error) {
		<-release
		return 0, path, nil
	}

	result := findResult(t, runDiskCheck(t, Options{}, deps), "disk space")
	if result.Severity != Warning || !strings.Contains(result.Detail, "did not finish within") {
		t.Fatalf("disk space result = %+v, want a bounded measurement failure", result)
	}
}

// The context case is why measureAvailable exists at all: statfs does not
// observe one, so without it Ctrl-C cannot interrupt a wedged mount.
func TestCheckDiskSpaceObservesACancelledContext(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	deps, _ := healthyDependencies()
	deps.commandTimeout = time.Minute
	deps.availableDiskBytes = func(path string) (uint64, string, error) {
		<-release
		return 0, path, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	report, err := checkWithDependencies(ctx, Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	result := findResult(t, report, "disk space")
	if result.Severity != Warning {
		t.Fatalf("disk space result = %+v, want Warning once the context is cancelled", result)
	}
}

func TestCheckDiskSpaceIsNotReportedOnUnsupportedHosts(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.goos = "windows"

	for _, result := range runDiskCheck(t, Options{}, deps).Results {
		if result.Name == "disk space" {
			t.Fatalf("disk space was checked on an unsupported host: %+v", result)
		}
	}
}

func TestCheckDiskSpaceIsInertWithoutAnAVDHome(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.avdHomes = func() ([]string, error) { return nil, errors.New("no AVD home") }
	deps.availableDiskBytes = func(path string) (uint64, string, error) {
		t.Error("disk was measured without a resolved AVD home")
		return 0, path, nil
	}

	result := findResult(t, runDiskCheck(t, Options{}, deps), "disk space")
	if result.Severity != Information || !strings.Contains(result.Detail, "not checked") {
		t.Fatalf("disk space result = %+v, want an Information observation", result)
	}
}

func TestCheckDiskSpaceRunsNoSubprocess(t *testing.T) {
	deps, commands := healthyDependencies()
	runDiskCheck(t, Options{}, deps)
	for _, command := range *commands {
		if strings.Contains(command, "df") || strings.Contains(command, " stat") {
			t.Fatalf("disk check shelled out: %q", command)
		}
	}
}

// A seam field wired into production but not validated - or the reverse - would
// fail every real run while leaving the suite green, so this asserts the
// property rather than an enumerated list of field names.
func TestEveryDependencyIsWiredAndValidated(t *testing.T) {
	production := reflect.ValueOf(productionDependencies())
	funcFields := 0
	for index := 0; index < production.NumField(); index++ {
		field := production.Type().Field(index)
		if field.Type.Kind() != reflect.Func {
			continue
		}
		funcFields++
		if production.Field(index).IsNil() {
			t.Errorf("productionDependencies() leaves %s nil", field.Name)
		}
	}
	if funcFields == 0 {
		t.Fatal("no func dependencies found; this test would silently pass forever")
	}
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
		{"seven gigabytes", 7 << 30, "8601.60 MiB"},
		{"just above the floor", 6145 << 20, "7374.00 MiB"},
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

func TestDataPartitionBytesReportsWhereTheSizeCameFrom(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		present    bool
		wantSize   uint64
		wantOrigin sizeOrigin
	}{
		{"absent", "", false, emulatorDataPartitionFloor, sizeDefaulted},
		{"empty", "", true, emulatorDataPartitionFloor, sizeDefaulted},
		{"unparseable", "banana", true, emulatorDataPartitionFloor, sizeUnparseable},
		{"rejected by the emulator", "7 G", true, emulatorDataPartitionFloor, sizeUnparseable},
		{"zero", "0", true, emulatorDataPartitionFloor, sizeRaised},
		{"below the floor", "900000", true, emulatorDataPartitionFloor, sizeRaised},
		{"one mebibyte below the floor", "6143M", true, emulatorDataPartitionFloor, sizeRaised},
		{"at the floor", "6144M", true, 6 << 30, sizeConfigured},
		{"one mebibyte above the floor", "6145M", true, 6145 << 20, sizeConfigured},
		{"avdmanager default", "10G", true, 10 << 30, sizeConfigured},
		{"implausible", "4000000000G", true, maxDataPartitionSize, sizeCapped},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := map[string]string{}
			if test.present {
				values["disk.dataPartition.size"] = test.raw
			}
			size, origin := dataPartitionBytes(values)
			if size != test.wantSize || origin != test.wantOrigin {
				t.Fatalf("dataPartitionBytes(%q) = %d, %v, want %d, %v",
					test.raw, size, origin, test.wantSize, test.wantOrigin)
			}
		})
	}
}

// The grammar mirrors the emulator's exactly. Being more permissive than the
// emulator would under-report the requirement, which is the one direction that
// produces a false pass: the emulator falls back to 6 GiB for a value it
// rejects, but honours 7GiB, which a stricter parser would miss.
func TestParseEmulatorSizeMirrorsTheEmulatorGrammar(t *testing.T) {
	accepted := []struct {
		raw  string
		want uint64
	}{
		{"6442450944", 6 << 30},
		{"900000", 900000},
		{"0", 0},
		{"7G", 7 << 30},
		{"7g", 7 << 30},
		{"7GB", 7 << 30},
		{"7gb", 7 << 30},
		{"7GiB", 7 << 30},
		{"7gib", 7 << 30},
		{"+7G", 7 << 30},
		{" 7G", 7 << 30},
		{"07G", 7 << 30},
		{"7168M", 7168 << 20},
		{"7168m", 7168 << 20},
		{"66MB", 66 << 20},
		{"7340032K", 7340032 << 10},
		{"512K", 512 << 10},
		{"10G", 10 << 30},
	}
	for _, test := range accepted {
		t.Run("accept/"+test.raw, func(t *testing.T) {
			got, ok := parseEmulatorSize(test.raw)
			if !ok || got != test.want {
				t.Fatalf("parseEmulatorSize(%q) = %d, %v, want %d, true", test.raw, got, ok, test.want)
			}
		})
	}
	// Every one of these was measured as rejected by emulator 36.6.11.0, which
	// then falls back to its own default size.
	rejected := []string{
		"7 G", "7 GB", "7168 M", "512 MB", "7516192768B", "7T", "7.5G", "0x7",
		"", "   ", "junk", "-1", "-2G", "G", "B", "18446744073709551615G",
		"99999999999999999999",
	}
	for _, raw := range rejected {
		t.Run("reject/"+raw, func(t *testing.T) {
			if got, ok := parseEmulatorSize(raw); ok {
				t.Fatalf("parseEmulatorSize(%q) = %d, true, want rejection", raw, got)
			}
		})
	}
}

func TestAddWithoutOverflowSaturates(t *testing.T) {
	if got := addWithoutOverflow(math.MaxUint64, 1); got != math.MaxUint64 {
		t.Fatalf("addWithoutOverflow() = %d, want saturation rather than a wrapped value", got)
	}
}

func TestDescribeOriginMatchesTheUnitOfItsMessage(t *testing.T) {
	if got := describeOrigin(sizeDefaulted, formatMiB); !strings.Contains(got, "MiB") {
		t.Fatalf("describeOrigin(_, formatMiB) = %q, want a MiB figure", got)
	}
	if got := describeOrigin(sizeDefaulted, formatGiB); !strings.Contains(got, "GiB") {
		t.Fatalf("describeOrigin(_, formatGiB) = %q, want a GiB figure", got)
	}
	if got := describeOrigin(sizeConfigured, formatMiB); got != "" {
		t.Fatalf("describeOrigin(sizeConfigured, _) = %q, want no assumption stated", got)
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

	// The qcow2 overlay alone does not stop the emulator rechecking; only the
	// raw image does.
	if err := os.WriteFile(filepath.Join(directory, "userdata-qemu.img.qcow2"), []byte("overlay"), 0o644); err != nil {
		t.Fatal(err)
	}
	if disk, _, err = productionAVDDisk("go_test", []string{home}); err != nil || disk.Created {
		t.Fatalf("productionAVDDisk() = %+v, %v, want the overlay alone treated as uncreated", disk, err)
	}

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
