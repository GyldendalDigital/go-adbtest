package adbtest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
	"github.com/GyldendalDigital/go-adbtest/cdp"
	"github.com/GyldendalDigital/go-adbtest/emulator"
	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
	"github.com/GyldendalDigital/go-adbtest/permissions"
	"github.com/GyldendalDigital/go-adbtest/ui"
)

const (
	defaultGPU         = "auto"
	defaultBootTimeout = 120 * time.Second
	defaultAppTimeout  = 30 * time.Second
	defaultCDPPort     = 9222
)

// Config describes the device and application used by an Android integration
// test suite. Exactly one of AVD and Serial must be set.
type Config struct {
	// AVD names an emulator to start and own. It is mutually exclusive with Serial.
	AVD string
	// Serial selects an already-running device that will not be stopped at teardown.
	Serial string
	// APK is the path to the application package installed during setup.
	APK string
	// AppPackage is the Android application ID. When empty, aapt inspects APK.
	AppPackage string
	// AppProcess optionally selects the Android process hosting the debuggable
	// WebView. Empty selects AppPackage; a colon-prefixed name is relative to it.
	AppProcess string
	// AppActivity optionally identifies the launcher activity or full component.
	AppActivity string
	// Headless starts an owned AVD without a window.
	Headless bool
	// GPU selects the GPU mode for an owned AVD. Zero uses auto.
	GPU string
	// Cores overrides the virtual CPU count for an owned AVD. Zero uses its setting.
	Cores int
	// MemoryMB overrides owned-AVD RAM in megabytes. Zero uses its setting;
	// non-zero values must be between 1536 and 8192.
	MemoryMB int
	// Acceleration selects owned-AVD VM acceleration: "auto", "on", or "off".
	// Empty uses the emulator default; "on" fails if no hypervisor is usable.
	Acceleration string
	// NoAudio disables audio for an owned AVD.
	NoAudio bool
	// WipeData wipes an owned AVD before boot.
	WipeData bool
	// NoSnapshot cold-boots an owned AVD and disables automatic snapshot saving.
	NoSnapshot bool
	// BootTimeout bounds AVD boot or attached-device readiness. Zero means 120 seconds.
	BootTimeout time.Duration
	// AppTimeout bounds each app operation, including APK inspection, install,
	// activity resolution, launch, restart, and cleanup. Initial launch and CDP
	// readiness share one budget. Zero means 30 seconds.
	AppTimeout time.Duration
	// CDPPort is the host TCP port used for WebView forwarding. Zero means 9222.
	CDPPort int
}

// HeadlessAVD returns a conservative starting configuration for an owned AVD.
// It caps the emulator at two virtual CPUs, disables the window, audio, and
// snapshots, requires VM acceleration, and lets the emulator choose the
// graphics backend. It deliberately leaves WipeData false and RAM
// image-managed. Callers may override any field before passing the
// configuration to Setup.
func HeadlessAVD(avd, apk string) Config {
	return Config{
		AVD:          avd,
		APK:          apk,
		Headless:     true,
		GPU:          "auto",
		Cores:        2,
		Acceleration: "on",
		NoAudio:      true,
		NoSnapshot:   true,
	}
}

// Device is the main handle for interacting with a configured Android test
// device and its WebView application.
type Device struct {
	// Emulator is the owned emulator instance, or nil in attached-device mode.
	Emulator *emulator.Instance
	// ADB is pinned to the configured or newly started device.
	ADB *adb.Client
	// UI provides native Android UI interaction.
	UI *ui.Interactor
	// CDP provides WebView DOM and JavaScript interaction.
	CDP *cdp.Client
	// Permissions handles Android runtime permission dialogs.
	Permissions *permissions.Handler
	// Config contains normalized defaults and resolved application metadata.
	Config Config

	component    string
	deps         testkitDependencies
	ownsEmulator bool

	stateMu     sync.Mutex
	appLaunched bool
	closed      bool
	teardown    sync.Once
	teardownErr error
}

// Setup validates cfg, prepares the requested device, installs and launches
// the APK, and connects the WebView CDP client. Setup is intended for TestMain
// and panics after cleaning up any partially acquired resources on failure.
//
//nolint:gocritic // Config is a public value-style options struct by design.
func Setup(cfg Config) *Device {
	deps := productionTestkitDependencies()
	device, err := setupWithDependencies(cfg, &deps)
	if err != nil {
		panic(err)
	}
	return device
}

