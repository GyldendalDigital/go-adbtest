package adbtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

func TestNormalizeAVDProfileUsesLightweightHostNativeDefaults(t *testing.T) {
	got, err := normalizeAVDProfile(AVDProfile{Name: "go_adbtest_api_35", APILevel: 35}, "linux", "amd64")
	if err != nil {
		t.Fatalf("normalizeAVDProfile() error: %v", err)
	}
	if got.Device != "small_phone" || got.Target != "google_apis" || got.Arch != "x86_64" {
		t.Fatalf("normalizeAVDProfile() = %+v", got)
	}
	if image := avdSystemImagePackage(&got); image != "system-images;android-35;google_apis;x86_64" {
		t.Fatalf("system image = %q", image)
	}
}

func TestNormalizeAVDProfileMapsARM64(t *testing.T) {
	got, err := normalizeAVDProfile(AVDProfile{Name: "arm_test", APILevel: 35}, "darwin", "arm64")
	if err != nil {
		t.Fatalf("normalizeAVDProfile() error: %v", err)
	}
	if got.Arch != "arm64-v8a" {
		t.Fatalf("Arch = %q, want arm64-v8a", got.Arch)
	}
}

func TestNormalizeAVDProfileAcceptsBuiltInDeviceIDs(t *testing.T) {
	got, err := normalizeAVDProfile(AVDProfile{
		Name:     "tablet_test",
		APILevel: 35,
		Device:   "10.1in WXGA (Tablet)",
	}, "linux", "amd64")
	if err != nil {
		t.Fatalf("normalizeAVDProfile() error: %v", err)
	}
	if got.Device != "10.1in WXGA (Tablet)" {
		t.Fatalf("Device = %q", got.Device)
	}
}

func TestNormalizeAVDProfileRejectsUnsafeProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profile  AVDProfile
		hostOS   string
		hostArch string
		want     string
	}{
		{name: "missing name", profile: AVDProfile{APILevel: 35}, hostOS: "linux", hostArch: "amd64", want: "name"},
		{name: "path name", profile: AVDProfile{Name: "../phone", APILevel: 35}, hostOS: "linux", hostArch: "amd64", want: "name"},
		{name: "missing API", profile: AVDProfile{Name: "phone"}, hostOS: "linux", hostArch: "amd64", want: "API level"},
		{name: "play target", profile: AVDProfile{Name: "phone", APILevel: 35, Target: "google_apis_playstore"}, hostOS: "linux", hostArch: "amd64", want: "not supported"},
		{name: "ATD target", profile: AVDProfile{Name: "phone", APILevel: 35, Target: "aosp_atd"}, hostOS: "linux", hostArch: "amd64", want: "not supported"},
		{name: "path target", profile: AVDProfile{Name: "phone", APILevel: 35, Target: ".."}, hostOS: "linux", hostArch: "amd64", want: "invalid system-image target"},
		{name: "cross architecture", profile: AVDProfile{Name: "phone", APILevel: 35, Arch: "arm64-v8a"}, hostOS: "linux", hostArch: "amd64", want: "does not match"},
		{name: "unsupported OS", profile: AVDProfile{Name: "phone", APILevel: 35}, hostOS: "freebsd", hostArch: "amd64", want: "not supported"},
		{name: "Windows provisioning", profile: AVDProfile{Name: "phone", APILevel: 35}, hostOS: "windows", hostArch: "amd64", want: "requires Linux or macOS"},
		{name: "unsupported host", profile: AVDProfile{Name: "phone", APILevel: 35}, hostOS: "linux", hostArch: "riscv64", want: "no supported"},
		{name: "unsupported Linux ARM", profile: AVDProfile{Name: "phone", APILevel: 35}, hostOS: "linux", hostArch: "arm64", want: "does not support"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeAVDProfile(test.profile, test.hostOS, test.hostArch)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalizeAVDProfile() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEnsureAVDRejectsCancelledContextBeforeReuse(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	deps := fakeAVDDependencies(root, home)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ensureAVDWithDependencies(ctx, AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EnsureAVD() error = %v, want context.Canceled", err)
	}
}

func TestEnsureAVDRequiresStableSDKEnvironment(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	deps := fakeAVDDependencies(root, home)
	deps.getenv = func(string) string { return "" }

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err == nil || !strings.Contains(err.Error(), "ANDROID_HOME is required") {
		t.Fatalf("EnsureAVD() error = %v, want stable SDK environment", err)
	}
}

func TestEnsureAVDReusesOnlyMatchingProfileWithoutMutation(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	installImage(t, root, 35, "google_apis", "x86_64")
	writeAVD(t, home, "go_test", 35, "google_apis", "x86_64", "small_phone", "\r\n")
	deps := fakeAVDDependencies(root, home)
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		if isListAVDs(path, args) {
			return androidsdk.CommandResult{Stdout: "go_test\n"}, nil
		}
		t.Fatalf("matching AVD unexpectedly ran mutating command: %s %v", path, args)
		return androidsdk.CommandResult{}, nil
	}

	got, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err != nil {
		t.Fatalf("EnsureAVD() error: %v", err)
	}
	if got.Created || got.Name != "go_test" || got.SystemImage != "system-images;android-35;google_apis;x86_64" {
		t.Fatalf("EnsureAVD() = %+v", got)
	}
}

func TestEnsureAVDRejectsExistingProfileMismatch(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	installImage(t, root, 35, "google_apis", "x86_64")
	writeAVD(t, home, "go_test", 35, "google_apis_playstore", "x86_64", "pixel_7", "\n")
	deps := fakeAVDDependencies(root, home)
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		if isListAVDs(path, args) {
			return androidsdk.CommandResult{Stdout: "go_test\n"}, nil
		}
		return androidsdk.CommandResult{}, fmt.Errorf("unexpected command")
	}

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err == nil || !strings.Contains(err.Error(), "does not match") || !strings.Contains(err.Error(), "choose another Name") {
		t.Fatalf("EnsureAVD() error = %v, want safe mismatch", err)
	}
}

