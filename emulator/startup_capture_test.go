package emulator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

// earlyExitDependencies drives a start that fails because the emulator process
// exits before a serial appears, which is the shape of every real startup
// failure this capture exists for.
//
//nolint:gocritic // The value-style dependency seam keeps tests isolated.
func earlyExitDependencies(t *testing.T, script string) (deps startDependencies, cmd **exec.Cmd) {
	t.Helper()
	deps = testStartDependencies()
	calls := 0
	deps.devices = func(ctx context.Context) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	held := new(*exec.Cmd)
	deps.command = func(string, ...string) *exec.Cmd {
		*held = exec.Command("sh", "-c", script)
		return *held
	}
	return deps, held
}

// The emulator writes its FATAL to stdout, not stderr: measured against 36.6.11.0,
// every ERROR and FATAL across five failure modes went to stdout with stderr at
// zero bytes.
const userdataFatal = `FATAL        | Not enough space to create userdata partition. ` +
	`Available: 3157.66 MB at /run/x/Fail.avd, need 245760.00 MB.`

func TestStartAttachesTheEmulatorsOwnExplanation(t *testing.T) {
	deps, _ := earlyExitDependencies(t, "printf '%s\\n' "+shellQuote(userdataFatal)+"; exit 1")

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 2 * time.Second}, deps)
	if err == nil {
		t.Fatal("Start returned no error")
	}
	if !strings.Contains(err.Error(), "exited before") {
		t.Fatalf("Start error = %v, want the early-exit error preserved", err)
	}
	if !strings.Contains(err.Error(), "Not enough space to create userdata partition") {
		t.Fatalf("Start error = %v, want the emulator's own FATAL attached", err)
	}
	// The FATAL is the last line here, as it is in every hard failure, so it is
	// carried by the tail alone rather than lifted and printed twice.
	if strings.Count(err.Error(), "Not enough space") != 1 {
		t.Fatalf("Start error = %v, want the FATAL exactly once", err)
	}
}

// A misconfiguration reports early and the emulator carries on, so here the
// explanation is not the last line and must be lifted out of the log.
func TestStartLiftsAProblemThatIsNotTheLastLine(t *testing.T) {
	script := "printf 'ERROR        | gpuChoiceBasedOnGpuOptions: not valid\\n'; " +
		"i=0; while [ $i -lt 40 ]; do printf 'INFO         | progress\\n'; i=$((i+1)); done; exit 1"
	deps, _ := earlyExitDependencies(t, script)

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 2 * time.Second}, deps)
	if err == nil {
		t.Fatal("Start returned no error")
	}
	if !strings.Contains(err.Error(), "emulator reported: ") ||
		!strings.Contains(err.Error(), "gpuChoiceBasedOnGpuOptions") {
		t.Fatalf("Start error = %v, want the early problem lifted past the tail", err)
	}
}

func TestStartAttachesOutputWrittenToStderr(t *testing.T) {
	// The un-prefixed gfxstream and QEMU channel: low volume, but where a
	// wedged QEMU speaks.
	//nolint:misspell // Verbatim emulator output; the typo is upstream.
	deps, _ := earlyExitDependencies(t, "printf 'WARNING: cannnot unmap ptr 0x7f\\n' >&2; exit 1")

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 2 * time.Second}, deps)
	//nolint:misspell // Verbatim emulator output; the typo is upstream.
	if err == nil || !strings.Contains(err.Error(), "cannnot unmap ptr") {
		t.Fatalf("Start error = %v, want stderr captured too", err)
	}
}

func TestStartLeavesASilentFailureUndecorated(t *testing.T) {
	deps, _ := earlyExitDependencies(t, "exit 7")

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 2 * time.Second}, deps)
	if err == nil {
		t.Fatal("Start returned no error")
	}
	if strings.Contains(err.Error(), "\n") {
		t.Fatalf("Start error = %q, want no trailing section when there is no output", err)
	}
}

func TestStartStreamsLiveOutputToSeparateWriters(t *testing.T) {
	var out, errOut bytes.Buffer
	var mu sync.Mutex
	deps, _ := earlyExitDependencies(t, "printf 'to-stdout\\n'; printf 'to-stderr\\n' >&2; exit 1")
	deps.output = &lockedWriter{writer: &out, mu: &mu}
	deps.errOutput = &lockedWriter{writer: &errOut, mu: &mu}

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 2 * time.Second}, deps)
	if err == nil {
		t.Fatal("Start returned no error")
	}
	mu.Lock()
	stdout, stderr := out.String(), errOut.String()
	mu.Unlock()

	if !strings.Contains(stdout, "to-stdout") {
		t.Fatalf("live stdout = %q, want the emulator's stdout", stdout)
	}
	// Merging the streams would silently move the emulator's stderr onto this
	// process's stdout, which anyone redirecting 2> would lose.
	if strings.Contains(stdout, "to-stderr") {
		t.Fatalf("live stdout = %q, want stderr kept separate", stdout)
	}
	if !strings.Contains(stderr, "to-stderr") {
		t.Fatalf("live stderr = %q, want the emulator's stderr", stderr)
	}
}

