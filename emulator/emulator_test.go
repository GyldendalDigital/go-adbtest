package emulator

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

func TestBuildArgs_AllOptions(t *testing.T) {
	cfg := Config{
		AVD:          "Pixel_7",
		Headless:     true,
		GPU:          "auto",
		Cores:        2,
		MemoryMB:     1536,
		Acceleration: "on",
		NoAudio:      true,
		WipeData:     true,
		NoSnapshot:   true,
	}

	args := buildArgs(cfg)

	expected := map[string]bool{
		"-avd":          true,
		"Pixel_7":       true,
		"-no-boot-anim": true,
		"-no-window":    true,
		"-gpu":          true,
		"auto":          true,
		"-cores":        true,
		"2":             true,
		"-memory":       true,
		"1536":          true,
		"-accel":        true,
		"on":            true,
		"-no-audio":     true,
		"-wipe-data":    true,
		"-no-snapshot":  true,
	}

	for _, arg := range args {
		if !expected[arg] {
			t.Errorf("unexpected arg: %q", arg)
		}
		delete(expected, arg)
	}

	if len(expected) > 0 {
		t.Errorf("missing args: %v", expected)
	}
}

func TestBuildArgs_MinimalOptions(t *testing.T) {
	cfg := Config{
		AVD: "Pixel_7",
	}

	args := buildArgs(cfg)

	// Should have -avd Pixel_7 -no-boot-anim and nothing else
	if len(args) != 3 {
		t.Fatalf("buildArgs() = %v (len %d), want 3 args", args, len(args))
	}
	if args[0] != "-avd" || args[1] != "Pixel_7" || args[2] != "-no-boot-anim" {
		t.Errorf("buildArgs() = %v, want [-avd Pixel_7 -no-boot-anim]", args)
	}
}

func TestBuildArgs_GPUOnly(t *testing.T) {
	cfg := Config{
		AVD: "Test",
		GPU: "host",
	}

	args := buildArgs(cfg)

	hasGPU := false
	for i, arg := range args {
		if arg == "-gpu" && i+1 < len(args) && args[i+1] == "host" {
			hasGPU = true
		}
	}
	if !hasGPU {
		t.Errorf("buildArgs() = %v, missing -gpu host", args)
	}
}

func TestConfig_DefaultTimeout(t *testing.T) {
	cfg := Config{AVD: "Test"}
	if cfg.Timeout != 0 {
		t.Errorf("default Timeout = %v, want 0 (set by Start)", cfg.Timeout)
	}
}

func TestNormalizeConfig(t *testing.T) {
	t.Run("defaults timeout", func(t *testing.T) {
		cfg, err := normalizeConfig(Config{AVD: "Test"})
		if err != nil {
			t.Fatalf("normalizeConfig() error: %v", err)
		}
		if cfg.Timeout != defaultBootTimeout {
			t.Fatalf("Timeout = %v, want %v", cfg.Timeout, defaultBootTimeout)
		}
	})

	t.Run("trims acceleration", func(t *testing.T) {
		cfg, err := normalizeConfig(Config{AVD: "Test", Acceleration: " on "})
		if err != nil {
			t.Fatalf("normalizeConfig() error: %v", err)
		}
		if cfg.Acceleration != "on" {
			t.Fatalf("Acceleration = %q, want on", cfg.Acceleration)
		}
	})

	for _, tc := range []struct {
		name string
		cfg  Config
	}{
		{name: "missing AVD", cfg: Config{}},
		{name: "blank AVD", cfg: Config{AVD: " \t "}},
		{name: "negative timeout", cfg: Config{AVD: "Test", Timeout: -time.Second}},
		{name: "negative cores", cfg: Config{AVD: "Test", Cores: -1}},
		{name: "too many cores", cfg: Config{AVD: "Test", Cores: 65}},
		{name: "memory below minimum", cfg: Config{AVD: "Test", MemoryMB: 1535}},
		{name: "memory above maximum", cfg: Config{AVD: "Test", MemoryMB: 8193}},
		{name: "invalid acceleration", cfg: Config{AVD: "Test", Acceleration: "maybe"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := normalizeConfig(tc.cfg); err == nil {
				t.Fatal("normalizeConfig() succeeded, want error")
			}
		})
	}
}