func TestVerifyAVDConfigUsesRealARMCPUAndABIValues(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	installImage(t, root, 35, "google_apis", "arm64-v8a")
	writeAVD(t, home, "arm_test", 35, "google_apis", "arm64-v8a", "small_phone", "\n")
	configPath := filepath.Join(home, "arm_test.avd", "config.ini")
	profile, err := normalizeAVDProfile(AVDProfile{Name: "arm_test", APILevel: 35}, "darwin", "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAVDConfig(configPath, root, &profile); err != nil {
		t.Fatalf("verifyAVDConfig() rejected realistic ARM metadata: %v", err)
	}
}

func TestVerifyAVDConfigRejectsAbsoluteImageFromAnotherSDK(t *testing.T) {
	root, otherRoot, home := t.TempDir(), t.TempDir(), t.TempDir()
	installImage(t, root, 35, "google_apis", "x86_64")
	installImage(t, otherRoot, 35, "google_apis", "x86_64")
	writeAVD(t, home, "go_test", 35, "google_apis", "x86_64", "small_phone", "\n")
	configPath := filepath.Join(home, "go_test.avd", "config.ini")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	otherImage := filepath.Join(otherRoot, "system-images", "android-35", "google_apis", "x86_64")
	data = []byte(strings.Replace(string(data), "system-images/android-35/google_apis/x86_64/", otherImage, 1))
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := normalizeAVDProfile(AVDProfile{Name: "go_test", APILevel: 35}, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAVDConfig(configPath, root, &profile); err == nil || !strings.Contains(err.Error(), "system image directory") {
		t.Fatalf("verifyAVDConfig() error = %v, want SDK-root mismatch", err)
	}
}

func TestVerifyAVDConfigCanonicalizesSymlinkedSDKWithMissingImage(t *testing.T) {
	realRoot, home := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "sdk-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("create SDK symlink: %v", err)
	}
	writeAVD(t, home, "go_test", 35, "google_apis", "x86_64", "small_phone", "\n")
	configPath := filepath.Join(home, "go_test.avd", "config.ini")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	linkedImage := filepath.Join(link, "system-images", "android-35", "google_apis", "x86_64")
	data = []byte(strings.Replace(string(data), "system-images/android-35/google_apis/x86_64/", linkedImage, 1))
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	profile, err := normalizeAVDProfile(AVDProfile{Name: "go_test", APILevel: 35}, "linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAVDConfig(configPath, realRoot, &profile); err != nil {
		t.Fatalf("verifyAVDConfig() rejected equivalent symlinked root: %v", err)
	}
}

