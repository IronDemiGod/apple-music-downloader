//go:build !unix && !windows

package app

import "os/exec"

func startLowPriority(cmd *exec.Cmd) error { return cmd.Start() }
