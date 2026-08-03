//go:build !unix

package emulator

import (
	"os"
	"os/exec"
)

func prepareEmulatorProcess(_ *exec.Cmd) bool {
	return false
}

func terminateEmulatorProcess(process *os.Process, _ bool) error {
	if process == nil {
		return nil
	}
	return process.Kill()
}