// Teardown closes CDP, force-stops an app launched by this Device, and stops an
// emulator owned by this Device. It is safe to call more than once.
func (d *Device) Teardown() error {
	if d == nil {
		return nil
	}
	d.teardown.Do(func() {
		d.teardownErr = d.teardownResources()
	})
	return d.teardownErr
}

// ForceStop force-stops the configured application.
func (d *Device) ForceStop() error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.appOperationTimeout())
	defer cancel()
	return d.forceStopContext(ctx)
}

// LaunchApp requests the configured activity launch and fails t when Android
// rejects the command or AppTimeout expires. It does not establish application
// readiness or reconnect CDP; use a UI or CDP wait when readiness matters.
func (d *Device) LaunchApp(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d.appOperationTimeout())
	defer cancel()
	if err := d.launchAppContext(ctx); err != nil {
		t.Fatalf("launch app: %v", err)
	}
}

// RestartApp force-stops and relaunches the application, then reconnects CDP.
func (d *Device) RestartApp(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d.appOperationTimeout())
	defer cancel()
	if err := d.restartAppContext(ctx); err != nil {
		t.Fatalf("restart app: %v", err)
	}
}

type apkMetadata struct {
	Package  string
	Activity string
}

type testkitDependencies struct {
	validateAPK    func(string) error
	inspectAPK     func(context.Context, string) (apkMetadata, error)
	startEmulator  func(emulator.Config) (*emulator.Instance, error)
	newADB         func(string) (*adb.Client, error)
	waitForDevice  func(context.Context, *adb.Client) error
	waitForBoot    func(context.Context, *adb.Client) error
	runADB         func(context.Context, *adb.Client, ...string) (string, error)
	shellADB       func(context.Context, *adb.Client, string) (string, error)
	newCDP         func(context.Context, *adb.Client, string, string, int) (*cdp.Client, error)
	reconnectCDP   func(context.Context, *cdp.Client) error
	closeCDP       func(context.Context, *cdp.Client) error
	killEmulator   func(*emulator.Instance) error
	newUI          func(*adb.Client) *ui.Interactor
	newPermissions func(*ui.Interactor) *permissions.Handler
}

func productionTestkitDependencies() testkitDependencies {
	return testkitDependencies{
		validateAPK:   validateAPKFile,
		inspectAPK:    inspectAPK,
		startEmulator: emulator.Start,
		newADB:        adb.New,
		waitForDevice: func(ctx context.Context, client *adb.Client) error {
			return client.WaitForDeviceContext(ctx)
		},
		waitForBoot: waitForAttachedBoot,
		runADB: func(ctx context.Context, client *adb.Client, args ...string) (string, error) {
			return client.RunContext(ctx, args...)
		},
		shellADB: func(ctx context.Context, client *adb.Client, command string) (string, error) {
			return client.ShellContext(ctx, command)
		},
		newCDP: cdp.NewClientForProcessContext,
		reconnectCDP: func(ctx context.Context, client *cdp.Client) error {
			return client.ReconnectContext(ctx)
		},
		closeCDP: func(ctx context.Context, client *cdp.Client) error {
			return client.CloseContext(ctx)
		},
		killEmulator: func(instance *emulator.Instance) error {
			return instance.Kill()
		},
		newUI:          ui.NewInteractor,
		newPermissions: permissions.NewHandler,
	}
}