func TestStartCapturesDespiteASlowLiveWriter(t *testing.T) {
	// A terminal or CI pipe can be slower than the emulator's final burst. If
	// the reader tees before it records, those last bytes sit unread in the
	// kernel pipe and the explanation is lost.
	deps, _ := earlyExitDependencies(t, "printf '%s\\n' "+shellQuote(userdataFatal)+"; exit 1")
	// Slower than drainGrace on purpose: with the tee first, the reader parks
	// in this write and the emulator's last bytes stay unread in the pipe.
	deps.output = slowWriter{delay: 300 * time.Millisecond}

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 2 * time.Second}, deps)
	if err == nil || !strings.Contains(err.Error(), "Not enough space") {
		t.Fatalf("Start error = %v, want the FATAL captured despite a slow writer", err)
	}
}

func TestStartSurvivesAFailingLiveWriter(t *testing.T) {
	deps, _ := earlyExitDependencies(t, "printf '%s\\n' "+shellQuote(userdataFatal)+"; exit 1")
	deps.output = failingWriter{}

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 2 * time.Second}, deps)
	if err == nil || !strings.Contains(err.Error(), "Not enough space") {
		t.Fatalf("Start error = %v, want capture to continue past a failing writer", err)
	}
}

func TestStartDoesNotLeakDescriptorsWhenTheProcessFailsToStart(t *testing.T) {
	before := openDescriptors(t)
	deps := testStartDependencies()
	deps.command = func(string, ...string) *exec.Cmd {
		return exec.Command("/definitely/not/a/binary")
	}
	for attempt := 0; attempt < 20; attempt++ {
		if _, err := startWithDependencies(Config{AVD: "Test", Timeout: time.Second}, deps); err == nil {
			t.Fatal("Start succeeded with a missing binary")
		}
	}
	// The reader goroutines close their own ends once the write ends are gone.
	waitForDescriptors(t, before)
}

func TestStartDoesNotLeakDescriptorsAcrossFailedStarts(t *testing.T) {
	before := openDescriptors(t)
	for attempt := 0; attempt < 20; attempt++ {
		deps, _ := earlyExitDependencies(t, "printf 'noise\\n'; exit 1")
		if _, err := startWithDependencies(Config{AVD: "Test", Timeout: 2 * time.Second}, deps); err == nil {
			t.Fatal("Start succeeded unexpectedly")
		}
	}
	waitForDescriptors(t, before)
}

type lockedWriter struct {
	writer *bytes.Buffer
	mu     *sync.Mutex
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}

type slowWriter struct{ delay time.Duration }

func (w slowWriter) Write(data []byte) (int, error) {
	time.Sleep(w.delay)
	return len(data), nil
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("live writer failed") }

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func openDescriptors(t *testing.T) int {
	t.Helper()
	// Let any earlier test's readers finish unwinding, so the baseline is not
	// inflated in a way that would hide a real leak.
	time.Sleep(50 * time.Millisecond)
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("descriptor accounting needs /proc: %v", err)
	}
	return len(entries)
}

// waitForDescriptors allows for reader goroutines that are still unwinding.
func waitForDescriptors(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		after := openDescriptors(t)
		if after <= before+2 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("descriptors grew from %d to %d", before, after)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The boot-timeout path attaches without waiting, because the emulator is still
// running and the pipes will not reach EOF at all.
func TestBootTimeoutAttachesOutputWithoutWaitingForDrain(t *testing.T) {
	deps := testStartDependencies()
	deps.drainGrace = 3 * time.Second
	deps.command = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c",
			"printf 'ERROR        | gpuChoiceBasedOnGpuOptions: not valid\\n'; while :; do sleep 0.05; done")
	}
	calls := 0
	deps.devices = func(context.Context) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return []string{"emulator-5554"}, nil
	}
	deps.shell = func(context.Context, *adb.Client, string) (string, error) { return "0", nil }

	started := time.Now()
	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 500 * time.Millisecond}, deps)
	elapsed := time.Since(started)

	if err == nil || !strings.Contains(err.Error(), "did not boot") {
		t.Fatalf("Start error = %v, want a boot timeout", err)
	}
	if !strings.Contains(err.Error(), "gpuChoiceBasedOnGpuOptions") {
		t.Fatalf("Start error = %v, want the emulator's own output attached", err)
	}
	// Waiting for a drain that cannot happen would burn the whole grace here.
	if elapsed > 2*time.Second {
		t.Fatalf("Start took %v; the boot-timeout path must not wait for EOF", elapsed)
	}
}

// The ring wraps through the real pipe and the 32 KiB reader buffer, not just
// in a unit test.
func TestStartKeepsTheTailWhenOutputExceedsTheRing(t *testing.T) {
	deps, _ := earlyExitDependencies(t,
		"i=0; while [ $i -lt 3000 ]; do printf 'INFO         | filler line %d\n' $i; i=$((i+1)); done; "+
			"printf '%s\n' "+shellQuote(userdataFatal)+"; exit 1")

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 10 * time.Second}, deps)
	if err == nil || !strings.Contains(err.Error(), "Not enough space") {
		t.Fatalf("Start error = %v, want the newest output kept across a wrap", err)
	}
	if len(err.Error()) > 8<<10 {
		t.Fatalf("Start error is %d bytes, want it bounded", len(err.Error()))
	}
}