func TestStart_SurfacesInitialADBErrorBeforeStartingProcess(t *testing.T) {
	started := false
	deps := testStartDependencies()
	deps.devices = func(context.Context) ([]string, error) {
		return nil, errors.New("adb server unavailable")
	}
	deps.command = func(string, ...string) *exec.Cmd {
		started = true
		return exec.Command("sh", "-c", "exit 0")
	}

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: time.Second}, deps)
	if err == nil || !strings.Contains(err.Error(), "list devices before starting emulator") {
		t.Fatalf("Start error = %v, want initial adb error", err)
	}
	if started {
		t.Fatal("emulator process started after initial adb failure")
	}
}

func TestStart_ReportsEarlyExitAndReapsProcess(t *testing.T) {
	deps := testStartDependencies()
	deviceCalls := 0
	deps.devices = func(ctx context.Context) ([]string, error) {
		deviceCalls++
		if deviceCalls == 1 {
			return nil, nil
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var cmd *exec.Cmd
	deps.command = func(string, ...string) *exec.Cmd {
		cmd = exec.Command("sh", "-c", "exit 7")
		return cmd
	}

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: time.Second}, deps)
	if err == nil || !strings.Contains(err.Error(), "exited before") {
		t.Fatalf("Start error = %v, want early-exit error", err)
	}
	if cmd == nil || cmd.ProcessState == nil {
		t.Fatal("early-exiting emulator process was not reaped")
	}
}

func TestStart_TimeoutKillsAndReapsProcess(t *testing.T) {
	deps := testStartDependencies()
	deps.pollInterval = time.Millisecond
	var cmd *exec.Cmd
	deps.command = func(string, ...string) *exec.Cmd {
		cmd = exec.Command("sh", "-c", "while :; do :; done")
		return cmd
	}

	started := time.Now()
	_, err := startWithDependencies(Config{AVD: "Test", Timeout: 40 * time.Millisecond}, deps)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Start error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Start took %v after boot deadline", elapsed)
	}
	if cmd == nil || cmd.ProcessState == nil {
		t.Fatal("timed-out emulator process was not reaped")
	}
}

func TestStart_NewADBFailureReapsProcess(t *testing.T) {
	deps := testStartDependencies()
	call := 0
	deps.devices = func(context.Context) ([]string, error) {
		call++
		if call == 1 {
			return nil, nil
		}
		return []string{"emulator-5554"}, nil
	}
	deps.newADB = func(string) (*adb.Client, error) {
		return nil, errors.New("cannot create adb client")
	}
	var cmd *exec.Cmd
	deps.command = func(string, ...string) *exec.Cmd {
		cmd = exec.Command("sh", "-c", "while :; do :; done")
		return cmd
	}

	_, err := startWithDependencies(Config{AVD: "Test", Timeout: time.Second}, deps)
	if err == nil || !strings.Contains(err.Error(), "create adb client") {
		t.Fatalf("Start error = %v, want adb-client error", err)
	}
	if cmd == nil || cmd.ProcessState == nil {
		t.Fatal("emulator process was not reaped after adb-client failure")
	}
}