//nolint:gocritic // Config is normalized as an immutable value at the boundary.
func setupWithDependencies(cfg Config, deps *testkitDependencies) (*Device, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	if err := validateDependencies(deps); err != nil {
		return nil, err
	}
	if err := deps.validateAPK(cfg.APK); err != nil {
		return nil, fmt.Errorf("validate APK %q: %w", cfg.APK, err)
	}

	if cfg.AppPackage == "" {
		inspectCtx, cancelInspect := context.WithTimeout(context.Background(), cfg.AppTimeout)
		metadata, inspectErr := deps.inspectAPK(inspectCtx, cfg.APK)
		cancelInspect()
		if inspectErr != nil {
			return nil, fmt.Errorf("inspect APK %q: %w", cfg.APK, inspectErr)
		}
		if err := validatePackageName(metadata.Package); err != nil {
			return nil, fmt.Errorf("APK package: %w", err)
		}
		cfg.AppPackage = metadata.Package
		if cfg.AppActivity == "" {
			cfg.AppActivity = metadata.Activity
		}
	}
	if cfg.AppActivity != "" {
		component, normalizeErr := normalizeComponent(cfg.AppPackage, cfg.AppActivity)
		if normalizeErr != nil {
			return nil, fmt.Errorf("app activity: %w", normalizeErr)
		}
		cfg.AppActivity = component
	}
	appProcess, processErr := normalizeAppProcess(cfg.AppPackage, cfg.AppProcess)
	if processErr != nil {
		return nil, fmt.Errorf("app process: %w", processErr)
	}
	cfg.AppProcess = appProcess

	device := &Device{Config: cfg, deps: *deps}
	fail := func(setupErr error) (*Device, error) {
		cleanupErr := device.Teardown()
		if cleanupErr != nil {
			return nil, errors.Join(setupErr, fmt.Errorf("clean up failed setup: %w", cleanupErr))
		}
		return nil, setupErr
	}

	if cfg.AVD != "" {
		instance, startErr := deps.startEmulator(emulator.Config{
			AVD:          cfg.AVD,
			Headless:     cfg.Headless,
			GPU:          cfg.GPU,
			Cores:        cfg.Cores,
			MemoryMB:     cfg.MemoryMB,
			Acceleration: cfg.Acceleration,
			NoAudio:      cfg.NoAudio,
			WipeData:     cfg.WipeData,
			NoSnapshot:   cfg.NoSnapshot,
			Timeout:      cfg.BootTimeout,
		})
		if instance != nil {
			device.Emulator = instance
			device.ADB = instance.ADB
			device.ownsEmulator = true
		}
		if startErr != nil {
			return fail(fmt.Errorf("start emulator: %w", startErr))
		}
		if instance == nil || instance.ADB == nil {
			return fail(errors.New("start emulator: returned no adb client"))
		}
	} else {
		client, newADBErr := deps.newADB(cfg.Serial)
		if newADBErr != nil {
			return fail(fmt.Errorf("attach adb client to %q: %w", cfg.Serial, newADBErr))
		}
		if client == nil {
			return fail(fmt.Errorf("attach adb client to %q: returned nil client", cfg.Serial))
		}
		device.ADB = client

		waitCtx, cancelWait := context.WithTimeout(context.Background(), cfg.BootTimeout)
		waitErr := deps.waitForDevice(waitCtx, client)
		if waitErr != nil {
			cancelWait()
			return fail(fmt.Errorf("wait for device %q: %w", cfg.Serial, waitErr))
		}
		bootErr := deps.waitForBoot(waitCtx, client)
		cancelWait()
		if bootErr != nil {
			return fail(fmt.Errorf("wait for device %q to finish booting: %w", cfg.Serial, bootErr))
		}
	}

	device.UI = deps.newUI(device.ADB)
	if device.UI == nil {
		return fail(errors.New("create UI interactor: returned nil"))
	}
	device.Permissions = deps.newPermissions(device.UI)
	if device.Permissions == nil {
		return fail(errors.New("create permission handler: returned nil"))
	}

	installCtx, cancelInstall := context.WithTimeout(context.Background(), cfg.AppTimeout)
	installOutput, installErr := deps.runADB(installCtx, device.ADB, "install", "-r", cfg.APK)
	cancelInstall()
	if installErr != nil {
		return fail(fmt.Errorf("install APK %q: %w", cfg.APK, installErr))
	}
	if commandOutputError(installOutput) != "" {
		return fail(fmt.Errorf("install APK %q: %s", cfg.APK, commandOutputError(installOutput)))
	}

	if cfg.AppActivity == "" {
		resolveCtx, cancelResolve := context.WithTimeout(context.Background(), cfg.AppTimeout)
		component, resolveErr := resolveActivity(resolveCtx, device.ADB, cfg.AppPackage, deps.shellADB)
		cancelResolve()
		if resolveErr != nil {
			return fail(resolveErr)
		}
		device.Config.AppActivity = component
	}
	device.component = device.Config.AppActivity

	appCtx, cancelApp := context.WithTimeout(context.Background(), cfg.AppTimeout)
	if err := device.launchAppContext(appCtx); err != nil {
		cancelApp()
		return fail(err)
	}
	cdpClient, cdpErr := deps.newCDP(
		appCtx,
		device.ADB,
		cfg.AppPackage,
		cfg.AppProcess,
		cfg.CDPPort,
	)
	if cdpClient != nil {
		device.CDP = cdpClient
	}
	cancelApp()
	if cdpErr != nil {
		return fail(fmt.Errorf("connect CDP: %w", cdpErr))
	}
	if cdpClient == nil {
		return fail(errors.New("connect CDP: returned nil client"))
	}

	return device, nil
}

