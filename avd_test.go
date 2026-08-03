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

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

func TestNormalizeAVDProfileUsesLightweightHostNativeDefaults(t *testing.T) {
	got, err := normalizeAVDProfile(AVDProfile{Name: "go_adbtest_api_35", APILevel: 35}, "amd64")
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
	got, err := normalizeAVDProfile(AVDProfile{Name: "arm_test", APILevel: 35}, "arm64")
	if err != nil {
		t.Fatalf("normalizeAVDProfile() error: %v", err)
	}
	if got.Arch != "arm64-v8a" {
		t.Fatalf("Arch = %q, want arm64-v8a", got.Arch)
	}
}

func TestNormalizeAVDProfileRejectsUnsafeProfiles(t *testing.T) {
	tests := []struct {
		name     string
		profile  AVDProfile
		hostArch string
		want     string
	}{
		{name: "missing name", profile: AVDProfile{APILevel: 35}, hostArch: "amd64", want: "name"},
		{name: "path name", profile: AVDProfile{Name: "../phone", APILevel: 35}, hostArch: "amd64", want: "name"},
		{name: "missing API", profile: AVDProfile{Name: "phone"}, hostArch: "amd64", want: "API level"},
		{name: "play target", profile: AVDProfile{Name: "phone", APILevel: 35, Target: "google_apis_playstore"}, hostArch: "amd64", want: "not supported"},
		{name: "ATD target", profile: AVDProfile{Name: "phone", APILevel: 35, Target: "aosp_atd"}, hostArch: "amd64", want: "not supported"},
		{name: "cross architecture", profile: AVDProfile{Name: "phone", APILevel: 35, Arch: "arm64-v8a"}, hostArch: "amd64", want: "does not match"},
		{name: "unsupported host", profile: AVDProfile{Name: "phone", APILevel: 35}, hostArch: "riscv64", want: "no supported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeAVDProfile(test.profile, test.hostArch)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("normalizeAVDProfile() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestEnsureAVDReusesOnlyMatchingProfileWithoutMutation(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	installImage(t, root, 35, "google_apis", "x86_64")
	writeAVD(t, home, "go_test", 35, "google_apis", "x86_64", "small_phone", "\r\n")
	deps := fakeAVDDependencies(root, home)
	deps.run = func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error) {
		t.Fatal("matching AVD unexpectedly ran an external command")
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

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err == nil || !strings.Contains(err.Error(), "does not match") || !strings.Contains(err.Error(), "choose another Name") {
		t.Fatalf("EnsureAVD() error = %v, want safe mismatch", err)
	}
}

func TestEnsureAVDMissingImageRequiresExplicitInstall(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	deps := fakeAVDDependencies(root, home)
	var calls [][]string
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		calls = append(calls, append([]string{path}, args...))
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
	if len(calls) != 1 || calls[0][1] != "list" {
		t.Fatalf("commands = %v, want only hardware-profile listing", calls)
	}
}

func TestEnsureAVDInstallsAndCreatesWithoutForce(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	deps := fakeAVDDependencies(root, home)
	var progress bytes.Buffer
	var calls []recordedAVDCommand
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
	if len(calls) != 3 {
		t.Fatalf("commands = %#v, want list, install, create", calls)
	}
	create := calls[2]
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
	deps.run = func(context.Context, string, []string, io.Reader, io.Writer) (androidsdk.CommandResult, error) {
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
	deps.run = func(_ context.Context, path string, args []string, _ io.Reader, _ io.Writer) (androidsdk.CommandResult, error) {
		if len(args) > 0 && args[0] == "list" {
			return androidsdk.CommandResult{Stdout: "small_phone\n"}, nil
		}
		return androidsdk.CommandResult{}, nil
	}

	_, err := ensureAVDWithDependencies(context.Background(), AVDProfile{Name: "go_test", APILevel: 35}, deps)
	if err == nil || !strings.Contains(err.Error(), "config.ini was not found") {
		t.Fatalf("EnsureAVD() error = %v, want creation verification failure", err)
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

func fakeAVDDependencies(root, home string) avdDependencies {
	return avdDependencies{
		hostArch: "amd64",
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
		"hw.cpu.arch=" + arch,
		"hw.device.name=" + device,
		"",
	}, newline)
	if err := os.WriteFile(filepath.Join(avdDirectory, "config.ini"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
}
