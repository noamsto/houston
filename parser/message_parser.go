package parser

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ParserConfig defines conversation delimiters and markers for a specific agent/format
type ParserConfig struct {
	Name               string   // e.g., "claude-code", "custom-agent"
	UserPrefix         string   // Marker for user input (e.g., ">")
	AgentPrefix        string   // Marker for agent responses (e.g., "●")
	ToolPrefix         string   // Marker for tool calls (e.g., "●", same as agent)
	ToolOutputPrefixes []string // Markers for tool output lines (e.g., ["⎿", "├", "└", "│"])
	SpinnerChars       []rune   // Activity spinner characters (e.g., ['✻'])
	KnownTools         []string // Tool names to detect (e.g., ["Read", "Write", "Bash"])
	PreserveColors     bool     // Whether to preserve ANSI color codes
	StripStatusBar     bool     // Whether to strip status bar lines (e.g., "-- INSERT --")
}

// ClaudeCodeConfig is the default configuration for Claude Code output
var ClaudeCodeConfig = ParserConfig{
	Name:               "claude-code",
	UserPrefix:         ">",
	AgentPrefix:        "●",
	ToolPrefix:         "●",
	ToolOutputPrefixes: []string{"⎿", "├", "└", "│"},
	SpinnerChars:       []rune{'✻', '⏺', '●', '◐', '◓', '◑', '◒'},
	KnownTools: []string{
		"Read", "Write", "Edit", "Bash", "Grep", "Glob", "Task",
		"NotebookEdit", "WebFetch", "WebSearch", "AskUserQuestion",
		"Skill", "TodoWrite", "EnterPlanMode", "ExitPlanMode",
	},
	PreserveColors: true,
	StripStatusBar: true,
}

// MessageType represents different types of messages in the conversation
type MessageType int

const (
	UserMessage MessageType = iota
	AgentMessage
	ToolCall
	ToolOutput
	Activity
)

func (t MessageType) String() string {
	return [...]string{"user", "agent", "tool-call", "tool-output", "activity"}[t]
}

// Message represents a single message in the conversation
type Message struct {
	Type       MessageType
	Content    string            // Clean content (colors stripped for matching)
	RawContent string            // Original with ANSI colors (for display)
	Timestamp  time.Time
	Metadata   map[string]string // tool name, activity, line numbers, etc.
}

// StateType represents the current state of the agent
type StateType int

const (
	StateIdle StateType = iota
	StateThinking
	StateResponding
	StateRunningTool
	StateWaitingForInput
	StateWaitingForClaude
)

func (s StateType) String() string {
	return [...]string{"idle", "thinking", "responding", "running-tool", "waiting-input", "waiting-claude"}[s]
}

// ConversationState tracks the parsed conversation and current state
type ConversationState struct {
	Messages     []Message
	CurrentState StateType
	LastActivity string
	LastUpdate   time.Time

	// UI state for frontend
	Question     string   // Current question from agent
	Choices      []string // Current choices from agent (for numbered buttons)
	HasError     bool     // Whether agent reported an error
	ErrorSnippet string   // Error message if any
}

// MessageParser parses agent output into structured messages
type MessageParser struct {
	config       ParserConfig
	buffer       []string          // Raw output lines with ANSI colors
	state        ConversationState
	seenMessages map[int]bool      // Track processed lines
	ansiRegex    *regexp.Regexp    // Compiled ANSI color regex
}

// NewMessageParser creates a new parser with the given configuration
func NewMessageParser(config ParserConfig) *MessageParser {
	return &MessageParser{
		config:       config,
		state:        ConversationState{Messages: []Message{}},
		seenMessages: make(map[int]bool),
		ansiRegex:    regexp.MustCompile(`\x1b\[[0-9;]*m`),
	}
}

// NewClaudeCodeParser creates a parser with Claude Code defaults
func NewClaudeCodeParser() *MessageParser {
	return NewMessageParser(ClaudeCodeConfig)
}

// ProcessBuffer processes a full output buffer (from tmux capture)
// This is the main entry point for polling-based updates
func (p *MessageParser) ProcessBuffer(output string) {
	lines := strings.Split(output, "\n")

	// Replace buffer with new capture
	p.buffer = lines

	// Reset seen messages when buffer is replaced
	p.seenMessages = make(map[int]bool)

	// Re-parse entire buffer
	p.detectMessages()

	p.state.LastUpdate = time.Now()
}