//nolint:gocritic // Returning a normalized Config value keeps setup state isolated.
func normalizeConfig(cfg Config) (Config, error) {
	cfg.AVD = strings.TrimSpace(cfg.AVD)
	cfg.Serial = strings.TrimSpace(cfg.Serial)
	cfg.APK = strings.TrimSpace(cfg.APK)
	cfg.AppPackage = strings.TrimSpace(cfg.AppPackage)
	cfg.AppProcess = strings.TrimSpace(cfg.AppProcess)
	cfg.AppActivity = strings.TrimSpace(cfg.AppActivity)
	cfg.GPU = strings.TrimSpace(cfg.GPU)
	cfg.Acceleration = strings.TrimSpace(cfg.Acceleration)

	if (cfg.AVD == "") == (cfg.Serial == "") {
		return Config{}, errors.New("exactly one of AVD or Serial is required")
	}
	if cfg.APK == "" {
		return Config{}, errors.New("APK is required")
	}
	if cfg.BootTimeout < 0 {
		return Config{}, errors.New("BootTimeout must not be negative")
	}
	if cfg.AppTimeout < 0 {
		return Config{}, errors.New("AppTimeout must not be negative")
	}
	if cfg.Cores < 0 || cfg.Cores > 64 {
		return Config{}, fmt.Errorf("cores must be between 0 and 64, got %d", cfg.Cores)
	}
	if cfg.MemoryMB != 0 && (cfg.MemoryMB < 1536 || cfg.MemoryMB > 8192) {
		return Config{}, fmt.Errorf("MemoryMB must be between 1536 and 8192, got %d", cfg.MemoryMB)
	}
	if cfg.Acceleration != "" && cfg.Acceleration != "auto" && cfg.Acceleration != "on" && cfg.Acceleration != "off" {
		return Config{}, fmt.Errorf("acceleration must be auto, on, or off, got %q", cfg.Acceleration)
	}
	if cfg.CDPPort < 0 || cfg.CDPPort > 65535 {
		return Config{}, fmt.Errorf("CDPPort must be between 1 and 65535, got %d", cfg.CDPPort)
	}
	if cfg.Serial != "" && (cfg.Headless || cfg.GPU != "" || cfg.Cores != 0 || cfg.MemoryMB != 0 ||
		cfg.Acceleration != "" ||
		cfg.NoAudio || cfg.WipeData || cfg.NoSnapshot) {
		return Config{}, errors.New(
			"headless, GPU, Cores, MemoryMB, Acceleration, NoAudio, WipeData, and NoSnapshot apply only when AVD is set",
		)
	}
	if cfg.AppPackage != "" {
		if err := validatePackageName(cfg.AppPackage); err != nil {
			return Config{}, fmt.Errorf("AppPackage: %w", err)
		}
	}
	if _, err := normalizeAppProcess(cfg.AppPackage, cfg.AppProcess); err != nil {
		return Config{}, fmt.Errorf("AppProcess: %w", err)
	}
	if strings.ContainsAny(cfg.AppActivity, "\r\n") {
		return Config{}, errors.New("AppActivity must not contain a newline")
	}

	if cfg.BootTimeout == 0 {
		cfg.BootTimeout = defaultBootTimeout
	}
	if cfg.AppTimeout == 0 {
		cfg.AppTimeout = defaultAppTimeout
	}
	if cfg.CDPPort == 0 {
		cfg.CDPPort = defaultCDPPort
	}
	if cfg.AVD != "" && cfg.GPU == "" {
		cfg.GPU = defaultGPU
	}
	return cfg, nil
}

