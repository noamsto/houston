package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// envelope builds a hookyard envelope payload wrapping native.
func envelope(t *testing.T, engine, canonicalEvent, nativeEvent, sessionID, toolName string, toolInput, native map[string]any) []byte {
	t.Helper()
	nativeBytes, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal native: %v", err)
	}
	env := map[string]any{
		"engine":          engine,
		"canonical_event": canonicalEvent,
		"native_event":    nativeEvent,
		"session_id":      sessionID,
		"cwd":             native["cwd"],
		"protocol":        "",
		"native":          json.RawMessage(nativeBytes),
	}
	if toolName != "" {
		env["tool_name"] = toolName
	}
	if toolInput != nil {
		env["tool_input"] = toolInput
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

// dispatchEnvelope runs Dispatch with the given CLI event arg and envelope
// payload, then reads back the resulting state file.
func dispatchEnvelope(t *testing.T, dir, cliEvent, sessionID string, payload []byte) SessionState {
	t.Helper()
	if err := Dispatch(cliEvent, dir, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	got, err := Read(Path(dir, sessionID))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return got
}

func TestEnvelopePiSessionStart(t *testing.T) {
	dir := t.TempDir()
	payload := envelope(t, "pi", "session_start", "session_start", "s1", "", nil, map[string]any{
		"cwd":             "/w",
		"hook_event_name": "session_start",
		"pi_version":      "0.85.1",
		"session_id":      "s1",
		"session_file":    "/s/2026_x.jsonl",
		"reason":          "startup",
	})
	got := dispatchEnvelope(t, dir, "", "s1", payload)

	if got.State != StateStarting {
		t.Errorf("State = %q, want starting", got.State)
	}
	if got.Agent != "pi" {
		t.Errorf("Agent = %q, want pi", got.Agent)
	}
	if got.Source != "startup" {
		t.Errorf("Source = %q, want startup", got.Source)
	}
	if got.TranscriptPath != "/s/2026_x.jsonl" {
		t.Errorf("TranscriptPath = %q, want session_file", got.TranscriptPath)
	}
}

func TestEnvelopePiPromptSubmit(t *testing.T) {
	dir := t.TempDir()
	payload := envelope(t, "pi", "prompt_submit", "input", "s2", "", nil, map[string]any{
		"cwd":        "/w",
		"session_id": "s2",
		"prompt":     "do the thing",
		"source":     "interactive",
	})
	got := dispatchEnvelope(t, dir, "", "s2", payload)

	if got.State != StateThinking {
		t.Errorf("State = %q, want thinking", got.State)
	}
	if got.Turn != 1 {
		t.Errorf("Turn = %d, want 1", got.Turn)
	}
}

func TestEnvelopePiPreTool(t *testing.T) {
	dir := t.TempDir()
	payload := envelope(t, "pi", "pre_tool", "tool_call", "s3", "Bash",
		map[string]any{"command": "echo hookyard-probe"},
		map[string]any{
			"cwd":         "/w",
			"session_id":  "s3",
			"tool_name":   "bash",
			"tool_use_id": "call_1",
			"tool_input":  map[string]any{"command": "echo hookyard-probe"},
		})
	got := dispatchEnvelope(t, dir, "", "s3", payload)

	if got.State != StateToolRunning {
		t.Errorf("State = %q, want tool-running", got.State)
	}
	if got.Tool != "Bash" {
		t.Errorf("Tool = %q, want Bash", got.Tool)
	}
	if got.ToolInputHint != "echo hookyard-probe" {
		t.Errorf("ToolInputHint = %q, want the command", got.ToolInputHint)
	}
}

func TestEnvelopePiPostTool(t *testing.T) {
	dir := t.TempDir()
	pre := envelope(t, "pi", "pre_tool", "tool_call", "s4", "Bash",
		map[string]any{"command": "echo x"},
		map[string]any{"cwd": "/w", "session_id": "s4", "tool_name": "bash", "tool_input": map[string]any{"command": "echo x"}})
	dispatchEnvelope(t, dir, "", "s4", pre)

	post := envelope(t, "pi", "post_tool", "tool_result", "s4", "Bash", nil,
		map[string]any{"cwd": "/w", "session_id": "s4", "tool_name": "bash"})
	got := dispatchEnvelope(t, dir, "", "s4", post)

	if got.State != StateThinking {
		t.Errorf("State = %q, want thinking", got.State)
	}
	if got.Tool != "" {
		t.Errorf("Tool = %q, want cleared", got.Tool)
	}
}

func TestEnvelopePiSequenceEndsWaitingOnAgentSettled(t *testing.T) {
	// pi fires turn_end after every LLM response; agent_settled is the run's
	// final signal. The full sequence must end waiting, thinking after each
	// turn_end.
	dir := t.TempDir()
	start := envelope(t, "pi", "session_start", "session_start", "s5", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s5", "session_file": "/s/pi.jsonl", "reason": "startup"})
	dispatchEnvelope(t, dir, "", "s5", start)

	input := envelope(t, "pi", "prompt_submit", "input", "s5", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s5", "prompt": "do the thing"})
	if got := dispatchEnvelope(t, dir, "", "s5", input); got.State != StateThinking {
		t.Fatalf("after input = %q, want thinking", got.State)
	}

	toolCall := envelope(t, "pi", "pre_tool", "tool_call", "s5", "Bash",
		map[string]any{"command": "echo x"},
		map[string]any{"cwd": "/w", "session_id": "s5", "tool_name": "bash", "tool_input": map[string]any{"command": "echo x"}})
	if got := dispatchEnvelope(t, dir, "", "s5", toolCall); got.State != StateToolRunning {
		t.Fatalf("after tool_call = %q, want tool-running", got.State)
	}

	toolResult := envelope(t, "pi", "post_tool", "tool_result", "s5", "Bash", nil,
		map[string]any{"cwd": "/w", "session_id": "s5", "tool_name": "bash"})
	if got := dispatchEnvelope(t, dir, "", "s5", toolResult); got.State != StateThinking {
		t.Fatalf("after tool_result = %q, want thinking", got.State)
	}

	for i := 0; i < 2; i++ {
		turnEnd := envelope(t, "pi", "turn_end", "turn_end", "s5", "", nil,
			map[string]any{"cwd": "/w", "session_id": "s5", "turn_index": i})
		if got := dispatchEnvelope(t, dir, "", "s5", turnEnd); got.State != StateThinking {
			t.Errorf("after turn_end %d = %q, want thinking", i, got.State)
		}
	}

	// The captured hookyard fixture shape for agent_settled.
	settled := envelope(t, "pi", "", "agent_settled", "s5", "", nil, map[string]any{
		"cwd":             "/w",
		"hook_event_name": "agent_settled",
		"pi_version":      "0.87.0",
		"session_file":    "/s/pi.jsonl",
		"session_id":      "s5",
	})
	got := dispatchEnvelope(t, dir, "", "s5", settled)
	if got.State != StateWaiting {
		t.Errorf("after agent_settled = %q, want waiting", got.State)
	}
}

func TestEnvelopePiAbortedRunSettlesWaiting(t *testing.T) {
	// Esc while a pi tool runs: tool_call then turn_end then agent_settled.
	dir := t.TempDir()
	toolCall := envelope(t, "pi", "pre_tool", "tool_call", "s5a", "Bash",
		map[string]any{"command": "echo x"},
		map[string]any{"cwd": "/w", "session_id": "s5a", "tool_name": "bash", "tool_input": map[string]any{"command": "echo x"}})
	dispatchEnvelope(t, dir, "", "s5a", toolCall)

	turnEnd := envelope(t, "pi", "turn_end", "turn_end", "s5a", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s5a"})
	dispatchEnvelope(t, dir, "", "s5a", turnEnd)

	settled := envelope(t, "pi", "", "agent_settled", "s5a", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s5a", "hook_event_name": "agent_settled"})
	got := dispatchEnvelope(t, dir, "", "s5a", settled)
	if got.State != StateWaiting {
		t.Errorf("aborted run after agent_settled = %q, want waiting", got.State)
	}
}

func TestEnvelopeCodexSessionEnd(t *testing.T) {
	dir := t.TempDir()
	start := envelope(t, "codex", "session_start", "session_start", "s14", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s14"})
	dispatchEnvelope(t, dir, "", "s14", start)

	payload := envelope(t, "codex", "", "SessionEnd", "s14", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s14", "reason": "quit"})
	got := dispatchEnvelope(t, dir, "", "s14", payload)
	if got.State != StateEnded {
		t.Errorf("State = %q, want ended", got.State)
	}
	if got.Reason != "quit" {
		t.Errorf("Reason = %q, want quit", got.Reason)
	}
}

func TestEnvelopeCursorSessionEnd(t *testing.T) {
	dir := t.TempDir()
	start := envelope(t, "cursor", "session_start", "session_start", "s15", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s15"})
	dispatchEnvelope(t, dir, "", "s15", start)

	payload := envelope(t, "cursor", "", "sessionEnd", "s15", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s15", "reason": "quit"})
	got := dispatchEnvelope(t, dir, "", "s15", payload)
	if got.State != StateEnded {
		t.Errorf("State = %q, want ended", got.State)
	}
	if got.Reason != "quit" {
		t.Errorf("Reason = %q, want quit", got.Reason)
	}
}

func TestEnvelopePiPreCompact(t *testing.T) {
	dir := t.TempDir()
	payload := envelope(t, "pi", "pre_compact", "pre_compact", "s6", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s6"})
	got := dispatchEnvelope(t, dir, "", "s6", payload)
	if got.State != StateCompacting {
		t.Errorf("State = %q, want compacting", got.State)
	}
}

func TestEnvelopePiSessionShutdown(t *testing.T) {
	dir := t.TempDir()
	payload := envelope(t, "pi", "", "session_shutdown", "s7", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s7", "reason": "quit"})
	got := dispatchEnvelope(t, dir, "", "s7", payload)
	if got.State != StateEnded {
		t.Errorf("State = %q, want ended", got.State)
	}
	if got.Reason != "quit" {
		t.Errorf("Reason = %q, want quit", got.Reason)
	}
}

func TestEnvelopePiSessionShutdownReloadIsNotAnEnd(t *testing.T) {
	dir := t.TempDir()
	start := envelope(t, "pi", "session_start", "session_start", "s7b", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s7b"})
	dispatchEnvelope(t, dir, "", "s7b", start)
	prompt := envelope(t, "pi", "prompt_submit", "input", "s7b", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s7b"})
	got := dispatchEnvelope(t, dir, "", "s7b", prompt)
	if got.State != StateThinking {
		t.Fatalf("setup: State = %q, want thinking", got.State)
	}

	path := Path(dir, "s7b")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}

	reload := envelope(t, "pi", "", "session_shutdown", "s7b", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s7b", "reason": "reload"})
	if err := Dispatch("", dir, bytes.NewReader(reload)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("state file rewritten on reload shutdown, want unchanged")
	}

	got, err = Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.State != StateThinking {
		t.Errorf("State = %q after reload shutdown, want thinking", got.State)
	}
}

func TestEnvelopeCodexTurnEndAlwaysWaiting(t *testing.T) {
	// Only pi gets the intermediate-turn exception.
	dir := t.TempDir()
	pre := envelope(t, "codex", "pre_tool", "tool_call", "s8", "Bash",
		map[string]any{"command": "echo x"},
		map[string]any{"cwd": "/w", "session_id": "s8", "tool_input": map[string]any{"command": "echo x"}})
	dispatchEnvelope(t, dir, "", "s8", pre)

	turnEnd := envelope(t, "codex", "turn_end", "turn_end", "s8", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s8"})
	got := dispatchEnvelope(t, dir, "", "s8", turnEnd)
	if got.State != StateWaiting {
		t.Errorf("codex turn_end = %q, want waiting", got.State)
	}
}

func TestEnvelopeUnknownCanonicalEventIsNoop(t *testing.T) {
	dir := t.TempDir()
	payload := envelope(t, "codex", "", "PermissionRequest", "s9", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s9"})
	if err := Dispatch("", dir, bytes.NewReader(payload)); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	got, err := Read(Path(dir, "s9"))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.State != "" {
		t.Errorf("state file written for an untracked envelope event: %+v", got)
	}
}

func TestEnvelopeUnknownEngineIsError(t *testing.T) {
	dir := t.TempDir()
	payload := envelope(t, "bogus-engine", "session_start", "session_start", "s10", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s10"})
	err := Dispatch("", dir, bytes.NewReader(payload))
	if err == nil {
		t.Fatalf("expected error for unknown engine, got nil")
	}
	got, _ := Read(Path(dir, "s10"))
	if got.State != "" {
		t.Errorf("state file written despite unknown engine: %+v", got)
	}
}

func TestEnvelopeCLIArgIgnored(t *testing.T) {
	// The envelope's own event wins over the CLI arg in envelope mode.
	dir := t.TempDir()
	payload := envelope(t, "pi", "pre_tool", "tool_call", "s11", "Bash",
		map[string]any{"command": "echo x"},
		map[string]any{"cwd": "/w", "session_id": "s11", "tool_input": map[string]any{"command": "echo x"}})
	got := dispatchEnvelope(t, dir, EventStop, "s11", payload)
	if got.State != StateToolRunning {
		t.Errorf("State = %q, want tool-running (CLI arg %q must be ignored)", got.State, EventStop)
	}
}

func TestEnvelopeFreshStateDirGetsWritten(t *testing.T) {
	// No pre-existing "claude/" subdir — lockStateDir must MkdirAll before
	// opening the lock file.
	dir := t.TempDir()
	payload := envelope(t, "pi", "session_start", "session_start", "s12", "", nil,
		map[string]any{"cwd": "/w", "session_id": "s12", "reason": "startup"})
	got := dispatchEnvelope(t, dir, "", "s12", payload)
	if got.State != StateStarting {
		t.Errorf("State = %q, want starting", got.State)
	}
}

func TestEnvelopeClaudeViaEnvelopeMatchesNativePath(t *testing.T) {
	// A claude-code envelope must yield the same SessionState as the
	// native payload, except for Agent.
	t.Setenv("TMUX_PANE", "")
	t.Setenv("TMUX", "")

	native := map[string]any{
		"hook_event_name":   "Notification",
		"session_id":        "s13",
		"cwd":               "/w",
		"transcript_path":   "/t/s13.jsonl",
		"notification_type": "permission_prompt",
		"message":           "Claude needs permission to run Bash",
	}
	nativeBytes, err := json.Marshal(native)
	if err != nil {
		t.Fatalf("marshal native: %v", err)
	}

	nativeDir := t.TempDir()
	if err := Dispatch("", nativeDir, bytes.NewReader(nativeBytes)); err != nil {
		t.Fatalf("Dispatch native: %v", err)
	}
	nativeGot, err := Read(Path(nativeDir, "s13"))
	if err != nil {
		t.Fatalf("Read native: %v", err)
	}

	envDir := t.TempDir()
	envPayload := envelope(t, "claude-code", "", "Notification", "s13", "", nil, native)
	if err := Dispatch("", envDir, bytes.NewReader(envPayload)); err != nil {
		t.Fatalf("Dispatch envelope: %v", err)
	}
	envGot, err := Read(Path(envDir, "s13"))
	if err != nil {
		t.Fatalf("Read envelope: %v", err)
	}

	if envGot.Agent != AgentClaude {
		t.Errorf("Agent = %q, want %q", envGot.Agent, AgentClaude)
	}
	if nativeGot.Agent != "" {
		t.Errorf("native path Agent = %q, want empty", nativeGot.Agent)
	}
	nativeGot.Agent, envGot.Agent = "", ""
	nativeGot.UpdatedAt, envGot.UpdatedAt = 0, 0
	nativeGot.Since, envGot.Since = 0, 0
	nativeGot.PID, envGot.PID = 0, 0
	if nativeGot != envGot {
		t.Errorf("native and envelope states differ:\n native = %+v\n envelope = %+v", nativeGot, envGot)
	}
}