// ProcessLine processes a single new line (for streaming/control mode)
func (p *MessageParser) ProcessLine(line string) {
	p.buffer = append(p.buffer, line)

	// Keep buffer size manageable
	if len(p.buffer) > 1000 {
		p.buffer = p.buffer[len(p.buffer)-1000:]
		// Clear old seen messages
		p.seenMessages = make(map[int]bool)
	}

	p.detectMessages()
	p.state.LastUpdate = time.Now()
}

// GetState returns the current conversation state
func (p *MessageParser) GetState() ConversationState {
	return p.state
}

// GetMessages returns all parsed messages
func (p *MessageParser) GetMessages() []Message {
	return p.state.Messages
}

// GetLastMessages returns the N most recent messages
func (p *MessageParser) GetLastMessages(n int) []Message {
	if len(p.state.Messages) <= n {
		return p.state.Messages
	}
	return p.state.Messages[len(p.state.Messages)-n:]
}

// stripColors removes ANSI escape codes from a line
func (p *MessageParser) stripColors(s string) string {
	if !p.config.PreserveColors {
		return s // Already stripped or not needed
	}
	return p.ansiRegex.ReplaceAllString(s, "")
}

// detectMessages scans the buffer for message boundaries
func (p *MessageParser) detectMessages() {
	// Scan forward through buffer (oldest to newest)
	for i := 0; i < len(p.buffer); i++ {
		if p.seenMessages[i] {
			continue
		}

		// Strip colors for pattern matching, keep raw for display
		rawLine := p.buffer[i]
		cleanLine := strings.TrimSpace(p.stripColors(rawLine))

		// Skip empty lines
		if cleanLine == "" {
			p.seenMessages[i] = true
			continue
		}

		// User message: starts with UserPrefix (">")
		if strings.HasPrefix(cleanLine, p.config.UserPrefix) {
			if msg := p.extractUserMessage(i); msg != nil {
				p.state.Messages = append(p.state.Messages, *msg)
				p.state.CurrentState = StateWaitingForClaude
			}
			continue
		}

		// Tool prefix: explicit tool calls (if different from agent prefix)
		if p.config.ToolPrefix != p.config.AgentPrefix && strings.HasPrefix(cleanLine, p.config.ToolPrefix) {
			if msg := p.extractToolCall(i); msg != nil {
				p.state.Messages = append(p.state.Messages, *msg)
				p.state.CurrentState = StateRunningTool
			}
			continue
		}

		// Agent prefix: could be agent text OR tool call (if ToolPrefix == AgentPrefix)
		if strings.HasPrefix(cleanLine, p.config.AgentPrefix) {
			if p.isToolCall(i) {
				if msg := p.extractToolCall(i); msg != nil {
					p.state.Messages = append(p.state.Messages, *msg)
					p.state.CurrentState = StateRunningTool
				}
			} else {
				if msg := p.extractAgentMessage(i); msg != nil {
					p.state.Messages = append(p.state.Messages, *msg)
					p.state.CurrentState = StateResponding
				}
			}
			continue
		}

		// Tool output: starts with tool output prefix
		if p.isToolOutput(cleanLine) {
			if msg := p.extractToolOutput(i); msg != nil {
				p.state.Messages = append(p.state.Messages, *msg)
			}
			continue
		}

		// Activity spinner
		if p.hasSpinner(cleanLine) {
			if activity := p.extractActivity(cleanLine); activity != "" {
				p.state.LastActivity = activity
				p.state.CurrentState = StateThinking
			}
			p.seenMessages[i] = true
			continue
		}

		p.seenMessages[i] = true
	}

	// After parsing all messages, detect UI state (choices, questions, errors)
	p.detectUIState()
}

