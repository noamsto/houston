package crewview

import (
	"bufio"
	"errors"
	"os"
	"slices"
	"testing"
)

func readLines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		lines = append(lines, slices.Clone(sc.Bytes()))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

func TestDecodeGolden(t *testing.T) {
	lines := readLines(t, "overview.ndjson")
	if len(lines) != 1 {
		t.Fatalf("golden has %d lines, want 1", len(lines))
	}
	ov, err := Decode(lines[0])
	if err != nil {
		t.Fatal(err)
	}
	if ov.SchemaVersion != 1 || len(ov.Workers) != 13 || len(ov.Attention) != 12 {
		t.Fatalf("got schema %d, %d workers, %d attention", ov.SchemaVersion, len(ov.Workers), len(ov.Attention))
	}
	var merge, peer *Item
	for i := range ov.Attention {
		switch it := &ov.Attention[i]; {
		case it.Kind == "merge_ready":
			merge = it
		case it.Origin == OriginPeer:
			peer = it
		}
	}
	if merge == nil || merge.Action == nil {
		t.Fatalf("merge_ready item or action missing: %+v", merge)
	}
	if want := []string{"crew", "merge", "12", "--crew", "c1"}; !slices.Equal(merge.Action.Argv, want) {
		t.Errorf("argv = %v, want %v", merge.Action.Argv, want)
	}
	if merge.Action.PR == nil || *merge.Action.PR != 12 {
		t.Errorf("action pr = %v, want 12", merge.Action.PR)
	}
	if peer == nil {
		t.Fatal("no peer item")
	}
	if peer.Action != nil {
		t.Errorf("peer action = %+v, want nil", peer.Action)
	}
}

func TestDecodeEdge(t *testing.T) {
	lines := readLines(t, "edge.ndjson")
	if len(lines) != 5 {
		t.Fatalf("edge has %d lines, want 5", len(lines))
	}

	var ue *UnsupportedError
	if _, err := Decode(lines[0]); !errors.As(err, &ue) || ue.Version != 2 {
		t.Errorf("line 1: err = %v, want UnsupportedError version 2", err)
	}
	ue = nil
	if _, err := Decode(lines[1]); !errors.As(err, &ue) || ue.View != "fleet" {
		t.Errorf("line 2: err = %v, want UnsupportedError view fleet", err)
	}
	ue = nil
	if _, err := Decode(lines[2]); err == nil || errors.As(err, &ue) {
		t.Errorf("line 3: err = %v, want a non-Unsupported error", err)
	}
	ov, err := Decode(lines[3])
	if err != nil {
		t.Fatal(err)
	}
	if len(ov.Workers) != 0 || len(ov.Attention) != 0 || len(ov.Errors) != 0 || ov.GeneratedAt != 100 {
		t.Errorf("line 4: got %+v", ov)
	}
	ue = nil
	if _, err := Decode(lines[4]); !errors.As(err, &ue) || ue.Version != 0 {
		t.Errorf("line 5: err = %v, want UnsupportedError version 0", err)
	}
}

// A worker row carries the liveness fields and the dispatcher registration
// (pid, pane, alive: null when unknown) crew's overview reports.
func TestDecodeWorkerDispatcher(t *testing.T) {
	line := `{"schema_version":1,"view":"overview","generated_at":1,"attention":[],"errors":[],"workers":[` +
		`{"crew":"c1","branch":"feat/a","session":"s1","live":true,"exit_suspect":false,"dispatcher":{"where":"here","pid":42,"pane":"%3","alive":true}},` +
		`{"crew":"c1","branch":"feat/b","dispatcher":{"where":"none","pid":null,"pane":null,"alive":null}},` +
		`{"crew":"c2","branch":"feat/c","dispatcher":null}]}`
	ov, err := Decode([]byte(line))
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := ov.Workers[0], ov.Workers[1], ov.Workers[2]
	if a.Session == nil || *a.Session != "s1" || a.Live == nil || !*a.Live || a.ExitSuspect == nil || *a.ExitSuspect {
		t.Errorf("liveness fields = %v %v %v", a.Session, a.Live, a.ExitSuspect)
	}
	if d := a.Dispatcher; d == nil || d.Where != "here" || d.PID == nil || *d.PID != 42 || d.Pane == nil || *d.Pane != "%3" || d.Alive == nil || !*d.Alive {
		t.Errorf("dispatcher = %+v", a.Dispatcher)
	}
	if d := b.Dispatcher; d == nil || d.PID != nil || d.Pane != nil || d.Alive != nil {
		t.Errorf("unknown dispatcher = %+v, want nil pid, pane and alive", d)
	}
	if c.Dispatcher != nil {
		t.Errorf("null dispatcher = %+v, want nil", c.Dispatcher)
	}
}
