//go:build !unix && !windows

package app

import "errors"

func freeBytes(path string) (int64, error) { return 0, errors.New("unsupported") }

// pidAlive: unknown here, so never delete another run's folder.
func pidAlive(pid int) bool { return true }
