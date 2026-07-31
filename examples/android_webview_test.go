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

	base := map[string]string{
		"ADBTEST_APK":                         apkPath,
		"ADBTEST_PERMISSION_TRIGGER_SELECTOR": "#request-permission",
		"ADBTEST_RESULT_SELECTOR":             "#permission-status",
		"ADBTEST_EXPECTED_TEXT":               "granted",
	}

	tests := []struct {
		name   string
		env    map[string]string
		assert func(*testing.T, adbtest.Config)
	}{
		{
			name: "owned AVD",
			env: mergeEnvironment(base, map[string]string{
				"ADBTEST_AVD":       "Pixel_API_35",
				"ADBTEST_HEADLESS":  "true",
				"ADBTEST_NO_AUDIO":  "true",
				"ADBTEST_WIPE_DATA": "true",
				"ADBTEST_GPU":       "auto",
			}),
			assert: func(t *testing.T, config adbtest.Config) {
				t.Helper()
				if config.AVD != "Pixel_API_35" || config.Serial != "" {
					t.Fatalf("unexpected AVD config: %+v", config)
				}
				if !config.Headless || !config.NoAudio || !config.WipeData {
					t.Fatalf("AVD flags were not applied: %+v", config)
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

	missing := make([]string, 0, 5)
	for _, required := range []struct {
		name       string
		configured bool
	}{
		{name: "ADBTEST_APK", configured: apk != ""},
		{name: "ADBTEST_PERMISSION_TRIGGER_SELECTOR", configured: triggerSelector != ""},
		{name: "ADBTEST_RESULT_SELECTOR", configured: resultSelector != ""},
		{name: "ADBTEST_EXPECTED_TEXT", configured: expectedText != ""},
	} {
		if !required.configured {
			missing = append(missing, required.name)
		}
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

	interactionTimeout, err := positiveDuration(value("ADBTEST_INTERACTION_TIMEOUT"), 15*time.Second)
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

	config := adbtest.Config{
		AVD:         avd,
		Serial:      serial,
		APK:         apk,
		AppPackage:  value("ADBTEST_APP_PACKAGE"),
		AppActivity: value("ADBTEST_APP_ACTIVITY"),
		BootTimeout: bootTimeout,
		AppTimeout:  appTimeout,
		CDPPort:     cdpPort,
	}
	if avd != "" {
		config.GPU = value("ADBTEST_GPU")
		config.Headless, err = optionalBool(value("ADBTEST_HEADLESS"))
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_HEADLESS: %w", err)
		}
		config.NoAudio, err = optionalBool(value("ADBTEST_NO_AUDIO"))
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_NO_AUDIO: %w", err)
		}
		config.WipeData, err = optionalBool(value("ADBTEST_WIPE_DATA"))
		if err != nil {
			return adbtest.Config{}, fixtureConfig{}, "", fmt.Errorf("ADBTEST_WIPE_DATA: %w", err)
		}
	} else {
		for _, name := range []string{
			"ADBTEST_GPU",
			"ADBTEST_HEADLESS",
			"ADBTEST_NO_AUDIO",
			"ADBTEST_WIPE_DATA",
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

func optionalBool(raw string) (bool, error) {
	if raw == "" {
		return false, nil
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