func TestEnsureAVDMissingImageRequiresExplicitInstall(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	deps := fakeAVDDependencies(root, home)
	var calls [][]string
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		calls = append(calls, append([]string{path}, args...))
		if isListAVDs(path, args) {
			return androidsdk.CommandResult{}, nil
		}
		if strings.HasSuffix(path, "avdmanager") {
			return androidsdk.CommandResult{Stdout: "small_phone\r\nmedium_phone\r\n"}, nil
		}
		return androidsdk.CommandResult{}, fmt.Errorf("unexpected command")
	}

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err == nil || !strings.Contains(err.Error(), "InstallSystemImage true") ||
		!strings.Contains(err.Error(), "--licenses") || !strings.Contains(err.Error(), "system-images;android-35;google_apis;x86_64") {
		t.Fatalf("EnsureAVD() error = %v, want explicit install hint", err)
	}
	if len(calls) != 1 || calls[0][1] != "-list-avds" {
		t.Fatalf("commands = %v, want only authoritative AVD listing", calls)
	}
}

func TestEnsureAVDPreflightsAVDManagerBeforeInstallingImage(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	deps := fakeAVDDependencies(root, home)
	deps.findTool = func(_ string, name string) (string, error) {
		if name == "avdmanager" {
			return "", errors.New("avdmanager missing")
		}
		return filepath.Join("fake-tools", name), nil
	}
	var calls [][]string
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		calls = append(calls, append([]string{path}, args...))
		if isListAVDs(path, args) {
			return androidsdk.CommandResult{}, nil
		}
		return androidsdk.CommandResult{}, fmt.Errorf("unexpected command")
	}

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{
		Name:               "go_test",
		APILevel:           35,
		InstallSystemImage: true,
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "avdmanager missing") {
		t.Fatalf("EnsureAVD() error = %v, want missing avdmanager", err)
	}
	if len(calls) != 1 || calls[0][1] != "-list-avds" {
		t.Fatalf("commands = %v, want no image installation before tool preflight", calls)
	}
}

func TestEnsureAVDRejectsPartialSystemImage(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	partial := filepath.Join(root, "system-images", "android-35", "google_apis", "x86_64")
	if err := os.MkdirAll(partial, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "package.xml"), []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	deps := fakeAVDDependencies(root, home)
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		if isListAVDs(path, args) {
			return androidsdk.CommandResult{}, nil
		}
		return androidsdk.CommandResult{}, fmt.Errorf("unexpected command")
	}

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err == nil || !strings.Contains(err.Error(), "system image") || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("EnsureAVD() error = %v, want incomplete image rejection", err)
	}
}

func TestEnsureAVDInstallsAndCreatesWithoutForce(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	deps := fakeAVDDependencies(root, home)
	var progress bytes.Buffer
	var calls []recordedAVDCommand
	created := false
	deps.run = func(_ context.Context, path string, args []string, stdin io.Reader, output io.Writer) (androidsdk.CommandResult, error) {
		call := recordedAVDCommand{path: path, args: append([]string(nil), args...)}
		if stdin != nil {
			data, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatal(err)
			}
			call.stdin = string(data)
		}
		calls = append(calls, call)
		switch {
		case isListAVDs(path, args):
			if created {
				return androidsdk.CommandResult{Stdout: "go_test\n"}, nil
			}
			return androidsdk.CommandResult{}, nil
		case strings.HasSuffix(path, "avdmanager") && reflect.DeepEqual(args, []string{"list", "device", "-c"}):
			return androidsdk.CommandResult{Stdout: "small_phone\n"}, nil
		case strings.HasSuffix(path, "sdkmanager"):
			installImage(t, root, 35, "google_apis", "x86_64")
			if output != nil {
				_, _ = io.WriteString(output, "downloaded\n")
			}
			return androidsdk.CommandResult{Stderr: "deprecation warning"}, nil
		case strings.HasSuffix(path, "avdmanager") && len(args) >= 2 && args[0] == "create":
			writeAVD(t, home, "go_test", 35, "google_apis", "x86_64", "small_phone", "\n")
			created = true
			return androidsdk.CommandResult{Stderr: "unrelated warning"}, nil
		default:
			return androidsdk.CommandResult{}, fmt.Errorf("unexpected command: %s %v", path, args)
		}
	}

	got, err := ensureAVDWithDependencies(context.Background(), AVDProfile{
		Name:               "go_test",
		APILevel:           35,
		InstallSystemImage: true,
		Progress:           &progress,
	}, deps)
	if err != nil {
		t.Fatalf("EnsureAVD() error: %v", err)
	}
	if !got.Created {
		t.Fatalf("EnsureAVD() = %+v, want Created", got)
	}
	if progress.String() != "downloaded\n" {
		t.Fatalf("progress = %q", progress.String())
	}
	if len(calls) != 5 {
		t.Fatalf("commands = %#v, want AVD list, install, profile list, create, AVD confirmation", calls)
	}
	create := calls[3]
	if create.stdin != "no\n" {
		t.Fatalf("create stdin = %q, want no newline", create.stdin)
	}
	joined := strings.Join(create.args, " ")
	if strings.Contains(joined, "--force") || strings.Contains(joined, " -f ") {
		t.Fatalf("create args overwrite an AVD: %v", create.args)
	}
	wantCreate := []string{
		"create", "avd", "--name", "go_test",
		"--package", "system-images;android-35;google_apis;x86_64",
		"--device", "small_phone",
	}
	if !reflect.DeepEqual(create.args, wantCreate) {
		t.Fatalf("create args = %v, want %v", create.args, wantCreate)
	}
}

