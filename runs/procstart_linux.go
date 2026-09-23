//go:build linux

package runs

import (
	"os"
	"strconv"
)

// userHZ is USER_HZ, the tick rate /proc/<pid>/stat reports starttime in.
// It's fixed at 100 by the userspace ABI on the arches this flake builds
// (amd64/arm64); getting the real value would need sysconf(CLK_TCK) via cgo.
const userHZ = 100

// procStartIn resolves pid's unix start time from root (normally /proc),
// combining its ticks-since-boot starttime with the kernel's boot time.
func procStartIn(root string, pid int) int64 {
	stat, err := os.ReadFile(root + "/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	_, ticks, ok := parseProcStat(string(stat))
	if !ok {
		return 0
	}

	sysStat, err := os.ReadFile(root + "/stat")
	if err != nil {
		return 0
	}
	btime, ok := parseBtime(string(sysStat))
	if !ok {
		return 0
	}

	return btime + ticks/userHZ
}

// foregroundStartIn resolves the tty's foreground process group leader
// starting from panePID (the pane's first process, its login shell) and
// returns the leader's start time. tpgid is a process group id, and the
// leader's pid equals its group id; if the leader has since exited, its
// /proc entry is gone and this returns 0.
func foregroundStartIn(root string, panePID int) int64 {
	if panePID <= 0 {
		return 0
	}
	stat, err := os.ReadFile(root + "/" + strconv.Itoa(panePID) + "/stat")
	if err != nil {
		return 0
	}
	tpgid, _, ok := parseProcStat(string(stat))
	if !ok || tpgid <= 0 {
		return 0
	}
	return procStartIn(root, tpgid)
}

func foregroundStart(panePID int) int64 {
	return foregroundStartIn("/proc", panePID)
}
