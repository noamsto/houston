package claude

import "strings"

// DetectFromOutput checks if output appears to be from Claude Code.
// Input should be ANSI-stripped.
func DetectFromOutput(output string) bool {
	// Claude Code status bar markers (high confidence)
	statusMarkers := []string{
		"-- INSERT --",
		"-- NORMAL --",
		"🤖", // Model indicator
		"📊", // Stats
		"💬", // Messages
	}
	for _, marker := range statusMarkers {
		if strings.Contains(output, marker) {
			return true
		}
	}

	// Claude conversation patterns
	conversationMarkers := []string{
		"Claude:",          // Claude's responses
		"Human:",           // User messages in transcript
		">>>",              // Claude Code prompt
		"Do you want to",   // Common Claude question pattern
		"Would you like",   // Common Claude question pattern
		"(Recommended)",    // Choice recommendation
		"[Y/n]",            // Yes/no prompt
		"[y/N]",            // Yes/no prompt
		"Select an option", // Choice prompt
	}
	for _, marker := range conversationMarkers {
		if strings.Contains(output, marker) {
			return true
		}
	}

	return false
}
