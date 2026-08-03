package androidsdk

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveSDKRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANDROID_HOME", root)
	t.Setenv("ANDROID_SDK_ROOT", root)

	got, err := ResolveSDKRoot("")
	if err != nil {
		t.Fatalf("ResolveSDKRoot() error: %v", err)
	}
	want, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ResolveSDKRoot() = %q, want %q", got, want)
	}
}

func TestResolveSDKRootRejectsConflictingEnvironment(t *testing.T) {
	t.Setenv("ANDROID_HOME", t.TempDir())
	t.Setenv("ANDROID_SDK_ROOT", t.TempDir())

	_, err := ResolveSDKRoot("")
	if err == nil || !strings.Contains(err.Error(), "different SDKs") {
		t.Fatalf("ResolveSDKRoot() error = %v, want conflicting SDK roots", err)
	}
}

func TestResolveSDKRootCanonicalizesSymlink(t *testing.T) {
	realRoot := t.TempDir()
	link := filepath.Join(t.TempDir(), "sdk-link")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Skipf("create SDK symlink: %v", err)
	}
	t.Setenv("ANDROID_HOME", link)
	t.Setenv("ANDROID_SDK_ROOT", "")

	got, err := ResolveSDKRoot("")
	if err != nil {
		t.Fatalf("ResolveSDKRoot() error: %v", err)
	}
	if got != realRoot {
		t.Fatalf("ResolveSDKRoot() = %q, want canonical %q", got, realRoot)
	}
}

func TestFindToolPrefersLatestCommandLineTools(t *testing.T) {
	root := t.TempDir()
	oldTool := createTool(t, root, "cmdline-tools", "9.0", "bin", "avdmanager")
	newTool := createTool(t, root, "cmdline-tools", "10.0", "bin", "avdmanager")

	got, err := FindTool(root, "avdmanager")
	if err != nil {
		t.Fatalf("FindTool() error: %v", err)
	}
	if got == oldTool || got != newTool {
		t.Fatalf("FindTool() = %q, want %q", got, newTool)
	}
}

func TestFindToolPrefersLatestAlias(t *testing.T) {
	root := t.TempDir()
	latest := createTool(t, root, "cmdline-tools", "latest", "bin", "sdkmanager")
	_ = createTool(t, root, "cmdline-tools", "99.0", "bin", "sdkmanager")

	got, err := FindTool(root, "sdkmanager")
	if err != nil {
		t.Fatalf("FindTool() error: %v", err)
	}
	if got != latest {
		t.Fatalf("FindTool() = %q, want latest alias %q", got, latest)
	}
}

func TestFindToolDoesNotMixConfiguredSDKWithPATH(t *testing.T) {
	root := t.TempDir()
	pathRoot := t.TempDir()
	createTool(t, pathRoot, "avdmanager")
	t.Setenv("PATH", pathRoot)

	_, err := FindTool(root, "avdmanager")
	if err == nil || !strings.Contains(err.Error(), root) {
		t.Fatalf("FindTool() error = %v, want root-only lookup failure", err)
	}
}

func TestAVDHomesUsesExplicitHomeExclusively(t *testing.T) {
	explicit := filepath.Join(t.TempDir(), "explicit")
	t.Setenv("ANDROID_AVD_HOME", explicit)
	t.Setenv("ANDROID_USER_HOME", filepath.Join(t.TempDir(), "user"))
	t.Setenv("ANDROID_EMULATOR_HOME", filepath.Join(t.TempDir(), "emulator"))

	homes, err := AVDHomes()
	if err != nil {
		t.Fatalf("AVDHomes() error: %v", err)
	}
	if len(homes) != 1 || homes[0] != filepath.Clean(explicit) {
		t.Fatalf("AVDHomes() = %v, want only explicit ANDROID_AVD_HOME", homes)
	}
}

func TestAVDHomesRequiresExplicitHomeForRelocationVariables(t *testing.T) {
	for _, variable := range []string{"ANDROID_USER_HOME", "ANDROID_EMULATOR_HOME", "ANDROID_SDK_HOME"} {
		t.Run(variable, func(t *testing.T) {
			t.Setenv("ANDROID_AVD_HOME", "")
			t.Setenv("ANDROID_USER_HOME", "")
			t.Setenv("ANDROID_EMULATOR_HOME", "")
			t.Setenv("ANDROID_SDK_HOME", "")
			t.Setenv(variable, t.TempDir())

			_, err := AVDHomes()
			if err == nil || !strings.Contains(err.Error(), "set ANDROID_AVD_HOME") {
				t.Fatalf("AVDHomes() error = %v, want coherent-home remediation", err)
			}
		})
	}
}

func TestAVDHomesUsesDefaultHomeWithoutRelocation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANDROID_AVD_HOME", "")
	t.Setenv("ANDROID_USER_HOME", "")
	t.Setenv("ANDROID_EMULATOR_HOME", "")
	t.Setenv("ANDROID_SDK_HOME", "")

	homes, err := AVDHomes()
	if err != nil {
		t.Fatalf("AVDHomes() error: %v", err)
	}
	want := filepath.Join(home, ".android", "avd")
	if len(homes) != 1 || homes[0] != want {
		t.Fatalf("AVDHomes() = %v, want [%s]", homes, want)
	}
}

func TestRunSeparatesStreamsAndDoesNotUseShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture is Unix-specific")
	}
	directory := t.TempDir()
	tool := filepath.Join(directory, "tool with spaces")
	script := "#!/bin/sh\nprintf 'out:%s' \"$1\"\nprintf 'err:%s' \"$2\" >&2\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := Run(context.Background(), tool, []string{"one;echo unsafe", "two"}, nil, nil)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if result.Stdout != "out:one;echo unsafe" || result.Stderr != "err:two" {
		t.Fatalf("Run() = %+v", result)
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Run(ctx, os.Args[0], nil, nil, nil)
	if err == nil || err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestCappedBufferPreservesActionableTail(t *testing.T) {
	var buffer cappedBuffer
	prefix := bytes.Repeat([]byte("x"), maxCommandOutput)
	if written, err := buffer.Write(prefix); err != nil || written != len(prefix) {
		t.Fatalf("Write(prefix) = %d, %v", written, err)
	}
	if written, err := buffer.Write([]byte("FINAL ERROR")); err != nil || written != len("FINAL ERROR") {
		t.Fatalf("Write(tail) = %d, %v", written, err)
	}
	got := buffer.String()
	if !strings.Contains(got, "showing tail") || !strings.HasSuffix(got, "FINAL ERROR") {
		t.Fatalf("String() did not preserve final diagnostics: suffix %q", got[len(got)-80:])
	}
}

func createTool(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
