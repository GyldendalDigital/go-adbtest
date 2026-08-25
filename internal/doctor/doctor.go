// Package doctor diagnoses the local prerequisites for go-adbtest's owned-AVD
// path without contacting a device or changing Android SDK or AVD state.
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

const (
	defaultDeviceProfile  = "small_phone"
	defaultCommandTimeout = 15 * time.Second
	minimumGoMajor        = 1
	minimumGoMinor        = 23
)

// Severity describes the effect of a diagnostic result on readiness.
type Severity uint8

const (
	// OK means a required prerequisite passed.
	OK Severity = iota
	// Warning means the path remains usable but deserves attention.
	Warning
	// Failure means the owned-AVD path is not ready.
	Failure
	// Information records a neutral observation.
	Information
)

// Result is one ordered diagnostic check.
type Result struct {
	Severity Severity
	Name     string
	Detail   string
	Hint     string
}

// Report is the complete ordered result of a doctor run.
type Report struct {
	Results []Result
}

// Options selects optional expectations for a doctor run.
type Options struct {
	// AVD, when non-empty, requires this exact existing command-line AVD ID.
	AVD string
	// DeviceProfile is the avdmanager hardware-profile ID required by
	// EnsureAVD. Empty uses small_phone.
	DeviceProfile string
}

type dependencies struct {
	goos           string
	goarch         string
	getenv         func(string) string
	lookPath       func(string) (string, error)
	resolveSDKRoot func(string) (string, error)
	findTool       func(string, string) (string, error)
	avdHomes       func() ([]string, error)
	run            func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error)
	// availableDiskBytes measures unprivileged-available space on the
	// filesystem holding a path. Injected so the disk check stays hermetic:
	// nothing in this package touches the host filesystem directly.
	availableDiskBytes func(string) (uint64, error)
	// avdDisk reads an AVD's config.ini and reports whether its userdata
	// partition already exists.
	avdDisk        func(string, []string) (avdDiskInfo, bool, error)
	commandTimeout time.Duration
}

// Check runs the read-only provisioning checks sequentially on Linux and
// macOS. It reads an AVD's config.ini and measures free space, but never
// starts an ADB
// server or emulator, contacts a device or repository, installs an SDK package,
// accepts a licence, or creates or changes an AVD.
func Check(ctx context.Context, options Options) (Report, error) {
	return checkWithDependencies(ctx, options, productionDependencies())
}

func productionDependencies() dependencies {
	return dependencies{
		goos:               runtime.GOOS,
		goarch:             runtime.GOARCH,
		getenv:             os.Getenv,
		lookPath:           exec.LookPath,
		resolveSDKRoot:     androidsdk.ResolveSDKRoot,
		findTool:           androidsdk.FindTool,
		avdHomes:           androidsdk.AVDHomes,
		run:                androidsdk.Run,
		availableDiskBytes: availableDiskBytes,
		avdDisk:            productionAVDDisk,
		commandTimeout:     defaultCommandTimeout,
	}
}

//nolint:gocritic // The value-style dependency seam keeps tests isolated.
func checkWithDependencies(ctx context.Context, options Options, deps dependencies) (Report, error) {
	if ctx == nil {
		return Report{}, errors.New("doctor: context is nil")
	}
	options, err := normalizeOptions(options)
	if err != nil {
		return Report{}, err
	}
	if err := validateDependencies(deps); err != nil {
		return Report{}, err
	}

	checker := checker{ctx: ctx, options: options, deps: deps}
	checker.checkGo()
	checker.checkSDKRoot()
	checker.checkAVDEnvironment()
	if !checker.checkHost() {
		checker.add(scopeResult())
		return checker.report, nil
	}
	checker.checkADB()
	checker.checkEmulator()
	checker.checkSDKManager()
	checker.checkAVDManager()
	checker.checkAcceleration()
	checker.checkAVDs()
	checker.checkDiskSpace()
	checker.checkAAPT()
	checker.add(scopeResult())
	return checker.report, nil
}

