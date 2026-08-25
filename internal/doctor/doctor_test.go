package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

func TestCheckRunsReadOnlyChecksSequentially(t *testing.T) {
	deps, commands := healthyDependencies()

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want 0; results: %+v", report.ExitCode(), report.Results)
	}

	wantCommands := []string{
		"/tools/go version",
		"/sdk/adb version",
		"/sdk/emulator -version",
		"/sdk/sdkmanager --version",
		"/sdk/avdmanager list device -c",
		"/sdk/emulator -accel-check",
		"/sdk/emulator -list-avds",
	}
	if strings.Join(*commands, "\n") != strings.Join(wantCommands, "\n") {
		t.Fatalf("commands =\n%s\nwant\n%s", strings.Join(*commands, "\n"), strings.Join(wantCommands, "\n"))
	}
	if result := findResult(t, report, "AVDs"); result.Severity != Information || !strings.Contains(result.Detail, "EnsureAVD") {
		t.Fatalf("AVDs result = %+v, want informational empty inventory", result)
	}
	if result := findResult(t, report, "acceleration"); result.Severity != OK || !strings.Contains(result.Detail, "KVM") {
		t.Fatalf("acceleration result = %+v", result)
	}
}

func TestCheckKeepsSuccessfulToolDiagnosticsAsWarnings(t *testing.T) {
	deps, _ := healthyDependencies()
	baseRun := deps.run
	deps.run = func(
		ctx context.Context,
		path string,
		args []string,
		stdin io.Reader,
		progress io.Writer,
	) (androidsdk.CommandResult, error) {
		result, err := baseRun(ctx, path, args, stdin, progress)
		switch path {
		case "/sdk/sdkmanager":
			result.Stderr = "sdkmanager is deprecated"
		case "/sdk/avdmanager":
			result.Stderr = "Error: unrelated image metadata is incomplete"
		}
		return result, err
	}
	deps.findTool = func(root, name string) (string, error) {
		if name == "aapt" {
			return "", errors.New("aapt missing")
		}
		return "/sdk/" + name, nil
	}

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	if report.ExitCode() != 0 {
		t.Fatalf("ExitCode() = %d, want warnings to remain successful", report.ExitCode())
	}
	for _, name := range []string{"sdkmanager", "avdmanager", "aapt"} {
		if result := findResult(t, report, name); result.Severity != Warning {
			t.Fatalf("%s result = %+v, want warning", name, result)
		}
	}
}

func TestCheckReportsMissingRequiredToolWithoutDependentCommands(t *testing.T) {
	deps, commands := healthyDependencies()
	deps.findTool = func(root, name string) (string, error) {
		if name == "emulator" {
			return "", errors.New("emulator not found")
		}
		return "/sdk/" + name, nil
	}

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
	if result := findResult(t, report, "emulator"); result.Severity != Failure {
		t.Fatalf("emulator result = %+v", result)
	}
	if result := findResult(t, report, "acceleration"); result.Severity != Information {
		t.Fatalf("acceleration result = %+v, want skipped information", result)
	}
	for _, command := range *commands {
		if strings.Contains(command, "-accel-check") || strings.Contains(command, "-list-avds") {
			t.Fatalf("dependent emulator command was run: %s", command)
		}
	}
}

func TestCheckRequiresStableSDKEnvironment(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.getenv = func(string) string { return "" }

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	result := findResult(t, report, "Android SDK")
	if result.Severity != Failure || !strings.Contains(result.Hint, "ANDROID_HOME") {
		t.Fatalf("Android SDK result = %+v, want stable-root failure", result)
	}
}

func TestCheckRejectsAmbiguousAVDHome(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.avdHomes = func() ([]string, error) {
		return nil, errors.New("ANDROID_USER_HOME is set; set ANDROID_AVD_HOME explicitly")
	}

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	result := findResult(t, report, "AVD home")
	if result.Severity != Failure || !strings.Contains(result.Hint, "ANDROID_AVD_HOME") {
		t.Fatalf("AVD home result = %+v, want coherence failure", result)
	}
}

func TestCheckTimesOutHungCommand(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.commandTimeout = 5 * time.Millisecond
	baseRun := deps.run
	deps.run = func(
		ctx context.Context,
		path string,
		args []string,
		stdin io.Reader,
		progress io.Writer,
	) (androidsdk.CommandResult, error) {
		if path == "/tools/go" {
			<-ctx.Done()
			return androidsdk.CommandResult{}, ctx.Err()
		}
		return baseRun(ctx, path, args, stdin, progress)
	}

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	result := findResult(t, report, "Go")
	if result.Severity != Failure || !strings.Contains(result.Detail, "timed out after 5ms") {
		t.Fatalf("Go result = %+v, want bounded timeout", result)
	}
}

