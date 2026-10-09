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
// "[1m]" id is the 1M variant. A window smaller than what the session already
// uses is wrong for that session (a 1M run reported under its base id), so
// it is dropped too.
func contextLimit(model string, used int) int {
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
	if limit < used {
		return 0
	}
	return limit
}
