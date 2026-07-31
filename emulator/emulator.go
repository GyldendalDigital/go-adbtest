// Package emulator manages Android emulator lifecycle — starting, waiting for
// boot, and stopping emulator instances.
package emulator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GyldendalDigital/go-adbtest/adb"
)

const (
	defaultBootTimeout     = 120 * time.Second
	defaultPollInterval    = time.Second
	defaultShutdownTimeout = 10 * time.Second
	minimumMemoryMB        = 1536
	maximumMemoryMB        = 8192
)

// Config holds emulator launch options.
type Config struct {
	// AVD is the configured Android Virtual Device name, for example "Pixel_7".
	AVD string
	// Headless adds the emulator's -no-window option.
	Headless bool
	// GPU selects the emulator GPU mode, such as "auto", "host", or "software".
	GPU string
	// Cores overrides the AVD's virtual CPU count. Zero uses the AVD setting.
	Cores int
	// MemoryMB overrides the AVD's RAM in megabytes. Zero uses the AVD setting;
	// non-zero values must be between 1536 and 8192.
	MemoryMB int
	// Acceleration selects VM acceleration: "auto", "on", or "off". Empty uses
	// the emulator default. "on" fails startup when the host hypervisor is unusable.
	Acceleration string
	// NoAudio adds the emulator's -no-audio option.
	NoAudio bool
	// WipeData starts the emulator with -wipe-data.
	WipeData bool
	// NoSnapshot disables loading and saving emulator snapshots.
	NoSnapshot bool
	// Timeout bounds serial detection and boot completion. Zero means 120 seconds.
	Timeout time.Duration
}

// Instance represents a running emulator.
type Instance struct {
	// PID is the host emulator process ID.
	PID int
	// Serial is the adb emulator serial, for example "emulator-5554".
	Serial string
	// ADB is a client pinned to Serial.
	ADB *adb.Client

	cmd              *exec.Cmd
	done             chan struct{}
	stateMu          sync.Mutex
	waitErr          error
	killOnce         sync.Once
	killErr          error
	shutdownTimeout  time.Duration
	ownsProcessGroup bool
}

// Start boots an emulator with the given config. Blocks until boot_completed=1.
//
//nolint:gocritic // Config is a public value-style options struct by design.
func Start(cfg Config) (*Instance, error) {
	return startWithDependencies(cfg, productionStartDependencies())
}

type startDependencies struct {
	findEmulator func() (string, error)
	devices      func(context.Context) ([]string, error)
	newADB       func(string) (*adb.Client, error)
	shell        func(context.Context, *adb.Client, string) (string, error)
	command      func(string, ...string) *exec.Cmd
	pollInterval time.Duration
}

func productionStartDependencies() startDependencies {
	return startDependencies{
		findEmulator: findEmulator,
		devices:      adb.DevicesContext,
		newADB:       adb.New,
		shell: func(ctx context.Context, client *adb.Client, command string) (string, error) {
			return client.ShellContext(ctx, command)
		},
		command:      exec.Command,
		pollInterval: defaultPollInterval,
	}
}

//nolint:gocritic // Copying keeps normalization from mutating caller-owned options.
func normalizeConfig(cfg Config) (Config, error) {
	cfg.Acceleration = strings.TrimSpace(cfg.Acceleration)
	if strings.TrimSpace(cfg.AVD) == "" {
		return Config{}, fmt.Errorf("emulator: AVD is required")
	}
	if cfg.Timeout < 0 {
		return Config{}, fmt.Errorf("emulator: timeout must not be negative")
	}
	if cfg.Cores < 0 || cfg.Cores > 64 {
		return Config{}, fmt.Errorf("emulator: cores must be between 0 and 64, got %d", cfg.Cores)
	}
	if cfg.MemoryMB != 0 && (cfg.MemoryMB < minimumMemoryMB || cfg.MemoryMB > maximumMemoryMB) {
		return Config{}, fmt.Errorf(
			"emulator: memory must be between %d and %d MB, got %d",
			minimumMemoryMB,
			maximumMemoryMB,
			cfg.MemoryMB,
		)
	}
	if cfg.Acceleration != "" && cfg.Acceleration != "auto" && cfg.Acceleration != "on" && cfg.Acceleration != "off" {
		return Config{}, fmt.Errorf("emulator: acceleration must be auto, on, or off, got %q", cfg.Acceleration)
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultBootTimeout
	}
	return cfg, nil
}

