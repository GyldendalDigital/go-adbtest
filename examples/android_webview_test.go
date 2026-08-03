//go:build android_integration

package examples_test

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	adbtest "github.com/GyldendalDigital/go-adbtest"
)

var (
	exampleDevice     *adbtest.Device
	exampleFixture    fixtureConfig
	exampleSkipReason string
)

type fixtureConfig struct {
	permissionTriggerSelector string
	resultSelector            string
	expectedText              string
	interactionTimeout        time.Duration
	filePickerEnabled         bool
}

func (f fixtureConfig) hasPermissionFlow() bool {
	return f.permissionTriggerSelector != "" && f.resultSelector != "" && f.expectedText != ""
}

func TestMain(m *testing.M) {
	os.Exit(runExampleSuite(m))
}

func runExampleSuite(m *testing.M) (exitCode int) {
	config, fixture, skipReason, err := loadExampleEnvironment(os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "android integration configuration: %v\n", err)
		return 2
	}

	exampleFixture = fixture
	if skipReason != "" {
		exampleSkipReason = skipReason
		return m.Run()
	}

	// Setup may panic when an Android prerequisite is unavailable. Keep the
	// failure visible while still tearing down any successfully created device.
	exitCode = 1
	defer func() {
		recovered := recover()
		if exampleDevice != nil {
			if err := exampleDevice.Teardown(); err != nil {
				fmt.Fprintf(os.Stderr, "android integration teardown: %v\n", err)
				if exitCode == 0 {
					exitCode = 1
				}
			}
		}
		if recovered != nil {
			fmt.Fprintf(os.Stderr, "android integration setup: %v\n", recovered)
			exitCode = 1
		}
	}()

	exampleDevice = adbtest.Setup(config)
	exitCode = m.Run()
	return exitCode
}

func TestWebViewPermissionFlow(t *testing.T) {
	device := requireExampleDevice(t)
	if !exampleFixture.hasPermissionFlow() {
		t.Skip("set all three permission-flow variables to run the optional runtime-permission example")
	}

	// Restarting gives every test a clean application lifecycle while retaining
	// the emulator/device selected by TestMain.
	device.RestartApp(t)

	device.CDP.WaitForSelector(
		t,
		exampleFixture.permissionTriggerSelector,
		exampleFixture.interactionTimeout,
	)
	device.CDP.Click(t, exampleFixture.permissionTriggerSelector)

	// The fixture's click should request one or more Android runtime permissions.
	device.Permissions.GrantAll(t, exampleFixture.interactionTimeout)

	device.CDP.WaitForSelector(
		t,
		exampleFixture.resultSelector,
		exampleFixture.interactionTimeout,
	)
	actual := device.CDP.WaitForText(
		t,
		exampleFixture.resultSelector,
		exampleFixture.expectedText,
		exampleFixture.interactionTimeout,
	)
	if !strings.Contains(actual, exampleFixture.expectedText) {
		t.Fatalf("selector %q contained %q; want text containing %q",
			exampleFixture.resultSelector, actual, exampleFixture.expectedText)
	}
}

func TestWebViewLifecycle(t *testing.T) {
	device := requireExampleDevice(t)
	device.RestartApp(t)
	device.CDP.WaitForSelector(t, "html", exampleFixture.interactionTimeout)
}

func requireExampleDevice(t *testing.T) *adbtest.Device {
	t.Helper()
	if exampleSkipReason != "" {
		t.Skip(exampleSkipReason)
	}
	if exampleDevice == nil {
		t.Fatal("android integration device was not initialized")
	}
	return exampleDevice
}