// detectUIState extracts UI-relevant state from AGENT messages only
// This prevents false positives from user input containing numbers or questions
func (p *MessageParser) detectUIState() {
	// Reset UI state
	p.state.Question = ""
	p.state.Choices = []string{}
	p.state.HasError = false
	p.state.ErrorSnippet = ""

	// Scan buffer forwards for agent sections (marked by ●)
	// Numbered choices appear as continuation lines after agent message with question
	var agentSections []string
	var currentSection []string
	inAgentSection := false

	for i := 0; i < len(p.buffer); i++ {
		rawLine := p.buffer[i]
		cleanLine := strings.TrimSpace(p.stripColors(rawLine))

		if cleanLine == "" {
			continue
		}

		// Start of agent section
		if strings.HasPrefix(cleanLine, p.config.AgentPrefix) {
			if inAgentSection && len(currentSection) > 0 {
				// Save previous section
				agentSections = append(agentSections, strings.Join(currentSection, "\n"))
			}
			currentSection = []string{strings.TrimSpace(strings.TrimPrefix(cleanLine, p.config.AgentPrefix))}
			inAgentSection = true
			continue
		}

		// User message or tool marks end of agent section
		if strings.HasPrefix(cleanLine, p.config.UserPrefix) || p.isToolOutput(cleanLine) {
			if inAgentSection && len(currentSection) > 0 {
				agentSections = append(agentSections, strings.Join(currentSection, "\n"))
				currentSection = []string{}
			}
			inAgentSection = false
			continue
		}

		// Continuation line (indented or following agent message)
		if inAgentSection {
			currentSection = append(currentSection, cleanLine)
		}
	}

	// Don't forget the last section
	if inAgentSection && len(currentSection) > 0 {
		agentSections = append(agentSections, strings.Join(currentSection, "\n"))
	}

	// Keep only last 3 agent sections (most recent)
	if len(agentSections) > 3 {
		agentSections = agentSections[len(agentSections)-3:]
	}

	// Look for choices in agent sections
	for _, section := range agentSections {
		// Check for question and numbered list in same section
		if !strings.Contains(section, "?") {
			continue
		}

		lines := strings.Split(section, "\n")
		var choices []string
		var questionLine string

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			// Check if this is a numbered choice
			if matched, choiceText := p.isNumberedChoice(line, len(choices)+1); matched {
				choices = append(choices, choiceText)
			} else if strings.HasSuffix(line, "?") {
				questionLine = line
			}
		}

		// Valid if we have a question and at least 2 choices
		if questionLine != "" && len(choices) >= 2 {
			p.state.Question = questionLine
			p.state.Choices = choices
			break
		}
	}

	// Check for errors in recent agent sections
	for _, section := range agentSections {
		if strings.Contains(strings.ToLower(section), "error") ||
			strings.Contains(strings.ToLower(section), "failed") ||
			strings.Contains(strings.ToLower(section), "fatal") {
			p.state.HasError = true
			p.state.ErrorSnippet = section
			if len(p.state.ErrorSnippet) > 100 {
				p.state.ErrorSnippet = p.state.ErrorSnippet[:97] + "..."
			}
			break
		}
	}
}

// isNumberedChoice checks if content looks like "N. text" or "N) text"
// Returns (matched, choiceText)
func (p *MessageParser) isNumberedChoice(content string, expectedNum int) (bool, string) {
	// Try "N. " format
	prefix1 := fmt.Sprintf("%d. ", expectedNum)
	if strings.HasPrefix(content, prefix1) {
		return true, strings.TrimSpace(content[len(prefix1):])
	}

	// Try "N) " format
	prefix2 := fmt.Sprintf("%d) ", expectedNum)
	if strings.HasPrefix(content, prefix2) {
		return true, strings.TrimSpace(content[len(prefix2):])
	}

	// Try "N] " format
	prefix3 := fmt.Sprintf("%d] ", expectedNum)
	if strings.HasPrefix(content, prefix3) {
		return true, strings.TrimSpace(content[len(prefix3):])
	}

	return false, ""
}

// isToolCall checks if a line with AgentPrefix is a tool call
func (p *MessageParser) isToolCall(lineIdx int) bool {
	if lineIdx >= len(p.buffer) {
		return false
	}

	cleanLine := p.stripColors(p.buffer[lineIdx])

	// Check for known tool names after the prefix
	for _, tool := range p.config.KnownTools {
		if strings.Contains(cleanLine, tool+"(") {
			return true
		}
	}

	// Check if next line has tool output prefix (⎿)
	if lineIdx+1 < len(p.buffer) {
		nextLine := strings.TrimSpace(p.stripColors(p.buffer[lineIdx+1]))
		for _, prefix := range p.config.ToolOutputPrefixes {
			if strings.HasPrefix(nextLine, prefix) {
				return true
			}
		}
	}

	return false
}

