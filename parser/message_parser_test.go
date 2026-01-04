package parser

import (
	"strings"
	"testing"
)

func TestMessageParser_ClaudeCodeFormat(t *testing.T) {
	parser := NewClaudeCodeParser()

	output := `
> list all tmux sessions

● I'll help you list all tmux sessions.

● Bash(tmux list-sessions)
  List all active tmux sessions
  ⎿ main: 3 windows (created Sat Jan  4 10:00:00 2025)
  ├ nix-config: 2 windows (created Sat Jan  4 09:30:00 2025)
  └ claude-agent: 1 window (created Sat Jan  4 11:00:00 2025)

● Here are your active tmux sessions. You have 3 sessions running.
`

	parser.ProcessBuffer(output)
	messages := parser.GetMessages()

	if len(messages) != 7 {
		t.Logf("Messages parsed:")
		for i, msg := range messages {
			t.Logf("  %d. %s: %q", i, msg.Type, msg.Content)
		}
		t.Fatalf("expected 7 messages, got %d", len(messages))
	}

	// Check message types: user, agent, tool call, 3x tool output, agent
	expected := []MessageType{
		UserMessage,
		AgentMessage,
		ToolCall,
		ToolOutput,
		ToolOutput,
		ToolOutput,
		AgentMessage,
	}

	for i, msg := range messages {
		if i >= len(expected) {
			break
		}
		if msg.Type != expected[i] {
			t.Errorf("message %d: expected type %s, got %s", i, expected[i], msg.Type)
		}
	}

	// Check tool call detection
	toolMsg := messages[2]
	if toolMsg.Type != ToolCall {
		t.Errorf("expected ToolCall, got %s", toolMsg.Type)
	}
	if toolMsg.Metadata["tool"] != "Bash" {
		t.Errorf("expected tool 'Bash', got %s", toolMsg.Metadata["tool"])
	}
}

func TestMessageParser_CustomConfig(t *testing.T) {
	// Custom agent that uses different markers
	customConfig := ParserConfig{
		Name:               "custom-agent",
		UserPrefix:         ">>",
		AgentPrefix:        "[AI]",
		ToolPrefix:         "[TOOL]",
		ToolOutputPrefixes: []string{"|", "+-"},
		SpinnerChars:       []rune{'*'},
		KnownTools:         []string{"FileRead", "Execute"},
		PreserveColors:     true,
		StripStatusBar:     false,
	}

	parser := NewMessageParser(customConfig)

	output := `
>> hello

[AI] Hi there!

[TOOL] FileRead(config.json)
| {
+-   "name": "test"
+- }
`

	parser.ProcessBuffer(output)
	messages := parser.GetMessages()

	// user, agent, tool call, 3x tool output = 6 messages
	if len(messages) != 6 {
		t.Logf("Messages parsed:")
		for i, msg := range messages {
			t.Logf("  %d. %s: %q (metadata: %v)", i, msg.Type, msg.Content, msg.Metadata)
		}
		t.Fatalf("expected 6 messages, got %d", len(messages))
	}

	// Check custom markers work
	if messages[0].Type != UserMessage {
		t.Errorf("expected UserMessage, got %s", messages[0].Type)
	}
	if !strings.Contains(messages[0].Content, "hello") {
		t.Errorf("expected user message to contain 'hello', got: %s", messages[0].Content)
	}

	if messages[1].Type != AgentMessage {
		t.Errorf("expected AgentMessage, got %s", messages[1].Type)
	}

	if messages[2].Type != ToolCall {
		t.Errorf("expected ToolCall, got %s", messages[2].Type)
	}
	if messages[2].Metadata["tool"] != "FileRead" {
		t.Errorf("expected tool 'FileRead', got %s", messages[2].Metadata["tool"])
	}
}

