//go:build unix

package app

import (
	"os/exec"
	"syscall"
)

// startLowPriority starts cmd and lowers its priority (nice 10), so the
// download (and its decryption) gets the CPU first whenever both need it. It
// changes only scheduling, never what the process does. Failing to lower the
// priority is not an error: the process then just runs at normal priority.
func startLowPriority(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = syscall.Setpriority(syscall.PRIO_PROCESS, cmd.Process.Pid, 10)
	return nil
}
