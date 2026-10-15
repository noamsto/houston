//go:build linux

package server

import (
	"io"
	"os"
	"strconv"
	"strings"
)

const maxLauncherArgv = 64 << 10

// readLauncherArgv returns the argv of process pid. The bound keeps a
// pathological process from making houston read an unbounded cmdline.
func readLauncherArgv(pid int) ([]string, bool) {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxLauncherArgv+1))
	if err != nil || len(b) > maxLauncherArgv {
		return nil, false
	}
	argv := strings.Split(string(b), "\x00")
	return argv[:len(argv)-1], true
}
