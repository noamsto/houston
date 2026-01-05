// parser/parser.go
package parser

import (
	"regexp"
	"strings"
)

type ResultType int

const (
	TypeIdle ResultType = iota
	TypeWorking
	TypeDone
	TypeQuestion
	TypeChoice
	TypeError
)

func (t ResultType) String() string {
	return [...]string{"idle", "working", "done", "question", "choice", "error"}[t]
}

type Mode int

const (
	ModeUnknown Mode = iota
	ModeInsert
	ModeNormal
)

func (m Mode) String() string {
	return [...]string{"unknown", "insert", "normal"}[m]
}

type Result struct {
	Type         ResultType
	Mode         Mode
	Question     string
	Choices      []string
	ErrorSnippet string
	Activity     string // What Claude is currently doing (for TypeWorking)
}

var (
	// Match choice lines: allow cursor chars (❯, >, -, *) before the number
	// Changed from [1-4] to [0-9]+ to support any number of choices (including tool permissions)
	choicePattern   = regexp.MustCompile(`(?m)^\s*[❯>\-\*]?\s*([0-9]+)[.)\]]\s+(.+)$`)
	questionPattern = regexp.MustCompile(`(?m)^(.+\?)\s*$`)
	// Error patterns - look for actual error messages, not just code containing "error"
	// Requires colon after error keyword to avoid matching code/comments
	// Matches: "Error: message" or "error: message" but not "// handle error" or "errorCount"
	errorPattern = regexp.MustCompile(`(?mi)^(?:error|failed|fatal|panic):\s+(.+)`)
	approvalPattern = regexp.MustCompile(`(?i)(proceed|continue|look right|does this|should i)\?`)

	// Claude Code working/activity patterns
	// Main spinner pattern - Claude uses ✻ followed by activity description and timing info
	// Matches: "● Working..." or "● Done." or "● Done!"
	spinnerPattern = regexp.MustCompile(`[✻⏺●◐◓◑◒]\s*([^…\n\.!]+?)(?:…|\.+|!)`)
	// Tool running indicator - "Running..." or "Running PreToolUse hook..."
	toolRunningPattern = regexp.MustCompile(`(?i)⎿\s*(Running[^…]*(?:…|\.{2,}))`)
	// Tool output lines (shows Claude completed/running a tool)
	toolOutputPattern = regexp.MustCompile(`^\s*[⎿├└│]`)
)

func Parse(output string) Result {
	lines := strings.Split(output, "\n")
	// Look at last 50 lines to capture edit prompts with diffs
	lastLines := lastN(lines, 50)
	text := strings.Join(lastLines, "\n")

	// Detect mode from last few lines
	mode := detectMode(lastN(lines, 5))

	// Check for multiple choice - MUST have a question (?) before numbered options
	// Real Claude Code choices look like:
	//   ? Do you want to proceed?
	//   1. Yes
	//   2. No
	qMatches := questionPattern.FindAllStringSubmatchIndex(text, -1)
	if len(qMatches) > 0 {
		// Get the last question and check if numbered options follow it
		lastQMatch := qMatches[len(qMatches)-1]
		textAfterQuestion := text[lastQMatch[1]:]

		choiceMatches := choicePattern.FindAllStringSubmatch(textAfterQuestion, -1)
		if len(choiceMatches) >= 2 {
			var choices []string
			for _, m := range choiceMatches {
				choices = append(choices, strings.TrimSpace(m[2]))
			}

			// Extract the question text
			question := strings.TrimSpace(text[lastQMatch[2]:lastQMatch[3]])

			return Result{
				Type:     TypeChoice,
				Mode:     mode,
				Question: question,
				Choices:  choices,
			}
		}
	}


	// Check for approval/confirmation question
	if approvalPattern.MatchString(text) {
		if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
			return Result{
				Type:     TypeQuestion,
				Mode:     mode,
				Question: strings.TrimSpace(qMatches[len(qMatches)-1][1]),
			}
		}
	}

	// Check for general question
	if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
		lastQ := qMatches[len(qMatches)-1][1]
		// Only flag as question if it's near the end (last 15 lines to account for Claude's status bar)
		recentLines := lastN(lines, 15)
		if strings.Contains(strings.Join(recentLines, "\n"), lastQ) {
			// Try to extract the full question (may span multiple lines)
			fullQuestion := extractFullQuestion(recentLines, lastQ)
			return Result{
				Type:     TypeQuestion,
				Mode:     mode,
				Question: fullQuestion,
			}
		}
	}

	// Check for working/activity state (check last 15 lines to account for Claude's status bar)
	activityLines := lastN(lines, 15)
	activity := detectActivity(activityLines)
	if activity != "" {
		// Check if this is a completion message (done state)
		activityLower := strings.ToLower(activity)
		if strings.HasPrefix(activityLower, "done") ||
			strings.HasPrefix(activityLower, "completed") ||
			strings.HasPrefix(activityLower, "finished") {
			return Result{
				Type:     TypeDone,
				Mode:     mode,
				Activity: activity,
			}
		}
		return Result{
			Type:     TypeWorking,
			Mode:     mode,
			Activity: activity,
		}
	}

	return Result{Type: TypeIdle, Mode: mode}
}