func scopeResult() Result {
	return Result{
		Severity: Information,
		Name:     "scope",
		Detail:   "SDK licences, network access, system-image download capacity, and WebView debugging were not probed",
		Hint:     "EnsureAVD reports licence and image-install remediation; the app must enable WebView debugging",
	}
}

func normalizeOptions(options Options) (Options, error) {
	options.AVD = strings.TrimSpace(options.AVD)
	options.DeviceProfile = strings.TrimSpace(options.DeviceProfile)
	if options.DeviceProfile == "" {
		options.DeviceProfile = defaultDeviceProfile
	}
	if containsControl(options.AVD) {
		return Options{}, errors.New("doctor: AVD name must not contain control characters")
	}
	if containsControl(options.DeviceProfile) {
		return Options{}, errors.New("doctor: device profile must not contain control characters")
	}
	return options, nil
}

//nolint:gocritic // Validation deliberately receives the complete immutable seam.
func validateDependencies(deps dependencies) error {
	if strings.TrimSpace(deps.goos) == "" || strings.TrimSpace(deps.goarch) == "" ||
		deps.getenv == nil || deps.lookPath == nil || deps.resolveSDKRoot == nil ||
		deps.findTool == nil || deps.avdHomes == nil || deps.run == nil ||
		deps.availableDiskBytes == nil || deps.avdDisk == nil {
		return errors.New("doctor: internal dependencies are incomplete")
	}
	if deps.commandTimeout <= 0 {
		return errors.New("doctor: command timeout must be positive")
	}
	return nil
}

type checker struct {
	ctx      context.Context
	options  Options
	deps     dependencies
	report   Report
	sdkRoot  string
	emulator string
	// avdHomes is cached from checkAVDEnvironment so the disk check cannot
	// measure a different home than the one already reported.
	avdHomes []string
}

func (c *checker) add(result Result) {
	c.report.Results = append(c.report.Results, result)
}

func (c *checker) checkGo() {
	path, err := c.deps.lookPath("go")
	if err != nil {
		c.add(Result{
			Severity: Failure,
			Name:     "Go",
			Detail:   "go was not found on PATH",
			Hint:     "install Go 1.23 or later and add its bin directory to PATH",
		})
		return
	}

	result, err := c.run(path, "version")
	if err != nil {
		c.add(commandFailureResult("Go", path, []string{"version"}, result, err, c.deps.commandTimeout,
			"install a working Go 1.23 or later toolchain"))
		return
	}
	major, minor, err := parseGoVersion(joinCommandOutput(result))
	if err != nil {
		c.add(Result{
			Severity: Failure,
			Name:     "Go",
			Detail:   fmt.Sprintf("%s returned an unrecognised version: %s", path, compact(joinCommandOutput(result))),
			Hint:     "install Go 1.23 or later",
		})
		return
	}
	if major < minimumGoMajor || major == minimumGoMajor && minor < minimumGoMinor {
		c.add(Result{
			Severity: Failure,
			Name:     "Go",
			Detail:   fmt.Sprintf("Go %d.%d at %s is older than the required Go 1.23", major, minor, path),
			Hint:     "upgrade Go to version 1.23 or later",
		})
		return
	}
	version := compact(result.Stdout)
	if version == "" {
		version = compact(result.Stderr)
	}
	c.add(Result{Severity: OK, Name: "Go", Detail: fmt.Sprintf("%s (%s)", version, path)})
}

