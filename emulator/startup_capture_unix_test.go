//go:build unix

package emulator

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	// Without setsid the descendant stays inside the emulator's process group,
	// where the existing group kill reaches it - so the test would pass against
	// the very design it exists to reject. macOS has the syscall but not the
	// binary, so skip loudly rather than pass silently.
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skipf("setsid is required to escape the process group: %v", err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readyReader.Close() }()

	script := `setsid sh -c 'printf "%d\n" "$$" >&3; sleep 5' & ` +
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
			// setsid made it a group leader, and it forked sleep as a child.
			// Killing only the leader orphans that child for a full minute.
			_ = syscall.Kill(-pid, syscall.SIGKILL)
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

// The graceful path returns without terminateAndWait, so it is the one exit
// that could leave the readers parked on a descendant outside the process
// group - once per Start, on the ordinary shutdown.
func TestKillReleasesReadersOnAGracefulShutdown(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skipf("setsid is required to hold the pipe outside the process group: %v", err)
	}
	directory := t.TempDir()
	pidFile := filepath.Join(directory, "emulator.pid")
	descendantFile := filepath.Join(directory, "descendant.pid")
	adbPath := filepath.Join(directory, "adb")
	script := "#!/bin/sh\nkill -TERM \"$(cat " + shellQuote(pidFile) + ")\" 2>/dev/null\nexit 0\n"
	if err := os.WriteFile(adbPath, []byte(script), 0o755); err != nil { //nolint:gosec // A test fixture must be executable.
		t.Fatal(err)
	}

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
	deps.newADB = func(serial string) (*adb.Client, error) {
		return &adb.Client{Serial: serial, ADBPath: adbPath}, nil
	}
	deps.command = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c",
			"printf '%d\\n' \"$$\" > "+shellQuote(pidFile)+"; "+
				"setsid sh -c 'printf \"%d\\n\" \"$$\" > "+shellQuote(descendantFile)+"; sleep 5' & "+
				"printf 'INFO | booting\\n'; while :; do sleep 0.05; done")
	}

	instance, err := startWithDependencies(Config{AVD: "Test", Timeout: 5 * time.Second}, deps)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}
	t.Cleanup(func() { killRecordedGroup(descendantFile) })

	if killErr := instance.Kill(); killErr != nil {
		t.Fatalf("Kill() error: %v", killErr)
	}
	select {
	case <-instance.drained:
	case <-time.After(5 * time.Second):
		t.Fatal("reader goroutines still parked after a graceful Kill")
	}
}

func killRecordedGroup(pidFile string) {
	data, err := os.ReadFile(pidFile) //nolint:gosec // Path is built by the test.
	if err != nil {
		return
	}
	if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && pid > 0 {
		// The recorded process is a setsid group leader, so its own children
		// go with it. Killing only the leader orphans them.
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
}

// The combination that actually loses output: a descendant outside the process
// group keeps the pipes open so the drain grace always expires, and if the
// reader is inside the live write when it is asked to stop, the emulator's last
// words go unread. Either condition alone is harmless, which is why this needs
// both.
func TestStartCapturesDespiteASlowWriterAndAHeldPipe(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skipf("setsid is required to hold the pipe outside the process group: %v", err)
	}
	directory := t.TempDir()
	descendantFile := filepath.Join(directory, "descendant.pid")
	// The filler must exceed the reader's buffer so the FATAL lands in a later
	// read than the one that parks the reader in the slow live write. Arriving
	// in the same read would be recorded before the tee and prove nothing.
	script := "setsid sh -c 'printf \"%d\\n\" \"$$\" > " + shellQuote(descendantFile) + "; sleep 5' & " +
		"i=0; while [ $i -lt 1200 ]; do printf 'INFO         | filler line %d\\n' $i; i=$((i+1)); done; " +
		"printf '%s\\n' " + shellQuote(userdataFatal) + "; exit 1"
	deps, _ := earlyExitDependencies(t, script)
	deps.output = slowWriter{delay: 300 * time.Millisecond}
	t.Cleanup(func() { killRecordedGroup(descendantFile) })

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 10 * time.Second}, deps)
	if err == nil {
		t.Fatal("Start returned no error")
	}
	if !strings.Contains(err.Error(), "Not enough space") {
		t.Fatalf("Start error = %v, want the FATAL captured despite a held pipe and a slow writer", err)
	}
}
