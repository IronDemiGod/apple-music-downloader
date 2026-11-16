//go:build windows

package app

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVT turns on ANSI escape sequence handling for the console, which the
// live list needs to redraw in place. It reports whether that worked.
func enableVT() bool {
	h := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
