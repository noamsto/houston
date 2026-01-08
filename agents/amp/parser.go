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

	// Match question patterns
	questionPattern = regexp.MustCompile(`(?m)^(.+\?)\s*$`)

	// Match Amp choice lines: "‣ Yes" (selected) or "  Allow All" (not selected)
	// Amp uses ‣ (U+2023) for selected item, spaces for others
	ampChoiceSelectedPattern = regexp.MustCompile(`^[│\s]*‣\s+(.+?)\s*[│]?\s*$`)
	ampChoiceUnselectedPattern = regexp.MustCompile(`^[│\s]{2,}([A-Z][a-zA-Z\s]+?)\s*[│]?\s*$`)

	// Match numbered choices (Claude style, kept for compatibility)
	numberedChoicePattern = regexp.MustCompile(`(?m)^\s*[❯>\-\*]?\s*([0-9]+)[.)\]]\s+(.+)$`)

	// Match hook running indicator
	hookPattern = regexp.MustCompile(`Running\s+\w+\s+hooks`)
)

// ParseOutput extracts state from Amp terminal output.
func ParseOutput(output string) parser.Result {
	lines := strings.Split(output, "\n")
	lastLines := lastN(lines, 50)
	text := strings.Join(lastLines, "\n")

	// Check for Amp-style choices (‣ cursor selection)
	choices, question := parseAmpChoices(lastLines)
	if len(choices) > 0 {
		return parser.Result{
			Type:     parser.TypeChoice,
			Question: question,
			Choices:  choices,
		}
	}

	// Check for numbered choices (Claude style)
	if qMatches := questionPattern.FindAllStringSubmatchIndex(text, -1); len(qMatches) > 0 {
		lastQMatch := qMatches[len(qMatches)-1]
		textAfterQuestion := text[lastQMatch[1]:]

		choiceMatches := numberedChoicePattern.FindAllStringSubmatch(textAfterQuestion, -1)
		if len(choiceMatches) >= 2 {
			var numberedChoices []string
			for _, m := range choiceMatches {
				numberedChoices = append(numberedChoices, strings.TrimSpace(m[2]))
			}

			q := strings.TrimSpace(text[lastQMatch[2]:lastQMatch[3]])
			return parser.Result{
				Type:     parser.TypeChoice,
				Question: q,
				Choices:  numberedChoices,
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

// parseAmpChoices extracts choices from Amp's cursor-based selection UI.
// Returns choices and the question text.
func parseAmpChoices(lines []string) ([]string, string) {
	var choices []string
	var question string
	inChoiceBlock := false
	selectedIdx := -1
	firstChoiceLineIdx := -1

	for i, line := range lines {
		// Check for selected choice (‣ prefix)
		if match := ampChoiceSelectedPattern.FindStringSubmatch(line); len(match) > 1 {
			choice := strings.TrimSpace(match[1])
			if choice != "" && !strings.HasPrefix(choice, "(") {
				if firstChoiceLineIdx == -1 {
					firstChoiceLineIdx = i
				}
				selectedIdx = len(choices)
				choices = append(choices, choice)
				inChoiceBlock = true
			}
			continue
		}

		// Check for unselected choices (indented, starts with capital letter)
		if inChoiceBlock {
			// Look for choice-like lines: indented text starting with capital
			trimmed := strings.TrimLeft(line, "│ \t")
			trimmed = strings.TrimRight(trimmed, "│ \t")
			if trimmed != "" && len(trimmed) > 1 && trimmed[0] >= 'A' && trimmed[0] <= 'Z' {
				// Make sure it's not a sentence (choices are typically short)
				if len(trimmed) < 40 && !strings.Contains(trimmed, ".") {
					choices = append(choices, trimmed)
					continue
				}
			}
			// If we hit an empty line or non-choice content after choices, stop
			if trimmed == "" || strings.HasPrefix(trimmed, "╰") {
				break
			}
		}
	}

	// Look for question in lines before the first choice
	if firstChoiceLineIdx > 0 {
		for j := firstChoiceLineIdx - 1; j >= 0 && j > firstChoiceLineIdx-15; j-- {
			prevLine := strings.TrimLeft(lines[j], "│ \t")
			prevLine = strings.TrimRight(prevLine, "│ \t")
			prevLine = strings.TrimSpace(prevLine)
			if strings.HasSuffix(prevLine, "?") && len(prevLine) > 3 {
				question = prevLine
				break
			}
		}
	}

	// Reorder so selected item is first (for UI display)
	if selectedIdx > 0 && selectedIdx < len(choices) {
		selected := choices[selectedIdx]
		choices = append([]string{selected}, append(choices[:selectedIdx], choices[selectedIdx+1:]...)...)
	}

	return choices, question
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
