package chat

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var piFixtures = []string{"basic", "tools", "noise"}

func piFixturePath(name string) string {
	return filepath.Join("testdata", "pi", name+".jsonl")
}

func piGoldenPath(name string) string {
	return filepath.Join("testdata", "pi", name+".golden.jsonl")
}

func TestPiRegistered(t *testing.T) {
	r := For("pi")
	if r == nil {
		t.Fatal(`For("pi") = nil`)
	}
	if r.Engine() != "pi" {
		t.Errorf("Engine() = %q, want pi", r.Engine())
	}
}

func TestPiGolden(t *testing.T) {
	for _, name := range piFixtures {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(piFixturePath(name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			path := copyToTemp(t, t.TempDir(), "s1", raw)

			got, _, reset, err := NewPi().Read(path, Cursor{})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if reset {
				t.Fatalf("Read reported reset on a fresh file")
			}

			gotBytes := marshalUpdates(t, got)
			gp := piGoldenPath(name)
			if *updateGoldens {
				if err := os.WriteFile(gp, gotBytes, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(gp)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if !bytes.Equal(gotBytes, want) {
				t.Fatalf("golden mismatch for %s:\n got: %s\nwant: %s", name, gotBytes, want)
			}
		})
	}
}

func TestPiReadNeverCarriesToolOutputOrRawInput(t *testing.T) {
	for _, name := range piFixtures {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(piFixturePath(name))
			if err != nil {
				t.Fatal(err)
			}
			path := copyToTemp(t, t.TempDir(), "s1", raw)
			updates, _, _, err := NewPi().Read(path, Cursor{})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			for _, u := range updates {
				if u.RawInput != nil {
					t.Errorf("update %s carries RawInput from Read", u.ID)
				}
				if u.SessionUpdate == SessionUpdateToolCallUpdate && u.Content != nil {
					t.Errorf("update %s (tool_call_update) carries Content from Read", u.ID)
				}
			}
		})
	}
}

func TestPiChunkingIndependent(t *testing.T) {
	r := NewPi()
	for _, name := range piFixtures {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(piFixturePath(name))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			want, _, _, err := r.Read(copyToTemp(t, dir, "full", raw), Cursor{})
			if err != nil {
				t.Fatalf("full Read: %v", err)
			}

			incPath := filepath.Join(dir, "inc.jsonl")
			var got []Update
			cur := Cursor{}
			for n := 0; n <= len(raw); n++ {
				if err := os.WriteFile(incPath, raw[:n], 0o644); err != nil {
					t.Fatal(err)
				}
				ups, next, _, err := r.Read(incPath, cur)
				if err != nil {
					t.Fatalf("incremental Read at byte %d: %v", n, err)
				}
				got = append(got, ups...)
				cur = next
			}
			if !equalUpdates(t, got, want) {
				t.Fatalf("chunking-dependent output for %s:\n full: %s\n incremental: %s",
					name, marshalUpdates(t, want), marshalUpdates(t, got))
			}
		})
	}
}