func (c *checker) checkSDKRoot() {
	androidHome := strings.TrimSpace(c.deps.getenv("ANDROID_HOME"))
	sdkRootEnvironment := strings.TrimSpace(c.deps.getenv("ANDROID_SDK_ROOT"))
	root, err := c.deps.resolveSDKRoot("")
	if err != nil {
		detail := err.Error()
		hint := "set ANDROID_HOME to one Android SDK root or add that SDK's tools to PATH"
		androidHome := strings.TrimSpace(c.deps.getenv("ANDROID_HOME"))
		sdkRoot := strings.TrimSpace(c.deps.getenv("ANDROID_SDK_ROOT"))
		if androidHome != "" && sdkRoot != "" && androidHome != sdkRoot {
			hint = "make ANDROID_HOME and ANDROID_SDK_ROOT refer to the same SDK, or unset the stale variable"
		}
		c.add(Result{Severity: Failure, Name: "Android SDK", Detail: detail, Hint: hint})
		return
	}
	c.sdkRoot = root
	if androidHome == "" && sdkRootEnvironment == "" {
		c.add(Result{
			Severity: Failure,
			Name:     "Android SDK",
			Detail:   root + " was inferred from PATH, but EnsureAVD requires one stable SDK root",
			Hint:     "set ANDROID_HOME to this SDK so provisioning and Setup resolve the same tools",
		})
		return
	}
	c.add(Result{Severity: OK, Name: "Android SDK", Detail: root + " (" + sdkRootSource(c.deps.getenv) + ")"})
}

func (c *checker) checkAVDEnvironment() {
	homes, err := c.deps.avdHomes()
	if err != nil {
		c.add(Result{
			Severity: Failure,
			Name:     "AVD home",
			Detail:   err.Error(),
			Hint:     "set ANDROID_AVD_HOME to one explicit directory used by both avdmanager and emulator",
		})
		return
	}
	if len(homes) == 0 {
		c.add(Result{Severity: Failure, Name: "AVD home", Detail: "no Android AVD directory could be resolved"})
		return
	}
	c.avdHomes = homes
	c.add(Result{Severity: OK, Name: "AVD home", Detail: homes[0]})
}

func (c *checker) checkHost() bool {
	if c.deps.goos != "linux" && c.deps.goos != "darwin" {
		c.add(Result{
			Severity: Failure,
			Name:     "host",
			Detail:   fmt.Sprintf("lightweight provisioning supports Linux and macOS, not %q", c.deps.goos),
			Hint:     "use Linux CI or macOS with a native Go toolchain; Setup with a separately managed AVD is outside this doctor path",
		})
		return false
	}
	arch, err := androidsdk.NativeArch(c.deps.goarch)
	if err != nil {
		c.add(Result{Severity: Failure, Name: "host", Detail: err.Error()})
		return false
	}
	if c.deps.goarch == "arm64" && c.deps.goos != "darwin" {
		c.add(Result{
			Severity: Failure,
			Name:     "host",
			Detail:   fmt.Sprintf("the Android Emulator does not support accelerated %s/arm64 hosts", c.deps.goos),
		})
		return false
	}
	c.add(Result{
		Severity: OK,
		Name:     "host",
		Detail:   fmt.Sprintf("%s/%s uses host-native Android image architecture %s", c.deps.goos, c.deps.goarch, arch),
	})
	return true
}

func (c *checker) checkADB() {
	c.checkRequiredTool("adb", []string{"version"},
		"install Android SDK Platform-Tools in Android Studio or with sdkmanager \"platform-tools\"")
}

func (c *checker) checkEmulator() {
	path, ok := c.checkRequiredTool("emulator", []string{"-version"},
		"install Android Emulator in Android Studio SDK Tools or with sdkmanager \"emulator\"")
	if ok {
		c.emulator = path
	}
}

func (c *checker) checkSDKManager() {
	c.checkRequiredTool("sdkmanager", []string{"--version"},
		"install Android SDK Command-line Tools (latest) from Android Studio's SDK Tools tab")
}

