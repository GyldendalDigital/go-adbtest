package adbtest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
	"github.com/GyldendalDigital/go-adbtest/cdp"
	"github.com/GyldendalDigital/go-adbtest/emulator"
	"github.com/GyldendalDigital/go-adbtest/permissions"
	"github.com/GyldendalDigital/go-adbtest/ui"
)

const (
	testAPK      = "/tmp/example app.apk"
	testPackage  = "com.example.app"
	testActivity = "com.example.app.MainActivity"
)

type eventLog struct {
	events []string
}

func (l *eventLog) add(event string) {
	l.events = append(l.events, event)
}

func (l *eventLog) joined() string {
	return strings.Join(l.events, "|")
}

func fakeDependencies(log *eventLog) testkitDependencies {
	client := &adb.Client{Serial: "emulator-5554", ADBPath: "/fake/adb"}
	cdpClient := &cdp.Client{ADB: client, AppPackage: testPackage, LocalPort: defaultCDPPort}
	return testkitDependencies{
		validateAPK: func(apkPath string) error {
			log.add("stat:" + apkPath)
			return nil
		},
		inspectAPK: func(ctx context.Context, apkPath string) (apkMetadata, error) {
			log.add("inspect:" + apkPath)
			if _, ok := ctx.Deadline(); !ok {
				return apkMetadata{}, errors.New("inspect context has no deadline")
			}
			return apkMetadata{Package: testPackage, Activity: testActivity}, nil
		},
		startEmulator: func(cfg emulator.Config) (*emulator.Instance, error) {
			log.add("start:" + cfg.AVD)
			return &emulator.Instance{Serial: client.Serial, ADB: client}, nil
		},
		newADB: func(serial string) (*adb.Client, error) {
			log.add("adb:" + serial)
			return &adb.Client{Serial: serial, ADBPath: "/fake/adb"}, nil
		},
		waitForDevice: func(ctx context.Context, _ *adb.Client) error {
			log.add("wait-device")
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("wait context has no deadline")
			}
			return nil
		},
		waitForBoot: func(ctx context.Context, _ *adb.Client) error {
			log.add("wait-boot")
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("boot context has no deadline")
			}
			return nil
		},
		runADB: func(ctx context.Context, _ *adb.Client, args ...string) (string, error) {
			log.add("adb-run:" + strings.Join(args, " "))
			if _, ok := ctx.Deadline(); !ok {
				return "", errors.New("adb context has no deadline")
			}
			return "Success", nil
		},
		shellADB: func(ctx context.Context, _ *adb.Client, command string) (string, error) {
			log.add("shell:" + command)
			if _, ok := ctx.Deadline(); !ok {
				return "", errors.New("shell context has no deadline")
			}
			if strings.HasPrefix(command, "cmd package resolve-activity") || strings.HasPrefix(command, "pm resolve-activity") {
				return testPackage + "/.ResolvedActivity", nil
			}
			return "Starting: Intent", nil
		},
		newCDP: func(ctx context.Context, _ *adb.Client, appPackage string, port int) (*cdp.Client, error) {
			log.add("cdp:" + appPackage)
			if _, ok := ctx.Deadline(); !ok {
				return nil, errors.New("CDP context has no deadline")
			}
			cdpClient.AppPackage = appPackage
			cdpClient.LocalPort = port
			return cdpClient, nil
		},
		reconnectCDP: func(ctx context.Context, _ *cdp.Client) error {
			log.add("reconnect-cdp")
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("reconnect context has no deadline")
			}
			return nil
		},
		closeCDP: func(ctx context.Context, _ *cdp.Client) error {
			log.add("close-cdp")
			if _, ok := ctx.Deadline(); !ok {
				return errors.New("close context has no deadline")
			}
			return nil
		},
		killEmulator: func(*emulator.Instance) error {
			log.add("kill-emulator")
			return nil
		},
		newUI: func(client *adb.Client) *ui.Interactor {
			log.add("new-ui")
			return ui.NewInteractor(client)
		},
		newPermissions: func(interactor *ui.Interactor) *permissions.Handler {
			log.add("new-permissions")
			return permissions.NewHandler(interactor)
		},
	}
}

func avdConfig() Config {
	return Config{AVD: "Pixel_7", APK: testAPK}
}

