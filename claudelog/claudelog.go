// Package claudelog reads Claude Code JSONL conversation logs.
package claudelog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/noamsto/houston/parser"
)

// Message represents a single entry in the JSONL log.
type Message struct {
	Type       string    `json:"type"` // "user", "assistant", "file-history-snapshot", "summary"
	UUID       string    `json:"uuid"`
	ParentUUID string    `json:"parentUuid"`
	SessionID  string    `json:"sessionId"`
	Timestamp  time.Time `json:"timestamp"`
	CWD        string    `json:"cwd"`
	GitBranch  string    `json:"gitBranch"`
	Todos      []Todo    `json:"todos"`

	// The actual message content
	Message MessageContent `json:"message"`

	// For summary type
	Summary string `json:"summary"`
}

// MessageContent represents the content of a user or assistant message.
type MessageContent struct {
	Role    string `json:"role"` // "user" or "assistant"
	Model   string `json:"model"`
	ID      string `json:"id"`
	Content any    `json:"content"` // string for user, []ContentBlock for assistant

	StopReason string `json:"stop_reason"` // "tool_use", "end_turn", etc.
	Usage      Usage  `json:"usage"`
}

// ContentBlock represents a block in assistant message content.
type ContentBlock struct {
	Type string `json:"type"` // "thinking", "text", "tool_use"

	// For "thinking" type
	Thinking string `json:"thinking"`

	// For "text" type
	Text string `json:"text"`

	// For "tool_use" type
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// ToolResult represents a tool result in user message content.
type ToolResult struct {
	Type      string `json:"type"` // "tool_result"
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
}

// Usage tracks token usage.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Todo represents a todo item.
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"` // "pending", "in_progress", "completed"
	ActiveForm string `json:"activeForm"`
}

// SessionState represents the current state of a Claude session.
type SessionState struct {
	SessionID    string
	CWD          string
	GitBranch    string
	LastActivity time.Time

	// Current state detection
	IsWorking           bool
	IsWaiting           bool   // Waiting for user input
	IsThinking          bool
	CurrentTool         string // Currently running tool (if any)
	LastToolName        string // Last tool that was called
	PendingToolUseID    string // Tool use ID waiting for permission/result
	PendingToolName     string // Tool name waiting for permission/result
	IsWaitingPermission bool   // Waiting for permission prompt
	Todos               []Todo // Current todo list
	Question            string // Current question being asked
	Choices             []string // Current choices being presented
	LastAssistant       string // Last assistant text (for activity display)
	Error               string // Last error if any
}

// ProjectDir returns the Claude projects directory for a given working directory.
func ProjectDir(cwd string) string {
	// Convert /home/foo/bar to -home-foo-bar
	encoded := strings.ReplaceAll(cwd, "/", "-")
	encoded = strings.TrimPrefix(encoded, "-") // Remove leading dash if present
	encoded = "-" + encoded                    // Add it back (Claude's format)

	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".claude", "projects", encoded)
}

// FindLatestSession finds the most recently modified session file in a project directory.
func FindLatestSession(projectDir string) (string, error) {
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("reading project dir: %w", err)
	}

	var sessions []os.DirEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			// Skip agent sessions (subagents)
			if strings.HasPrefix(e.Name(), "agent-") {
				continue
			}
			sessions = append(sessions, e)
		}
	}

	if len(sessions) == 0 {
		return "", fmt.Errorf("no session files found in %s", projectDir)
	}

	// Sort by modification time (newest first)
	sort.Slice(sessions, func(i, j int) bool {
		infoI, _ := sessions[i].Info()
		infoJ, _ := sessions[j].Info()
		if infoI == nil || infoJ == nil {
			return false
		}
		return infoI.ModTime().After(infoJ.ModTime())
	})

	return filepath.Join(projectDir, sessions[0].Name()), nil
}

// ReadSession reads all messages from a session file.
func ReadSession(path string) ([]Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening session file: %w", err)
	}
	defer f.Close()

	var messages []Message
	scanner := bufio.NewScanner(f)
	// Increase buffer size for large messages
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			// Skip malformed lines
			continue
		}

		// Skip file-history-snapshot entries
		if msg.Type == "file-history-snapshot" {
			continue
		}

		messages = append(messages, msg)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning session file: %w", err)
	}

	return messages, nil
}

// ReadLastMessages reads the last N messages from a session file.
// This is more efficient than reading the entire file.
func ReadLastMessages(path string, n int) ([]Message, error) {
	// For now, read all and return last N
	// TODO: Optimize with reverse file reading
	all, err := ReadSession(path)
	if err != nil {
		return nil, err
	}

	if len(all) <= n {
		return all, nil
	}
	return all[len(all)-n:], nil
}