func TestPiPartialLineNotConsumed(t *testing.T) {
	line1 := `{"type":"message","id":"a","timestamp":"2025-01-01T00:00:00.000Z","message":{"role":"user","content":"first","timestamp":1735689600000}}` + "\n"
	line2 := `{"type":"message","id":"b","timestamp":"2025-01-01T00:00:01.000Z","message":{"role":"user","content":"second","timestamp":1735689601000}}` + "\n"

	path := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(path, []byte(line1+line2[:len(line2)-20]), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewPi()
	updates, next, _, err := r.Read(path, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || next.Offset != int64(len(line1)) {
		t.Fatalf("got %d updates at offset %d, want 1 at %d", len(updates), next.Offset, len(line1))
	}

	if err := os.WriteFile(path, []byte(line1+line2), 0o644); err != nil {
		t.Fatal(err)
	}
	updates, _, _, err = r.Read(path, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 {
		t.Fatalf("got %d updates from the completed line, want 1", len(updates))
	}
}

func TestPiTruncationResets(t *testing.T) {
	raw, err := os.ReadFile(piFixturePath("basic"))
	if err != nil {
		t.Fatal(err)
	}
	path := copyToTemp(t, t.TempDir(), "s1", raw)
	_, _, reset, err := NewPi().Read(path, Cursor{Offset: int64(len(raw)) + 1000})
	if err != nil {
		t.Fatal(err)
	}
	if !reset {
		t.Fatal("reset = false when Cursor.Offset exceeds file size")
	}
}

func TestPiRegrowPastCursorResets(t *testing.T) {
	first := `{"type":"message","id":"a","message":{"role":"user","content":"old session","timestamp":1}}` + "\n"
	replaced := `{"type":"message","id":"b","message":{"role":"user","content":"new session, one","timestamp":2}}` + "\n" +
		`{"type":"message","id":"c","message":{"role":"user","content":"new session, two","timestamp":3}}` + "\n"

	path := filepath.Join(t.TempDir(), "s1.jsonl")
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewPi()
	_, cur, _, err := r.Read(path, Cursor{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(replaced), 0o644); err != nil {
		t.Fatal(err)
	}
	got, _, reset, err := r.Read(path, cur)
	if err != nil {
		t.Fatal(err)
	}
	if !reset {
		t.Fatal("reset = false after the file was replaced by a longer one")
	}
	if len(got) != 2 {
		t.Fatalf("got %d updates after reset, want 2", len(got))
	}
}

func TestPiConcurrentUse(t *testing.T) {
	raw, err := os.ReadFile(piFixturePath("tools"))
	if err != nil {
		t.Fatal(err)
	}
	path := copyToTemp(t, t.TempDir(), "s1", raw)

	r := NewPi()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, err := r.Read(path, Cursor{}); err != nil {
				t.Errorf("Read: %v", err)
			}
			if _, err := r.Tool(path, "call_edit1"); err != nil {
				t.Errorf("Tool: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestPiTool(t *testing.T) {
	r := NewPi()

	t.Run("read ok", func(t *testing.T) {
		u, err := r.Tool(piFixturePath("basic"), "call_read1")
		if err != nil {
			t.Fatal(err)
		}
		if u.SessionUpdate != SessionUpdateToolCallUpdate || u.Status != StatusCompleted {
			t.Errorf("got %s/%s, want tool_call_update/completed", u.SessionUpdate, u.Status)
		}
		if got := findText(u.Content); got != `name = "demo"` {
			t.Errorf("text = %q", got)
		}
		if string(u.RawInput) != `{"path":"/work/demo/config.toml"}` {
			t.Errorf("RawInput = %s", u.RawInput)
		}
		if u.Title != "config.toml" || u.Kind != KindRead {
			t.Errorf("title/kind = %q/%q", u.Title, u.Kind)
		}
		if len(u.Locations) != 1 || u.Locations[0].Path != "/work/demo/config.toml" {
			t.Errorf("Locations = %v", u.Locations)
		}
		if u.Meta["tool"] != "read" {
			t.Errorf("Meta[tool] = %v, want read", u.Meta["tool"])
		}
		if findDiff(u.Content) != nil {
			t.Error("read carries a diff")
		}
	})

	t.Run("edit diff", func(t *testing.T) {
		u, err := r.Tool(piFixturePath("tools"), "call_edit1")
		if err != nil {
			t.Fatal(err)
		}
		d := findDiff(u.Content)
		if d == nil {
			t.Fatal("no diff")
		}
		if d.Path != "/work/demo/main.go" || d.OldText != "foo\n\nbaz" || d.NewText != "bar\n\nqux" {
			t.Errorf("diff = %+v", *d)
		}
	})

	t.Run("failed bash", func(t *testing.T) {
		u, err := r.Tool(piFixturePath("tools"), "call_bash1")
		if err != nil {
			t.Fatal(err)
		}
		if u.Status != StatusFailed {
			t.Errorf("status = %s, want failed", u.Status)
		}
		if !strings.Contains(findText(u.Content), "No rule to make target") {
			t.Errorf("error text missing: %+v", u.Content)
		}
		if findDiff(u.Content) != nil {
			t.Error("failed call carries a diff")
		}
	})

	t.Run("call without result", func(t *testing.T) {
		u, err := r.Tool(piFixturePath("tools"), "call_write1")
		if err != nil {
			t.Fatal(err)
		}
		if u.SessionUpdate != SessionUpdateToolCall || u.Status != StatusInProgress {
			t.Errorf("got %s/%s, want tool_call/in_progress", u.SessionUpdate, u.Status)
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		if _, err := r.Tool(piFixturePath("tools"), "nope"); !errors.Is(err, ErrToolNotFound) {
			t.Errorf("err = %v, want ErrToolNotFound", err)
		}
	})
}

func TestPiToolWriteHasNoDiffAndImageResultIsPlaceholder(t *testing.T) {
	raw := `{"type":"message","id":"a","message":{"role":"assistant","content":[{"type":"toolCall","id":"w1","name":"write","arguments":{"path":"/x/y.txt","content":"hi"}},{"type":"toolCall","id":"r1","name":"read","arguments":{"path":"/x/p.png"}}],"timestamp":1}}` + "\n" +
		`{"type":"message","id":"b","message":{"role":"toolResult","toolCallId":"w1","toolName":"write","content":[{"type":"text","text":"wrote 2 bytes"}],"isError":false,"timestamp":2}}` + "\n" +
		`{"type":"message","id":"c","message":{"role":"toolResult","toolCallId":"r1","toolName":"read","content":[{"type":"image","data":"AAAA","mimeType":"image/png"}],"isError":false,"timestamp":3}}` + "\n"
	path := copyToTemp(t, t.TempDir(), "s1", []byte(raw))

	u, err := NewPi().Tool(path, "w1")
	if err != nil {
		t.Fatal(err)
	}
	if findDiff(u.Content) != nil {
		t.Error("write carries a diff")
	}
	u, err = NewPi().Tool(path, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if got := findText(u.Content); got != "[image]" {
		t.Errorf("text = %q, want [image]", got)
	}
}
