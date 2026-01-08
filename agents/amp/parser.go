package amp

import (
	"regexp"
	"strings"

	"github.com/noamsto/houston/parser"
)

var (
	// Match thinking indicators: "✻ Cogitated for 1m 30s" or "✻ Baked for 30s"
	thinkingPattern = regexp.MustCompile(`✻\s*(Cogitated|Baked)\s+for\s+(\d+[ms]\s*)+`)

	// Match tool invocation: "● ToolName(...)"
	toolPattern = regexp.MustCompile(`●\s+(\w+)\s*\(`)

	// Match question patterns (similar to Claude)
	questionPattern = regexp.MustCompile(`(?m)^(.+\?)\s*$`)

	// Match numbered choices
	choicePattern = regexp.MustCompile(`(?m)^\s*[❯>\-\*]?\s*([0-9]+)[.)\]]\s+(.+)$`)

	// Match hook running indicator
	hookPattern = regexp.MustCompile(`Running\s+\w+\s+hooks`)
)

// ParseOutput extracts state from Amp terminal output.
func ParseOutput(output string) parser.Result {
	lines := strings.Split(output, "\n")
	lastLines := lastN(lines, 50)
	text := strings.Join(lastLines, "\n")

	// Check for questions with choices
	if qMatches := questionPattern.FindAllStringSubmatchIndex(text, -1); len(qMatches) > 0 {
		lastQMatch := qMatches[len(qMatches)-1]
		textAfterQuestion := text[lastQMatch[1]:]

		choiceMatches := choicePattern.FindAllStringSubmatch(textAfterQuestion, -1)
		if len(choiceMatches) >= 2 {
			var choices []string
			for _, m := range choiceMatches {
				choices = append(choices, strings.TrimSpace(m[2]))
			}

			question := strings.TrimSpace(text[lastQMatch[2]:lastQMatch[3]])
			return parser.Result{
				Type:     parser.TypeChoice,
				Question: question,
				Choices:  choices,
			}
		}
	}

	// Check for thinking indicator
	if thinkingPattern.MatchString(text) {
		return parser.Result{
			Type:     parser.TypeWorking,
			Activity: "Thinking",
		}
	}

	// Check for hook running
	if hookPattern.MatchString(text) {
		return parser.Result{
			Type:     parser.TypeWorking,
			Activity: "Running hooks",
		}
	}

	// Check for tool activity
	if match := toolPattern.FindStringSubmatch(text); len(match) > 1 {
		return parser.Result{
			Type:     parser.TypeWorking,
			Activity: toolToActivity(match[1]),
		}
	}

	// Check for standalone question
	if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
		recentLines := lastN(lines, 15)
		lastQ := qMatches[len(qMatches)-1][1]
		if strings.Contains(strings.Join(recentLines, "\n"), lastQ) {
			return parser.Result{
				Type:     parser.TypeQuestion,
				Question: lastQ,
			}
		}
	}

	return parser.Result{Type: parser.TypeIdle}
}

func toolToActivity(tool string) string {
	switch tool {
	case "Read":
		return "Reading file"
	case "Bash":
		return "Running command"
	case "edit_file", "create_file":
		return "Editing file"
	case "Grep":
		return "Searching"
	case "glob":
		return "Finding files"
	case "Task":
		return "Running agent"
	case "web_search":
		return "Searching web"
	case "read_web_page":
		return "Reading web page"
	case "oracle":
		return "Consulting oracle"
	case "finder":
		return "Finding code"
	default:
		if tool != "" {
			return "Running " + tool
		}
		return "Working"
	}
}

func lastN(slice []string, n int) []string {
	if len(slice) <= n {
		return slice
	}
	return slice[len(slice)-n:]
}
