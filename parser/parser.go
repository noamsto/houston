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
	TypeQuestion
	TypeChoice
	TypeError
)

func (t ResultType) String() string {
	return [...]string{"idle", "working", "question", "choice", "error"}[t]
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
	choicePattern   = regexp.MustCompile(`(?m)^\s*[❯>\-\*]?\s*([1-4])[.)\]]\s+(.+)$`)
	questionPattern = regexp.MustCompile(`(?m)^(.+\?)\s*$`)
	errorPattern    = regexp.MustCompile(`(?mi)(error|failed|exception)[:\s]+(.{0,100})`)
	approvalPattern = regexp.MustCompile(`(?i)(proceed|continue|look right|does this|should i)\?`)

	// Claude Code working/activity patterns
	workingPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)[✻⏺●◐◓◑◒]\s*(.+?)\s*[…\.]{2,}`),           // ✻ Thinking..., ● Running...
		regexp.MustCompile(`(?i)(thinking|reading|writing|running|searching|analyzing)`), // Activity words
		regexp.MustCompile(`(?i)^\s*[⎿├└│]\s*(.+)`),                         // Tool output indicators
	}
)

func Parse(output string) Result {
	lines := strings.Split(output, "\n")
	lastLines := lastN(lines, 30)
	text := strings.Join(lastLines, "\n")

	// Detect mode from last few lines
	mode := detectMode(lastN(lines, 5))

	// Check for errors only in the last 5 lines (recent errors only)
	recentText := strings.Join(lastN(lines, 5), "\n")
	if matches := errorPattern.FindStringSubmatch(recentText); len(matches) > 0 {
		return Result{
			Type:         TypeError,
			Mode:         mode,
			ErrorSnippet: strings.TrimSpace(matches[0]),
		}
	}

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
		// Only flag as question if it's near the end
		if strings.Contains(strings.Join(lastN(lines, 5), "\n"), lastQ) {
			return Result{
				Type:     TypeQuestion,
				Mode:     mode,
				Question: strings.TrimSpace(lastQ),
			}
		}
	}

	// Check for working/activity state (check last few lines for recent activity)
	recentLines := lastN(lines, 8)
	activity := detectActivity(recentLines)
	if activity != "" {
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
		// Check for spinner/activity indicators
		if match := workingPatterns[0].FindStringSubmatch(line); len(match) > 1 {
			return strings.TrimSpace(match[1])
		}
		// Check for tool output lines (shows Claude is running tools)
		if strings.HasPrefix(strings.TrimSpace(line), "⎿") ||
			strings.HasPrefix(strings.TrimSpace(line), "├") ||
			strings.HasPrefix(strings.TrimSpace(line), "│") {
			// Look for what tool is running
			for j := i - 1; j >= 0 && j > i-5; j-- {
				if strings.Contains(lines[j], "Read") {
					return "Reading file"
				}
				if strings.Contains(lines[j], "Write") {
					return "Writing file"
				}
				if strings.Contains(lines[j], "Bash") {
					return "Running command"
				}
				if strings.Contains(lines[j], "Edit") {
					return "Editing file"
				}
			}
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
