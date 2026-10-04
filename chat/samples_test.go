//go:build samples

package chat

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSamplesClaudeCode replays real claude-code transcripts named by
// $HOUSTON_SAMPLES. It never prints or copies transcript content — only
// counts (file base name, record/update counts) reach t.Logf. Skips when
// the env var is unset or the claude-code sample dir is missing, so this
// -tags samples test is a no-op in CI and for anyone without the samples.
func TestSamplesClaudeCode(t *testing.T) {
	dir := os.Getenv("HOUSTON_SAMPLES")
	if dir == "" {
		t.Skip("HOUSTON_SAMPLES not set")
	}

	ccDir := filepath.Join(dir, "claude-code")
	files, err := filepath.Glob(filepath.Join(ccDir, "*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no claude-code sample files")
	}

	r := NewClaude(nil)
	for _, f := range files {
		f := f
		t.Run(filepath.Base(f), func(t *testing.T) {
			checkSampleFile(t, r, f)
		})
	}

	subFiles, _ := filepath.Glob(filepath.Join(dir, "claude-code-subagent", "*.jsonl"))
	for _, f := range subFiles {
		f := f
		t.Run("subagent/"+filepath.Base(f), func(t *testing.T) {
			checkSubagentFile(t, r, f)
		})
	}
}

// rawRecord and rawBlock decode just enough of the raw transcript,
// independently of the reader's own types, to compute expected counts.
type rawRecord struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Origin      *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message *struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment *struct {
		Type        string `json:"type"`
		CommandMode string `json:"commandMode"`
	} `json:"attachment"`
}

type rawBlock struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func checkSampleFile(t *testing.T, r Reader, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var (
		recordCount       int
		humanOriginCount  int
		queuedPromptCount int
		toolUseIDs        = map[string]bool{}
	)

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec rawRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		recordCount++
		if rec.IsSidechain {
			continue
		}
		switch rec.Type {
		case "user":
			if rec.Origin != nil && rec.Origin.Kind == "human" {
				humanOriginCount++
			}
		case "assistant":
			if rec.Message == nil {
				continue
			}
			var blocks []rawBlock
			if err := json.Unmarshal(rec.Message.Content, &blocks); err == nil {
				for _, b := range blocks {
					if b.Type == "tool_use" && b.ID != "" {
						toolUseIDs[b.ID] = true
					}
				}
			}
		case "attachment":
			if rec.Attachment != nil && rec.Attachment.Type == "queued_command" && rec.Attachment.CommandMode == "prompt" {
				queuedPromptCount++
			}
		}
	}

	updates, _, _, err := r.Read(path, Cursor{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	var (
		userChunksNoOrigin  int
		toolCallCount       = map[string]int{}
		toolCallUpdateCount = map[string]int{}
		kindCounts          = map[string]int{}
	)

	for _, u := range updates {
		kindCounts[u.SessionUpdate]++

		switch u.SessionUpdate {
		case SessionUpdateUserMessageChunk:
			// A task-notification legitimately starts with "<", so the
			// no-injected-text check covers origin-less chunks only.
			if _, ok := u.Meta["origin"]; !ok {
				userChunksNoOrigin++
				for _, c := range u.Content {
					if c.Content != nil && strings.HasPrefix(c.Content.Text, "<") {
						t.Errorf("update %s: origin-less user_message_chunk text starts with '<'", u.ID)
					}
				}
			}
		case SessionUpdateToolCall:
			toolCallCount[u.ToolCallID]++
			if !toolUseIDs[u.ToolCallID] {
				t.Errorf("tool_call for id %s not present in the raw file's tool_use ids", u.ToolCallID)
			}
		case SessionUpdateToolCallUpdate:
			toolCallUpdateCount[u.ToolCallID]++
			if !toolUseIDs[u.ToolCallID] {
				t.Errorf("tool_call_update for id %s not present in the raw file's tool_use ids", u.ToolCallID)
			}
		}
	}

	if want := humanOriginCount + queuedPromptCount; userChunksNoOrigin != want {
		t.Errorf("user_message_chunk without _meta.origin = %d, want %d (human %d + queued prompts %d)",
			userChunksNoOrigin, want, humanOriginCount, queuedPromptCount)
	}
	for id := range toolUseIDs {
		if toolCallCount[id] != 1 {
			t.Errorf("tool_use id %s produced %d tool_call updates, want exactly 1", id, toolCallCount[id])
		}
		if toolCallUpdateCount[id] > 1 {
			t.Errorf("tool_use id %s produced %d tool_call_update updates, want <=1", id, toolCallUpdateCount[id])
		}
	}

	incUpdates := readIncrementalByLine(t, r, path, data)
	if !equalUpdates(t, updates, incUpdates) {
		t.Errorf("chunking-dependent output for %s", filepath.Base(path))
	}

	t.Logf("%s: %d records, %d updates (by kind: %v)", filepath.Base(path), recordCount, len(updates), kindCounts)
}

func checkSubagentFile(t *testing.T, r Reader, path string) {
	t.Helper()
	updates, _, _, err := r.Read(path, Cursor{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(updates) != 0 {
		t.Errorf("subagent file produced %d updates, want 0", len(updates))
	}
	t.Logf("%s: subagent file, %d updates", filepath.Base(path), len(updates))
}

// readIncrementalByLine mirrors the session's directory layout (including
// any subagents dir, so subagent-link lookups behave identically) into a
// temp dir, then replays the file one line at a time, carrying the cursor
// forward, to check chunking-independence against a real sample.
func readIncrementalByLine(t *testing.T, r Reader, origPath string, data []byte) []Update {
	t.Helper()

	tmpDir := t.TempDir()
	sid := strings.TrimSuffix(filepath.Base(origPath), filepath.Ext(origPath))
	origDir := filepath.Dir(origPath)

	if entries, err := os.ReadDir(filepath.Join(origDir, sid, "subagents")); err == nil {
		dstSub := filepath.Join(tmpDir, sid, "subagents")
		if err := os.MkdirAll(dstSub, 0o755); err != nil {
			t.Fatalf("mkdir subagents mirror: %v", err)
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(origDir, sid, "subagents", e.Name()))
			if err != nil {
				t.Fatalf("read subagent file: %v", err)
			}
			if err := os.WriteFile(filepath.Join(dstSub, e.Name()), b, 0o644); err != nil {
				t.Fatalf("write subagent mirror: %v", err)
			}
		}
	}

	incPath := filepath.Join(tmpDir, sid+".jsonl")
	var got []Update
	cur := Cursor{}
	var written []byte
	for _, line := range splitKeepingEnds(data) {
		written = append(written, line...)
		if err := os.WriteFile(incPath, written, 0o644); err != nil {
			t.Fatalf("write incremental copy: %v", err)
		}
		ups, next, _, err := r.Read(incPath, cur)
		if err != nil {
			t.Fatalf("incremental Read: %v", err)
		}
		got = append(got, ups...)
		cur = next
	}
	return got
}

// splitKeepingEnds splits data into lines, each retaining its trailing '\n'
// (the final line may have none).
func splitKeepingEnds(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i+1])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}

