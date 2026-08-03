// Package adb wraps Android Debug Bridge (adb) commands for communicating
// with Android devices and emulators.
package adb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/internal/androidsdk"
)

// Client wraps adb commands targeting a specific device/emulator.
type Client struct {
	// Serial is the explicit adb device serial, for example "emulator-5554".
	// When empty, adb applies its normal device-selection rules and may fail if
	// more than one device is connected.
	Serial string
	// ADBPath is the path to the adb executable.
	ADBPath string
}

// New creates a Client, auto-detecting the adb path.
// If serial is empty, adb applies its normal device-selection rules.
func New(serial string) (*Client, error) {
	path, err := findADB()
	if err != nil {
		return nil, err
	}
	return &Client{Serial: serial, ADBPath: path}, nil
}

// Run executes an adb host command and returns combined stdout and stderr.
func (c *Client) Run(args ...string) (string, error) {
	return c.RunContext(context.Background(), args...)
}

// RunContext executes an adb host command and cancels the process when ctx is
// done. Serial targeting is applied before args.
func (c *Client) RunContext(ctx context.Context, args ...string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("adb: nil client")
	}
	if c.ADBPath == "" {
		return "", fmt.Errorf("adb: empty executable path")
	}

	cmdArgs := c.baseArgs()
	cmdArgs = append(cmdArgs, args...)
	// ADBPath is either SDK/PATH-resolved by New or explicitly supplied by the
	// caller for dependency injection; arguments are not re-parsed by a host shell.
	//nolint:gosec // Executing the configured adb binary is the purpose of Client.
	cmd := exec.CommandContext(ctx, c.ADBPath, cmdArgs...)
	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if err == nil {
		return output, nil
	}

	cause := err
	if ctxErr := ctx.Err(); ctxErr != nil {
		cause = ctxErr
	}
	if output == "" {
		return "", fmt.Errorf("adb %s: %w", strings.Join(args, " "), cause)
	}
	return output, fmt.Errorf("adb %s: %w\noutput: %s", strings.Join(args, " "), cause, output)
}

// Shell runs `adb shell <cmd>` and returns combined stdout+stderr.
func (c *Client) Shell(cmd string) (string, error) {
	return c.ShellContext(context.Background(), cmd)
}

// ShellContext runs `adb shell <cmd>` and cancels the adb process when ctx is
// done.
func (c *Client) ShellContext(ctx context.Context, cmd string) (string, error) {
	return c.RunContext(ctx, "shell", cmd)
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
	_, err := c.Run("install", "-r", apkPath)
	return err
}

// Push pushes a local file to the device.
func (c *Client) Push(local, remote string) error {
	_, err := c.Run("push", local, remote)
	return err
}

// Pull pulls a device file to local.
func (c *Client) Pull(remote, local string) error {
	_, err := c.Run("pull", remote, local)
	return err
}

// Forward sets up a TCP port forward: `adb forward tcp:<local> localabstract:<remote>`.
func (c *Client) Forward(localPort int, abstractSocket string) error {
	_, err := c.Run("forward", fmt.Sprintf("tcp:%d", localPort), "localabstract:"+abstractSocket)
	return err
}

// RemoveForward removes a previously established port forward.
func (c *Client) RemoveForward(localPort int) error {
	_, err := c.Run("forward", "--remove", fmt.Sprintf("tcp:%d", localPort))
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
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	err := c.WaitForDeviceContext(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("timed out waiting for device after %v: %w", timeout, err)
	}
	return err
}

// WaitForDeviceContext blocks until a device is connected or ctx is done.
func (c *Client) WaitForDeviceContext(ctx context.Context) error {
	if _, err := c.RunContext(ctx, "wait-for-device"); err != nil {
		return fmt.Errorf("wait for device: %w", err)
	}
	return nil
}

// Devices returns the serial of every device listed by adb, including devices
// that are currently offline or unauthorized.
func Devices() ([]string, error) {
	return DevicesContext(context.Background())
}

// DevicesContext returns every serial listed by adb, including offline and
// unauthorized devices, and cancels adb when ctx is done.
func DevicesContext(ctx context.Context) ([]string, error) {
	adbPath, err := findADB()
	if err != nil {
		return nil, err
	}
	return devices(ctx, adbPath)
}

func devices(ctx context.Context, adbPath string) ([]string, error) {
	out, err := (&Client{ADBPath: adbPath}).RunContext(ctx, "devices")
	if err != nil {
		return nil, fmt.Errorf("adb devices: %w", err)
	}

	var serials []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of") || strings.HasPrefix(line, "*") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			serials = append(serials, parts[0])
		}
	}
	return serials, nil
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
	if strings.TrimSpace(os.Getenv("ANDROID_HOME")) != "" || strings.TrimSpace(os.Getenv("ANDROID_SDK_ROOT")) != "" {
		root, err := androidsdk.ResolveSDKRoot("")
		if err != nil {
			return "", fmt.Errorf("adb: %w", err)
		}
		path, err := androidsdk.FindTool(root, "adb")
		if err != nil {
			return "", fmt.Errorf("adb: %w", err)
		}
		return path, nil
	}

	path, err := exec.LookPath("adb")
	if err != nil {
		return "", fmt.Errorf("adb not found: set ANDROID_HOME or ensure adb is in PATH")
	}
	return path, nil
}