func (c *checker) checkAVDManager() {
	path, err := c.deps.findTool(c.sdkRoot, "avdmanager")
	if err != nil {
		c.add(Result{
			Severity: Failure,
			Name:     "avdmanager",
			Detail:   err.Error(),
			Hint:     "install Android SDK Command-line Tools (latest) from Android Studio's SDK Tools tab",
		})
		return
	}
	args := []string{"list", "device", "-c"}
	result, err := c.run(path, args...)
	if err != nil {
		c.add(commandFailureResult("avdmanager", path, args, result, err, c.deps.commandTimeout,
			"repair or reinstall Android SDK Command-line Tools, then rerun avdmanager list device -c"))
		return
	}
	if !containsExactLine(result.Stdout, c.options.DeviceProfile) {
		c.add(Result{
			Severity: Failure,
			Name:     "avdmanager",
			Detail:   fmt.Sprintf("hardware profile %q is unavailable", c.options.DeviceProfile),
			Hint:     "update Command-line Tools or select an exact ID reported by avdmanager list device -c",
		})
		return
	}
	if strings.TrimSpace(result.Stderr) != "" {
		c.add(Result{
			Severity: Warning,
			Name:     "avdmanager",
			Detail:   fmt.Sprintf("profile %q is available, but the tool reported: %s", c.options.DeviceProfile, compact(result.Stderr)),
			Hint:     "rerun avdmanager list device -c directly to inspect the complete SDK diagnostics",
		})
		return
	}
	c.add(Result{
		Severity: OK,
		Name:     "avdmanager",
		Detail:   fmt.Sprintf("hardware profile %q is available (%s)", c.options.DeviceProfile, path),
	})
}

func (c *checker) checkAcceleration() {
	if c.emulator == "" {
		c.add(Result{
			Severity: Information,
			Name:     "acceleration",
			Detail:   "not checked because the emulator executable is unavailable",
		})
		return
	}
	args := []string{"-accel-check"}
	result, err := c.run(c.emulator, args...)
	if err != nil {
		c.add(commandFailureResult("acceleration", c.emulator, args, result, err, c.deps.commandTimeout,
			accelerationHint(c.deps.goos)))
		return
	}
	detail := accelerationDetail(result)
	if detail == "" {
		detail = "the Android Emulator reports that VM acceleration is usable"
	}
	c.add(Result{Severity: OK, Name: "acceleration", Detail: detail})
}

func (c *checker) checkAVDs() {
	if c.emulator == "" {
		c.add(Result{
			Severity: Information,
			Name:     "AVDs",
			Detail:   "not checked because the emulator executable is unavailable",
		})
		return
	}
	args := []string{"-list-avds"}
	result, err := c.run(c.emulator, args...)
	if err != nil {
		c.add(commandFailureResult("AVDs", c.emulator, args, result, err, c.deps.commandTimeout,
			"repair the Android Emulator installation and rerun emulator -list-avds"))
		return
	}
	avds := nonEmptyLines(result.Stdout)
	if c.options.AVD != "" {
		if !containsString(avds, c.options.AVD) {
			c.add(Result{
				Severity: Failure,
				Name:     "AVDs",
				Detail:   fmt.Sprintf("expected AVD %q was not found", c.options.AVD),
				Hint:     "create it with adbtest.EnsureAVD or choose an exact ID reported by emulator -list-avds",
			})
			return
		}
		if strings.TrimSpace(result.Stderr) != "" {
			c.add(Result{
				Severity: Warning,
				Name:     "AVDs",
				Detail:   fmt.Sprintf("AVD %q exists, but the emulator reported: %s", c.options.AVD, compact(result.Stderr)),
				Hint:     "rerun emulator -list-avds directly to inspect the complete diagnostics",
			})
			return
		}
		c.add(Result{Severity: OK, Name: "AVDs", Detail: fmt.Sprintf("expected AVD %q exists", c.options.AVD)})
		return
	}
	if strings.TrimSpace(result.Stderr) != "" {
		c.add(Result{
			Severity: Warning,
			Name:     "AVDs",
			Detail:   "the emulator listed AVDs but also reported: " + compact(result.Stderr),
			Hint:     "rerun emulator -list-avds directly to inspect the complete diagnostics",
		})
		return
	}
	if len(avds) == 0 {
		c.add(Result{
			Severity: Information,
			Name:     "AVDs",
			Detail:   "none found; EnsureAVD can create the requested lightweight AVD",
		})
		return
	}
	c.add(Result{Severity: Information, Name: "AVDs", Detail: strings.Join(avds, ", ")})
}

