//go:build unix

package emulator

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// prepareEmulatorProcess isolates the emulator and all descendants from the
// caller's process group. Pgid zero makes the child use its own PID as PGID.
func prepareEmulatorProcess(cmd *exec.Cmd) bool {
	if cmd == nil {
		return false
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	cmd.SysProcAttr.Pgid = 0
	return true
}

func terminateEmulatorProcess(process *os.Process, ownsProcessGroup bool) error {
	if process == nil {
		return nil
	}
	if !ownsProcessGroup {
		return process.Kill()
	}

	err := syscall.Kill(-process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