// TestExampleConfigurationModes exercises both supported setup modes without
// invoking adb or an emulator. It also keeps the example compile-checkable on
// machines that do not have the Android SDK installed.
func TestExampleConfigurationModes(t *testing.T) {
	apkPath := t.TempDir() + "/fixture.apk"
	if err := os.WriteFile(apkPath, []byte("compile-check fixture"), 0o600); err != nil {
		t.Fatal(err)
	}

	base := map[string]string{"ADBTEST_APK": apkPath}

	tests := []struct {
		name   string
		env    map[string]string
		assert func(*testing.T, adbtest.Config)
	}{
		{
			name: "owned AVD safe defaults",
			env: mergeEnvironment(base, map[string]string{
				"ADBTEST_AVD":         "small_phone_api_34",
				"ADBTEST_APP_PROCESS": ":webview",
			}),
			assert: func(t *testing.T, config adbtest.Config) {
				t.Helper()
				if config.AVD != "small_phone_api_34" || config.Serial != "" {
					t.Fatalf("unexpected AVD config: %+v", config)
				}
				if !config.Headless || !config.NoAudio || config.WipeData || !config.NoSnapshot ||
					config.Acceleration != "on" ||
					config.GPU != "auto" || config.Cores != 2 || config.MemoryMB != 0 {
					t.Fatalf("safe AVD defaults were not applied: %+v", config)
				}
				if config.AppProcess != ":webview" {
					t.Fatalf("app process = %q, want :webview", config.AppProcess)
				}
			},
		},
		{
			name: "owned AVD explicit overrides",
			env: mergeEnvironment(base, map[string]string{
				"ADBTEST_AVD":          "Pixel_7",
				"ADBTEST_CORES":        "3",
				"ADBTEST_MEMORY_MB":    "2048",
				"ADBTEST_ACCELERATION": "auto",
				"ADBTEST_HEADLESS":     "false",
				"ADBTEST_NO_AUDIO":     "false",
				"ADBTEST_WIPE_DATA":    "true",
				"ADBTEST_NO_SNAPSHOT":  "false",
				"ADBTEST_GPU":          "swiftshader",
			}),
			assert: func(t *testing.T, config adbtest.Config) {
				t.Helper()
				if config.Headless || config.NoAudio || !config.WipeData || config.NoSnapshot ||
					config.Acceleration != "auto" ||
					config.GPU != "swiftshader" || config.Cores != 3 || config.MemoryMB != 2048 {
					t.Fatalf("explicit AVD overrides were not applied: %+v", config)
				}
			},
		},
		{
			name: "attached serial",
			env: mergeEnvironment(base, map[string]string{
				"ADBTEST_SERIAL": "emulator-5554",
			}),
			assert: func(t *testing.T, config adbtest.Config) {
				t.Helper()
				if config.Serial != "emulator-5554" || config.AVD != "" {
					t.Fatalf("unexpected serial config: %+v", config)
				}
				if config.Headless || config.NoAudio || config.WipeData || config.GPU != "" {
					t.Fatalf("serial mode received AVD-only flags: %+v", config)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config, _, skipReason, err := loadExampleEnvironment(mapEnvironment(test.env))
			if err != nil {
				t.Fatal(err)
			}
			if skipReason != "" {
				t.Fatalf("configuration unexpectedly skipped: %s", skipReason)
			}
			test.assert(t, config)
		})
	}
}

func TestExampleConfigurationRejectsPartialPermissionFlow(t *testing.T) {
	apkPath := t.TempDir() + "/fixture.apk"
	if err := os.WriteFile(apkPath, []byte("compile-check fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := loadExampleEnvironment(mapEnvironment(map[string]string{
		"ADBTEST_APK":                         apkPath,
		"ADBTEST_SERIAL":                      "emulator-5554",
		"ADBTEST_PERMISSION_TRIGGER_SELECTOR": "#request-permission",
	}))
	if err == nil || !strings.Contains(err.Error(), "permission-flow variables") {
		t.Fatalf("partial permission fixture error = %v", err)
	}
}

func TestExampleConfigurationEnablesFilePicker(t *testing.T) {
	apkPath := t.TempDir() + "/fixture.apk"
	if err := os.WriteFile(apkPath, []byte("compile-check fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, fixture, skipReason, err := loadExampleEnvironment(mapEnvironment(map[string]string{
		"ADBTEST_APK":         apkPath,
		"ADBTEST_SERIAL":      "emulator-5554",
		"ADBTEST_FILE_PICKER": "true",
	}))
	if err != nil || skipReason != "" || !fixture.filePickerEnabled {
		t.Fatalf("file-picker configuration = %+v, skip %q, error %v", fixture, skipReason, err)
	}
}

