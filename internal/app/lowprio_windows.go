//go:build windows

package app

import (
	"os/exec"
	"syscall"
)

// belowNormalPriorityClass is BELOW_NORMAL_PRIORITY_CLASS from the Windows API.
const belowNormalPriorityClass = 0x00004000

// startLowPriority starts cmd at below-normal priority, so the download (and
// its decryption) gets the CPU first whenever both need it. It changes only
// scheduling, never what the process does.
func startLowPriority(cmd *exec.Cmd) error {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= belowNormalPriorityClass
	return cmd.Start()
}