// detectActivity looks for Claude Code activity indicators
func detectActivity(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]

		// Check mode line for activity indicators (these appear at the very end)
		// Examples: "-- INSERT -- ⏵⏵ accept edits on" or "-- INSERT -- ⏸ plan mode on"
		if strings.Contains(line, "-- INSERT --") || strings.Contains(line, "-- NORMAL --") {
			if strings.Contains(line, "⏵⏵ accept edits") || strings.Contains(line, "accept edits") {
				return "Edits pending"
			}
			if strings.Contains(line, "⏸ plan mode") || strings.Contains(line, "plan mode") {
				return "Planning"
			}
			// Mode line found but no activity indicators - continue checking other lines
			continue
		}

		// Check for main spinner activity (✻ Thinking..., ✻ Sussing..., etc.)
		if match := spinnerPattern.FindStringSubmatch(line); len(match) > 1 {
			activity := strings.TrimSpace(match[1])
			// Clean up parenthetical timing info if present
			if idx := strings.Index(activity, "("); idx > 0 {
				activity = strings.TrimSpace(activity[:idx])
			}
			// Check for completion indicators - return as "done" state
			activityLower := strings.ToLower(activity)
			if strings.HasPrefix(activityLower, "done") ||
				strings.HasPrefix(activityLower, "completed") ||
				strings.HasPrefix(activityLower, "finished") {
				return activity // Will be detected as TypeDone below
			}
			return activity
		}

		// Check for tool running indicator (⎿ Running...)
		if match := toolRunningPattern.FindStringSubmatch(line); len(match) > 1 {
			return "Running tool"
		}

		// Check for recent tool output (shows Claude just ran a tool)
		if toolOutputPattern.MatchString(line) {
			// Look backwards for what tool was run
			for j := i - 1; j >= 0 && j > i-8; j-- {
				prevLine := lines[j]
				// Tool invocation lines look like: "● Read(...)" or "● Bash(...)"
				if strings.Contains(prevLine, "● Read") || strings.Contains(prevLine, "Read(") {
					return "Reading"
				}
				if strings.Contains(prevLine, "● Write") || strings.Contains(prevLine, "Write(") {
					return "Writing"
				}
				if strings.Contains(prevLine, "● Bash") || strings.Contains(prevLine, "Bash(") {
					return "Running command"
				}
				if strings.Contains(prevLine, "● Edit") || strings.Contains(prevLine, "Edit(") {
					return "Editing"
				}
				if strings.Contains(prevLine, "● Grep") || strings.Contains(prevLine, "Grep(") {
					return "Searching"
				}
				if strings.Contains(prevLine, "● Glob") || strings.Contains(prevLine, "Glob(") {
					return "Finding files"
				}
				if strings.Contains(prevLine, "● Task") || strings.Contains(prevLine, "Task(") {
					return "Running agent"
				}
			}
			// Generic fallback if we see tool output but can't identify the tool
			return "Working"
		}
	}
	return ""
}

// detectMode checks for INSERT or NORMAL mode indicators in the output
func detectMode(lines []string) Mode {
	for _, line := range lines {
		if strings.Contains(line, "-- INSERT --") {
			return ModeInsert
		}
		if strings.Contains(line, "-- NORMAL --") {
			return ModeNormal
		}
	}
	return ModeUnknown
}

func lastN(slice []string, n int) []string {
	if len(slice) <= n {
		return slice
	}
	return slice[len(slice)-n:]
}

// extractFullQuestion tries to find the complete question text, including wrapped lines
func extractFullQuestion(lines []string, questionFragment string) string {
	// Find the line containing the question fragment
	questionLineIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], questionFragment) {
			questionLineIdx = i
			break
		}
	}

	if questionLineIdx == -1 {
		return strings.TrimSpace(questionFragment)
	}

	// Look backwards to find the start of the question
	// Questions often start with capital letters or after certain patterns
	var questionLines []string
	for i := questionLineIdx; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			break // Empty line marks sentence boundary
		}
		// Check if this line looks like the start of a sentence
		questionLines = append([]string{line}, questionLines...)

		// Stop if we find a sentence boundary (ends with period, or starts with capital after space)
		if i > 0 {
			prevLine := strings.TrimSpace(lines[i-1])
			// Previous line ends with period/colon or is empty - this is likely the start
			if prevLine == "" || strings.HasSuffix(prevLine, ".") || strings.HasSuffix(prevLine, ":") {
				break
			}
		}

		// Don't go back more than 3 lines
		if questionLineIdx-i >= 3 {
			break
		}
	}

	// Join the lines and clean up
	fullQuestion := strings.Join(questionLines, " ")
	// Remove extra whitespace
	fullQuestion = strings.Join(strings.Fields(fullQuestion), " ")

	// Truncate if too long
	if len(fullQuestion) > 100 {
		fullQuestion = fullQuestion[:97] + "..."
	}

	return fullQuestion
}
