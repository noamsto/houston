package hub

import "strings"

const (
	contextWindow   = 200_000
	contextWindow1M = 1_000_000
)

// contextLimits maps a model-id prefix to its context window. Only models
// whose window is known belong here: an unlisted model has no limit rather
// than a guessed one.
var contextLimits = []struct {
	prefix string
	limit  int
}{
	{"claude-opus-", contextWindow},
	{"claude-sonnet-", contextWindow},
	{"claude-haiku-", contextWindow},
	{"claude-fable-", contextWindow},
	{"claude-3", contextWindow},
}

// contextLimit returns the model's context window, or 0 when unknown. A
// "[1m]" id is the 1M variant. A window smaller than the most the session has
// used is wrong for that session (a 1M run reported under its base id), so
// it is dropped too. Claude Code writes the API model id, which has no
// "[1m]", so a 1M session reads against 200k until it first passes 200k.
func contextLimit(model string, peak int) int {
	limit := 0
	switch {
	case strings.HasSuffix(model, "[1m]"):
		limit = contextWindow1M
	default:
		for _, l := range contextLimits {
			if strings.HasPrefix(model, l.prefix) {
				limit = l.limit
				break
			}
		}
	}
	if limit < peak {
		return 0
	}
	return limit
}
