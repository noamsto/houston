package claude

import (
	"strings"

	"github.com/noamsto/houston/parser"
)

// StatusIndicators contains patterns that identify Claude's status bar elements.
var StatusIndicators = []string{
	"-- INSERT --", "-- NORMAL --", // vim mode
	"🤖", "📊", "⏱️", "💬", // Claude stats
	"❄", "📂", // env/path indicators
	"accept edits", // edit acceptance hint
}

// IsStatusLine checks if a line is part of Claude's status bar.
func IsStatusLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}

	// Horizontal separator lines (─────)
	// Use rune count for proper Unicode handling
	runeCount := len([]rune(trimmed))
	dashCount := strings.Count(trimmed, "─")
	if runeCount > 10 && dashCount > runeCount/2 {
		return true
	}

	for _, indicator := range StatusIndicators {
		if strings.Contains(line, indicator) {
			return true
		}
	}

	return false
}

// FilterStatusBar removes status bar lines from output, keeping content.
func FilterStatusBar(output string) string {
	lines := strings.Split(output, "\n")
	var filtered []string

	for _, line := range lines {
		if !IsStatusLine(line) {
			filtered = append(filtered, line)
		}
	}

	return strings.Join(filtered, "\n")
}

// DetectMode checks for INSERT or NORMAL mode in the output.
func DetectMode(output string) parser.Mode {
	lines := strings.Split(output, "\n")

	// Only check last 5 lines where status bar appears
	start := len(lines) - 5
	if start < 0 {
		start = 0
	}

	bottomLines := strings.Join(lines[start:], "\n")
	if strings.Contains(bottomLines, "-- INSERT --") {
		return parser.ModeInsert
	}
	if strings.Contains(bottomLines, "-- NORMAL --") {
		return parser.ModeNormal
	}

	return parser.ModeNormal // Default to normal if no mode indicator found
}

// ExtractStatusLine finds Claude's status bar line with ANSI colors intact.
func ExtractStatusLine(output string) string {
	lines := strings.Split(output, "\n")

	start := len(lines) - 20
	if start < 0 {
		start = 0
	}

	// Find the LAST horizontal separator
	lastSeparatorIdx := -1
	for i := start; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		dashCount := strings.Count(trimmed, "─")
		if dashCount >= 20 {
			lastSeparatorIdx = i
		}
	}

	if lastSeparatorIdx >= 0 {
		var statusLines []string
		for j := lastSeparatorIdx + 1; j < len(lines); j++ {
			line := lines[j]
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			statusLines = append(statusLines, strings.TrimSpace(line))
		}
		return strings.Join(statusLines, "\n")
	}

	return ""
}