func (c *checker) checkAAPT() {
	if configured := strings.TrimSpace(c.deps.getenv("AAPT")); configured != "" {
		path, err := c.deps.lookPath(configured)
		if err == nil {
			c.add(Result{Severity: OK, Name: "aapt", Detail: path + " (AAPT)"})
			return
		}
		c.add(Result{
			Severity: Warning,
			Name:     "aapt",
			Detail:   fmt.Sprintf("configured AAPT %q was not found", configured),
			Hint:     "fix or unset AAPT; alternatively set Config.AppPackage to skip APK package auto-detection",
		})
		return
	}
	path, err := c.deps.findTool(c.sdkRoot, "aapt")
	if err != nil {
		c.add(Result{
			Severity: Warning,
			Name:     "aapt",
			Detail:   "optional Android build tool was not found",
			Hint:     "install Android SDK Build-Tools or set Config.AppPackage to skip APK package auto-detection",
		})
		return
	}
	c.add(Result{Severity: OK, Name: "aapt", Detail: path})
}

func (c *checker) checkRequiredTool(name string, args []string, hint string) (string, bool) {
	path, err := c.deps.findTool(c.sdkRoot, name)
	if err != nil {
		c.add(Result{Severity: Failure, Name: name, Detail: err.Error(), Hint: hint})
		return "", false
	}
	result, err := c.run(path, args...)
	if err != nil {
		c.add(commandFailureResult(name, path, args, result, err, c.deps.commandTimeout, hint))
		return "", false
	}
	detail := compact(result.Stdout)
	if detail == "" {
		detail = compact(result.Stderr)
	}
	if detail == "" {
		detail = path
	} else {
		detail += " (" + path + ")"
	}
	if strings.TrimSpace(result.Stderr) != "" && strings.TrimSpace(result.Stdout) != "" {
		c.add(Result{
			Severity: Warning,
			Name:     name,
			Detail:   detail + "; diagnostics: " + compact(result.Stderr),
			Hint:     "the tool is usable; rerun the version command directly to inspect its complete diagnostics",
		})
		return path, true
	}
	c.add(Result{Severity: OK, Name: name, Detail: detail})
	return path, true
}

func (c *checker) run(path string, args ...string) (androidsdk.CommandResult, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.deps.commandTimeout)
	defer cancel()
	return c.deps.run(ctx, path, args, nil, nil)
}

// ExitCode returns 1 when any required check failed and 0 otherwise.
func (r Report) ExitCode() int {
	for _, result := range r.Results {
		if result.Severity == Failure {
			return 1
		}
	}
	return 0
}

