package hub

import (
	"os"
	"path/filepath"
	"testing"
)

const sampleJSONL = `{"type":"user","timestamp":"2026-04-18T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"refactor the thing"}]}}
{"type":"assistant","timestamp":"2026-04-18T10:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"I should read the file first"},{"type":"tool_use","id":"tu_1","name":"Read","input":{"file_path":"main.go"}}],"usage":{"input_tokens":100,"output_tokens":50}}}
{"type":"user","timestamp":"2026-04-18T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"file contents here","is_error":false}]}}
{"type":"assistant","timestamp":"2026-04-18T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"Got it, editing now."},{"type":"tool_use","id":"tu_2","name":"Edit","input":{"file_path":"main.go","old_string":"x","new_string":"y"}}],"usage":{"input_tokens":180,"output_tokens":75}}}
{"type":"user","timestamp":"2026-04-18T10:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_2","content":"edit applied","is_error":false}]}}
`

func writeJSONL(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	return path
}

func TestReadTranscriptParsesToolUseAndResult(t *testing.T) {
	path := writeJSONL(t, sampleJSONL)
	evts, offset, err := ReadTranscriptFrom(path, 0)
	if err != nil {
		t.Fatalf("ReadTranscriptFrom: %v", err)
	}
	if offset <= 0 {
		t.Errorf("offset = %d, want > 0", offset)
	}

	var tools, results, thinking, text int
	for _, e := range evts {
		switch e.Type {
		case "tool_use":
			tools++
		case "tool_result":
			results++
		case "thinking":
			thinking++
		case "text":
			text++
		}
	}
	if tools != 2 {
		t.Errorf("tool_use count = %d, want 2", tools)
	}
	if results != 2 {
		t.Errorf("tool_result count = %d, want 2", results)
	}
	if thinking != 1 {
		t.Errorf("thinking count = %d, want 1", thinking)
	}
	if text < 2 {
		t.Errorf("text count = %d, want >=2", text)
	}
}

func TestReadTranscriptExtractsFilePath(t *testing.T) {
	path := writeJSONL(t, sampleJSONL)
	evts, _, _ := ReadTranscriptFrom(path, 0)
	var readUse *TranscriptEvent
	for i := range evts {
		if evts[i].Type == "tool_use" && evts[i].ToolName == "Read" {
			readUse = &evts[i]
			break
		}
	}
	if readUse == nil {
		t.Fatalf("Read tool_use not found")
	}
	if readUse.Text != "main.go" {
		t.Errorf("hint = %q, want main.go", readUse.Text)
	}
}

func TestReadTranscriptResumesFromOffset(t *testing.T) {
	path := writeJSONL(t, sampleJSONL)
	first, offset, _ := ReadTranscriptFrom(path, 0)
	if len(first) == 0 {
		t.Fatal("first read empty")
	}
	second, _, _ := ReadTranscriptFrom(path, offset)
	if len(second) != 0 {
		t.Errorf("second read after offset got %d events, want 0", len(second))
	}

	appended := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"new!"}]}}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	_, _ = f.WriteString(appended)
	_ = f.Close()

	third, _, _ := ReadTranscriptFrom(path, offset)
	if len(third) != 1 {
		t.Errorf("after append, read %d events, want 1", len(third))
	}
	if third[0].Text != "new!" {
		t.Errorf("third[0].Text = %q, want 'new!'", third[0].Text)
	}
}

func TestReadTranscriptCapturesTokenUsage(t *testing.T) {
	path := writeJSONL(t, sampleJSONL)
	evts, _, _ := ReadTranscriptFrom(path, 0)
	var maxIn, maxOut int
	for _, e := range evts {
		if e.InputTokens > maxIn {
			maxIn = e.InputTokens
		}
		if e.OutputTokens > maxOut {
			maxOut = e.OutputTokens
		}
	}
	if maxIn != 180 {
		t.Errorf("max input tokens = %d, want 180", maxIn)
	}
	if maxOut != 75 {
		t.Errorf("max output tokens = %d, want 75", maxOut)
	}
}

func TestReadTranscriptToleratesMalformedLine(t *testing.T) {
	body := `not-json` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}` + "\n"
	path := writeJSONL(t, body)
	evts, _, err := ReadTranscriptFrom(path, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil (malformed lines silently skipped)", err)
	}
	if len(evts) != 1 {
		t.Errorf("got %d events, want 1 (malformed line skipped)", len(evts))
	}
}
