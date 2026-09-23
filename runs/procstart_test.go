package runs

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// realShapedStat builds a /proc/<pid>/stat line with the full 52-field shape
// (comm containing spaces and a stray ")"), overriding tpgid (field 8) and
// starttime (field 22).
func realShapedStat(tpgid, startTicks int64) string {
	fields := make([]string, 50) // fields 3..52
	for i := range fields {
		fields[i] = "0"
	}
	fields[8-3] = strconv.FormatInt(tpgid, 10)
	fields[22-3] = strconv.FormatInt(startTicks, 10)
	return "1234 (a) b (c)) " + strings.Join(fields, " ")
}

func TestParseProcStat(t *testing.T) {
	tests := []struct {
		name           string
		stat           string
		wantTpgid      int
		wantStartTicks int64
		wantOK         bool
	}{
		{
			name:           "real-shaped line with comm containing spaces and parens",
			stat:           realShapedStat(5678, 987654),
			wantTpgid:      5678,
			wantStartTicks: 987654,
			wantOK:         true,
		},
		{
			name:   "empty",
			stat:   "",
			wantOK: false,
		},
		{
			name:   "no closing paren",
			stat:   "1234 a b c d e f g h i j k l m n o p q r s t u v w x y z",
			wantOK: false,
		},
		{
			name:   "too few fields after comm",
			stat:   "1234 (a) S 1 1234 1234 34827 5678",
			wantOK: false,
		},
		{
			name:   "non-numeric tpgid",
			stat:   realShapedStatWithField(8-3, "abc"),
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpgid, startTicks, ok := parseProcStat(tt.stat)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if tpgid != tt.wantTpgid {
				t.Errorf("tpgid = %d, want %d", tpgid, tt.wantTpgid)
			}
			if startTicks != tt.wantStartTicks {
				t.Errorf("startTicks = %d, want %d", startTicks, tt.wantStartTicks)
			}
		})
	}
}

// realShapedStatWithField is realShapedStat with one post-comm field index
// overridden to a non-numeric value, to exercise the parse-error path.
func realShapedStatWithField(idx int, val string) string {
	fields := make([]string, 50)
	for i := range fields {
		fields[i] = "0"
	}
	fields[idx] = val
	return "1234 (a) b (c)) " + strings.Join(fields, " ")
}

func TestParseBtime(t *testing.T) {
	tests := []struct {
		name string
		stat string
		want int64
		ok   bool
	}{
		{
			name: "present",
			stat: "cpu  123 456\nbtime 1700000000\nprocesses 5\n",
			want: 1700000000,
			ok:   true,
		},
		{
			name: "absent",
			stat: "cpu  123 456\nprocesses 5\n",
			ok:   false,
		},
		{
			name: "garbage value",
			stat: "btime notanumber\n",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseBtime(tt.stat)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestParseLstart(t *testing.T) {
	tests := []struct {
		name string
		out  string
		want int64
		ok   bool
	}{
		{
			name: "single-digit day",
			out:  "Wed Sep 23 07:51:13 2026",
			want: time.Date(2026, time.September, 23, 7, 51, 13, 0, time.UTC).Unix(),
			ok:   true,
		},
		{
			name: "double-space padded day",
			out:  "Thu Oct  1 00:00:00 2026",
			want: time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC).Unix(),
			ok:   true,
		},
		{
			name: "empty",
			out:  "",
			ok:   false,
		},
		{
			name: "garbage",
			out:  "not a date at all",
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLstart(tt.out)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}
