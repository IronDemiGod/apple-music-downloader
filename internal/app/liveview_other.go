//go:build !windows

package app

// enableVT: terminals outside Windows handle ANSI escape sequences already.
func enableVT() bool { return true }