func TestNormalizeConfigAppliesDefaultsWithoutChangingBools(t *testing.T) {
	got, err := normalizeConfig(avdConfig())
	if err != nil {
		t.Fatalf("normalizeConfig() error: %v", err)
	}
	if got.GPU != defaultGPU || got.BootTimeout != defaultBootTimeout ||
		got.AppTimeout != defaultAppTimeout || got.CDPPort != defaultCDPPort {
		t.Fatalf("defaults = GPU %q, boot %v, app %v, port %d", got.GPU, got.BootTimeout, got.AppTimeout, got.CDPPort)
	}
	if got.Headless || got.NoAudio || got.WipeData {
		t.Fatalf("bool zero values changed: Headless=%v NoAudio=%v WipeData=%v", got.Headless, got.NoAudio, got.WipeData)
	}

	serial, err := normalizeConfig(Config{Serial: "device-1", APK: testAPK})
	if err != nil {
		t.Fatalf("normalize serial config: %v", err)
	}
	if serial.GPU != "" {
		t.Fatalf("serial GPU = %q, want empty", serial.GPU)
	}
}

func TestNormalizeConfigPreservesExplicitValues(t *testing.T) {
	cfg := Config{
		AVD:         " Pixel_8 ",
		APK:         " app.apk ",
		AppPackage:  " com.example.app ",
		AppActivity: " .MainActivity ",
		Headless:    true,
		GPU:         " host ",
		NoAudio:     true,
		WipeData:    true,
		BootTimeout: 45 * time.Second,
		AppTimeout:  12 * time.Second,
		CDPPort:     9333,
	}
	got, err := normalizeConfig(cfg)
	if err != nil {
		t.Fatalf("normalizeConfig() error: %v", err)
	}
	if got.AVD != "Pixel_8" || got.APK != "app.apk" || got.AppPackage != testPackage ||
		got.AppActivity != ".MainActivity" || got.GPU != "host" {
		t.Fatalf("trimmed config = %+v", got)
	}
	if !got.Headless || !got.NoAudio || !got.WipeData || got.BootTimeout != 45*time.Second ||
		got.AppTimeout != 12*time.Second || got.CDPPort != 9333 {
		t.Fatalf("explicit values changed: %+v", got)
	}
}

func TestNormalizeConfigRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{name: "neither target", cfg: Config{APK: testAPK}, want: "exactly one"},
		{name: "both targets", cfg: Config{AVD: "a", Serial: "s", APK: testAPK}, want: "exactly one"},
		{name: "missing APK", cfg: Config{AVD: "a"}, want: "APK is required"},
		{name: "negative boot timeout", cfg: Config{AVD: "a", APK: testAPK, BootTimeout: -1}, want: "BootTimeout"},
		{name: "negative app timeout", cfg: Config{AVD: "a", APK: testAPK, AppTimeout: -1}, want: "AppTimeout"},
		{name: "negative port", cfg: Config{AVD: "a", APK: testAPK, CDPPort: -1}, want: "CDPPort"},
		{name: "large port", cfg: Config{AVD: "a", APK: testAPK, CDPPort: 65536}, want: "CDPPort"},
		{name: "AVD option with serial", cfg: Config{Serial: "s", APK: testAPK, NoAudio: true}, want: "apply only"},
		{name: "invalid package", cfg: Config{AVD: "a", APK: testAPK, AppPackage: "bad package"}, want: "invalid package"},
		{name: "activity newline", cfg: Config{AVD: "a", APK: testAPK, AppActivity: ".Main\nInjected"}, want: "newline"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeConfig(test.cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalizeConfig() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseAAPTBadging(t *testing.T) {
	output := "package: name='com.example.app' versionCode='1' versionName='1.0'\r\n" +
		"sdkVersion:'23'\nlaunchable-activity: name='com.example.app.MainActivity' label='Example' icon=''\n"
	got, err := parseAAPTBadging(output)
	if err != nil {
		t.Fatalf("parseAAPTBadging() error: %v", err)
	}
	if got != (apkMetadata{Package: testPackage, Activity: testActivity}) {
		t.Fatalf("parseAAPTBadging() = %+v", got)
	}

	withoutActivity, err := parseAAPTBadging("package: name='com.example.app' versionCode='1'\n")
	if err != nil || withoutActivity.Package != testPackage || withoutActivity.Activity != "" {
		t.Fatalf("package-only parse = %+v, %v", withoutActivity, err)
	}

	if _, err := parseAAPTBadging("launchable-activity: name='.Main'\n"); err == nil {
		t.Fatal("parseAAPTBadging() accepted output without a package")
	}
}

func TestValidateAPKFileRequiresRegularFile(t *testing.T) {
	tempDir := t.TempDir()
	apkPath := filepath.Join(tempDir, "app.apk")
	if err := os.WriteFile(apkPath, []byte("apk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateAPKFile(apkPath); err != nil {
		t.Fatalf("validateAPKFile(file) error: %v", err)
	}
	if err := validateAPKFile(tempDir); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("validateAPKFile(directory) error = %v", err)
	}
	if err := validateAPKFile(filepath.Join(tempDir, "missing.apk")); err == nil {
		t.Fatal("validateAPKFile(missing) unexpectedly succeeded")
	}
}

func TestInspectAPKInvokesAAPTDirectly(t *testing.T) {
	tempDir := t.TempDir()
	aaptPath := filepath.Join(tempDir, "aapt")
	argsPath := filepath.Join(tempDir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$AAPT_ARGS_FILE\"\nprintf '%s\\n' \"package: name='com.example.app' versionCode='1'\" \"launchable-activity: name='com.example.app.MainActivity'\"\n"
	if err := os.WriteFile(aaptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AAPT", aaptPath)
	t.Setenv("AAPT_ARGS_FILE", argsPath)

	got, err := inspectAPK(context.Background(), testAPK)
	if err != nil {
		t.Fatalf("inspectAPK() error: %v", err)
	}
	if got.Package != testPackage || got.Activity != testActivity {
		t.Fatalf("inspectAPK() = %+v", got)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "dump\nbadging\n" + testAPK + "\n"
	if string(args) != want {
		t.Fatalf("aapt arguments = %q, want %q", args, want)
	}
}

func TestFindAAPTSelectsNewestExecutableSDKBuildTools(t *testing.T) {
	sdkRoot := t.TempDir()
	for _, version := range []string{"9.0.0", "10.0.0"} {
		directory := filepath.Join(sdkRoot, "build-tools", version)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "aapt"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	nonExecutableDir := filepath.Join(sdkRoot, "build-tools", "99.0.0")
	if err := os.MkdirAll(nonExecutableDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonExecutableDir, "aapt"), []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AAPT", "")
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("ANDROID_HOME", sdkRoot)
	t.Setenv("ANDROID_SDK_ROOT", "")
	got, err := findAAPT()
	if err != nil {
		t.Fatalf("findAAPT() error: %v", err)
	}
	want := filepath.Join(sdkRoot, "build-tools", "10.0.0", "aapt")
	if got != want {
		t.Fatalf("findAAPT() = %q, want %q", got, want)
	}
}

func TestWaitForAttachedBootUsesTargetedADBContext(t *testing.T) {
	tempDir := t.TempDir()
	adbPath := filepath.Join(tempDir, "adb")
	argsPath := filepath.Join(tempDir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ADB_ARGS_FILE\"\nprintf '1\\r\\n'\n"
	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADB_ARGS_FILE", argsPath)
	client := &adb.Client{Serial: "physical-1", ADBPath: adbPath}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForAttachedBoot(ctx, client); err != nil {
		t.Fatalf("waitForAttachedBoot() error: %v", err)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "-s\nphysical-1\nshell\ngetprop sys.boot_completed\n"
	if string(args) != want {
		t.Fatalf("adb arguments = %q, want %q", args, want)
	}
}

func TestNormalizeComponent(t *testing.T) {
	tests := []struct {
		name     string
		activity string
		want     string
	}{
		{name: "relative", activity: ".MainActivity", want: testPackage + "/.MainActivity"},
		{name: "short", activity: "MainActivity", want: testPackage + "/.MainActivity"},
		{name: "qualified in package", activity: testActivity, want: testPackage + "/.MainActivity"},
		{name: "qualified outside package", activity: "com.vendor.EntryActivity", want: testPackage + "/com.vendor.EntryActivity"},
		{name: "component relative", activity: testPackage + "/.MainActivity", want: testPackage + "/.MainActivity"},
		{name: "component qualified", activity: testPackage + "/" + testActivity, want: testPackage + "/.MainActivity"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeComponent(testPackage, test.activity)
			if err != nil || got != test.want {
				t.Fatalf("normalizeComponent(%q) = %q, %v; want %q", test.activity, got, err, test.want)
			}
		})
	}

	invalid := []string{"", "other.package/.Main", testPackage + "/", testPackage + "/a/b", ".Bad-Activity"}
	for _, activity := range invalid {
		if _, err := normalizeComponent(testPackage, activity); err == nil {
			t.Errorf("normalizeComponent(%q) unexpectedly succeeded", activity)
		}
	}
}

func TestResolveActivityUsesCmdThenPMFallback(t *testing.T) {
	var commands []string
	component, err := resolveActivity(context.Background(), &adb.Client{}, testPackage,
		func(_ context.Context, _ *adb.Client, command string) (string, error) {
			commands = append(commands, command)
			if strings.HasPrefix(command, "cmd ") {
				return "No activity found", nil
			}
			return "priority=0\n" + testPackage + "/.FallbackActivity\n", nil
		})
	if err != nil {
		t.Fatalf("resolveActivity() error: %v", err)
	}
	if component != testPackage+"/.FallbackActivity" {
		t.Fatalf("component = %q", component)
	}
	want := []string{
		"cmd package resolve-activity --brief -a android.intent.action.MAIN -c android.intent.category.LAUNCHER " + testPackage,
		"pm resolve-activity --brief -a android.intent.action.MAIN -c android.intent.category.LAUNCHER " + testPackage,
	}
	if len(commands) != len(want) || commands[0] != want[0] || commands[1] != want[1] {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

func TestResolveActivityStopsAfterCmdSuccess(t *testing.T) {
	calls := 0
	component, err := resolveActivity(context.Background(), &adb.Client{}, testPackage,
		func(context.Context, *adb.Client, string) (string, error) {
			calls++
			return "{" + testPackage + "/.MainActivity}", nil
		})
	if err != nil || component != testPackage+"/.MainActivity" || calls != 1 {
		t.Fatalf("resolveActivity() = %q, %v, calls=%d", component, err, calls)
	}
}

func TestSetupAVDOrchestratesAndAppliesEffectiveConfig(t *testing.T) {
	log := &eventLog{}
	deps := fakeDependencies(log)
	var emulatorConfig emulator.Config
	originalStart := deps.startEmulator
	deps.startEmulator = func(cfg emulator.Config) (*emulator.Instance, error) {
		emulatorConfig = cfg
		return originalStart(cfg)
	}
	cfg := avdConfig()
	cfg.Headless = true
	cfg.NoAudio = true
	cfg.WipeData = true

	device, err := setupWithDependencies(cfg, &deps)
	if err != nil {
		t.Fatalf("setupWithDependencies() error: %v", err)
	}
	if device.Emulator == nil || device.ADB == nil || device.UI == nil || device.CDP == nil || device.Permissions == nil {
		t.Fatalf("incomplete device: %+v", device)
	}
	if device.Config.AppPackage != testPackage || device.Config.AppActivity != testPackage+"/.MainActivity" {
		t.Fatalf("effective app config = %+v", device.Config)
	}
	if emulatorConfig.AVD != cfg.AVD || emulatorConfig.GPU != defaultGPU || !emulatorConfig.Headless ||
		!emulatorConfig.NoAudio || !emulatorConfig.WipeData || emulatorConfig.Timeout != defaultBootTimeout {
		t.Fatalf("emulator config = %+v", emulatorConfig)
	}
	wantEvents := "stat:" + testAPK + "|inspect:" + testAPK + "|start:Pixel_7|new-ui|new-permissions|" +
		"adb-run:install -r " + testAPK + "|shell:am start -W -n " + shellQuote(testPackage+"/.MainActivity") + "|cdp:" + testPackage
	if log.joined() != wantEvents {
		t.Fatalf("events = %s\nwant   = %s", log.joined(), wantEvents)
	}

	if err := device.Teardown(); err != nil {
		t.Fatalf("Teardown() error: %v", err)
	}
}

func TestSetupSerialAttachesWithoutOwningDevice(t *testing.T) {
	log := &eventLog{}
	deps := fakeDependencies(log)
	var transportDeadline, bootDeadline time.Time
	originalWaitForDevice := deps.waitForDevice
	deps.waitForDevice = func(ctx context.Context, client *adb.Client) error {
		transportDeadline, _ = ctx.Deadline()
		return originalWaitForDevice(ctx, client)
	}
	originalWaitForBoot := deps.waitForBoot
	deps.waitForBoot = func(ctx context.Context, client *adb.Client) error {
		bootDeadline, _ = ctx.Deadline()
		return originalWaitForBoot(ctx, client)
	}
	cfg := Config{Serial: "physical-1", APK: testAPK}

	device, err := setupWithDependencies(cfg, &deps)
	if err != nil {
		t.Fatalf("setupWithDependencies() error: %v", err)
	}
	if device.Emulator != nil || device.ADB == nil || device.ADB.Serial != cfg.Serial || device.ownsEmulator {
		t.Fatalf("serial device ownership = Emulator:%v ADB:%+v owns:%v", device.Emulator, device.ADB, device.ownsEmulator)
	}
	if device.Config.GPU != "" {
		t.Fatalf("serial effective GPU = %q, want empty", device.Config.GPU)
	}
	if err := device.Teardown(); err != nil {
		t.Fatalf("Teardown() error: %v", err)
	}
	if strings.Contains(log.joined(), "start:") || strings.Contains(log.joined(), "kill-emulator") {
		t.Fatalf("serial setup managed an emulator: %s", log.joined())
	}
	if !strings.Contains(log.joined(), "adb:physical-1|wait-device|wait-boot") {
		t.Fatalf("serial attach order missing: %s", log.joined())
	}
	if transportDeadline.IsZero() || !transportDeadline.Equal(bootDeadline) {
		t.Fatalf("transport deadline %v and boot deadline %v do not share one budget", transportDeadline, bootDeadline)
	}
}

func TestSetupActivityPrecedence(t *testing.T) {
	tests := []struct {
		name             string
		appPackage       string
		configured       string
		aapt             string
		wantComponent    string
		wantInspectCalls int
		wantResolveCalls int
	}{
		{name: "explicit activity", configured: ".ExplicitActivity", aapt: testActivity, wantComponent: testPackage + "/.ExplicitActivity", wantInspectCalls: 1},
		{name: "aapt activity", aapt: testActivity, wantComponent: testPackage + "/.MainActivity", wantInspectCalls: 1},
		{name: "aapt has no activity", wantComponent: testPackage + "/.ResolvedActivity", wantInspectCalls: 1, wantResolveCalls: 1},
		{name: "explicit package resolves on device", appPackage: testPackage, wantComponent: testPackage + "/.ResolvedActivity", wantResolveCalls: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := &eventLog{}
			deps := fakeDependencies(log)
			inspectCalls := 0
			deps.inspectAPK = func(context.Context, string) (apkMetadata, error) {
				inspectCalls++
				return apkMetadata{Package: testPackage, Activity: test.aapt}, nil
			}
			resolveCalls := 0
			originalShell := deps.shellADB
			deps.shellADB = func(ctx context.Context, client *adb.Client, command string) (string, error) {
				if strings.Contains(command, "resolve-activity") {
					resolveCalls++
				}
				return originalShell(ctx, client, command)
			}
			cfg := avdConfig()
			cfg.AppPackage = test.appPackage
			cfg.AppActivity = test.configured

			device, err := setupWithDependencies(cfg, &deps)
			if err != nil {
				t.Fatalf("setupWithDependencies() error: %v", err)
			}
			defer func() { _ = device.Teardown() }()
			if device.component != test.wantComponent || device.Config.AppActivity != test.wantComponent {
				t.Fatalf("component = %q, config = %q, want %q", device.component, device.Config.AppActivity, test.wantComponent)
			}
			if resolveCalls != test.wantResolveCalls {
				t.Fatalf("resolve calls = %d, want %d", resolveCalls, test.wantResolveCalls)
			}
			if inspectCalls != test.wantInspectCalls {
				t.Fatalf("inspect calls = %d, want %d", inspectCalls, test.wantInspectCalls)
			}
		})
	}
}

func TestSetupValidatesAndInspectsBeforeAcquiringDevice(t *testing.T) {
	t.Run("invalid config", func(t *testing.T) {
		log := &eventLog{}
		deps := fakeDependencies(log)
		_, err := setupWithDependencies(Config{APK: testAPK}, &deps)
		if err == nil {
			t.Fatal("setupWithDependencies() unexpectedly succeeded")
		}
		if len(log.events) != 0 {
			t.Fatalf("events before validation = %v", log.events)
		}
	})

	t.Run("APK stat failure", func(t *testing.T) {
		log := &eventLog{}
		deps := fakeDependencies(log)
		deps.validateAPK = func(string) error {
			log.add("stat")
			return errors.New("not a regular file")
		}
		_, err := setupWithDependencies(avdConfig(), &deps)
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("setup error = %v", err)
		}
		if log.joined() != "stat" {
			t.Fatalf("events = %s, want only stat", log.joined())
		}
	})

	t.Run("inspection failure", func(t *testing.T) {
		log := &eventLog{}
		deps := fakeDependencies(log)
		deps.inspectAPK = func(context.Context, string) (apkMetadata, error) {
			log.add("inspect")
			return apkMetadata{}, errors.New("bad APK")
		}
		_, err := setupWithDependencies(avdConfig(), &deps)
		if err == nil || !strings.Contains(err.Error(), "bad APK") {
			t.Fatalf("setup error = %v", err)
		}
		if log.joined() != "stat:"+testAPK+"|inspect" {
			t.Fatalf("events = %s, want validation then inspection", log.joined())
		}
	})

	t.Run("explicit package skips aapt", func(t *testing.T) {
		log := &eventLog{}
		deps := fakeDependencies(log)
		deps.inspectAPK = func(context.Context, string) (apkMetadata, error) {
			t.Fatal("inspectAPK called with explicit AppPackage")
			return apkMetadata{}, nil
		}
		cfg := avdConfig()
		cfg.AppPackage = testPackage
		cfg.AppActivity = ".MainActivity"
		device, err := setupWithDependencies(cfg, &deps)
		if err != nil {
			t.Fatalf("setup error = %v", err)
		}
		defer func() { _ = device.Teardown() }()
		if strings.Contains(log.joined(), "inspect:") {
			t.Fatalf("aapt inspection ran with explicit package: %s", log.joined())
		}
	})
}

func TestSetupFailureCleansEveryAcquiredResourceInOrder(t *testing.T) {
	log := &eventLog{}
	deps := fakeDependencies(log)
	fakeCDP := &cdp.Client{}
	deps.newCDP = func(context.Context, *adb.Client, string, int) (*cdp.Client, error) {
		log.add("cdp-fail")
		return fakeCDP, errors.New("CDP unavailable")
	}
	deps.closeCDP = func(_ context.Context, client *cdp.Client) error {
		if client != fakeCDP {
			t.Fatalf("closed CDP client %p, want %p", client, fakeCDP)
		}
		log.add("cleanup-close")
		return errors.New("close failed")
	}
	originalShell := deps.shellADB
	deps.shellADB = func(ctx context.Context, client *adb.Client, command string) (string, error) {
		if strings.HasPrefix(command, "am force-stop") {
			log.add("cleanup-force-stop")
			return "", errors.New("force-stop failed")
		}
		return originalShell(ctx, client, command)
	}
	deps.killEmulator = func(*emulator.Instance) error {
		log.add("cleanup-kill")
		return errors.New("kill failed")
	}

	device, err := setupWithDependencies(avdConfig(), &deps)
	if device != nil {
		t.Fatalf("device = %+v, want nil", device)
	}
	for _, want := range []string{"CDP unavailable", "close failed", "force-stop failed", "kill failed"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("setup error = %v, want containing %q", err, want)
		}
	}
	events := log.joined()
	cleanup := "cleanup-close|cleanup-force-stop|cleanup-kill"
	if !strings.HasSuffix(events, cleanup) {
		t.Fatalf("cleanup order = %s, want suffix %s", events, cleanup)
	}
}

func TestSetupCleansPartialEmulatorReturnedWithStartError(t *testing.T) {
	log := &eventLog{}
	deps := fakeDependencies(log)
	client := &adb.Client{}
	partial := &emulator.Instance{ADB: client, Serial: "emulator-5554"}
	deps.startEmulator = func(emulator.Config) (*emulator.Instance, error) {
		log.add("start-fail")
		return partial, errors.New("boot failed")
	}
	deps.killEmulator = func(instance *emulator.Instance) error {
		if instance != partial {
			t.Fatalf("killed instance %p, want %p", instance, partial)
		}
		log.add("kill-partial")
		return nil
	}

	_, err := setupWithDependencies(avdConfig(), &deps)
	if err == nil || !strings.Contains(err.Error(), "boot failed") {
		t.Fatalf("setup error = %v", err)
	}
	if !strings.HasSuffix(log.joined(), "start-fail|kill-partial") {
		t.Fatalf("events = %s", log.joined())
	}
}

func TestSetupFailureCleanupMatchesAcquisitionStage(t *testing.T) {
	tests := []struct {
		name          string
		configure     func(*testkitDependencies, *eventLog)
		wantForceStop bool
		wantCloseCDP  bool
	}{
		{
			name: "install",
			configure: func(deps *testkitDependencies, _ *eventLog) {
				deps.runADB = func(context.Context, *adb.Client, ...string) (string, error) {
					return "", errors.New("install failed")
				}
			},
		},
		{
			name: "activity resolution",
			configure: func(deps *testkitDependencies, _ *eventLog) {
				deps.inspectAPK = func(context.Context, string) (apkMetadata, error) {
					return apkMetadata{Package: testPackage}, nil
				}
				deps.shellADB = func(context.Context, *adb.Client, string) (string, error) {
					return "", errors.New("resolve failed")
				}
			},
		},
		{
			name: "launch",
			configure: func(deps *testkitDependencies, log *eventLog) {
				original := deps.shellADB
				deps.shellADB = func(ctx context.Context, client *adb.Client, command string) (string, error) {
					if strings.HasPrefix(command, "am start") {
						return "", errors.New("launch failed")
					}
					log.add("cleanup-force-stop")
					return original(ctx, client, command)
				}
			},
			wantForceStop: true,
		},
		{
			name: "CDP",
			configure: func(deps *testkitDependencies, _ *eventLog) {
				deps.newCDP = func(context.Context, *adb.Client, string, int) (*cdp.Client, error) {
					return &cdp.Client{}, errors.New("CDP failed")
				}
			},
			wantForceStop: true,
			wantCloseCDP:  true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			log := &eventLog{}
			deps := fakeDependencies(log)
			test.configure(&deps, log)
			_, err := setupWithDependencies(avdConfig(), &deps)
			if err == nil {
				t.Fatal("setupWithDependencies() unexpectedly succeeded")
			}
			events := log.joined()
			if strings.Contains(events, "am force-stop") != test.wantForceStop && strings.Contains(events, "cleanup-force-stop") != test.wantForceStop {
				t.Fatalf("force-stop presence in %q, want %v", events, test.wantForceStop)
			}
			if strings.Contains(events, "close-cdp") != test.wantCloseCDP {
				t.Fatalf("close-CDP presence in %q, want %v", events, test.wantCloseCDP)
			}
			if !strings.Contains(events, "kill-emulator") {
				t.Fatalf("owned emulator not killed: %s", events)
			}
		})
	}
}

func TestSerialSetupFailureNeverKillsAttachedDevice(t *testing.T) {
	log := &eventLog{}
	deps := fakeDependencies(log)
	deps.runADB = func(context.Context, *adb.Client, ...string) (string, error) {
		return "", errors.New("install failed")
	}
	_, err := setupWithDependencies(Config{Serial: "physical-1", APK: testAPK}, &deps)
	if err == nil {
		t.Fatal("setupWithDependencies() unexpectedly succeeded")
	}
	if strings.Contains(log.joined(), "kill-emulator") {
		t.Fatalf("attached device was killed: %s", log.joined())
	}
}

func TestTeardownIsIdempotentAndJoinsErrorsInOrder(t *testing.T) {
	log := &eventLog{}
	deps := fakeDependencies(log)
	deps.closeCDP = func(context.Context, *cdp.Client) error {
		log.add("close")
		return errors.New("close boom")
	}
	deps.shellADB = func(context.Context, *adb.Client, string) (string, error) {
		log.add("force")
		return "", errors.New("force boom")
	}
	deps.killEmulator = func(*emulator.Instance) error {
		log.add("kill")
		return errors.New("kill boom")
	}
	device := &Device{
		Emulator:     &emulator.Instance{},
		ADB:          &adb.Client{},
		CDP:          &cdp.Client{},
		Config:       Config{AppPackage: testPackage, AppTimeout: time.Second},
		deps:         deps,
		ownsEmulator: true,
		appLaunched:  true,
	}

	first := device.Teardown()
	second := device.Teardown()
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Fatalf("Teardown errors = %v and %v", first, second)
	}
	for _, want := range []string{"close boom", "force boom", "kill boom"} {
		if !strings.Contains(first.Error(), want) {
			t.Fatalf("Teardown error = %v, want containing %q", first, want)
		}
	}
	if log.joined() != "close|force|kill" {
		t.Fatalf("cleanup events = %s", log.joined())
	}

	var nilDevice *Device
	if err := nilDevice.Teardown(); err != nil {
		t.Fatalf("nil Device.Teardown() = %v", err)
	}
}

func TestLifecycleCommandsAndRestartOrder(t *testing.T) {
	log := &eventLog{}
	deps := fakeDependencies(log)
	device := &Device{
		ADB:       &adb.Client{},
		CDP:       &cdp.Client{},
		Config:    Config{AppPackage: testPackage, AppTimeout: time.Second},
		component: testPackage + "/.MainActivity",
		deps:      deps,
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := device.launchAppContext(ctx); err != nil {
		t.Fatalf("launchAppContext() error: %v", err)
	}
	if err := device.forceStopContext(ctx); err != nil {
		t.Fatalf("forceStopContext() error: %v", err)
	}
	if err := device.restartAppContext(ctx); err != nil {
		t.Fatalf("restartAppContext() error: %v", err)
	}
	wantSuffix := "shell:am start -W -n " + shellQuote(testPackage+"/.MainActivity") + "|" +
		"shell:am force-stop " + testPackage + "|" +
		"shell:am force-stop " + testPackage + "|" +
		"shell:am start -W -n " + shellQuote(testPackage+"/.MainActivity") + "|reconnect-cdp"
	if log.joined() != wantSuffix {
		t.Fatalf("lifecycle events = %s\nwant             = %s", log.joined(), wantSuffix)
	}
}

func TestLifecycleRejectsCommandReportedErrors(t *testing.T) {
	for _, output := range []string{
		"Error: Activity class does not exist",
		"Error type 3\nError: Activity class does not exist",
		"Starting: Intent\nStatus: timeout\nComplete",
	} {
		deps := fakeDependencies(&eventLog{})
		deps.shellADB = func(context.Context, *adb.Client, string) (string, error) {
			return output, nil
		}
		device := &Device{
			ADB:       &adb.Client{},
			CDP:       &cdp.Client{},
			Config:    Config{AppPackage: testPackage, AppTimeout: time.Second},
			component: testPackage + "/.Missing",
			deps:      deps,
		}
		if err := device.launchAppContext(context.Background()); err == nil {
			t.Fatalf("launchAppContext() accepted output %q", output)
		}
		if !device.appLaunched {
			t.Fatal("failed launch was not marked for cleanup")
		}
	}
}

func TestForceStopUsesConfiguredDeadlineAndCommand(t *testing.T) {
	deps := fakeDependencies(&eventLog{})
	var command string
	var remaining time.Duration
	deps.shellADB = func(ctx context.Context, _ *adb.Client, got string) (string, error) {
		command = got
		deadline, ok := ctx.Deadline()
		if !ok {
			return "", errors.New("no deadline")
		}
		remaining = time.Until(deadline)
		return "", nil
	}
	device := &Device{
		ADB:    &adb.Client{},
		Config: Config{AppPackage: testPackage, AppTimeout: 2 * time.Second},
		deps:   deps,
	}
	if err := device.ForceStop(); err != nil {
		t.Fatalf("ForceStop() error: %v", err)
	}
	if command != "am force-stop "+testPackage {
		t.Fatalf("force-stop command = %q", command)
	}
	if remaining <= 0 || remaining > 2*time.Second {
		t.Fatalf("force-stop deadline remaining = %v", remaining)
	}
}

func TestCommandOutputError(t *testing.T) {
	for _, output := range []string{"Error: bad activity", "Status\nFailure [INSTALL_FAILED]\n"} {
		if commandOutputError(output) == "" {
			t.Errorf("commandOutputError(%q) returned empty", output)
		}
	}
	if got := commandOutputError("Starting: Intent\nStatus: ok"); got != "" {
		t.Fatalf("commandOutputError(success) = %q", got)
	}
	if got := activityStartOutputError("Starting: Intent\nStatus: ok\nComplete"); got != "" {
		t.Fatalf("activityStartOutputError(success) = %q", got)
	}
}