// GetSessionState analyzes messages and returns the current session state.
func GetSessionState(messages []Message) SessionState {
	state := SessionState{}

	if len(messages) == 0 {
		return state
	}

	// Get basic info from first/last messages
	for _, msg := range messages {
		if msg.SessionID != "" {
			state.SessionID = msg.SessionID
		}
		if msg.CWD != "" {
			state.CWD = msg.CWD
		}
		if msg.GitBranch != "" {
			state.GitBranch = msg.GitBranch
		}
	}

	// First pass: scan last 20 messages to find pending tool_use
	// Process FORWARD (oldest to newest) to correctly track tool_use -> tool_result flow
	startIdx := len(messages) - 20
	if startIdx < 0 {
		startIdx = 0
	}

	for i := startIdx; i < len(messages); i++ {
		msg := messages[i]

		if !msg.Timestamp.IsZero() && msg.Timestamp.After(state.LastActivity) {
			state.LastActivity = msg.Timestamp
		}

		if msg.Type == "assistant" {
			blocks := parseContentBlocks(msg.Message.Content)

			for _, block := range blocks {
				switch block.Type {
				case "tool_use":
					// Track this tool_use as pending
					state.PendingToolUseID = block.ID
					state.PendingToolName = block.Name
				}
			}
		}

		if msg.Type == "user" {
			// Check if this user message contains tool_result for pending tool_use
			if state.PendingToolUseID != "" && hasToolResultFor(msg.Message.Content, state.PendingToolUseID) {
				// Tool result received, clear pending
				state.PendingToolUseID = ""
				state.PendingToolName = ""
			}
		}
	}

	// Second pass: analyze backwards from end for current state
	for i := len(messages) - 1; i >= 0 && i >= len(messages)-20; i-- {
		msg := messages[i]

		// Get todos from the most recent message that has them
		if len(state.Todos) == 0 && len(msg.Todos) > 0 {
			state.Todos = msg.Todos
		}

		if msg.Type == "assistant" {
			blocks := parseContentBlocks(msg.Message.Content)

			for _, block := range blocks {
				switch block.Type {
				case "thinking":
					if state.LastAssistant == "" {
						state.IsThinking = true
					}
				case "text":
					if state.LastAssistant == "" {
						state.LastAssistant = block.Text
					}
					// Check for questions/choices in the text
					if state.Question == "" {
						state.Question, state.Choices = detectQuestionAndChoices(block.Text)
					}
				case "tool_use":
					if state.LastToolName == "" {
						state.LastToolName = block.Name
					}
					// If the last message is a tool_use, we're waiting for results
					if i == len(messages)-1 {
						state.IsWorking = true
						state.CurrentTool = block.Name
					}
				}
			}

			// Check stop reason
			if msg.Message.StopReason == "tool_use" && i == len(messages)-1 {
				state.IsWorking = true
			}
		}

		if msg.Type == "user" {
			// If user message is last and contains tool_result, Claude is still working
			if i == len(messages)-1 {
				if isToolResult(msg.Message.Content) {
					state.IsWorking = true
				} else {
					// User sent a message, waiting for Claude
					state.IsWaiting = true
				}
			}
			break // Stop looking back once we hit a user message
		}
	}

	// If we have a question but no working state, we're waiting for input
	if state.Question != "" && !state.IsWorking {
		state.IsWaiting = true
	}

	// If we have a pending tool_use without result, might be waiting for permission
	// The caller should check terminal output for permission prompts
	if state.PendingToolUseID != "" {
		state.IsWaitingPermission = true
	}

	return state
}

// parseContentBlocks parses the content field into ContentBlocks.
func parseContentBlocks(content any) []ContentBlock {
	if content == nil {
		return nil
	}

	// Content could be a string (user message) or []any (assistant message)
	switch c := content.(type) {
	case string:
		return []ContentBlock{{Type: "text", Text: c}}
	case []any:
		var blocks []ContentBlock
		for _, item := range c {
			if m, ok := item.(map[string]any); ok {
				block := ContentBlock{}
				if t, ok := m["type"].(string); ok {
					block.Type = t
				}
				if t, ok := m["thinking"].(string); ok {
					block.Thinking = t
				}
				if t, ok := m["text"].(string); ok {
					block.Text = t
				}
				if t, ok := m["name"].(string); ok {
					block.Name = t
				}
				if t, ok := m["id"].(string); ok {
					block.ID = t
				}
				if t, ok := m["input"].(map[string]any); ok {
					block.Input = t
				}
				blocks = append(blocks, block)
			}
		}
		return blocks
	}
	return nil
}

// isToolResult checks if a message content is a tool result.
func isToolResult(content any) bool {
	if arr, ok := content.([]any); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t == "tool_result" {
					return true
				}
			}
		}
	}
	return false
}

// hasToolResultFor checks if content has a tool_result for a specific tool_use ID.
func hasToolResultFor(content any, toolUseID string) bool {
	if arr, ok := content.([]any); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["type"].(string); ok && t == "tool_result" {
					if id, ok := m["tool_use_id"].(string); ok && id == toolUseID {
						return true
					}
				}
			}
		}
	}
	return false
}