func normalizeAppProcess(appPackage, appProcess string) (string, error) {
	if appProcess == "" {
		return appPackage, nil
	}
	if strings.HasPrefix(appProcess, ":") {
		if appPackage == "" {
			// The package may still be discovered from the APK. Keep the relative
			// form until setup has resolved it.
			return appProcess, validateAppProcess(appProcess)
		}
		appProcess = appPackage + appProcess
	}
	if err := validateAppProcess(appProcess); err != nil {
		return "", err
	}
	return appProcess, nil
}

func validateAppProcess(appProcess string) error {
	if appProcess == "" {
		return nil
	}
	if strings.HasPrefix(appProcess, ".") || strings.HasSuffix(appProcess, ".") ||
		strings.Contains(appProcess, "..") || strings.HasSuffix(appProcess, ":") ||
		strings.Count(appProcess, ":") > 1 {
		return fmt.Errorf("invalid app process %q", appProcess)
	}
	for _, char := range appProcess {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9' || char == '_' || char == '.' || char == ':' {
			continue
		}
		return fmt.Errorf("invalid app process %q", appProcess)
	}
	return nil
}

func validateDependencies(deps *testkitDependencies) error {
	if deps == nil {
		return errors.New("testkit dependencies are nil")
	}
	if deps.validateAPK == nil || deps.inspectAPK == nil || deps.startEmulator == nil || deps.newADB == nil ||
		deps.waitForDevice == nil || deps.waitForBoot == nil || deps.runADB == nil || deps.shellADB == nil ||
		deps.newCDP == nil || deps.reconnectCDP == nil || deps.closeCDP == nil ||
		deps.killEmulator == nil || deps.newUI == nil || deps.newPermissions == nil {
		return errors.New("testkit dependencies are incomplete")
	}
	return nil
}

func validateAPKFile(apkPath string) error {
	info, err := os.Stat(apkPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	return nil
}

func inspectAPK(ctx context.Context, apkPath string) (apkMetadata, error) {
	aaptPath, err := findAAPT()
	if err != nil {
		return apkMetadata{}, err
	}
	//nolint:gosec // aaptPath is resolved from explicit Android SDK configuration.
	cmd := exec.CommandContext(ctx, aaptPath, "dump", "badging", apkPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		cause := err
		if ctxErr := ctx.Err(); ctxErr != nil {
			cause = ctxErr
		}
		message := strings.TrimSpace(string(output))
		if message == "" {
			return apkMetadata{}, fmt.Errorf("aapt dump badging: %w", cause)
		}
		return apkMetadata{}, fmt.Errorf("aapt dump badging: %w: %s", cause, message)
	}
	return parseAAPTBadging(string(output))
}

func findAAPT() (string, error) {
	if configured := strings.TrimSpace(os.Getenv("AAPT")); configured != "" {
		path, err := exec.LookPath(configured)
		if err != nil {
			return "", fmt.Errorf("configured aapt %q not found: %w", configured, err)
		}
		return path, nil
	}
	if strings.TrimSpace(os.Getenv("ANDROID_HOME")) != "" || strings.TrimSpace(os.Getenv("ANDROID_SDK_ROOT")) != "" {
		root, err := androidsdk.ResolveSDKRoot("")
		if err != nil {
			return "", fmt.Errorf("aapt: %w", err)
		}
		path, err := androidsdk.FindTool(root, "aapt")
		if err != nil {
			return "", fmt.Errorf("aapt: %w", err)
		}
		return path, nil
	}
	path, err := exec.LookPath("aapt")
	if err != nil {
		return "", errors.New("aapt not found: install Android build-tools, add aapt to PATH, or set AAPT")
	}
	return path, nil
}

func waitForAttachedBoot(ctx context.Context, client *adb.Client) error {
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr != nil {
				return fmt.Errorf("boot completion: %w (last adb error: %v)", err, lastErr)
			}
			return fmt.Errorf("boot completion: %w", err)
		}
		output, err := client.ShellContext(ctx, "getprop sys.boot_completed")
		if err == nil && strings.TrimSpace(output) == "1" {
			return nil
		}
		if err != nil {
			lastErr = err
		}

		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-timer.C:
			continue
		}
	}
}

