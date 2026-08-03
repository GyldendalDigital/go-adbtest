package androidsdk

import (
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

func TestAVDHomesUsesDocumentedPrecedence(t *testing.T) {
	t.Setenv("ANDROID_AVD_HOME", filepath.Join(t.TempDir(), "explicit"))
	t.Setenv("ANDROID_USER_HOME", filepath.Join(t.TempDir(), "user"))
	t.Setenv("ANDROID_EMULATOR_HOME", filepath.Join(t.TempDir(), "emulator"))

	homes, err := AVDHomes()
	if err != nil {
		t.Fatalf("AVDHomes() error: %v", err)
	}
	if len(homes) < 3 {
		t.Fatalf("AVDHomes() = %v, want at least three locations", homes)
	}
	if !strings.HasSuffix(homes[0], "explicit") {
		t.Fatalf("first AVD home = %q, want explicit ANDROID_AVD_HOME", homes[0])
	}
	if !strings.HasSuffix(homes[1], filepath.Join("user", "avd")) {
		t.Fatalf("second AVD home = %q, want ANDROID_USER_HOME/avd", homes[1])
	}
	if !strings.HasSuffix(homes[2], filepath.Join("emulator", "avd")) {
		t.Fatalf("third AVD home = %q, want ANDROID_EMULATOR_HOME/avd", homes[2])
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
