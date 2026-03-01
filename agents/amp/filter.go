package amp

import (
	"regexp"
	"strings"
)

// Box border patterns for status box extraction.
var (
	boxTopPattern    = regexp.MustCompile(`^\s*╭─.*─╮\s*$`)
	boxBottomPattern = regexp.MustCompile(`^\s*╰─.*─╯\s*$`)
)

// ExtractStatusLine extracts Amp's status box content with ANSI colors intact.
// Returns the LAST status box found (most recent).
func ExtractStatusLine(output string) string {
	lines := strings.Split(output, "\n")
	var lastStatusLines []string
	var currentStatusLines []string
	inStatusBox := false

	for _, line := range lines {
		if boxTopPattern.MatchString(line) {
			inStatusBox = true
			currentStatusLines = []string{line}
			continue
		}

		if boxBottomPattern.MatchString(line) {
			currentStatusLines = append(currentStatusLines, line)
			// Save this as the last complete status box
			lastStatusLines = currentStatusLines
			currentStatusLines = nil
			inStatusBox = false
			continue
		}

		if inStatusBox {
			currentStatusLines = append(currentStatusLines, line)
		}
	}

	return strings.Join(lastStatusLines, "\n")
}
