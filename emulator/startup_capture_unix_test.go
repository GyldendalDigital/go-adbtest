//go:build unix

package emulator

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

// The emulator's crashpad_handler reparents to init with its own process group
// and session while holding both of the descriptors it was given, so killing
// the emulator's process group does not reach it. An ordinary grandchild is in
// the group and would be killed, which is why this test uses setsid: without
// it, the test passes against the very design it exists to reject.
//
// Measured against the real emulator: with cmd.Stdout wrapped in an io.Writer,
// Wait was still blocked 60 seconds after the emulator was reaped. Owning the
// pipes keeps it at milliseconds.
func TestStartDoesNotHangOnADescendantOutsideTheProcessGroup(t *testing.T) {
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readyReader.Close() }()

	script := `setsid sh -c 'printf "%d\n" "$$" >&3; sleep 60' & ` +
		`printf '%s\n' ` + shellQuote(userdataFatal) + `; exit 1`
	deps, _ := earlyExitDependencies(t, "true")
	deps.command = func(string, ...string) *exec.Cmd {
		cmd := exec.Command("sh", "-c", script)
		cmd.ExtraFiles = []*os.File{readyWriter}
		return cmd
	}

	started := time.Now()
	_, startErr := startWithDependencies(Config{AVD: "Test", Timeout: 20 * time.Second}, deps)
	elapsed := time.Since(started)
	_ = readyWriter.Close()

	// Reap the escaped descendant before asserting, so a failure does not leave
	// a sleeping process behind for the rest of the suite.
	if line, readErr := bufio.NewReader(readyReader).ReadString('\n'); readErr == nil {
		if pid, convErr := strconv.Atoi(strings.TrimSpace(line)); convErr == nil && pid > 0 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}

	if startErr == nil {
		t.Fatal("Start returned no error")
	}
	// Distinguishing milliseconds from never, so the budget is generous.
	if elapsed > 10*time.Second {
		t.Fatalf("Start took %v with a descendant holding the pipe; it should not wait for one", elapsed)
	}
	if !strings.Contains(startErr.Error(), "Not enough space") {
		t.Fatalf("Start error = %v, want the emulator's own output despite the descendant", startErr)
	}
}

// The acceptance criterion for this change is that teardown timing does not
// move, which nothing asserted before.
func TestKillRemainsPromptWithCaptureInPlace(t *testing.T) {
	deps := testStartDependencies()
	calls := 0
	deps.devices = func(context.Context) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return []string{"emulator-5554"}, nil
	}
	deps.shell = func(context.Context, *adb.Client, string) (string, error) { return "1", nil }
	deps.command = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf 'INFO | booting\\n'; while :; do sleep 0.01; done")
	}

	instance, err := startWithDependencies(Config{AVD: "Test", Timeout: 5 * time.Second}, deps)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}
	started := time.Now()
	if killErr := instance.Kill(); killErr != nil {
		t.Fatalf("Kill() error: %v", killErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Kill took %v, want teardown timing unchanged", elapsed)
	}
}