func TestEnsureAVDReportsLicenceHintWhenInstallFails(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	deps := fakeAVDDependencies(root, home)
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		if isListAVDs(path, args) {
			return androidsdk.CommandResult{}, nil
		}
		if strings.HasSuffix(path, "avdmanager") {
			return androidsdk.CommandResult{Stdout: "small_phone\n"}, nil
		}
		return androidsdk.CommandResult{Stderr: "License android-sdk-license not accepted"}, errors.New("exit status 1")
	}

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{
		Name:               "go_test",
		APILevel:           35,
		InstallSystemImage: true,
	}, deps)
	if err == nil || !strings.Contains(err.Error(), "not accepted") || !strings.Contains(err.Error(), "--licenses") {
		t.Fatalf("EnsureAVD() error = %v, want licence remediation", err)
	}
}

func TestEnsureAVDRequiresAvailableHardwareProfile(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	installImage(t, root, 35, "google_apis", "x86_64")
	deps := fakeAVDDependencies(root, home)
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		if isListAVDs(path, args) {
			return androidsdk.CommandResult{}, nil
		}
		return androidsdk.CommandResult{Stdout: "medium_phone\n"}, nil
	}

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err == nil || !strings.Contains(err.Error(), `hardware profile "small_phone" is unavailable`) {
		t.Fatalf("EnsureAVD() error = %v, want missing profile", err)
	}
}

func TestEnsureAVDConfirmsCreatedMetadata(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	installImage(t, root, 35, "google_apis", "x86_64")
	deps := fakeAVDDependencies(root, home)
	created := false
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		if isListAVDs(path, args) {
			if created {
				return androidsdk.CommandResult{Stdout: "go_test\n"}, nil
			}
			return androidsdk.CommandResult{}, nil
		}
		if len(args) > 0 && args[0] == "list" {
			return androidsdk.CommandResult{Stdout: "small_phone\n"}, nil
		}
		if len(args) > 0 && args[0] == "create" {
			created = true
		}
		return androidsdk.CommandResult{}, nil
	}

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err == nil || !strings.Contains(err.Error(), "config.ini was not found") {
		t.Fatalf("EnsureAVD() error = %v, want creation verification failure", err)
	}
}

func TestEnsureAVDRecoversWhenAnotherProcessCreatesMatchingAVD(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	installImage(t, root, 35, "google_apis", "x86_64")
	deps := fakeAVDDependencies(root, home)
	created := false
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		switch {
		case isListAVDs(path, args):
			if created {
				return androidsdk.CommandResult{Stdout: "go_test\n"}, nil
			}
			return androidsdk.CommandResult{}, nil
		case strings.HasSuffix(path, "avdmanager") && len(args) > 0 && args[0] == "list":
			return androidsdk.CommandResult{Stdout: "small_phone\n"}, nil
		case strings.HasSuffix(path, "avdmanager") && len(args) > 0 && args[0] == "create":
			writeAVD(t, home, "go_test", 35, "google_apis", "x86_64", "small_phone", "\n")
			created = true
			return androidsdk.CommandResult{Stderr: "AVD already exists"}, errors.New("exit status 1")
		default:
			return androidsdk.CommandResult{}, fmt.Errorf("unexpected command: %s %v", path, args)
		}
	}

	got, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err != nil {
		t.Fatalf("EnsureAVD() error: %v", err)
	}
	if got.Created {
		t.Fatalf("EnsureAVD() = %+v, want conservative reused result after create collision", got)
	}
}

