package amp

import (
	"regexp"
	"strings"
)

// Box border patterns for filtering
var (
	boxTopPattern    = regexp.MustCompile(`^\s*╭─.*─╮\s*$`)
	boxMiddlePattern = regexp.MustCompile(`^\s*│.*│\s*$`)
	boxBottomPattern = regexp.MustCompile(`^\s*╰─.*─╯\s*$`)
)

// FilterStatusBar removes Amp's box-style status bar from output.
func FilterStatusBar(output string) string {
	lines := strings.Split(output, "\n")
	var filtered []string
	inStatusBox := false

	for _, line := range lines {
		// Check if entering status box
		if boxTopPattern.MatchString(line) {
			inStatusBox = true
			continue
		}

		// Check if exiting status box
		if boxBottomPattern.MatchString(line) {
			inStatusBox = false
			continue
		}

		// Skip lines inside status box
		if inStatusBox && boxMiddlePattern.MatchString(line) {
			continue
		}

		// Skip empty box middle lines (just │ on each side)
		if inStatusBox {
			continue
		}

		filtered = append(filtered, line)
	}

	return strings.Join(filtered, "\n")
}

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