func parseAAPTBadging(output string) (apkMetadata, error) {
	var metadata apkMetadata
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		switch {
		case strings.HasPrefix(line, "package:"):
			metadata.Package = quotedAttribute(line, "name")
		case strings.HasPrefix(line, "launchable-activity:"):
			if metadata.Activity == "" {
				metadata.Activity = quotedAttribute(line, "name")
			}
		}
	}
	if metadata.Package == "" {
		return apkMetadata{}, errors.New("aapt output did not contain a package name")
	}
	return metadata, nil
}

func quotedAttribute(line, name string) string {
	prefix := name + "='"
	start := strings.Index(line, prefix)
	if start < 0 {
		return ""
	}
	value := line[start+len(prefix):]
	end := strings.IndexByte(value, '\'')
	if end < 0 {
		return ""
	}
	return value[:end]
}

func resolveActivity(
	ctx context.Context,
	client *adb.Client,
	appPackage string,
	shell func(context.Context, *adb.Client, string) (string, error),
) (string, error) {
	intent := "-a android.intent.action.MAIN -c android.intent.category.LAUNCHER " + appPackage
	commands := []string{
		"cmd package resolve-activity --brief " + intent,
		"pm resolve-activity --brief " + intent,
	}

	var attempts []error
	for _, command := range commands {
		output, err := shell(ctx, client, command)
		if err != nil {
			attempts = append(attempts, fmt.Errorf("%s: %w", command, err))
			continue
		}
		activity := resolvedActivityFromOutput(output)
		if activity == "" {
			attempts = append(attempts, fmt.Errorf("%s: no activity in output %q", command, strings.TrimSpace(output)))
			continue
		}
		component, normalizeErr := normalizeComponent(appPackage, activity)
		if normalizeErr != nil {
			attempts = append(attempts, fmt.Errorf("%s: %w", command, normalizeErr))
			continue
		}
		return component, nil
	}
	return "", fmt.Errorf("resolve launcher activity for %q: %w", appPackage, errors.Join(attempts...))
}

func resolvedActivityFromOutput(output string) string {
	for _, rawLine := range strings.Split(output, "\n") {
		for _, field := range strings.Fields(strings.TrimSpace(rawLine)) {
			if strings.Count(field, "/") == 1 {
				return strings.Trim(field, "{}[],")
			}
		}
	}
	return ""
}

func normalizeComponent(appPackage, activity string) (string, error) {
	if err := validatePackageName(appPackage); err != nil {
		return "", err
	}
	activity = strings.TrimSpace(activity)
	if activity == "" {
		return "", errors.New("activity is empty")
	}

	if strings.Contains(activity, "/") {
		parts := strings.Split(activity, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", fmt.Errorf("invalid activity component %q", activity)
		}
		if parts[0] != appPackage {
			return "", fmt.Errorf("activity component package %q does not match app package %q", parts[0], appPackage)
		}
		activity = parts[1]
	}

	var className string
	switch {
	case strings.HasPrefix(activity, "."):
		className = appPackage + activity
	case !strings.Contains(activity, "."):
		className = appPackage + "." + activity
	default:
		className = activity
	}
	if err := validateQualifiedName(className, true); err != nil {
		return "", fmt.Errorf("invalid activity %q: %w", activity, err)
	}

	if strings.HasPrefix(className, appPackage+".") {
		return appPackage + "/." + strings.TrimPrefix(className, appPackage+"."), nil
	}
	return appPackage + "/" + className, nil
}

func validatePackageName(packageName string) error {
	if packageName == "" {
		return errors.New("package name is empty")
	}
	if err := validateQualifiedName(packageName, false); err != nil {
		return fmt.Errorf("invalid package name %q: %w", packageName, err)
	}
	return nil
}

func validateQualifiedName(value string, allowDollar bool) error {
	for _, segment := range strings.Split(value, ".") {
		if segment == "" {
			return errors.New("name contains an empty segment")
		}
		for index, char := range segment {
			valid := char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z'
			if index > 0 {
				valid = valid || char >= '0' && char <= '9' || allowDollar && char == '$'
			}
			if !valid {
				return fmt.Errorf("segment %q contains invalid character %q", segment, char)
			}
		}
	}
	return nil
}

