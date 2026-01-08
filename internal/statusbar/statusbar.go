// Package statusbar provides utilities for detecting and handling Claude Code's status bar.
package statusbar

import "strings"

// Mode represents the vim-like mode Claude Code can be in.
type Mode string

const (
	ModeInsert  Mode = "insert"
	ModeNormal  Mode = "normal"
	ModeUnknown Mode = ""
)

// StatusIndicators contains patterns that identify Claude's status bar elements.
var StatusIndicators = []string{
	"-- INSERT --", "-- NORMAL --", // vim mode
	"🤖", "📊", "⏱️", "💬",        // Claude stats
	"❄", "📂",                      // env/path indicators
	"accept edits",                 // edit acceptance hint
}

// IsStatusLine checks if a line is part of Claude's status bar.
// Status bar lines should be filtered from preview but kept for mode detection.
func IsStatusLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}

	// Horizontal separator lines (─────)
	// These mark boundaries in Claude's UI
	if len(trimmed) > 10 && strings.Count(trimmed, "─") > len(trimmed)/2 {
		return true
	}

	// Check all status indicators
	for _, indicator := range StatusIndicators {
		if strings.Contains(line, indicator) {
			return true
		}
	}

	return false
}

// DetectMode checks for INSERT or NORMAL mode in the output.
// Claude Code shows "-- INSERT --" at the bottom; absence means NORMAL mode.
func DetectMode(output string) Mode {
	lines := strings.Split(output, "\n")

	// Only check last 5 lines where status bar appears
	start := len(lines) - 5
	if start < 0 {
		start = 0
	}

	bottomLines := strings.Join(lines[start:], "\n")
	if strings.Contains(bottomLines, "-- INSERT --") {
		return ModeInsert
	}
	if strings.Contains(bottomLines, "-- NORMAL --") {
		return ModeNormal
	}

	// Default to normal if no mode indicator found
	return ModeNormal
}

// DetectModeFromLines checks for mode in a slice of lines (last N lines typically).
func DetectModeFromLines(lines []string) Mode {
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

// FilterOutput removes status bar lines from output, keeping content.
func FilterOutput(output string) string {
	lines := strings.Split(output, "\n")
	var filtered []string

	for _, line := range lines {
		if !IsStatusLine(line) {
			filtered = append(filtered, line)
		}
	}

	return strings.Join(filtered, "\n")
}
