package chat

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var updateGoldens = flag.Bool("update", false, "rewrite golden files")

// goldenFixtures lists the fixtures with a matching *.golden.jsonl file,
// checked by TestClaudeGolden. subagent_task.jsonl is deliberately excluded:
// its expected output depends on whether the subagent file exists, which
// TestClaudeSubagentLink exercises directly instead of via a golden file.
var goldenFixtures = []string{
	"human",
	"human_image",
	"task_notification",
	"is_meta",
	"sidechain",
	"split_message",
	"parallel_tools",
	"queued_prompt",
	"legacy_no_origin",
	"unknown_origin",
	"noise",
}

func fixturePath(name string) string {
	return filepath.Join("testdata", "claude", name+".jsonl")
}

func goldenPath(name string) string {
	return filepath.Join("testdata", "claude", name+".golden.jsonl")
}

// marshalUpdates renders updates the same way a golden file is stored: one
// compact JSON object per line, in order.
func marshalUpdates(t *testing.T, updates []Update) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, u := range updates {
		b, err := json.Marshal(u)
		if err != nil {
			t.Fatalf("marshal update: %v", err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

func copyToTemp(t *testing.T, dir, sid string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write fixture copy: %v", err)
	}
	return path
}

func TestClaudeGolden(t *testing.T) {
	for _, name := range goldenFixtures {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(fixturePath(name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			dir := t.TempDir()
			path := copyToTemp(t, dir, "s1", raw)

			r := NewClaude(nil)
			got, _, reset, err := r.Read(path, Cursor{})
			if err != nil {
				t.Fatalf("Read: %v", err)
			}
			if reset {
				t.Fatalf("Read reported reset on a fresh file")
			}

			gp := goldenPath(name)
			gotBytes := marshalUpdates(t, got)

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

// TestClaudeSubagentLink covers R3: _meta.subagent appears on the Agent
// tool's tool_call_update only when the subagent file it names actually
// exists next to the transcript.
func TestClaudeSubagentLink(t *testing.T) {
	raw, err := os.ReadFile(fixturePath("subagent_task"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	r := NewClaude(nil)

	t.Run("without subagent file", func(t *testing.T) {
		dir := t.TempDir()
		path := copyToTemp(t, dir, "s1", raw)
		updates, _, _, err := r.Read(path, Cursor{})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		for _, u := range updates {
			if u.SessionUpdate == SessionUpdateToolCallUpdate {
				if _, ok := u.Meta["subagent"]; ok {
					t.Fatalf("update %+v carries _meta.subagent with no subagent file present", u)
				}
			}
		}
	})

	t.Run("with subagent file", func(t *testing.T) {
		dir := t.TempDir()
		path := copyToTemp(t, dir, "s1", raw)
		subDir := filepath.Join(dir, "s1", "subagents")
		if err := os.MkdirAll(subDir, 0o755); err != nil {
			t.Fatalf("mkdir subagents dir: %v", err)
		}
		subFile := filepath.Join(subDir, "agent-abc123.jsonl")
		if err := os.WriteFile(subFile, []byte(`{"type":"user","isSidechain":true,"agentId":"abc123"}`+"\n"), 0o644); err != nil {
			t.Fatalf("write subagent file: %v", err)
		}

		updates, _, _, err := r.Read(path, Cursor{})
		if err != nil {
			t.Fatalf("Read: %v", err)
		}

		var found bool
		for _, u := range updates {
			if u.SessionUpdate != SessionUpdateToolCallUpdate {
				continue
			}
			if u.Meta["subagent"] == "abc123" {
				found = true
			}
		}
		if !found {
			t.Fatalf("no tool_call_update carried _meta.subagent==abc123: %+v", updates)
		}
	})
}

// TestClaudeUnknownOriginLogsOnce covers R7: an unknown origin.kind is
// reported once per distinct value, not once per record.
func TestClaudeUnknownOriginLogsOnce(t *testing.T) {
	raw, err := os.ReadFile(fixturePath("unknown_origin"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := copyToTemp(t, dir, "s1", raw)

	var mu sync.Mutex
	var calls []string
	r := NewClaude(func(msg string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, msg)
	})

	updates, _, _, err := r.Read(path, Cursor{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(updates) != 0 {
		t.Fatalf("got %d updates, want 0 (both records have an unknown origin.kind)", len(updates))
	}
	if len(calls) != 1 {
		t.Fatalf("logf called %d times, want 1: %v", len(calls), calls)
	}
}

// TestClaudeReadNeverCarriesToolOutputOrRawInput covers the parent spec's
// common rule: rawInput and tool output never ride Read's updates — only
// Tool() serves them, on tap.
func TestClaudeReadNeverCarriesToolOutputOrRawInput(t *testing.T) {
	r := NewClaude(nil)
	for _, fixture := range allFixtureNames(t) {
		t.Run(fixture, func(t *testing.T) {
			raw, err := os.ReadFile(fixturePath(fixture))
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			setupSubagentDir(t, dir, "s1", fixture)
			path := copyToTemp(t, dir, "s1", raw)

			updates, _, _, err := r.Read(path, Cursor{})
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

func allFixtureNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("testdata", "claude"))
	if err != nil {
		t.Fatalf("read testdata dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if filepath.Ext(name) != ".jsonl" || strings.HasSuffix(name, ".golden.jsonl") {
			continue
		}
		names = append(names, name[:len(name)-len(".jsonl")])
	}
	return names
}

// equalUpdates compares two update slices by their JSON encoding, so map
// key order and other incidental Go differences don't cause false failures.
func equalUpdates(t *testing.T, a, b []Update) bool {
	t.Helper()
	return bytes.Equal(marshalUpdates(t, a), marshalUpdates(t, b))
}

// setupSubagentDir mirrors subagent_task.jsonl's expectations (see
// TestClaudeSubagentLink) into dir/sid/subagents so a chunking-independence
// pass over that fixture behaves identically to a single full read.
func setupSubagentDir(t *testing.T, dir, sid, fixture string) {
	t.Helper()
	if fixture != "subagent_task" {
		return
	}
	subDir := filepath.Join(dir, sid, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir subagents dir: %v", err)
	}
	subFile := filepath.Join(subDir, "agent-abc123.jsonl")
	if err := os.WriteFile(subFile, []byte(`{"type":"user","isSidechain":true,"agentId":"abc123"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write subagent file: %v", err)
	}
}

func TestClaudeChunkingIndependent(t *testing.T) {
	r := NewClaude(nil)
	for _, fixture := range allFixtureNames(t) {
		t.Run(fixture, func(t *testing.T) {
			raw, err := os.ReadFile(fixturePath(fixture))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}

			base := t.TempDir()
			fullDir := filepath.Join(base, "full")
			incDir := filepath.Join(base, "inc")
			if err := os.MkdirAll(fullDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(incDir, 0o755); err != nil {
				t.Fatal(err)
			}
			setupSubagentDir(t, fullDir, "s1", fixture)
			setupSubagentDir(t, incDir, "s1", fixture)

			fullPath := copyToTemp(t, fullDir, "s1", raw)
			want, _, _, err := r.Read(fullPath, Cursor{})
			if err != nil {
				t.Fatalf("full Read: %v", err)
			}

			incPath := filepath.Join(incDir, "s1.jsonl")
			var got []Update
			cur := Cursor{}
			for n := 0; n <= len(raw); n++ {
				if err := os.WriteFile(incPath, raw[:n], 0o644); err != nil {
					t.Fatalf("write incremental byte cut: %v", err)
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
					fixture, marshalUpdates(t, want), marshalUpdates(t, got))
			}
		})
	}
}

func TestClaudePartialLineNotConsumed(t *testing.T) {
	line1 := `{"type":"user","uuid":"u1","timestamp":"2024-01-01T00:00:00.000Z","origin":{"kind":"human"},"message":{"role":"user","content":"first"}}` + "\n"
	line2Full := `{"type":"user","uuid":"u2","timestamp":"2024-01-01T00:00:01.000Z","origin":{"kind":"human"},"message":{"role":"user","content":"second"}}` + "\n"
	line2Partial := line2Full[:len(line2Full)-20] // cut mid-line, no trailing newline

	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	if err := os.WriteFile(path, []byte(line1+line2Partial), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewClaude(nil)
	updates, next, reset, err := r.Read(path, Cursor{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if reset {
		t.Fatalf("unexpected reset")
	}
	if len(updates) != 1 {
		t.Fatalf("got %d updates, want 1 (only the complete line)", len(updates))
	}
	if next.Offset != int64(len(line1)) {
		t.Fatalf("next.Offset = %d, want %d (start of the partial line)", next.Offset, len(line1))
	}

	// Complete the second line and read again from the returned cursor.
	if err := os.WriteFile(path, []byte(line1+line2Full), 0o644); err != nil {
		t.Fatal(err)
	}
	updates2, _, _, err := r.Read(path, next)
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if len(updates2) != 1 {
		t.Fatalf("got %d updates from the completed line, want 1", len(updates2))
	}
}

func TestClaudeTruncationResets(t *testing.T) {
	raw, err := os.ReadFile(fixturePath("human"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := copyToTemp(t, dir, "s1", raw)

	r := NewClaude(nil)
	updates, _, reset, err := r.Read(path, Cursor{Offset: int64(len(raw)) + 1000})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reset {
		t.Fatalf("reset = false, want true when Cursor.Offset exceeds file size")
	}
	if len(updates) != 1 {
		t.Fatalf("got %d updates after reset, want 1 (re-read from 0)", len(updates))
	}
}

func TestClaudeConcurrentUse(t *testing.T) {
	raw, err := os.ReadFile(fixturePath("tool_detail"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := copyToTemp(t, dir, "s1", raw)

	r := NewClaude(nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, _, err := r.Read(path, Cursor{}); err != nil {
				t.Errorf("Read: %v", err)
			}
			if _, err := r.Tool(path, "toolu_edit"); err != nil {
				t.Errorf("Tool: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestClaudeTool(t *testing.T) {
	path := fixturePath("tool_detail")
	r := NewClaude(nil)

	t.Run("Edit", func(t *testing.T) {
		u, err := r.Tool(path, "toolu_edit")
		if err != nil {
			t.Fatalf("Tool: %v", err)
		}
		if u.Title != "a.go" {
			t.Errorf("Title = %q, want %q", u.Title, "a.go")
		}
		if u.Kind != KindEdit {
			t.Errorf("Kind = %q, want %q", u.Kind, KindEdit)
		}
		if u.Status != StatusCompleted {
			t.Errorf("Status = %q, want %q", u.Status, StatusCompleted)
		}
		diff := findDiff(u.Content)
		if diff == nil {
			t.Fatalf("no diff content in %+v", u.Content)
		}
		if diff.Path != "/tmp/a.go" || diff.OldText != "foo" || diff.NewText != "bar" {
			t.Errorf("diff = %+v, want path=/tmp/a.go old=foo new=bar", diff)
		}
	})

	t.Run("Write null originalFile", func(t *testing.T) {
		u, err := r.Tool(path, "toolu_write")
		if err != nil {
			t.Fatalf("Tool: %v", err)
		}
		diff := findDiff(u.Content)
		if diff == nil {
			t.Fatalf("no diff content in %+v", u.Content)
		}
		if diff.Path != "/tmp/b.go" || diff.OldText != "" || diff.NewText != "package b\n" {
			t.Errorf("diff = %+v, want path=/tmp/b.go old=\"\" new=\"package b\\n\"", diff)
		}
	})

	t.Run("Write non-null originalFile", func(t *testing.T) {
		u, err := r.Tool(path, "toolu_write2")
		if err != nil {
			t.Fatalf("Tool: %v", err)
		}
		diff := findDiff(u.Content)
		if diff == nil {
			t.Fatalf("no diff content in %+v", u.Content)
		}
		if diff.OldText != "package d_old\n" || diff.NewText != "package d\n" {
			t.Errorf("diff = %+v, want old=package d_old new=package d", diff)
		}
	})

	t.Run("Bash string result no diff", func(t *testing.T) {
		u, err := r.Tool(path, "toolu_bash")
		if err != nil {
			t.Fatalf("Tool: %v", err)
		}
		if u.Title != "build" {
			t.Errorf("Title = %q, want %q", u.Title, "build")
		}
		if u.Kind != KindExecute {
			t.Errorf("Kind = %q, want %q", u.Kind, KindExecute)
		}
		if findDiff(u.Content) != nil {
			t.Errorf("Bash should carry no diff content: %+v", u.Content)
		}
		text := findText(u.Content)
		if text != "build ok" {
			t.Errorf("output text = %q, want %q", text, "build ok")
		}
	})

	t.Run("MultiEdit joins with blank lines", func(t *testing.T) {
		u, err := r.Tool(path, "toolu_multi")
		if err != nil {
			t.Fatalf("Tool: %v", err)
		}
		diff := findDiff(u.Content)
		if diff == nil {
			t.Fatalf("no diff content in %+v", u.Content)
		}
		if diff.OldText != "one\n\ntwo" || diff.NewText != "1\n\n2" {
			t.Errorf("diff = %+v, want old=%q new=%q", diff, "one\n\ntwo", "1\n\n2")
		}
	})

	t.Run("unknown id", func(t *testing.T) {
		_, err := r.Tool(path, "does-not-exist")
		if !errors.Is(err, ErrToolNotFound) {
			t.Fatalf("err = %v, want ErrToolNotFound", err)
		}
	})
}

func findDiff(cs []Content) *Content {
	for i := range cs {
		if cs[i].Type == "diff" {
			return &cs[i]
		}
	}
	return nil
}

func findText(cs []Content) string {
	for _, c := range cs {
		if c.Type == "content" && c.Content != nil {
			return c.Content.Text
		}
	}
	return ""
}