func (d *Device) launchAppContext(ctx context.Context) error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	if d.component == "" {
		return errors.New("app activity component is empty")
	}

	d.stateMu.Lock()
	d.appLaunched = true
	d.stateMu.Unlock()
	output, err := d.deps.shellADB(ctx, d.ADB, "am start -W -n "+shellQuote(d.component))
	if err != nil {
		return fmt.Errorf("start activity %s: %w", d.component, err)
	}
	if outputErr := activityStartOutputError(output); outputErr != "" {
		return fmt.Errorf("start activity %s: %s", d.component, outputErr)
	}
	return nil
}

func (d *Device) forceStopContext(ctx context.Context) error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	if err := d.forceStopCommand(ctx); err != nil {
		return err
	}
	d.stateMu.Lock()
	d.appLaunched = false
	d.stateMu.Unlock()
	return nil
}

func (d *Device) forceStopCommand(ctx context.Context) error {
	if d.ADB == nil {
		return errors.New("force-stop app: adb client is nil")
	}
	if d.Config.AppPackage == "" {
		return errors.New("force-stop app: app package is empty")
	}
	output, err := d.deps.shellADB(ctx, d.ADB, "am force-stop "+d.Config.AppPackage)
	if err != nil {
		return fmt.Errorf("force-stop app %s: %w", d.Config.AppPackage, err)
	}
	if outputErr := commandOutputError(output); outputErr != "" {
		return fmt.Errorf("force-stop app %s: %s", d.Config.AppPackage, outputErr)
	}
	return nil
}

func (d *Device) restartAppContext(ctx context.Context) error {
	if err := d.ensureOpen(); err != nil {
		return err
	}
	if d.CDP == nil {
		return errors.New("restart app: CDP client is nil")
	}
	if err := d.forceStopContext(ctx); err != nil {
		return err
	}
	if err := d.launchAppContext(ctx); err != nil {
		return err
	}
	if err := d.deps.reconnectCDP(ctx, d.CDP); err != nil {
		return fmt.Errorf("reconnect CDP: %w", err)
	}
	return nil
}

func (d *Device) ensureOpen() error {
	if d == nil {
		return errors.New("device is nil")
	}
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.closed {
		return errors.New("device is torn down")
	}
	if d.deps.shellADB == nil {
		return errors.New("device dependencies are incomplete")
	}
	return nil
}

func (d *Device) teardownResources() error {
	d.stateMu.Lock()
	d.closed = true
	launched := d.appLaunched
	d.stateMu.Unlock()

	var cleanupErrors []error
	if d.CDP != nil && d.deps.closeCDP != nil {
		timeout := d.appOperationTimeout()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := d.deps.closeCDP(ctx, d.CDP)
		cancel()
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("close CDP: %w", err))
		}
	}
	if launched && d.ADB != nil && d.Config.AppPackage != "" && d.deps.shellADB != nil {
		timeout := d.appOperationTimeout()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		err := d.forceStopCommand(ctx)
		cancel()
		if err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if d.ownsEmulator && d.Emulator != nil && d.deps.killEmulator != nil {
		if err := d.deps.killEmulator(d.Emulator); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("kill emulator: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func (d *Device) appOperationTimeout() time.Duration {
	if d != nil && d.Config.AppTimeout > 0 {
		return d.Config.AppTimeout
	}
	return defaultAppTimeout
}

func commandOutputError(output string) string {
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "Error:") || strings.HasPrefix(line, "Failure [") {
			return line
		}
	}
	return ""
}

func activityStartOutputError(output string) string {
	if outputErr := commandOutputError(output); outputErr != "" {
		return outputErr
	}
	for _, rawLine := range strings.Split(output, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "Error type") {
			return line
		}
		if !strings.HasPrefix(line, "Status:") {
			continue
		}
		status := strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		// Activity Manager can stop waiting for the first rendered frame and
		// report "timeout" after it has accepted the launch result.
		// Setup and RestartApp subsequently prove readiness through CDP, while
		// actual launch failures are reported as Error/Error type lines above.
		if !strings.EqualFold(status, "ok") && !strings.EqualFold(status, "timeout") {
			return line
		}
	}
	return ""
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
