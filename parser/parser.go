// parser/parser.go
package parser

import (
	"regexp"
	"strings"
)

type ResultType int

const (
	TypeIdle ResultType = iota
	TypeWorking
	TypeQuestion
	TypeChoice
	TypeError
)

func (t ResultType) String() string {
	return [...]string{"idle", "working", "question", "choice", "error"}[t]
}

type Result struct {
	Type         ResultType
	Question     string
	Choices      []string
	ErrorSnippet string
}

var (
	choicePattern   = regexp.MustCompile(`(?m)^\s*([1-4])[.)\]]\s+(.+)$`)
	questionPattern = regexp.MustCompile(`(?m)^(.+\?)\s*$`)
	errorPattern    = regexp.MustCompile(`(?mi)(error|failed|exception)[:\s]+(.{0,100})`)
	approvalPattern = regexp.MustCompile(`(?i)(proceed|continue|look right|does this|should i)\?`)
)

func Parse(output string) Result {
	lines := strings.Split(output, "\n")
	lastLines := lastN(lines, 30)
	text := strings.Join(lastLines, "\n")

	// Check for errors first (highest priority)
	if matches := errorPattern.FindStringSubmatch(text); len(matches) > 0 {
		return Result{
			Type:         TypeError,
			ErrorSnippet: strings.TrimSpace(matches[0]),
		}
	}

	// Check for multiple choice
	choiceMatches := choicePattern.FindAllStringSubmatch(text, -1)
	if len(choiceMatches) >= 2 {
		var choices []string
		for _, m := range choiceMatches {
			choices = append(choices, strings.TrimSpace(m[2]))
		}

		// Find the question before choices
		question := ""
		if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
			question = strings.TrimSpace(qMatches[len(qMatches)-1][1])
		}

		return Result{
			Type:     TypeChoice,
			Question: question,
			Choices:  choices,
		}
	}

	// Check for approval/confirmation question
	if approvalPattern.MatchString(text) {
		if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
			return Result{
				Type:     TypeQuestion,
				Question: strings.TrimSpace(qMatches[len(qMatches)-1][1]),
			}
		}
	}

	// Check for general question
	if qMatches := questionPattern.FindAllStringSubmatch(text, -1); len(qMatches) > 0 {
		lastQ := qMatches[len(qMatches)-1][1]
		// Only flag as question if it's near the end
		if strings.Contains(strings.Join(lastN(lines, 5), "\n"), lastQ) {
			return Result{
				Type:     TypeQuestion,
				Question: strings.TrimSpace(lastQ),
			}
		}
	}

	return Result{Type: TypeIdle}
}

func lastN(slice []string, n int) []string {
	if len(slice) <= n {
		return slice
	}
	return slice[len(slice)-n:]
}