func TestMessageParser_PreserveColors(t *testing.T) {
	parser := NewClaudeCodeParser()

	// Output with ANSI color codes (green ●)
	output := "\033[32m●\033[0m I'll help you with that."

	parser.ProcessBuffer(output)
	messages := parser.GetMessages()

	if len(messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(messages))
	}

	msg := messages[0]

	// RawContent should preserve colors
	if !strings.Contains(msg.RawContent, "\033[32m") {
		t.Errorf("expected RawContent to preserve ANSI codes, got: %s", msg.RawContent)
	}

	// Content should be clean
	if strings.Contains(msg.Content, "\033[") {
		t.Errorf("expected Content to strip ANSI codes, got: %s", msg.Content)
	}
}

func TestMessageParser_ActivityDetection(t *testing.T) {
	parser := NewClaudeCodeParser()

	output := `
✻ Thinking about your request...

● Let me help with that.
`

	parser.ProcessBuffer(output)
	state := parser.GetState()

	if state.LastActivity != "Thinking about your request" {
		t.Errorf("expected activity 'Thinking about your request', got: %s", state.LastActivity)
	}

	if state.CurrentState != StateResponding {
		t.Errorf("expected state Responding, got: %s", state.CurrentState)
	}
}

func TestMessageParser_ToolDetection(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		expectTool bool
		toolName   string
	}{
		{
			name:       "tool with parentheses",
			output:     "● Read(file.go)",
			expectTool: true,
			toolName:   "Read",
		},
		{
			name:       "tool with tree output",
			output:     "● Bash(ls)\n  ⎿ file1.txt",
			expectTool: true,
			toolName:   "Bash",
		},
		{
			name:       "agent text (not a tool)",
			output:     "● I'll help you with that task.",
			expectTool: false,
		},
		{
			name:       "agent text mentioning tool",
			output:     "● Let me read the file for you.",
			expectTool: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := NewClaudeCodeParser()
			parser.ProcessBuffer(tt.output)
			messages := parser.GetMessages()

			if len(messages) == 0 {
				t.Fatal("expected at least one message")
			}

			msg := messages[0]
			isTool := msg.Type == ToolCall

			if isTool != tt.expectTool {
				t.Errorf("expected tool=%v, got tool=%v (type=%s)", tt.expectTool, isTool, msg.Type)
			}

			if tt.expectTool && msg.Metadata["tool"] != tt.toolName {
				t.Errorf("expected tool name '%s', got '%s'", tt.toolName, msg.Metadata["tool"])
			}
		})
	}
}

func TestMessageParser_MultilineMessages(t *testing.T) {
	parser := NewClaudeCodeParser()

	output := `
> write a hello world program

● I'll create a simple hello world program for you.
  Let me write it to a file.

● Write(hello.go)
  Create hello world program
  ⎿ 1→package main
  ├ 2→
  ├ 3→import "fmt"
  ├ 4→
  └ 5→func main() {

● Done! I've created the hello world program.
`

	parser.ProcessBuffer(output)
	messages := parser.GetMessages()

	// Should detect: user, agent, tool call, 5x tool output, agent
	if len(messages) < 8 {
		t.Errorf("expected at least 8 messages, got %d", len(messages))
	}

	// Verify we have the right sequence
	types := []MessageType{}
	for _, msg := range messages {
		types = append(types, msg.Type)
	}

	// Check for user message
	if types[0] != UserMessage {
		t.Errorf("expected first message to be UserMessage, got %s", types[0])
	}

	// Check for tool call
	hasToolCall := false
	for _, msgType := range types {
		if msgType == ToolCall {
			hasToolCall = true
			break
		}
	}
	if !hasToolCall {
		t.Error("expected to find a ToolCall message")
	}

	// Check for tool output
	hasToolOutput := false
	for _, msgType := range types {
		if msgType == ToolOutput {
			hasToolOutput = true
			break
		}
	}
	if !hasToolOutput {
		t.Error("expected to find ToolOutput messages")
	}
}

func BenchmarkMessageParser_ProcessBuffer(b *testing.B) {
	parser := NewClaudeCodeParser()

	output := strings.Repeat(`
> test command

● Processing your request...

● Read(file.go)
  ⎿ package main
  ├ import "fmt"
  └ func main() {}

● Done!
`, 10)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parser.ProcessBuffer(output)
	}
}