func TestEnsureAVDLockSerializesAndReleasesRegistryEntry(t *testing.T) {
	key := t.Name()
	firstRelease, err := acquireEnsureAVDLock(context.Background(), key)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	secondAcquired := make(chan func(), 1)
	secondErr := make(chan error, 1)
	go func() {
		release, acquireErr := acquireEnsureAVDLock(context.Background(), key)
		if acquireErr != nil {
			secondErr <- acquireErr
			return
		}
		secondAcquired <- release
	}()

	select {
	case <-secondAcquired:
		t.Fatal("second acquisition did not wait")
	case err := <-secondErr:
		t.Fatalf("second acquire: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	firstRelease()

	var secondRelease func()
	select {
	case secondRelease = <-secondAcquired:
	case err := <-secondErr:
		t.Fatalf("second acquire: %v", err)
	case <-time.After(time.Second):
		t.Fatal("second acquisition remained blocked after release")
	}
	secondRelease()

	ensureAVDLockRegistry.Lock()
	_, retained := ensureAVDLockRegistry.locks[key]
	ensureAVDLockRegistry.Unlock()
	if retained {
		t.Fatal("released lock remained in the registry")
	}
}

func TestAVDHeadlessConfigUsesSafeRuntimeFlags(t *testing.T) {
	got := (AVD{Name: "go_test"}).HeadlessConfig("bin/app.apk")
	want := HeadlessAVD("go_test", "bin/app.apk")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HeadlessConfig() = %+v, want %+v", got, want)
	}
	if !got.Headless || !got.NoAudio || !got.NoSnapshot || got.Cores != 2 || got.GPU != "auto" || got.Acceleration != "on" {
		t.Fatalf("HeadlessConfig() lost safe flags: %+v", got)
	}
}

type recordedAVDCommand struct {
	path  string
	args  []string
	stdin string
}

func isListAVDs(path string, args []string) bool {
	return strings.HasSuffix(path, "emulator") && reflect.DeepEqual(args, []string{"-list-avds"})
}

func fakeAVDDependencies(root, home string) avdDependencies {
	return avdDependencies{
		hostArch: "amd64",
		hostOS:   "linux",
		getenv: func(name string) string {
			if name == "ANDROID_HOME" {
				return root
			}
			return ""
		},
		resolveSDKRoot: func(string) (string, error) {
			return root, nil
		},
		findTool: func(_ string, name string) (string, error) {
			return filepath.Join("fake-tools", name), nil
		},
		avdHomes: func() ([]string, error) {
			return []string{home}, nil
		},
		run: func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error) {
			return androidsdk.CommandResult{}, errors.New("unexpected command")
		},
	}
}

func installImage(t *testing.T, root string, api int, target, arch string) {
	t.Helper()
	directory := filepath.Join(root, "system-images", fmt.Sprintf("android-%d", api), target, arch)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "package.xml"), []byte("package"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"source.properties", "system.img", "ramdisk.img"} {
		if err := os.WriteFile(filepath.Join(directory, marker), []byte(marker), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeAVD(t *testing.T, home, name string, api int, target, arch, device, newline string) {
	t.Helper()
	avdDirectory := filepath.Join(home, name+".avd")
	if err := os.MkdirAll(avdDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := "path=" + avdDirectory + newline + "target=android-" + fmt.Sprint(api) + newline
	if err := os.WriteFile(filepath.Join(home, name+".ini"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	config := strings.Join([]string{
		"AvdId=" + name,
		fmt.Sprintf("target=android-%d", api),
		"image.sysdir.1=system-images/android-" + fmt.Sprint(api) + "/" + target + "/" + arch + "/",
		"tag.id=" + target,
		"abi.type=" + arch,
		"hw.cpu.arch=" + emulatorCPUArch(arch),
		"hw.device.name=" + device,
		"",
	}, newline)
	if err := os.WriteFile(filepath.Join(avdDirectory, "config.ini"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}
