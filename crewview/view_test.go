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
	if len(lines) != 4 {
		t.Fatalf("edge has %d lines, want 4", len(lines))
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
}
