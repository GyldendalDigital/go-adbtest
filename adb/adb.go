// Package adb wraps Android Debug Bridge (adb) commands for communicating
// with Android devices and emulators.
package adb

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Client wraps adb commands targeting a specific device/emulator.
type Client struct {
	Serial  string // e.g. "emulator-5554"; empty = first device
	ADBPath string // path to adb binary
}

// New creates a Client, auto-detecting the adb path.
// If serial is empty, commands target the first connected device.
func New(serial string) (*Client, error) {
	path, err := findADB()
	if err != nil {
		return nil, err
	}
	return &Client{Serial: serial, ADBPath: path}, nil
}

// Shell runs `adb shell <cmd>` and returns combined stdout+stderr.
func (c *Client) Shell(cmd string) (string, error) {
	out, err := c.run("shell", cmd)
	return strings.TrimSpace(out), err
}

// ShellOrFail is Shell but calls t.Fatal on error.
func (c *Client) ShellOrFail(t testing.TB, cmd string) string {
	t.Helper()
	out, err := c.Shell(cmd)
	if err != nil {
		t.Fatalf("adb shell %q: %v", cmd, err)
	}
	return out
}

// Install installs an APK (-r for reinstall).
func (c *Client) Install(apkPath string) error {
	_, err := c.run("install", "-r", apkPath)
	return err
}

// Push pushes a local file to the device.
func (c *Client) Push(local, remote string) error {
	_, err := c.run("push", local, remote)
	return err
}

// Pull pulls a device file to local.
func (c *Client) Pull(remote, local string) error {
	_, err := c.run("pull", remote, local)
	return err
}

// Forward sets up a TCP port forward: `adb forward tcp:<local> localabstract:<remote>`.
func (c *Client) Forward(localPort int, abstractSocket string) error {
	_, err := c.run("forward", fmt.Sprintf("tcp:%d", localPort), "localabstract:"+abstractSocket)
	return err
}

// RemoveForward removes a previously established port forward.
func (c *Client) RemoveForward(localPort int) error {
	_, err := c.run("forward", "--remove", fmt.Sprintf("tcp:%d", localPort))
	return err
}

// Screencap captures a PNG screenshot to a local file.
func (c *Client) Screencap(localPath string) error {
	const remotePath = "/sdcard/screenshot.png"
	if _, err := c.Shell("screencap -p " + remotePath); err != nil {
		return fmt.Errorf("screencap: %w", err)
	}
	if err := c.Pull(remotePath, localPath); err != nil {
		return fmt.Errorf("pull screenshot: %w", err)
	}
	_, _ = c.Shell("rm " + remotePath)
	return nil
}

// WaitForDevice blocks until a device is connected (with timeout).
func (c *Client) WaitForDevice(timeout time.Duration) error {
	args := c.baseArgs()
	args = append(args, "wait-for-device")

	cmd := exec.Command(c.ADBPath, args...)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()

	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return fmt.Errorf("timed out waiting for device after %v", timeout)
	}
}

// Devices returns all connected device serials.
func Devices(adbPath string) ([]string, error) {
	if adbPath == "" {
		var err error
		adbPath, err = findADB()
		if err != nil {
			return nil, err
		}
	}

	out, err := exec.Command(adbPath, "devices").Output()
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}

	var serials []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of") || strings.HasPrefix(line, "*") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 && parts[1] == "device" {
			serials = append(serials, parts[0])
		}
	}
	return serials, nil
}

// run executes an adb command with the client's serial prefix.
func (c *Client) run(args ...string) (string, error) {
	cmdArgs := c.baseArgs()
	cmdArgs = append(cmdArgs, args...)

	cmd := exec.Command(c.ADBPath, cmdArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return strings.TrimSpace(string(out)), fmt.Errorf("adb %s: %w\noutput: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// baseArgs returns the serial flag args if a serial is set.
func (c *Client) baseArgs() []string {
	if c.Serial != "" {
		return []string{"-s", c.Serial}
	}
	return nil
}

// findADB locates the adb binary.
// Resolution order: $ANDROID_HOME/platform-tools/adb → $ANDROID_SDK_ROOT/platform-tools/adb → PATH.
func findADB() (string, error) {
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if root := os.Getenv(env); root != "" {
			candidate := filepath.Join(root, "platform-tools", "adb")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}

	path, err := exec.LookPath("adb")
	if err != nil {
		return "", fmt.Errorf("adb not found: set ANDROID_HOME or ensure adb is in PATH")
	}
	return path, nil
}
