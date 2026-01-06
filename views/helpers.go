package views

import (
	"fmt"
	"net/url"
	"time"
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
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
	result := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}

func reverseStrings(ss []string) []string {
	result := make([]string, len(ss))
	for i, s := range ss {
		result[len(ss)-1-i] = s
	}
	return result
}

func trimLine(s string) string {
	// Trim leading/trailing whitespace but keep content
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
