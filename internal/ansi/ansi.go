// Package ansi provides utilities for handling ANSI escape sequences.
package ansi

import "regexp"

// Pattern matches all ANSI escape sequences (colors, cursor control, etc).
// This covers:
// - Color codes: \x1b[32m (green), \x1b[0m (reset)
// - Cursor control: \x1b[2J (clear screen), \x1b[H (cursor home)
// - Other CSI sequences: \x1b[?25h (show cursor)
// Also matches sequences with ␛ (U+241B) which tmux uses as visible ESC symbol.
var Pattern = regexp.MustCompile(`[\x1b␛]\[[0-9;?]*[a-zA-Z]`)

// OrphanedSGRPattern matches SGR sequences that lost their ESC prefix.
// These appear as "[39m" or "[0;1;32m" when the \x1b is stripped.
// Only matches sequences ending in 'm' (Select Graphic Rendition) to avoid false positives.
var OrphanedSGRPattern = regexp.MustCompile(`\[[0-9;]*m`)

// OSC8Pattern matches OSC 8 hyperlink sequences.
// Format: \x1b]8;params;url\x1b\\ or \x1b]8;params;url\x07
// The hyperlink text follows, then \x1b]8;;\x1b\\ or \x1b]8;;\x07 terminates.
// We strip the escape sequences but keep the visible text.
var OSC8Pattern = regexp.MustCompile(`\x1b\]8;[^;]*;[^\x1b\x07]*[\x1b\x07][\\\x07]?`)

// Strip removes all ANSI escape sequences from a string.
// Use this before pattern matching or text analysis.
func Strip(s string) string {
	return Pattern.ReplaceAllString(s, "")
}

// StripOrphaned also removes orphaned SGR sequences (color codes without ESC prefix).
// Use this when ANSI codes may have lost their ESC character through serialization.
func StripOrphaned(s string) string {
	s = Pattern.ReplaceAllString(s, "")
	s = OrphanedSGRPattern.ReplaceAllString(s, "")
	return s
}