//nolint:gocritic // The test seam mirrors Start's public value-style API.
func startWithDependencies(cfg Config, deps startDependencies) (*Instance, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}

	emulatorPath, err := deps.findEmulator()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.Timeout)
	defer cancel()

	beforeDevices, err := deps.devices(ctx)
	if err != nil {
		return nil, fmt.Errorf("list devices before starting emulator: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("emulator boot deadline expired before launch: %w", err)
	}

	args := buildArgs(cfg)
	cmd := deps.command(emulatorPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	ownsProcessGroup := prepareEmulatorProcess(cmd)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start emulator: %w", err)
	}
	inst := newInstance(cmd, ownsProcessGroup)
	processCtx, stopProcessMonitor := context.WithCancel(ctx)
	defer stopProcessMonitor()
	go func() {
		select {
		case <-inst.processDone():
			stopProcessMonitor()
		case <-processCtx.Done():
		}
	}()

	serial, err := detectNewSerial(processCtx, beforeDevices, inst, deps.devices, deps.pollInterval)
	if err != nil {
		return nil, failStart(inst, fmt.Errorf("detect emulator serial: %w", err))
	}
	inst.Serial = serial

	adbClient, err := deps.newADB(serial)
	if err != nil {
		return nil, failStart(inst, fmt.Errorf("create adb client: %w", err))
	}
	inst.ADB = adbClient

	if err := inst.waitForBoot(processCtx, deps.shell, deps.pollInterval); err != nil {
		return nil, failStart(inst, err)
	}

	return inst, nil
}

func newInstance(cmd *exec.Cmd, ownsProcessGroup bool) *Instance {
	instance := &Instance{
		PID:              cmd.Process.Pid,
		cmd:              cmd,
		done:             make(chan struct{}),
		shutdownTimeout:  defaultShutdownTimeout,
		ownsProcessGroup: ownsProcessGroup,
	}
	go func() {
		err := cmd.Wait()
		instance.stateMu.Lock()
		instance.waitErr = err
		instance.stateMu.Unlock()
		close(instance.done)
	}()
	return instance
}

func failStart(instance *Instance, startErr error) error {
	if cleanupErr := instance.terminateAndWait(); cleanupErr != nil {
		return errors.Join(startErr, fmt.Errorf("clean up emulator process: %w", cleanupErr))
	}
	return startErr
}

// WaitForBoot polls sys.boot_completed with a 1s interval until timeout.
func (i *Instance) WaitForBoot(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return i.waitForBoot(ctx, func(ctx context.Context, client *adb.Client, command string) (string, error) {
		return client.ShellContext(ctx, command)
	}, defaultPollInterval)
}

func (i *Instance) waitForBoot(
	ctx context.Context,
	shell func(context.Context, *adb.Client, string) (string, error),
	pollInterval time.Duration,
) error {
	if i == nil || i.ADB == nil {
		return fmt.Errorf("emulator: cannot wait for boot without an adb client")
	}
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}

	var lastErr error
	for {
		if err := i.exitedBefore("boot completed"); err != nil {
			return err
		}

		out, err := shell(ctx, i.ADB, "getprop sys.boot_completed")
		if err == nil && strings.TrimSpace(out) == "1" {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if err := i.exitedBefore("boot completed"); err != nil {
			return err
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			// The process monitor also cancels ctx. Prefer the actionable
			// process-exit error when shutdown races the boot deadline.
			if err := i.exitedBefore("boot completed"); err != nil {
				return err
			}
			if lastErr != nil {
				return fmt.Errorf("emulator %s did not boot: %w (last adb error: %v)", i.Serial, ctx.Err(), lastErr)
			}
			return fmt.Errorf("emulator %s did not boot: %w", i.Serial, ctx.Err())
		case <-i.processDone():
			stopTimer(timer)
			return i.exitError("boot completed")
		case <-timer.C:
		}
	}
}

// Kill stops the emulator gracefully, falling back to SIGKILL.
func (i *Instance) Kill() error {
	if i == nil {
		return nil
	}
	i.killOnce.Do(func() {
		i.killErr = i.kill()
	})
	return i.killErr
}

func (i *Instance) kill() error {
	if !i.IsRunning() {
		return nil
	}
	if i.ADB == nil || i.cmd == nil || i.cmd.Process == nil {
		return i.terminateAndWait()
	}

	timeout := i.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	_, gracefulErr := i.ADB.RunContext(ctx, "emu", "kill")
	if gracefulErr != nil {
		cancel()
		if err := i.terminateAndWait(); err != nil {
			return errors.Join(gracefulErr, err)
		}
		return nil
	}

	select {
	case <-i.processDone():
		cancel()
		return nil
	case <-ctx.Done():
		cancel()
	}

	if err := i.terminateAndWait(); err != nil {
		if gracefulErr != nil {
			return errors.Join(gracefulErr, err)
		}
		return err
	}
	return nil
}

