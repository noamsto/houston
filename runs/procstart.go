package runs

import (
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var probeBroken sync.Once

// warnProbeBroken logs, once per process, a probe failure that is not a
// legitimately exited process: every such failure fails a join closed (a
// terminal worker record to its pane, a crew to its dispatcher pane), so a
// probe broken for good would otherwise stop those joins with no trace above
// Debug.
func warnProbeBroken(err error) {
	probeBroken.Do(func() {
		slog.Warn("crew source: process probe failed, terminal crew records and crews with a pid file will not join their panes", "error", err)
	})
}

// hostProcs is the production procProbe, over this host's process table.
// Start and Descends live in the per-OS files.
type hostProcs struct{}

func (hostProcs) Alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true
	}
	if !errors.Is(err, syscall.ESRCH) {
		warnProbeBroken(err)
	}
	return false
}

// maxPPIDSteps bounds the parent walk: a dispatcher sits a few levels below
// its pane's shell, and the bound keeps a cycle from a racing pid reuse
// finite.
const maxPPIDSteps = 32

// descendsVia walks pid's ancestry through parent until it reaches root
// (true), init or a top-level process (false), or maxPPIDSteps parent hops.
// pid == root counts as descending.
func descendsVia(parent func(pid int) (int, error), pid, root int) (bool, error) {
	cur := pid
	for range maxPPIDSteps {
		if cur == root {
			return true, nil
		}
		if cur <= 1 {
			return false, nil
		}
		next, err := parent(cur)
		if err != nil {
			return false, err
		}
		cur = next
	}
	return cur == root, nil
}

// procStartFunc returns the unix start time of the foreground process group
// leader on the tty of the pane whose first process is panePID, or 0 when
// that can't be established.
type procStartFunc func(panePID int) int64

// parseProcStat parses a /proc/<pid>/stat line. comm (field 2) is
// parenthesized and can itself contain spaces and parens, so everything up to
// the LAST ")" is skipped rather than split on whitespace; proc(5) numbers
// fields from 1, and the fields after comm map to index = field-3 in what
// remains. tpgid is field 8, starttime is field 22.
func parseProcStat(stat string) (tpgid int, startTicks int64, ok bool) {
	i := strings.LastIndex(stat, ")")
	if i < 0 || i+1 >= len(stat) {
		return 0, 0, false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 20 {
		return 0, 0, false
	}
	tpgid, err := strconv.Atoi(fields[8-3])
	if err != nil {
		return 0, 0, false
	}
	startTicks, err = strconv.ParseInt(fields[22-3], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return tpgid, startTicks, true
}

// parsePPID returns the ppid (field 4) of a /proc/<pid>/stat line, skipping
// comm the same way parseProcStat does.
func parsePPID(stat string) (int, bool) {
	i := strings.LastIndex(stat, ")")
	if i < 0 || i+1 >= len(stat) {
		return 0, false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[4-3])
	if err != nil {
		return 0, false
	}
	return ppid, true
}

// parseBtime extracts the "btime <N>" line from /proc/stat: the kernel boot
// time, needed to turn a starttime in ticks-since-boot into a unix time.
func parseBtime(procStat string) (int64, bool) {
	for _, line := range strings.Split(procStat, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "btime" {
			btime, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				return 0, false
			}
			return btime, true
		}
	}
	return 0, false
}

// parseLstart parses darwin `ps -o lstart=` output, e.g.
// "Wed Sep 23 07:51:13 2026". Fields are rejoined with single spaces first
// because ps pads the day-of-month to two columns.
func parseLstart(out string) (int64, bool) {
	joined := strings.Join(strings.Fields(out), " ")
	if joined == "" {
		return 0, false
	}
	t, err := time.Parse("Mon Jan 2 15:04:05 2006", joined)
	if err != nil {
		return 0, false
	}
	return t.UTC().Unix(), true
}
