//go:build !linux

package server

// readLauncherArgv is unavailable without procfs.
func readLauncherArgv(int) ([]string, bool) { return nil, false }