// IsRunning checks if the emulator process is still alive.
func (i *Instance) IsRunning() bool {
	if i == nil || i.cmd == nil || i.cmd.Process == nil {
		return false
	}
	if i.done == nil {
		return i.cmd.ProcessState == nil
	}
	select {
	case <-i.done:
		return false
	default:
		return true
	}
}

// buildArgs composes emulator command-line flags from config.
//
//nolint:gocritic // Keeping a value mirrors Config's public value-style API.
func buildArgs(cfg Config) []string {
	args := []string{"-avd", cfg.AVD, "-no-boot-anim"}

	if cfg.Headless {
		args = append(args, "-no-window")
	}
	if cfg.GPU != "" {
		args = append(args, "-gpu", cfg.GPU)
	}
	if cfg.Cores > 0 {
		args = append(args, "-cores", strconv.Itoa(cfg.Cores))
	}
	if cfg.MemoryMB > 0 {
		args = append(args, "-memory", strconv.Itoa(cfg.MemoryMB))
	}
	if cfg.Acceleration != "" {
		args = append(args, "-accel", cfg.Acceleration)
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
func detectNewSerial(
	ctx context.Context,
	beforeDevices []string,
	instance *Instance,
	devices func(context.Context) ([]string, error),
	pollInterval time.Duration,
) (string, error) {
	beforeSet := make(map[string]struct{}, len(beforeDevices))
	for _, s := range beforeDevices {
		beforeSet[s] = struct{}{}
	}
	if pollInterval <= 0 {
		pollInterval = defaultPollInterval
	}

	var lastErr error
	for {
		if err := instance.exitedBefore("its serial was detected"); err != nil {
			return "", err
		}

		current, err := devices(ctx)
		if err == nil {
			for _, serial := range current {
				if _, existed := beforeSet[serial]; !existed {
					return serial, nil
				}
			}
		} else {
			lastErr = err
		}
		if err := instance.exitedBefore("its serial was detected"); err != nil {
			return "", err
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			// The process monitor also cancels ctx. Prefer the actionable
			// process-exit error when shutdown races the serial deadline.
			if err := instance.exitedBefore("its serial was detected"); err != nil {
				return "", err
			}
			if lastErr != nil {
				return "", fmt.Errorf("no new emulator appeared: %w (last adb error: %v)", ctx.Err(), lastErr)
			}
			return "", fmt.Errorf("no new emulator appeared: %w", ctx.Err())
		case <-instance.processDone():
			stopTimer(timer)
			return "", instance.exitError("its serial was detected")
		case <-timer.C:
		}
	}
}

func stopTimer(timer *time.Timer) {
	if timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (i *Instance) processDone() <-chan struct{} {
	if i == nil {
		return nil
	}
	return i.done
}

func (i *Instance) exitedBefore(phase string) error {
	if i == nil || i.done == nil {
		return nil
	}
	select {
	case <-i.done:
		return i.exitError(phase)
	default:
		return nil
	}
}

func (i *Instance) exitError(phase string) error {
	i.stateMu.Lock()
	waitErr := i.waitErr
	i.stateMu.Unlock()
	if waitErr != nil {
		return fmt.Errorf("emulator process exited before %s: %w", phase, waitErr)
	}
	return fmt.Errorf("emulator process exited before %s", phase)
}

func (i *Instance) terminateAndWait() error {
	if i == nil || i.cmd == nil || i.cmd.Process == nil {
		return nil
	}
	running := i.IsRunning()
	if !running && !i.ownsProcessGroup {
		if i.done != nil {
			<-i.done
		}
		return nil
	}

	// A Unix emulator is launched as a process-group leader. Kill the whole
	// group even when the leader has already exited: a launcher can otherwise
	// leave its emulator descendant running after startup fails.
	killErr := terminateEmulatorProcess(i.cmd.Process, i.ownsProcessGroup)
	if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		return fmt.Errorf("kill emulator process: %w", killErr)
	}

	if i.done != nil {
		<-i.done
		return nil
	}
	if waitErr := i.cmd.Wait(); waitErr != nil && killErr != nil {
		return fmt.Errorf("wait for emulator process: %w", waitErr)
	}
	return nil
}

// findEmulator locates the emulator binary.
func findEmulator() (string, error) {
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if root := os.Getenv(env); root != "" {
			candidate := filepath.Join(root, "emulator", "emulator")
			//nolint:gosec // Android SDK roots are explicit user configuration.
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
