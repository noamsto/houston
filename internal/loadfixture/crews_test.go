package loadfixture

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteCrews(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	fx, err := WriteCrews(dir, CrewOptions{Crews: 3, Workers: 4, Seed: 7, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.Crews) != 3 || len(fx.Workers) != 12 {
		t.Fatalf("crews=%d workers=%d", len(fx.Crews), len(fx.Workers))
	}

	f, err := os.Open(filepath.Join(dir, "repo", ".git", "crew", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	kinds := map[string]int{}
	var last int64
	var states = map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var rec struct {
			TS     int64           `json:"ts"`
			Kind   string          `json:"kind"`
			Branch string          `json:"branch"`
			Body   json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("unparseable line %q: %v", sc.Text(), err)
		}
		if rec.TS < last || rec.TS > now.UnixMilli() {
			t.Fatalf("ts %d out of order or in the future (last %d)", rec.TS, last)
		}
		last = rec.TS
		kinds[rec.Kind]++
		if rec.Kind == "status" {
			var body struct{ State string }
			if err := json.Unmarshal(rec.Body, &body); err != nil {
				t.Fatalf("status body: %v", err)
			}
			states[rec.Branch] = body.State
		}
		if rec.Kind == "msg" {
			var s string
			if err := json.Unmarshal(rec.Body, &s); err != nil {
				t.Fatalf("msg body is not a JSON string: %s", rec.Body)
			}
		}
	}
	if kinds["dispatch"] != 12+3*reapedPerCrew || kinds["reap"] != 3*reapedPerCrew {
		t.Fatalf("kinds = %v", kinds)
	}
	if fx.Events != kinds["dispatch"]+kinds["status"]+kinds["msg"]+kinds["reap"] {
		t.Fatalf("Events=%d kinds=%v", fx.Events, kinds)
	}
	for _, w := range fx.Workers {
		if got := states[w.Branch]; got != w.State || got == "done" || got == "failed" {
			t.Fatalf("worker %s final state %q, manifest %q", w.Branch, got, w.State)
		}
	}

	b, err := os.ReadFile(filepath.Join(dir, CrewManifest))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 3+12 {
		t.Fatalf("manifest has %d lines", len(lines))
	}
	for _, l := range lines[:3] {
		if f := strings.Split(l, "\t"); len(f) != 2 || f[0] != "dispatcher" {
			t.Fatalf("dispatcher line %q", l)
		}
	}
	seen := map[string]bool{}
	for _, l := range lines[3:] {
		f := strings.Split(l, "\t")
		if len(f) != 5 || f[0] != "worker" || seen[f[3]] {
			t.Fatalf("worker line %q", l)
		}
		seen[f[3]] = true
	}
	if _, err := os.Stat(filepath.Join(dir, "repo", ".git", "crew", "crews", fx.Crews[0], "pane")); err == nil {
		t.Fatal("pane file written by the fixture")
	}
}

func TestWriteCrewsDeterministic(t *testing.T) {
	now := time.Now()
	read := func() string {
		dir := t.TempDir()
		if _, err := WriteCrews(dir, CrewOptions{Crews: 2, Workers: 2, Seed: 3, Now: now}); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "repo", ".git", "crew", "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	first, second := read(), read()
	if first != second {
		t.Fatal("same seed produced different buses")
	}
}