// Write renders a stable, log-friendly doctor report without terminal colour.
func (r Report) Write(writer io.Writer) error {
	if writer == nil {
		return errors.New("doctor: report writer is nil")
	}
	if _, err := fmt.Fprintln(writer, "go-adbtest doctor"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer); err != nil {
		return err
	}
	passed, warnings, failed := 0, 0, 0
	for _, result := range r.Results {
		label := "INFO"
		switch result.Severity {
		case OK:
			label = "OK"
			passed++
		case Warning:
			label = "WARN"
			warnings++
		case Failure:
			label = "FAIL"
			failed++
		case Information:
		default:
			return fmt.Errorf("doctor: invalid result severity %d", result.Severity)
		}
		if _, err := fmt.Fprintf(writer, "[%-4s] %s: %s\n", label, safeLine(result.Name), safeLine(result.Detail)); err != nil {
			return err
		}
		if result.Hint != "" {
			if _, err := fmt.Fprintf(writer, "       hint: %s\n", safeLine(result.Hint)); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(writer); err != nil {
		return err
	}
	status := "Ready"
	if failed > 0 {
		status = "Not ready"
	}
	_, err := fmt.Fprintf(writer, "%s: %d checks passed, %d warnings, %d failed.\n", status, passed, warnings, failed)
	return err
}

func commandFailureResult(
	name string,
	path string,
	args []string,
	result androidsdk.CommandResult,
	err error,
	timeout time.Duration,
	hint string,
) Result {
	detail := fmt.Sprintf("%s %s failed: %v", path, strings.Join(args, " "), err)
	if errors.Is(err, context.DeadlineExceeded) {
		detail = fmt.Sprintf("%s %s timed out after %s", path, strings.Join(args, " "), timeout)
	}
	if output := compact(joinCommandOutput(result)); output != "" {
		detail += "; output: " + output
	}
	return Result{Severity: Failure, Name: name, Detail: detail, Hint: hint}
}

func parseGoVersion(output string) (major, minor int, err error) {
	for index := 0; index+3 < len(output); index++ {
		if output[index] != 'g' || output[index+1] != 'o' || output[index+2] < '0' || output[index+2] > '9' {
			continue
		}
		majorStart := index + 2
		majorEnd := majorStart
		for majorEnd < len(output) && output[majorEnd] >= '0' && output[majorEnd] <= '9' {
			majorEnd++
		}
		if majorEnd >= len(output) || output[majorEnd] != '.' {
			continue
		}
		minorStart := majorEnd + 1
		minorEnd := minorStart
		for minorEnd < len(output) && output[minorEnd] >= '0' && output[minorEnd] <= '9' {
			minorEnd++
		}
		if minorEnd == minorStart {
			continue
		}
		major, majorErr := strconv.Atoi(output[majorStart:majorEnd])
		minor, minorErr := strconv.Atoi(output[minorStart:minorEnd])
		if majorErr == nil && minorErr == nil {
			return major, minor, nil
		}
	}
	return 0, 0, errors.New("go version was not found")
}

func sdkRootSource(getenv func(string) string) string {
	androidHome := strings.TrimSpace(getenv("ANDROID_HOME"))
	sdkRoot := strings.TrimSpace(getenv("ANDROID_SDK_ROOT"))
	switch {
	case androidHome != "" && sdkRoot != "":
		return "ANDROID_HOME and ANDROID_SDK_ROOT"
	case androidHome != "":
		return "ANDROID_HOME"
	case sdkRoot != "":
		return "ANDROID_SDK_ROOT"
	default:
		return "inferred from PATH"
	}
}

func accelerationHint(goos string) string {
	switch goos {
	case "linux":
		return "enable CPU virtualisation, install KVM, grant the user access to /dev/kvm, sign in again, then rerun emulator -accel-check"
	case "darwin":
		return "use a supported macOS host with Hypervisor.Framework available, then rerun emulator -accel-check"
	case "windows":
		return "enable Windows Hypervisor Platform and firmware virtualisation, reboot if required, then rerun emulator -accel-check"
	default:
		return "configure a supported Android Emulator hypervisor, then rerun emulator -accel-check"
	}
}

func accelerationDetail(result androidsdk.CommandResult) string {
	for _, line := range nonEmptyLines(joinCommandOutput(result)) {
		lower := strings.ToLower(line)
		if lower == "accel:" || lower == "accel" || lower == "0" {
			continue
		}
		return compact(line)
	}
	return ""
}

func joinCommandOutput(result androidsdk.CommandResult) string {
	if result.Stdout == "" {
		return result.Stderr
	}
	if result.Stderr == "" {
		return result.Stdout
	}
	return result.Stdout + "\n" + result.Stderr
}

func containsExactLine(output, expected string) bool {
	return containsString(nonEmptyLines(output), expected)
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func nonEmptyLines(output string) []string {
	output = strings.ReplaceAll(output, "\r\n", "\n")
	lines := strings.Split(output, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func compact(output string) string {
	const maximumLength = 300
	value := strings.Join(nonEmptyLines(output), " | ")
	value = safeLine(value)
	if len(value) <= maximumLength {
		return value
	}
	return value[:maximumLength-3] + "..."
}

func safeLine(value string) string {
	return strings.Map(func(character rune) rune {
		switch {
		case character == '\r' || character == '\n' || character == '\t':
			return ' '
		case unicode.IsControl(character):
			return -1
		default:
			return character
		}
	}, value)
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