func TestExampleConfigurationRejectsInvalidResourceSettings(t *testing.T) {
	apkPath := t.TempDir() + "/fixture.apk"
	if err := os.WriteFile(apkPath, []byte("compile-check fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "too many cores",
			env: map[string]string{
				"ADBTEST_APK":   apkPath,
				"ADBTEST_AVD":   "small_phone_api_34",
				"ADBTEST_CORES": "65",
			},
			want: "ADBTEST_CORES",
		},
		{
			name: "memory below emulator minimum",
			env: map[string]string{
				"ADBTEST_APK":       apkPath,
				"ADBTEST_AVD":       "small_phone_api_34",
				"ADBTEST_MEMORY_MB": "1024",
			},
			want: "ADBTEST_MEMORY_MB",
		},
		{
			name: "invalid acceleration mode",
			env: map[string]string{
				"ADBTEST_APK":          apkPath,
				"ADBTEST_AVD":          "small_phone_api_34",
				"ADBTEST_ACCELERATION": "sometimes",
			},
			want: "ADBTEST_ACCELERATION",
		},
		{
			name: "AVD resource with serial",
			env: map[string]string{
				"ADBTEST_APK":    apkPath,
				"ADBTEST_SERIAL": "emulator-5554",
				"ADBTEST_CORES":  "2",
			},
			want: "only valid with ADBTEST_AVD",
		},
		{
			name: "acceleration with serial",
			env: map[string]string{
				"ADBTEST_APK":          apkPath,
				"ADBTEST_SERIAL":       "emulator-5554",
				"ADBTEST_ACCELERATION": "on",
			},
			want: "only valid with ADBTEST_AVD",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := loadExampleEnvironment(mapEnvironment(test.env))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadExampleEnvironment() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func loadExampleEnvironment(getenv func(string) string) (adbtest.Config, fixtureConfig, string, error) {
	value := func(name string) string {
		return strings.TrimSpace(getenv(name))
	}

	apk := value("ADBTEST_APK")
	avd := value("ADBTEST_AVD")
	serial := value("ADBTEST_SERIAL")
	triggerSelector := value("ADBTEST_PERMISSION_TRIGGER_SELECTOR")
	resultSelector := value("ADBTEST_RESULT_SELECTOR")
	expectedText := value("ADBTEST_EXPECTED_TEXT")

	missing := make([]string, 0, 2)
	if apk == "" {
		missing = append(missing, "ADBTEST_APK")
	}
	if avd == "" && serial == "" {
		missing = append(missing, "ADBTEST_AVD or ADBTEST_SERIAL")
	}
	if len(missing) > 0 {
		return adbtest.Config{}, fixtureConfig{},
			"set the Android integration fixture environment (missing: " + strings.Join(missing, ", ") + ")", nil
	}
	if avd != "" && serial != "" {
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf(
			"ADBTEST_AVD and ADBTEST_SERIAL are mutually exclusive",
		)
	}

	permissionValues := 0
	for _, configured := range []bool{triggerSelector != "", resultSelector != "", expectedText != ""} {
		if configured {
			permissionValues++
		}
	}
	if permissionValues != 0 && permissionValues != 3 {
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf(
			"permission-flow variables must be set together: ADBTEST_PERMISSION_TRIGGER_SELECTOR, ADBTEST_RESULT_SELECTOR, and ADBTEST_EXPECTED_TEXT",
		)
	}

	info, err := os.Stat(apk)
	if err != nil {
		if os.IsNotExist(err) {
			return adbtest.Config{}, fixtureConfig{}, "APK fixture does not exist: " + apk, nil
		}
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("inspect ADBTEST_APK %q: %w", apk, err)
	}
	if !info.Mode().IsRegular() {
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_APK %q is not a regular file", apk)
	}

	interactionTimeout, err := positiveDuration(value("ADBTEST_INTERACTION_TIMEOUT"), 30*time.Second)
	if err != nil {
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_INTERACTION_TIMEOUT: %w", err)
	}
	bootTimeout, err := optionalPositiveDuration(value("ADBTEST_BOOT_TIMEOUT"))
	if err != nil {
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_BOOT_TIMEOUT: %w", err)
	}
	appTimeout, err := optionalPositiveDuration(value("ADBTEST_APP_TIMEOUT"))
	if err != nil {
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_APP_TIMEOUT: %w", err)
	}
	cdpPort, err := optionalPort(value("ADBTEST_CDP_PORT"))
	if err != nil {
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_CDP_PORT: %w", err)
	}
	filePickerEnabled, err := boolWithDefault(value("ADBTEST_FILE_PICKER"), false)
	if err != nil {
		return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_FILE_PICKER: %w", err)
	}

	config := adbtest.Config{
		Serial:      serial,
		APK:         apk,
		AppPackage:  value("ADBTEST_APP_PACKAGE"),
		AppProcess:  value("ADBTEST_APP_PROCESS"),
		AppActivity: value("ADBTEST_APP_ACTIVITY"),
		BootTimeout: bootTimeout,
		AppTimeout:  appTimeout,
		CDPPort:     cdpPort,
	}
	if avd != "" {
		config = adbtest.HeadlessAVD(avd, apk)
		config.AppPackage = value("ADBTEST_APP_PACKAGE")
		config.AppProcess = value("ADBTEST_APP_PROCESS")
		config.AppActivity = value("ADBTEST_APP_ACTIVITY")
		config.BootTimeout = bootTimeout
		config.AppTimeout = appTimeout
		config.CDPPort = cdpPort
		if gpu := value("ADBTEST_GPU"); gpu != "" {
			config.GPU = gpu
		}
		if acceleration := value("ADBTEST_ACCELERATION"); acceleration != "" {
			if acceleration != "auto" && acceleration != "on" && acceleration != "off" {
				return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf(
					"ADBTEST_ACCELERATION: must be auto, on, or off",
				)
			}
			config.Acceleration = acceleration
		}
		config.Cores, err = intWithDefault(value("ADBTEST_CORES"), config.Cores, 0, 64)
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_CORES: %w", err)
		}
		config.MemoryMB, err = intWithDefault(value("ADBTEST_MEMORY_MB"), config.MemoryMB, 0, 8192)
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_MEMORY_MB: %w", err)
		}
		if config.MemoryMB != 0 && config.MemoryMB < 1536 {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_MEMORY_MB: must be zero or at least 1536")
		}
		config.Headless, err = boolWithDefault(value("ADBTEST_HEADLESS"), config.Headless)
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_HEADLESS: %w", err)
		}
		config.NoAudio, err = boolWithDefault(value("ADBTEST_NO_AUDIO"), config.NoAudio)
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_NO_AUDIO: %w", err)
		}
		config.WipeData, err = boolWithDefault(value("ADBTEST_WIPE_DATA"), config.WipeData)
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_WIPE_DATA: %w", err)
		}
		config.NoSnapshot, err = boolWithDefault(value("ADBTEST_NO_SNAPSHOT"), config.NoSnapshot)
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_NO_SNAPSHOT: %w", err)
		}
	} else {
		for _, name := range []string{
			"ADBTEST_ACCELERATION",
			"ADBTEST_GPU",
			"ADBTEST_CORES",
			"ADBTEST_MEMORY_MB",
			"ADBTEST_HEADLESS",
			"ADBTEST_NO_AUDIO",
			"ADBTEST_WIPE_DATA",
			"ADBTEST_NO_SNAPSHOT",
		} {
			if value(name) != "" {
				return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("%s is only valid with ADBTEST_AVD", name)
			}
		}
	}

	return config, fixtureConfig{
		permissionTriggerSelector: triggerSelector,
		resultSelector:            resultSelector,
		expectedText:              expectedText,
		interactionTimeout:        interactionTimeout,
		filePickerEnabled:         filePickerEnabled,
	}, "", nil
}

func positiveDuration(raw string, defaultValue time.Duration) (time.Duration, error) {
	if raw == "" {
		return defaultValue, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if duration <= 0 {
		return 0, fmt.Errorf("must be greater than zero")
	}
	return duration, nil
}

func optionalPositiveDuration(raw string) (time.Duration, error) {
	if raw == "" {
		return 0, nil
	}
	return positiveDuration(raw, 0)
}

func optionalPort(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("must be between 1 and 65535")
	}
	return port, nil
}

func intWithDefault(raw string, defaultValue, minimum, maximum int) (int, error) {
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if value < minimum || value > maximum {
		return 0, fmt.Errorf("must be between %d and %d", minimum, maximum)
	}
	return value, nil
}

func boolWithDefault(raw string, defaultValue bool) (bool, error) {
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, err
	}
	return value, nil
}

func mapEnvironment(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}

func mergeEnvironment(base, overrides map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(overrides))
	for name, value := range base {
		merged[name] = value
	}
	for name, value := range overrides {
		merged[name] = value
	}
	return merged
}