func TestStart_UsesOneDeadlineForSerialAndBoot(t *testing.T) {
	deps := testStartDependencies()
	call := 0
	var serialDeadline time.Time
	var bootDeadline time.Time
	deps.devices = func(ctx context.Context) ([]string, error) {
		call++
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("devices context has no deadline")
		}
		if call == 1 {
			return nil, nil
		}
		serialDeadline = deadline
		return []string{"emulator-5554"}, nil
	}
	deps.newADB = func(serial string) (*adb.Client, error) {
		return &adb.Client{Serial: serial, ADBPath: "unused"}, nil
	}
	deps.shell = func(ctx context.Context, _ *adb.Client, _ string) (string, error) {
		var ok bool
		bootDeadline, ok = ctx.Deadline()
		if !ok {
			t.Fatal("boot context has no deadline")
		}
		return "1", nil
	}
	deps.command = func(string, ...string) *exec.Cmd {
		return exec.Command("sh", "-c", "while :; do :; done")
	}

	instance, err := startWithDependencies(Config{AVD: "Test", Timeout: time.Second}, deps)
	if err != nil {
		t.Fatalf("Start error: %v", err)
	}
	instance.shutdownTimeout = time.Millisecond
	t.Cleanup(func() {
		if err := instance.Kill(); err != nil {
			t.Errorf("Kill() error: %v", err)
		}
	})
	if serialDeadline.IsZero() || !serialDeadline.Equal(bootDeadline) {
		t.Fatalf("serial deadline %v differs from boot deadline %v", serialDeadline, bootDeadline)
	}
}

func TestInstance_KillUsesHostEmuCommandAndIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	fakeADB := filepath.Join(tmp, "adb")
	argsFile := filepath.Join(tmp, "args")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$ADBTEST_ARGS_FILE"
`
	if err := os.WriteFile(fakeADB, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ADBTEST_ARGS_FILE", argsFile)

	cmd := exec.Command("sh", "-c", "while :; do :; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake emulator: %v", err)
	}
	instance := newInstance(cmd, false)
	instance.Serial = "emulator-5554"
	instance.ADB = &adb.Client{Serial: instance.Serial, ADBPath: fakeADB}
	instance.shutdownTimeout = 20 * time.Millisecond

	started := time.Now()
	if err := instance.Kill(); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Kill() took %v", elapsed)
	}
	if instance.IsRunning() {
		t.Fatal("instance still running after Kill")
	}
	if cmd.ProcessState == nil {
		t.Fatal("emulator process was not reaped")
	}

	if err := instance.Kill(); err != nil {
		t.Fatalf("second Kill() error: %v", err)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read adb argv: %v", err)
	}
	got := strings.Fields(string(data))
	want := []string{"-s", "emulator-5554", "emu", "kill"}
	if !sameStrings(got, want) {
		t.Fatalf("adb argv = %v, want %v", got, want)
	}
}

func TestInstance_KillFallsBackImmediatelyWhenADBCommandFails(t *testing.T) {
	tmp := t.TempDir()
	fakeADB := filepath.Join(tmp, "adb")
	if err := os.WriteFile(fakeADB, []byte("#!/bin/sh\nexit 7\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("sh", "-c", "while :; do :; done")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake emulator: %v", err)
	}
	instance := newInstance(cmd, false)
	instance.Serial = "emulator-5554"
	instance.ADB = &adb.Client{Serial: instance.Serial, ADBPath: fakeADB}
	instance.shutdownTimeout = 5 * time.Second

	started := time.Now()
	if err := instance.Kill(); err != nil {
		t.Fatalf("Kill() error: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Kill() waited %v after adb emu kill failed", elapsed)
	}
	if instance.IsRunning() || cmd.ProcessState == nil {
		t.Fatal("fallback did not stop and reap the emulator process")
	}
}

func TestInstance_IsRunningNilSafe(t *testing.T) {
	var instance *Instance
	if instance.IsRunning() {
		t.Fatal("nil instance reported running")
	}
	if err := instance.Kill(); err != nil {
		t.Fatalf("nil Kill() error: %v", err)
	}
}

func testStartDependencies() startDependencies {
	return startDependencies{
		findEmulator: func() (string, error) { return "/fake/emulator", nil },
		devices:      func(context.Context) ([]string, error) { return nil, nil },
		newADB: func(serial string) (*adb.Client, error) {
			return &adb.Client{Serial: serial, ADBPath: "unused"}, nil
		},
		shell: func(context.Context, *adb.Client, string) (string, error) {
			return "0", nil
		},
		command: func(string, ...string) *exec.Cmd {
			return exec.Command("sh", "-c", "while :; do :; done")
		},
		pollInterval: time.Millisecond,
	}
}

func sameStrings(got, want []string) bool {
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
