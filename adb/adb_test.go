package adb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestFindADB_FromAndroidHome(t *testing.T) {
	// Create a temp directory with a fake adb binary
	tmp := t.TempDir()
	platformTools := filepath.Join(tmp, "platform-tools")
	if err := os.MkdirAll(platformTools, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeADB := filepath.Join(platformTools, "adb")
	if err := os.WriteFile(fakeADB, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANDROID_HOME", tmp)
	t.Setenv("ANDROID_SDK_ROOT", "")

	path, err := findADB()
	if err != nil {
		t.Fatalf("findADB() error: %v", err)
	}
	if path != fakeADB {
		t.Errorf("findADB() = %q, want %q", path, fakeADB)
	}
}

func TestFindADB_FromSDKRoot(t *testing.T) {
	tmp := t.TempDir()
	platformTools := filepath.Join(tmp, "platform-tools")
	if err := os.MkdirAll(platformTools, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeADB := filepath.Join(platformTools, "adb")
	if err := os.WriteFile(fakeADB, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANDROID_HOME", "")
	t.Setenv("ANDROID_SDK_ROOT", tmp)

	path, err := findADB()
	if err != nil {
		t.Fatalf("findADB() error: %v", err)
	}
	if path != fakeADB {
		t.Errorf("findADB() = %q, want %q", path, fakeADB)
	}
}

func TestFindADB_NotFound(t *testing.T) {
	t.Setenv("ANDROID_HOME", "")
	t.Setenv("ANDROID_SDK_ROOT", "")
	t.Setenv("PATH", "/nonexistent")

	_, err := findADB()
	if err == nil {
		t.Fatal("expected error when adb is not found")
	}
	if !strings.Contains(err.Error(), "adb not found") {
		t.Errorf("expected 'adb not found' error, got: %v", err)
	}
}

func TestNew_WithFakeADB(t *testing.T) {
	tmp := t.TempDir()
	platformTools := filepath.Join(tmp, "platform-tools")
	if err := os.MkdirAll(platformTools, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeADB := filepath.Join(platformTools, "adb")
	if err := os.WriteFile(fakeADB, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ANDROID_HOME", tmp)
	t.Setenv("ANDROID_SDK_ROOT", "")

	client, err := New("emulator-5554")
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if client.Serial != "emulator-5554" {
		t.Errorf("Serial = %q, want %q", client.Serial, "emulator-5554")
	}
	if client.ADBPath != fakeADB {
		t.Errorf("ADBPath = %q, want %q", client.ADBPath, fakeADB)
	}
}

func TestClient_baseArgs(t *testing.T) {
	c := &Client{Serial: "emulator-5554", ADBPath: "/usr/bin/adb"}
	args := c.baseArgs()
	if len(args) != 2 || args[0] != "-s" || args[1] != "emulator-5554" {
		t.Errorf("baseArgs() = %v, want [-s emulator-5554]", args)
	}

	c2 := &Client{ADBPath: "/usr/bin/adb"}
	args2 := c2.baseArgs()
	if len(args2) != 0 {
		t.Errorf("baseArgs() with empty serial = %v, want []", args2)
	}
}

func TestDevices_ParseOutput(t *testing.T) {
	// Test the parsing logic by creating a fake adb that outputs known data
	tmp := t.TempDir()
	fakeADB := filepath.Join(tmp, "adb")
	script := `#!/bin/sh
echo "List of devices attached"
echo "emulator-5554	device"
echo "emulator-5556	device"
echo "physical-1	offline"
echo "physical-2	unauthorized usb:1-2"
echo ""
`
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	serials, err := devices(context.Background(), fakeADB)
	if err != nil {
		t.Fatalf("Devices() error: %v", err)
	}
	if len(serials) != 4 {
		t.Fatalf("Devices() returned %d serials, want 4", len(serials))
	}
	want := []string{"emulator-5554", "emulator-5556", "physical-1", "physical-2"}
	if !equalStrings(serials, want) {
		t.Errorf("Devices() = %v, want %v", serials, want)
	}
}

func TestDevices_NoDevices(t *testing.T) {
	tmp := t.TempDir()
	fakeADB := filepath.Join(tmp, "adb")
	script := `#!/bin/sh
echo "List of devices attached"
echo ""
`
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	serials, err := devices(context.Background(), fakeADB)
	if err != nil {
		t.Fatalf("Devices() error: %v", err)
	}
	if len(serials) != 0 {
		t.Errorf("Devices() = %v, want empty", serials)
	}
}

func TestDevices_UsesDetectedADB(t *testing.T) {
	tmp := t.TempDir()
	platformTools := filepath.Join(tmp, "platform-tools")
	if err := os.MkdirAll(platformTools, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeADB := filepath.Join(platformTools, "adb")
	script := `#!/bin/sh
echo "List of devices attached"
echo "emulator-5554 device"
`
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANDROID_HOME", tmp)
	t.Setenv("ANDROID_SDK_ROOT", "")

	serials, err := Devices()
	if err != nil {
		t.Fatalf("Devices() error: %v", err)
	}
	if len(serials) != 1 || serials[0] != "emulator-5554" {
		t.Fatalf("Devices() = %v, want [emulator-5554]", serials)
	}
}

func TestClient_RunContext_HostCommandIncludesSerial(t *testing.T) {
	tmp := t.TempDir()
	fakeADB := filepath.Join(tmp, "adb")
	script := `#!/bin/sh
printf '%s\n' "$@"
`
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	client := &Client{Serial: "emulator-5554", ADBPath: fakeADB}
	out, err := client.RunContext(context.Background(), "emu", "kill")
	if err != nil {
		t.Fatalf("RunContext() error: %v", err)
	}
	if got, want := strings.Fields(out), []string{"-s", "emulator-5554", "emu", "kill"}; !equalStrings(got, want) {
		t.Fatalf("RunContext() argv = %v, want %v", got, want)
	}
}

func TestClient_ShellContext_PreservesConveniencePrefix(t *testing.T) {
	tmp := t.TempDir()
	fakeADB := filepath.Join(tmp, "adb")
	script := `#!/bin/sh
printf '%s\n' "$@"
`
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	client := &Client{Serial: "device-1", ADBPath: fakeADB}
	out, err := client.ShellContext(context.Background(), "getprop sys.boot_completed")
	if err != nil {
		t.Fatalf("ShellContext() error: %v", err)
	}
	if got, want := strings.Fields(out), []string{"-s", "device-1", "shell", "getprop", "sys.boot_completed"}; !equalStrings(got, want) {
		t.Fatalf("ShellContext() argv = %v, want %v", got, want)
	}
}

func TestClient_WaitForDevice_CancelsAndReaps(t *testing.T) {
	tmp := t.TempDir()
	fakeADB := filepath.Join(tmp, "adb")
	pidFile := filepath.Join(tmp, "pid")
	script := `#!/bin/sh
echo $$ > "$ADBTEST_PID_FILE"
while :; do :; done
`
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADBTEST_PID_FILE", pidFile)

	client := &Client{ADBPath: fakeADB}
	started := time.Now()
	err := client.WaitForDevice(50 * time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForDevice() error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("WaitForDevice() took %v after timeout", elapsed)
	}

	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("read fake adb pid: %v", readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		t.Fatalf("parse fake adb pid: %v", parseErr)
	}

	deadline := time.Now().Add(time.Second)
	for processExists(pid) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if processExists(pid) {
		t.Fatalf("fake adb process %d still exists after WaitForDevice returned", pid)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func processExists(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
