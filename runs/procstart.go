package runs

import (
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// probeBroken ensures the foreground-process probe's broken-probe warning
// (as opposed to the legitimate "process/leader has exited") fires once per
// process, not once per join attempt.
var probeBroken sync.Once

// warnProbeBroken logs, once, that the foreground-process probe itself is
// failing rather than reporting a legitimately gone process — meaning
// terminal crew records will stop joining their panes.
func warnProbeBroken(err error) {
	probeBroken.Do(func() {
		slog.Warn("crew source: foreground-process probe failed, terminal crew records will not join their panes", "error", err)
	})
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