func TestCheckRequiresRequestedHardwareProfileAndAVD(t *testing.T) {
	deps, _ := healthyDependencies()
	report, err := checkWithDependencies(context.Background(), Options{
		DeviceProfile: "missing_phone",
		AVD:           "expected_avd",
	}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	if result := findResult(t, report, "avdmanager"); result.Severity != Failure || !strings.Contains(result.Detail, "missing_phone") {
		t.Fatalf("avdmanager result = %+v", result)
	}
	if result := findResult(t, report, "AVDs"); result.Severity != Failure || !strings.Contains(result.Detail, "expected_avd") {
		t.Fatalf("AVDs result = %+v", result)
	}
}

func TestCheckAcceptsExpectedAVD(t *testing.T) {
	deps, _ := healthyDependencies()
	baseRun := deps.run
	deps.run = func(
		ctx context.Context,
		path string,
		args []string,
		stdin io.Reader,
		progress io.Writer,
	) (androidsdk.CommandResult, error) {
		if path == "/sdk/emulator" && strings.Join(args, " ") == "-list-avds" {
			return androidsdk.CommandResult{Stdout: "other\r\nexpected_avd\r\n"}, nil
		}
		return baseRun(ctx, path, args, stdin, progress)
	}

	report, err := checkWithDependencies(context.Background(), Options{AVD: "expected_avd"}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	if result := findResult(t, report, "AVDs"); result.Severity != OK {
		t.Fatalf("AVDs result = %+v, want OK", result)
	}
}

func TestCheckRejectsOldGoAndUnsupportedHost(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.goarch = "riscv64"
	baseRun := deps.run
	deps.run = func(
		ctx context.Context,
		path string,
		args []string,
		stdin io.Reader,
		progress io.Writer,
	) (androidsdk.CommandResult, error) {
		if path == "/tools/go" {
			return androidsdk.CommandResult{Stdout: "go version go1.22.9 linux/amd64"}, nil
		}
		return baseRun(ctx, path, args, stdin, progress)
	}

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	if result := findResult(t, report, "Go"); result.Severity != Failure || !strings.Contains(result.Detail, "older") {
		t.Fatalf("Go result = %+v", result)
	}
	if result := findResult(t, report, "host"); result.Severity != Failure || !strings.Contains(result.Detail, "riscv64") {
		t.Fatalf("host result = %+v", result)
	}
}

func TestCheckRejectsUnsupportedLinuxARMHost(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.goarch = "arm64"

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	result := findResult(t, report, "host")
	if result.Severity != Failure || !strings.Contains(result.Detail, "linux/arm64") {
		t.Fatalf("host result = %+v, want unsupported Linux ARM", result)
	}
}

func TestCheckRejectsWindowsWithoutRunningAndroidBatchTools(t *testing.T) {
	deps, commands := healthyDependencies()
	deps.goos = "windows"

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	result := findResult(t, report, "host")
	if result.Severity != Failure || !strings.Contains(result.Detail, "Linux and macOS") {
		t.Fatalf("host result = %+v, want unsupported Windows provisioning", result)
	}
	if got := strings.Join(*commands, "\n"); got != "/tools/go version" {
		t.Fatalf("commands = %q, want no Android batch-tool execution", got)
	}
}

func TestCheckUsesConfiguredAAPT(t *testing.T) {
	deps, _ := healthyDependencies()
	deps.getenv = func(name string) string {
		switch name {
		case "ANDROID_HOME":
			return "/sdk"
		case "AAPT":
			return "/custom tools/aapt"
		default:
			return ""
		}
	}
	deps.lookPath = func(name string) (string, error) {
		if name == "go" {
			return "/tools/go", nil
		}
		if name == "/custom tools/aapt" {
			return name, nil
		}
		return "", fmt.Errorf("unexpected lookup %q", name)
	}

	report, err := checkWithDependencies(context.Background(), Options{}, deps)
	if err != nil {
		t.Fatalf("checkWithDependencies() error: %v", err)
	}
	if result := findResult(t, report, "aapt"); result.Severity != OK || !strings.Contains(result.Detail, "custom tools") {
		t.Fatalf("aapt result = %+v", result)
	}
}

func TestCheckValidatesOptionsAndDependencies(t *testing.T) {
	deps, _ := healthyDependencies()
	if _, err := checkWithDependencies(context.Background(), Options{AVD: "bad\nname"}, deps); err == nil {
		t.Fatal("checkWithDependencies() accepted a control character")
	}
	if _, err := checkWithDependencies(nil, Options{}, deps); err == nil { //nolint:staticcheck // Nil-boundary behavior is intentional.
		t.Fatal("checkWithDependencies() accepted a nil context")
	}
	deps.run = nil
	if _, err := checkWithDependencies(context.Background(), Options{}, deps); err == nil {
		t.Fatal("checkWithDependencies() accepted incomplete dependencies")
	}
}

func TestReportWriteAndExitCode(t *testing.T) {
	report := Report{Results: []Result{
		{Severity: OK, Name: "one", Detail: "passed"},
		{Severity: Warning, Name: "two", Detail: "warning", Hint: "fix\nthis"},
		{Severity: Failure, Name: "three", Detail: "failed"},
		{Severity: Information, Name: "four", Detail: "neutral"},
	}}
	var output bytes.Buffer
	if err := report.Write(&output); err != nil {
		t.Fatalf("Write() error: %v", err)
	}
	if report.ExitCode() != 1 {
		t.Fatalf("ExitCode() = %d, want 1", report.ExitCode())
	}
	wantFragments := []string{
		"go-adbtest doctor",
		"[OK  ] one: passed",
		"[WARN] two: warning",
		"hint: fix this",
		"[FAIL] three: failed",
		"[INFO] four: neutral",
		"Not ready: 1 checks passed, 1 warnings, 1 failed.",
	}
	for _, fragment := range wantFragments {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("output %q does not contain %q", output.String(), fragment)
		}
	}
}

//nolint:gocritic // The pointer exposes the command log accumulated by the fake.
func healthyDependencies() (dependencies, *[]string) {
	commands := make([]string, 0, 8)
	deps := dependencies{
		goos:   "linux",
		goarch: "amd64",
		getenv: func(name string) string {
			if name == "ANDROID_HOME" {
				return "/sdk"
			}
			return ""
		},
		lookPath: func(name string) (string, error) {
			if name == "go" {
				return "/tools/go", nil
			}
			return "", fmt.Errorf("unexpected lookup %q", name)
		},
		resolveSDKRoot: func(string) (string, error) { return "/sdk", nil },
		avdHomes:       func() ([]string, error) { return []string{"/user/.android/avd"}, nil },
		findTool: func(_ string, name string) (string, error) {
			return "/sdk/" + name, nil
		},
		// Comfortably above any requirement so the disk check is inert unless a
		// test overrides it deliberately.
		availableDiskBytes: func(string) (uint64, error) { return 512 << 30, nil },
		avdDisk: func(string, []string) (avdDiskInfo, bool, error) {
			return avdDiskInfo{}, false, nil
		},
		commandTimeout: 100 * time.Millisecond,
	}
	deps.run = func(
		_ context.Context,
		path string,
		args []string,
		_ io.Reader,
		_ io.Writer,
	) (androidsdk.CommandResult, error) {
		commands = append(commands, strings.TrimSpace(path+" "+strings.Join(args, " ")))
		key := path + " " + strings.Join(args, " ")
		switch key {
		case "/tools/go version":
			return androidsdk.CommandResult{Stdout: "go version go1.23.9 linux/amd64"}, nil
		case "/sdk/adb version":
			return androidsdk.CommandResult{Stdout: "Android Debug Bridge version 1.0.41"}, nil
		case "/sdk/emulator -version":
			return androidsdk.CommandResult{Stdout: "Android emulator version 36.4.9"}, nil
		case "/sdk/sdkmanager --version":
			return androidsdk.CommandResult{Stdout: "22.0"}, nil
		case "/sdk/avdmanager list device -c":
			return androidsdk.CommandResult{Stdout: "medium_phone\r\nsmall_phone\r\n"}, nil
		case "/sdk/emulator -accel-check":
			return androidsdk.CommandResult{Stdout: "accel:\n0\nKVM is installed and usable."}, nil
		case "/sdk/emulator -list-avds":
			return androidsdk.CommandResult{}, nil
		default:
			return androidsdk.CommandResult{}, fmt.Errorf("unexpected command %q", key)
		}
	}
	return deps, &commands
}

func findResult(t *testing.T, report Report, name string) Result {
	t.Helper()
	for _, result := range report.Results {
		if result.Name == name {
			return result
		}
	}
	t.Fatalf("result %q not found in %+v", name, report.Results)
	return Result{}
}
