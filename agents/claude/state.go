package claude

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
	Message    MessageContent `json:"message"`
	Summary    string `json:"summary"`
}

// MessageContent represents the content of a user or assistant message.
type MessageContent struct {
	Role       string `json:"role"`
	Model      string `json:"model"`
	ID         string `json:"id"`
	Content    any    `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      Usage  `json:"usage"`
}

// ContentBlock represents a block in assistant message content.
type ContentBlock struct {
	Type     string         `json:"type"`
	Thinking string         `json:"thinking"`
	Text     string         `json:"text"`
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Input    map[string]any `json:"input"`
}

// Usage tracks token usage.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Todo represents a todo item.
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm"`
}

// SessionState represents the current state of a Claude session.
type SessionState struct {
	SessionID           string
	CWD                 string
	GitBranch           string
	LastActivity        time.Time
	IsWorking           bool
	IsWaiting           bool
	IsThinking          bool
	CurrentTool         string
	LastToolName        string
	PendingToolUseID    string
	PendingToolName     string
	IsWaitingPermission bool
	Todos               []Todo
	Question            string
	Choices             []string
	LastAssistant       string
	Error               string
}

// ProjectDir returns the Claude projects directory for a given working directory.
func ProjectDir(cwd string) string {
	encoded := strings.ReplaceAll(cwd, "/", "-")
	encoded = strings.TrimPrefix(encoded, "-")
	encoded = "-" + encoded

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
			if strings.HasPrefix(e.Name(), "agent-") {
				continue
			}
			sessions = append(sessions, e)
		}
	}

	if len(sessions) == 0 {
		return "", fmt.Errorf("no session files found in %s", projectDir)
	}

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

// ReadLastMessages reads the last N messages from a session file.
func ReadLastMessages(path string, n int) ([]Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening session file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var messages []Message
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		if msg.Type == "file-history-snapshot" {
			continue
		}

		messages = append(messages, msg)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning session file: %w", err)
	}

	if len(messages) <= n {
		return messages, nil
	}
	return messages[len(messages)-n:], nil
}

// GetSessionState analyzes messages and returns the current session state.
func GetSessionState(messages []Message) SessionState {
	state := SessionState{}

	if len(messages) == 0 {
		return state
	}

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

	startIdx := max(len(messages)-20, 0)

	for i := startIdx; i < len(messages); i++ {
		msg := messages[i]

		if !msg.Timestamp.IsZero() && msg.Timestamp.After(state.LastActivity) {
			state.LastActivity = msg.Timestamp
		}

		switch msg.Type {
		case "assistant":
			blocks := parseContentBlocks(msg.Message.Content)

			for _, block := range blocks {
				switch block.Type {
				case "tool_use":
					state.PendingToolUseID = block.ID
					state.PendingToolName = block.Name
					state.IsWaitingPermission = true
					state.LastToolName = block.Name
				case "text":
					state.LastAssistant = block.Text
					q, c := detectQuestionAndChoices(block.Text)
					if q != "" {
						state.Question = q
					}
					if len(c) > 0 {
						state.Choices = c
					}
				case "thinking":
					state.IsThinking = true
				}
			}

			switch msg.Message.StopReason {
			case "tool_use":
				state.IsWorking = true
			case "end_turn":
				state.IsWaiting = true
				state.IsWorking = false
			}
		case "user":
			if hasToolResultFor(msg.Message.Content, state.PendingToolUseID) {
				state.PendingToolUseID = ""
				state.PendingToolName = ""
				state.IsWaitingPermission = false
			}

			if isToolResult(msg.Message.Content) {
				state.IsWorking = true
				state.IsWaiting = false
			} else {
				state.IsWaiting = false
				state.Question = ""
				state.Choices = nil
			}
		}

		if len(msg.Todos) > 0 {
			state.Todos = msg.Todos
		}
	}

	return state
}

// GetStateFromFiles reads state from Claude's JSONL files.
func GetStateFromFiles(cwd string) (*parser.Result, error) {
	projectDir := ProjectDir(cwd)

	sessionPath, err := FindLatestSession(projectDir)
	if err != nil {
		return nil, err
	}

	messages, err := ReadLastMessages(sessionPath, 50)
	if err != nil {
		return nil, err
	}

	state := GetSessionState(messages)
	result := state.ToParserResult()
	return &result, nil
}

// ToParserResult converts SessionState to parser.Result.
func (s *SessionState) ToParserResult() parser.Result {
	result := parser.Result{
		Question: s.Question,
		Choices:  s.Choices,
		Activity: s.Activity(),
	}

	if len(s.Choices) > 0 {
		result.Type = parser.TypeChoice
	} else if s.IsWaitingPermission {
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
		// Only treat as question if there's actually a question to answer.
		// Plain end_turn (waiting for user input) is idle/done.
		if s.Question != "" {
			result.Type = parser.TypeQuestion
		} else {
			result.Type = parser.TypeDone
		}
	} else {
		result.Type = parser.TypeIdle
	}

	result.Mode = parser.ModeUnknown
	return result
}

// Activity returns a human-readable description of current activity.
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

func parseContentBlocks(content any) []ContentBlock {
	if content == nil {
		return nil
	}

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

func detectQuestionAndChoices(text string) (string, []string) {
	lines := strings.Split(text, "\n")
	var question string
	var choices []string

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "❯") || strings.HasPrefix(line, ">") {
			choice := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(line, "❯"), ">"))
			if choice != "" && len(choice) < 100 {
				choices = append([]string{choice}, choices...)
			}
			continue
		}

		if len(choices) > 0 && len(line) > 2 {
			if (line[0] >= '1' && line[0] <= '9') && (line[1] == '.' || line[1] == ')') {
				choice := strings.TrimSpace(line[2:])
				if len(choice) < 100 {
					choices = append([]string{choice}, choices...)
				}
			}
		}

		if len(line) > 100 {
			break
		}
	}

	for i := len(lines) - 1; i >= 0 && i >= len(lines)-10; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasSuffix(line, "?") && len(line) > 5 && len(line) < 200 {
			question = line
			break
		}
	}

	return question, choices
}
