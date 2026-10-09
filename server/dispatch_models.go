package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
)

// dispatchModelSet is what the Dispatch form offers and POST accepts: literal
// model ids per engine, and the default per engine and tier.
type dispatchModelSet struct {
	Engines    map[string][]string
	TierModels map[string]map[string]string
}

// dispatchLiteralModelRe is the closed set of characters a model id may use.
// It excludes the glob metacharacters (*?[) that dispatch's tier map allows,
// and a leading "-", so an accepted id is always a literal and never an option
// to dispatch.
var dispatchLiteralModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@+-]*$`)

// builtinDispatchModels is the fallback for a dispatch without
// `--models --json`, or one that failed: the literal entries of dispatch's tier
// map (adapters/core/defaults.json modelMap), engines' union in map order.
var builtinDispatchModels = dispatchModelSet{
	Engines: map[string][]string{
		"claude": {"opus", "sonnet", "fable", "haiku"},
		"codex":  {"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.6-luna"},
		"cursor": {
			"kimi-k3-high", "grok-4.7-medium", "grok-4.7-medium-fast", "grok-4.7-high", "grok-4.7-high-fast",
			"cursor-grok-4.6-medium", "cursor-grok-4.6-medium-fast", "cursor-grok-4.6-high", "cursor-grok-4.6-high-fast",
			"composer-2.5", "composer-2.5-fast",
			"grok-4.7-low", "grok-4.7-low-fast", "cursor-grok-4.6-low", "cursor-grok-4.6-low-fast",
		},
		"pi": {
			"openrouter/deepseek/deepseek-v4.1-flash",
			"openrouter/deepseek/deepseek-v4-flash",
			"openrouter/z-ai/glm-5.3-flash",
			"openrouter/qwen/qwen3.8-flash",
		},
	},
	TierModels: map[string]map[string]string{
		"claude": {"trivial": "sonnet", "standard": "sonnet", "deep": "opus"},
		"codex":  {"trivial": "gpt-5.6-luna", "standard": "gpt-5.6-terra", "deep": "gpt-5.6-sol"},
		"cursor": {"trivial": "grok-4.7-low", "standard": "grok-4.7-medium", "deep": "kimi-k3-high"},
		"pi": {
			"trivial":  "openrouter/deepseek/deepseek-v4-flash",
			"standard": "openrouter/deepseek/deepseek-v4.1-flash",
			"deep":     "openrouter/deepseek/deepseek-v4.1-flash",
		},
	},
}

type dispatchModelsJSON struct {
	Engines map[string]struct {
		Tiers map[string]struct {
			Default *string  `json:"default"`
			Models  []string `json:"models"`
		} `json:"tiers"`
		Models []string `json:"models"`
	} `json:"engines"`
}

// parseDispatchModels turns `dispatch --models --json` into a literal-only
// model set. Glob entries and the regex lists are dropped, never expanded:
// houston offers and accepts only ids it can compare by string equality.
func parseDispatchModels(raw []byte) (dispatchModelSet, error) {
	var in dispatchModelsJSON
	if err := json.Unmarshal(raw, &in); err != nil {
		return dispatchModelSet{}, fmt.Errorf("unparseable dispatch --models output: %w", err)
	}

	out := dispatchModelSet{
		Engines:    map[string][]string{},
		TierModels: map[string]map[string]string{},
	}
	for _, engine := range dispatchEngineOrder {
		e, ok := in.Engines[engine]
		if !ok {
			continue
		}
		var union []string
		add := func(m string) {
			if dispatchLiteralModelRe.MatchString(m) && !slices.Contains(union, m) {
				union = append(union, m)
			}
		}
		for _, m := range e.Models {
			add(m)
		}
		tierModels := map[string]string{}
		for _, tier := range dispatchTiers {
			t, ok := e.Tiers[tier]
			if !ok {
				continue
			}
			for _, m := range t.Models {
				add(m)
			}
			switch {
			case t.Default != nil && dispatchLiteralModelRe.MatchString(*t.Default):
				add(*t.Default)
				tierModels[tier] = *t.Default
			default:
				if i := slices.IndexFunc(t.Models, dispatchLiteralModelRe.MatchString); i >= 0 {
					tierModels[tier] = t.Models[i]
				}
			}
		}
		if len(union) == 0 {
			continue
		}
		out.Engines[engine] = union
		out.TierModels[engine] = tierModels
	}
	if len(out.Engines) == 0 {
		return dispatchModelSet{}, errors.New("dispatch --models lists no engine houston knows")
	}
	return out, nil
}

// execDispatchModels reads dispatch's tier map, with serverPath as in
// dispatchEnv.
func execDispatchModels(ctx context.Context, serverPath string) (dispatchModelSet, error) {
	ctx, cancel := context.WithTimeout(ctx, dispatchEnginesTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "dispatch", "--models", "--json")
	cmd.Env = dispatchEnv(serverPath)
	out, err := cmd.Output()
	if err != nil {
		return dispatchModelSet{}, fmt.Errorf("dispatch --models --json: %w", err)
	}
	return parseDispatchModels(out)
}

// dispatchModelSet is the model set for one request: dispatch's own, or the
// built-in fallback with the reason. It is read once per request and never
// cached, so an options GET or a POST sees one consistent list, and a changed
// tier map shows up on the next request without a restart. A POST pays one
// extra dispatch exec, the same as the engines lookup.
func (s *Server) dispatchModelSet(ctx context.Context, serverPath string) (dispatchModelSet, error) {
	set, err := s.dispatchModels(ctx, serverPath)
	if err != nil {
		return builtinDispatchModels, err
	}
	return set, nil
}
