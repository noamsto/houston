package amp

import (
	"regexp"
	"strings"
)

// AmpStatus contains parsed Amp status bar information.
type AmpStatus struct {
	TokenPercent string // e.g., "27%"
	TokenLimit   string // e.g., "168k"
	Cost         string // e.g., "$0.63"
	CostNote     string // e.g., "(free)" or empty
	Mode         string // e.g., "smart", "rush", "auto"
	Path         string // e.g., "~/Data/git/houston"
	Branch       string // e.g., "main"
}

var (
	// Parse top line: ╭─27% of 168k · $0.63 (free)────────────────────smart─╮
	// Groups: 1=percent, 2=limit, 3=cost, 4=cost_note (optional), 5=mode
	topLinePattern = regexp.MustCompile(`╭─(\d+%)\s+of\s+(\d+k)\s*·\s*(\$[\d.]+)\s*(\([^)]+\))?\s*─+\s*(\w+)\s*─╮`)

	// Parse bottom line: ╰────────────────────────~/path/to/project (branch)─╯
	// Groups: 1=path, 2=branch (optional)
	bottomLinePattern = regexp.MustCompile(`╰─+([~/][^(]+?)\s*(?:\(([^)]+)\))?\s*─╯`)

	// Simpler patterns for when the full regex doesn't match
	tokenPattern = regexp.MustCompile(`(\d+%)\s+of\s+(\d+k)`)
	costPattern  = regexp.MustCompile(`(\$[\d.]+)\s*(\([^)]+\))?`)
	modePattern  = regexp.MustCompile(`─(smart|rush|auto|manual)─╮`)
	pathPattern  = regexp.MustCompile(`([~/][^\s(─]+)\s*(?:\(([^)]+)\))?─╯`)
)

// ParseStatus extracts structured status information from Amp's status box.
func ParseStatus(statusLine string) AmpStatus {
	status := AmpStatus{}

	lines := strings.Split(statusLine, "\n")
	for _, line := range lines {
		// Try to parse top line
		if strings.HasPrefix(strings.TrimSpace(line), "╭") {
			// Try full pattern first
			if match := topLinePattern.FindStringSubmatch(line); len(match) > 5 {
				status.TokenPercent = match[1]
				status.TokenLimit = match[2]
				status.Cost = match[3]
				status.CostNote = match[4]
				status.Mode = match[5]
			} else {
				// Fall back to individual patterns
				if match := tokenPattern.FindStringSubmatch(line); len(match) > 2 {
					status.TokenPercent = match[1]
					status.TokenLimit = match[2]
				}
				if match := costPattern.FindStringSubmatch(line); len(match) > 1 {
					status.Cost = match[1]
					if len(match) > 2 {
						status.CostNote = match[2]
					}
				}
				if match := modePattern.FindStringSubmatch(line); len(match) > 1 {
					status.Mode = match[1]
				}
			}
		}

		// Try to parse bottom line
		if strings.HasPrefix(strings.TrimSpace(line), "╰") {
			if match := bottomLinePattern.FindStringSubmatch(line); len(match) > 1 {
				status.Path = strings.TrimSpace(match[1])
				if len(match) > 2 {
					status.Branch = match[2]
				}
			} else if match := pathPattern.FindStringSubmatch(line); len(match) > 1 {
				status.Path = strings.TrimSpace(match[1])
				if len(match) > 2 {
					status.Branch = match[2]
				}
			}
		}
	}

	return status
}

// FormatStatusJSON returns JSON-like data for frontend consumption.
func (s AmpStatus) FormatStatusJSON() string {
	parts := []string{}
	if s.TokenPercent != "" {
		parts = append(parts, `"tokenPercent":"`+s.TokenPercent+`"`)
	}
	if s.TokenLimit != "" {
		parts = append(parts, `"tokenLimit":"`+s.TokenLimit+`"`)
	}
	if s.Cost != "" {
		parts = append(parts, `"cost":"`+s.Cost+`"`)
	}
	if s.CostNote != "" {
		parts = append(parts, `"costNote":"`+s.CostNote+`"`)
	}
	if s.Mode != "" {
		parts = append(parts, `"mode":"`+s.Mode+`"`)
	}
	if s.Path != "" {
		parts = append(parts, `"path":"`+s.Path+`"`)
	}
	if s.Branch != "" {
		parts = append(parts, `"branch":"`+s.Branch+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}
