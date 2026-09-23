package hook

import (
	"encoding/json"
	"fmt"
)

// input decodes both stdin shapes: Claude Code's native payload and
// hookyard's envelope. Claude's session_id/cwd/tool_name/tool_input keys
// coincide with the envelope's, so one decode serves both; Engine is empty
// for the native payload.
type input struct {
	Event
	Engine         string          `json:"engine,omitempty"`
	CanonicalEvent string          `json:"canonical_event,omitempty"`
	NativeEvent    string          `json:"native_event,omitempty"`
	Native         json.RawMessage `json:"native,omitempty"`
}

// canonicalEventMap maps hookyard's engine-neutral canonical events onto
// houston's Claude event constants, which double as its internal vocabulary.
var canonicalEventMap = map[string]string{
	"session_start": EventSessionStart,
	"prompt_submit": EventUserPromptSubmit,
	"pre_tool":      EventPreToolUse,
	"post_tool":     EventPostToolUse,
	"pre_compact":   EventPreCompact,
	"turn_end":      EventStop,
}

// nativeExtra is what fromEnvelope reads from a non-Claude native payload.
type nativeExtra struct {
	TranscriptPath string `json:"transcript_path"`
	SessionFile    string `json:"session_file"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// fromEnvelope maps a hookyard envelope onto houston's internal event
// vocabulary. event == "" means the caller should no-op (no state-file
// write) — an envelope event houston doesn't track (e.g. an engine-scoped
// extra like codex:PermissionRequest).
func fromEnvelope(in input) (event string, ev Event, agent string, err error) {
	switch in.Engine {
	case "claude-code":
		agent = AgentClaude
	case "codex", "cursor", "pi":
		agent = in.Engine
	default:
		return "", Event{}, "", fmt.Errorf("unknown engine %q", in.Engine)
	}

	if in.Engine == "claude-code" {
		if err := json.Unmarshal(in.Native, &ev); err != nil {
			return "", Event{}, "", fmt.Errorf("decode claude-code native payload: %w", err)
		}
		event = ev.HookEventName
		if event == "" {
			event = in.NativeEvent
		}
		return event, ev, agent, nil
	}

	var extra nativeExtra
	if len(in.Native) > 0 {
		if err := json.Unmarshal(in.Native, &extra); err != nil {
			return "", Event{}, "", fmt.Errorf("decode %s native payload: %w", in.Engine, err)
		}
	}
	ev = Event{
		SessionID:      in.SessionID,
		CWD:            in.CWD,
		TranscriptPath: first(extra.TranscriptPath, extra.SessionFile),
		ToolName:       in.ToolName,
		ToolInput:      in.ToolInput,
		Source:         first(extra.Source, extra.Reason),
		Reason:         extra.Reason,
	}

	switch {
	case in.Engine == "pi" && in.NativeEvent == "session_shutdown" && extra.Reason == "reload":
		// pi's /reload emits this then re-emits session_start for the same
		// session id, both as separate detached processes; if the start
		// lands first, treating this as SessionEnd would end a live session.
		event = ""
	case in.CanonicalEvent == "" && in.Engine == "pi" && in.NativeEvent == "session_shutdown":
		event = EventSessionEnd
	case in.Engine == "pi" && in.CanonicalEvent == "turn_end":
		// pi fires turn_end after every LLM response; only agent_settled
		// (below) is the run's final signal, so this turn is intermediate.
		event = EventTurnEnd
	case in.Engine == "pi" && in.NativeEvent == "agent_settled":
		event = EventStop
	case in.Engine == "codex" && in.NativeEvent == "SessionEnd":
		event = EventSessionEnd
	case in.Engine == "cursor" && in.NativeEvent == "sessionEnd":
		event = EventSessionEnd
	default:
		event = canonicalEventMap[in.CanonicalEvent]
	}
	return event, ev, agent, nil
}