// TestSamplesPi replays real pi sessions from $HOUSTON_SAMPLES/pi. Like the
// claude-code test it logs counts only, never transcript content.
func TestSamplesPi(t *testing.T) {
	dir := os.Getenv("HOUSTON_SAMPLES")
	if dir == "" {
		t.Skip("HOUSTON_SAMPLES not set")
	}
	files, err := filepath.Glob(filepath.Join(dir, "pi", "*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) == 0 {
		t.Skip("no pi sample files")
	}
	for _, f := range files {
		f := f
		t.Run(filepath.Base(f), func(t *testing.T) {
			checkPiSampleFile(t, NewPi(), f)
		})
	}
}

type rawPiEntry struct {
	Type    string `json:"type"`
	Message *struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
	} `json:"message"`
}

type rawPiBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	ID   string `json:"id"`
}

func checkPiSampleFile(t *testing.T, r Reader, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var userMsgs, textBlocks, toolCalls, toolResults int
	var callIDs []string
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var e rawPiEntry
		if json.Unmarshal(line, &e) != nil || e.Type != "message" || e.Message == nil {
			continue
		}
		switch e.Message.Role {
		case "user":
			var s string
			if json.Unmarshal(e.Message.Content, &s) == nil {
				if s != "" {
					userMsgs++
				}
				continue
			}
			var blocks []rawPiBlock
			if json.Unmarshal(e.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type == "image" || (b.Type == "text" && b.Text != "") {
					userMsgs++
					break
				}
			}
		case "assistant":
			var blocks []rawPiBlock
			if json.Unmarshal(e.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.Text != "" {
						textBlocks++
					}
				case "toolCall":
					toolCalls++
					callIDs = append(callIDs, b.ID)
				}
			}
		case "toolResult":
			toolResults++
		}
	}

	updates, _, _, err := r.Read(path, Cursor{})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	kindCounts := map[string]int{}
	seenCall := map[string]bool{}
	orphanResults := 0
	for _, u := range updates {
		kindCounts[u.SessionUpdate]++
		switch u.SessionUpdate {
		case SessionUpdateToolCall:
			seenCall[u.ToolCallID] = true
		case SessionUpdateToolCallUpdate:
			if !seenCall[u.ToolCallID] {
				orphanResults++
			}
		}
	}
	if got := kindCounts[SessionUpdateUserMessageChunk]; got != userMsgs {
		t.Errorf("user_message_chunk = %d, want %d", got, userMsgs)
	}
	if got := kindCounts[SessionUpdateAgentMessageChunk]; got != textBlocks {
		t.Errorf("agent_message_chunk = %d, want %d", got, textBlocks)
	}
	if got := kindCounts[SessionUpdateToolCall]; got != toolCalls {
		t.Errorf("tool_call = %d, want %d", got, toolCalls)
	}
	if got := kindCounts[SessionUpdateToolCallUpdate]; got != toolResults {
		t.Errorf("tool_call_update = %d, want %d", got, toolResults)
	}
	for _, id := range callIDs {
		if _, err := r.Tool(path, id); err != nil {
			t.Errorf("Tool(%s): %v", id, err)
		}
	}

	if inc := readIncrementalByLine(t, r, path, data); !equalUpdates(t, updates, inc) {
		t.Errorf("chunking-dependent output for %s", filepath.Base(path))
	}

	t.Logf("%s: %d updates (by kind: %v), %d results without a preceding call", filepath.Base(path), len(updates), kindCounts, orphanResults)
}
