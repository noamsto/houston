package amp

import (
	"regexp"
	"strings"
)

// boxStatusPattern matches Amp's box-style status bar: ╭─...─╮
var boxStatusPattern = regexp.MustCompile(`╭─.*─╮`)

// DetectFromOutput checks if output appears to be from Amp.
// Input should be ANSI-stripped.
func DetectFromOutput(output string) bool {
	// Amp-specific markers (high confidence)
	ampMarkers := []string{
		"Cogitated for",           // Amp thinking indicator
		"Baked for",               // Amp thinking variant
		"Running PostToolUse hooks", // Amp hook indicator
	}
	for _, marker := range ampMarkers {
		if strings.Contains(output, marker) {
			return true
		}
	}

	// Check for box-style status bar
	if boxStatusPattern.MatchString(output) {
		// Additional validation: look for Amp-specific content in box
		if strings.Contains(output, "smart") || // Mode indicator
			strings.Contains(output, "of 168k") || // Token format
			strings.Contains(output, "(free)") { // Cost indicator
			return true
		}
	}

	return false
}
