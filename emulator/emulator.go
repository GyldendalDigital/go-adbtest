// Package emulator manages Android emulator lifecycle — starting, waiting for
// boot, and stopping emulator instances.
package emulator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

// Config holds emulator launch options.
type Config struct {
	AVD        string        // AVD name (e.g. "Pixel_7")
	Headless   bool          // -no-window
	GPU        string        // "swiftshader_indirect" (safe), "host" (fast)
	NoAudio    bool          // -no-audio
	WipeData   bool          // -wipe-data (clean state)
	NoSnapshot bool          // -no-snapshot
	Timeout    time.Duration // boot timeout (default 120s)
}

// Instance represents a running emulator.
type Instance struct {
	PID    int
	Serial string // e.g. "emulator-5554"
	ADB    *adb.Client
	cmd    *exec.Cmd
}

// Start boots an emulator with the given config. Blocks until boot_completed=1.
func Start(cfg Config) (*Instance, error) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 120 * time.Second
	}

	emulatorPath, err := findEmulator()
	if err != nil {
		return nil, err
	}

	// Snapshot of devices before launch to detect the new one
	beforeDevices, _ := adb.Devices("")

	args := buildArgs(cfg)
	cmd := exec.Command(emulatorPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start emulator: %w", err)
	}

	// Detect the new serial by diffing device lists
	serial, err := detectNewSerial(beforeDevices, cfg.Timeout)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("detect emulator serial: %w", err)
	}

	adbClient, err := adb.New(serial)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("create adb client: %w", err)
	}

	inst := &Instance{
		PID:    cmd.Process.Pid,
		Serial: serial,
		ADB:    adbClient,
		cmd:    cmd,
	}

	if err := inst.WaitForBoot(cfg.Timeout); err != nil {
		_ = inst.Kill()
		return nil, err
	}

	return inst, nil
}

// WaitForBoot polls sys.boot_completed with a 1s interval until timeout.
func (i *Instance) WaitForBoot(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := i.ADB.Shell("getprop sys.boot_completed")
		if err == nil && strings.TrimSpace(out) == "1" {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("emulator %s did not boot within %v", i.Serial, timeout)
}

// Kill stops the emulator gracefully, falling back to SIGKILL.
func (i *Instance) Kill() error {
	// Try graceful shutdown via adb
	_, _ = i.ADB.Shell("emu kill")

	// Wait up to 10s for process to exit
	done := make(chan error, 1)
	go func() { done <- i.cmd.Wait() }()

	select {
	case <-done:
		return nil
	case <-time.After(10 * time.Second):
		if i.cmd.Process != nil {
			return i.cmd.Process.Kill()
		}
		return nil
	}
}

// IsRunning checks if the emulator process is still alive.
func (i *Instance) IsRunning() bool {
	if i.cmd.Process == nil {
		return false
	}
	// On Unix, sending signal 0 checks if the process exists.
	err := i.cmd.Process.Signal(os.Signal(nil))
	return err == nil
}

// buildArgs composes emulator command-line flags from config.
func buildArgs(cfg Config) []string {
	args := []string{"-avd", cfg.AVD, "-no-boot-anim"}

	if cfg.Headless {
		args = append(args, "-no-window")
	}
	if cfg.GPU != "" {
		args = append(args, "-gpu", cfg.GPU)
	}
	if cfg.NoAudio {
		args = append(args, "-no-audio")
	}
	if cfg.WipeData {
		args = append(args, "-wipe-data")
	}
	if cfg.NoSnapshot {
		args = append(args, "-no-snapshot")
	}

	return args
}

// detectNewSerial waits for a new device serial that wasn't in the before list.
func detectNewSerial(beforeDevices []string, timeout time.Duration) (string, error) {
	beforeSet := make(map[string]bool, len(beforeDevices))
	for _, s := range beforeDevices {
		beforeSet[s] = true
	}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		current, err := adb.Devices("")
		if err != nil {
			continue
		}
		for _, s := range current {
			if !beforeSet[s] {
				return s, nil
			}
		}
	}
	return "", fmt.Errorf("no new emulator appeared within %v", timeout)
}

// findEmulator locates the emulator binary.
func findEmulator() (string, error) {
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if root := os.Getenv(env); root != "" {
			candidate := filepath.Join(root, "emulator", "emulator")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}

	path, err := exec.LookPath("emulator")
	if err != nil {
		return "", fmt.Errorf("emulator not found: set ANDROID_HOME or ensure emulator is in PATH")
	}
	return path, nil
}