// detectQuestionAndChoices looks for Claude Code's specific choice UI patterns.
// Claude Code uses a specific format for choices, not general markdown lists.
func detectQuestionAndChoices(text string) (string, []string) {
	lines := strings.Split(text, "\n")
	var question string
	var choices []string

	// Look for Claude Code's specific choice format:
	// Usually appears at the end of messages with numbered options
	// and cursor indicators like ❯ or >
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		// Look for cursor indicators (Claude Code's choice UI)
		if strings.HasPrefix(line, "❯") || strings.HasPrefix(line, ">") {
			choice := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "❯"), ">"))
			if choice != "" && len(choice) < 100 { // Reasonable choice length
				choices = append([]string{choice}, choices...)
			}
			continue
		}

		// Look for numbered choices in Claude's format (without the cursor)
		// But only if we already found a cursor choice (indicates we're in a choice block)
		if len(choices) > 0 && len(line) > 2 {
			if (line[0] >= '1' && line[0] <= '9') && (line[1] == '.' || line[1] == ')') {
				choice := strings.TrimSpace(line[2:])
				if len(choice) < 100 {
					choices = append([]string{choice}, choices...)
				}
			}
		}

		// Stop looking once we hit substantial text (not in choice block)
		if len(line) > 100 {
			break
		}
	}

	// Look for question (usually ends with ?)
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-10; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasSuffix(line, "?") && len(line) > 5 && len(line) < 200 {
			question = line
			break
		}
	}

	return question, choices
}

// ToParserResult converts SessionState to parser.Result for compatibility with existing code.
func (s *SessionState) ToParserResult() parser.Result {
	result := parser.Result{
		Question: s.Question,
		Choices:  s.Choices,
		Activity: s.Activity(),
	}

	// Determine type based on state
	if len(s.Choices) > 0 {
		result.Type = parser.TypeChoice
	} else if s.IsWaitingPermission {
		// Waiting for permission prompt - mark as needing attention
		// The actual choices should be parsed from terminal by the caller
		result.Type = parser.TypeQuestion
		if result.Question == "" {
			result.Question = "Waiting for permission..."
		}
	} else if s.Question != "" {
		result.Type = parser.TypeQuestion
	} else if s.Error != "" {
		result.Type = parser.TypeError
		result.ErrorSnippet = s.Error
	} else if s.IsWorking || s.CurrentTool != "" {
		result.Type = parser.TypeWorking
	} else if s.IsWaiting {
		result.Type = parser.TypeQuestion
	} else {
		result.Type = parser.TypeIdle
	}

	// Mode detection would need terminal output, so leave as unknown
	result.Mode = parser.ModeUnknown

	return result
}

// Activity returns a human-readable description of what Claude is currently doing.
func (s *SessionState) Activity() string {
	if s.CurrentTool != "" {
		return toolToActivity(s.CurrentTool)
	}
	if s.IsThinking {
		return "Thinking..."
	}
	if s.IsWorking {
		if s.LastToolName != "" {
			return toolToActivity(s.LastToolName)
		}
		return "Working..."
	}
	if s.IsWaiting {
		if s.Question != "" {
			return s.Question
		}
		return "Waiting for input"
	}
	return "Idle"
}

// StatusType returns a status type string for UI indicators.
func (s *SessionState) StatusType() string {
	if s.Question != "" || len(s.Choices) > 0 {
		return "attention"
	}
	if s.IsWorking || s.CurrentTool != "" {
		return "working"
	}
	if s.IsWaiting {
		return "attention"
	}
	return "idle"
}

// toolToActivity converts a tool name to a human-readable activity.
func toolToActivity(tool string) string {
	switch tool {
	case "Read":
		return "Reading file"
	case "Write":
		return "Writing file"
	case "Edit":
		return "Editing file"
	case "Bash":
		return "Running command"
	case "Glob":
		return "Searching files"
	case "Grep":
		return "Searching content"
	case "Task":
		return "Running agent"
	case "TodoWrite":
		return "Updating todos"
	case "WebFetch":
		return "Fetching URL"
	case "WebSearch":
		return "Searching web"
	case "AskUserQuestion":
		return "Asking question"
	default:
		if tool != "" {
			return "Running " + tool
		}
		return "Working..."
	}
}

// GetStateForPane returns the session state for a tmux pane's working directory.
func GetStateForPane(paneCWD string) (*SessionState, error) {
	projectDir := ProjectDir(paneCWD)

	sessionPath, err := FindLatestSession(projectDir)
	if err != nil {
		return nil, err
	}

	// Read last 50 messages for efficiency
	messages, err := ReadLastMessages(sessionPath, 50)
	if err != nil {
		return nil, err
	}

	state := GetSessionState(messages)
	return &state, nil
}
