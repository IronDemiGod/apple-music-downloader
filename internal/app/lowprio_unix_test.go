//go:build linux

package app

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func TestStartLowPriority(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := startLowPriority(cmd); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()

	// Field 19 of /proc/<pid>/stat is the nice value.
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	s := string(stat)
	fields := strings.Fields(s[strings.LastIndex(s, ")")+2:])
	nice, err := strconv.Atoi(fields[16])
	if err != nil {
		t.Fatal(err)
	}
	if nice < 10 {
		t.Errorf("nice = %d, want >= 10", nice)
	}
}
