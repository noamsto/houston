package views

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Helper functions

func urlEncode(s string) string {
	return url.PathEscape(s)
}

func timeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2")
	}
}

func truncate(s string, n int) string {
	// Use rune count instead of byte length to handle UTF-8 properly
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	// Convert to runes, truncate, and add ellipsis
	runes := []rune(s)
	if n < 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}

// lastLines returns the last n non-empty lines of a string
func lastLines(s string, n int) []string {
	lines := []string{}
	for _, line := range reverseStrings(splitLines(s)) {
		trimmed := trimLine(line)
		if trimmed != "" {
			lines = append([]string{trimmed}, lines...)
			if len(lines) >= n {
				break
			}
		}
	}
	return lines
}

func splitLines(s string) []string {
	return strings.Split(s, "\n")
}

func reverseStrings(ss []string) []string {
	result := make([]string, len(ss))
	for i, s := range ss {
		result[len(ss)-1-i] = s
	}
	return result
}

func trimLine(s string) string {
	// Use stdlib TrimSpace which handles all Unicode whitespace
	return strings.TrimSpace(s)
}
