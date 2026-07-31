package adb

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
echo ""
`
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	serials, err := Devices(fakeADB)
	if err != nil {
		t.Fatalf("Devices() error: %v", err)
	}
	if len(serials) != 2 {
		t.Fatalf("Devices() returned %d serials, want 2", len(serials))
	}
	if serials[0] != "emulator-5554" || serials[1] != "emulator-5556" {
		t.Errorf("Devices() = %v, want [emulator-5554 emulator-5556]", serials)
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

	serials, err := Devices(fakeADB)
	if err != nil {
		t.Fatalf("Devices() error: %v", err)
	}
	if len(serials) != 0 {
		t.Errorf("Devices() = %v, want empty", serials)
	}
}