// isToolOutput checks if a line starts with tool output prefix
func (p *MessageParser) isToolOutput(line string) bool {
	for _, prefix := range p.config.ToolOutputPrefixes {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

// hasSpinner checks if a line contains a spinner character
func (p *MessageParser) hasSpinner(line string) bool {
	for _, spinner := range p.config.SpinnerChars {
		if strings.ContainsRune(line, spinner) {
			return true
		}
	}
	return false
}

// extractUserMessage extracts a user message starting at lineIdx
func (p *MessageParser) extractUserMessage(lineIdx int) *Message {
	if lineIdx >= len(p.buffer) {
		return nil
	}

	rawLine := p.buffer[lineIdx]
	cleanLine := p.stripColors(rawLine)

	// Remove prefix and trim
	content := strings.TrimSpace(strings.TrimPrefix(cleanLine, p.config.UserPrefix))

	p.seenMessages[lineIdx] = true

	return &Message{
		Type:       UserMessage,
		Content:    content,
		RawContent: rawLine,
		Timestamp:  time.Now(),
		Metadata:   map[string]string{"line": string(rune(lineIdx))},
	}
}

// extractAgentMessage extracts an agent response message
func (p *MessageParser) extractAgentMessage(lineIdx int) *Message {
	if lineIdx >= len(p.buffer) {
		return nil
	}

	rawLine := p.buffer[lineIdx]
	cleanLine := p.stripColors(rawLine)

	// Remove prefix and trim
	content := strings.TrimSpace(strings.TrimPrefix(cleanLine, p.config.AgentPrefix))

	p.seenMessages[lineIdx] = true

	return &Message{
		Type:       AgentMessage,
		Content:    content,
		RawContent: rawLine,
		Timestamp:  time.Now(),
		Metadata:   map[string]string{"line": string(rune(lineIdx))},
	}
}

// extractToolCall extracts a tool call message
func (p *MessageParser) extractToolCall(lineIdx int) *Message {
	if lineIdx >= len(p.buffer) {
		return nil
	}

	rawLine := p.buffer[lineIdx]
	cleanLine := p.stripColors(rawLine)

	// Remove prefix (try ToolPrefix first, then AgentPrefix)
	content := cleanLine
	if strings.HasPrefix(cleanLine, p.config.ToolPrefix) {
		content = strings.TrimSpace(strings.TrimPrefix(cleanLine, p.config.ToolPrefix))
	} else if strings.HasPrefix(cleanLine, p.config.AgentPrefix) {
		content = strings.TrimSpace(strings.TrimPrefix(cleanLine, p.config.AgentPrefix))
	}

	// Extract tool name (before opening paren)
	toolName := content
	if idx := strings.Index(content, "("); idx > 0 {
		toolName = content[:idx]
	}

	p.seenMessages[lineIdx] = true

	return &Message{
		Type:       ToolCall,
		Content:    content,
		RawContent: rawLine,
		Timestamp:  time.Now(),
		Metadata: map[string]string{
			"line": string(rune(lineIdx)),
			"tool": toolName,
		},
	}
}

// extractToolOutput extracts tool output lines
func (p *MessageParser) extractToolOutput(lineIdx int) *Message {
	if lineIdx >= len(p.buffer) {
		return nil
	}

	rawLine := p.buffer[lineIdx]
	cleanLine := p.stripColors(rawLine)

	// Remove tree prefix
	content := cleanLine
	for _, prefix := range p.config.ToolOutputPrefixes {
		if strings.HasPrefix(content, prefix) {
			content = strings.TrimSpace(strings.TrimPrefix(content, prefix))
			break
		}
	}

	p.seenMessages[lineIdx] = true

	return &Message{
		Type:       ToolOutput,
		Content:    content,
		RawContent: rawLine,
		Timestamp:  time.Now(),
		Metadata:   map[string]string{"line": string(rune(lineIdx))},
	}
}

// extractActivity extracts activity text from spinner line
func (p *MessageParser) extractActivity(line string) string {
	// Find spinner character position
	spinnerIdx := -1
	var spinnerLen int
	for _, spinner := range p.config.SpinnerChars {
		if idx := strings.IndexRune(line, spinner); idx >= 0 {
			spinnerIdx = idx
			spinnerLen = len(string(spinner))
			break
		}
	}

	if spinnerIdx < 0 {
		return ""
	}

	// Extract text after spinner (skip the spinner character itself)
	activity := strings.TrimSpace(line[spinnerIdx+spinnerLen:])

	// Remove trailing "..." or "…"
	activity = strings.TrimSuffix(activity, "...")
	activity = strings.TrimSuffix(activity, "…")
	activity = strings.TrimSpace(activity)

	// Remove timing info in parentheses
	if idx := strings.Index(activity, "("); idx > 0 {
		activity = strings.TrimSpace(activity[:idx])
	}

	return activity
}
