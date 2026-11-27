package hook

import (
	"encoding/json"
	"fmt"

	"github.com/noamsto/hookyard/pkg/envelope"
)

// input decodes both stdin shapes: Claude Code's native payload and
// hookyard's envelope. Claude's session_id/cwd/tool_name/tool_input keys
// coincide with the envelope's, so the same bytes decode into Event (native
// payload) and into hookyard's envelope.Envelope; Engine is empty for the
// native payload.
type input struct {
	Event
	Env envelope.Envelope
}

// UnmarshalJSON decodes data twice: Event and envelope.Envelope declare the
// same json keys, which encoding/json would drop as ambiguous if both were
// embedded in one decode.
func (in *input) UnmarshalJSON(data []byte) error {
	if err := json.Unmarshal(data, &in.Event); err != nil {
		return err
	}
	return json.Unmarshal(data, &in.Env)
}

// canonicalEventMap maps hookyard's engine-neutral canonical events onto
// houston's Claude event constants, which double as its internal vocabulary.
var canonicalEventMap = map[string]string{
	envelope.SessionStart: EventSessionStart,
	envelope.PromptSubmit: EventUserPromptSubmit,
	envelope.PreTool:      EventPreToolUse,
	envelope.PostTool:     EventPostToolUse,
	envelope.PreCompact:   EventPreCompact,
	envelope.TurnEnd:      EventStop,
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
	switch in.Env.Engine {
	case envelope.ClaudeCode:
		agent = AgentClaude
	case envelope.Codex, envelope.Cursor, envelope.Pi:
		agent = string(in.Env.Engine)
	default:
		return "", Event{}, "", fmt.Errorf("unknown engine %q", in.Env.Engine)
	}

	native, err := json.Marshal(in.Env.Native)
	if err != nil {
		return "", Event{}, "", fmt.Errorf("encode %s native payload: %w", in.Env.Engine, err)
	}

	if in.Env.Engine == envelope.ClaudeCode {
		if in.Env.Native == nil {
			return "", Event{}, "", fmt.Errorf("decode claude-code native payload: missing native")
		}
		if err := json.Unmarshal(native, &ev); err != nil {
			return "", Event{}, "", fmt.Errorf("decode claude-code native payload: %w", err)
		}
		event = ev.HookEventName
		if event == "" {
			event = in.Env.NativeEvent
		}
		return event, ev, agent, nil
	}

	var extra nativeExtra
	if len(in.Env.Native) > 0 {
		if err := json.Unmarshal(native, &extra); err != nil {
			return "", Event{}, "", fmt.Errorf("decode %s native payload: %w", in.Env.Engine, err)
		}
	}
	ev = Event{
		SessionID:      in.Env.SessionID,
		CWD:            in.Env.Cwd,
		TranscriptPath: first(extra.TranscriptPath, extra.SessionFile),
		ToolName:       in.Env.ToolName,
		ToolInput:      in.Env.ToolInput,
		Source:         first(extra.Source, extra.Reason),
		Reason:         extra.Reason,
	}

	switch {
	case in.Env.Engine == envelope.Pi && in.Env.NativeEvent == "session_shutdown" && extra.Reason == "reload":
		// pi's /reload emits this then re-emits session_start for the same
		// session id, both as separate detached processes; if the start
		// lands first, treating this as SessionEnd would end a live session.
		event = ""
	case in.Env.CanonicalEvent == "" && in.Env.Engine == envelope.Pi && in.Env.NativeEvent == "session_shutdown":
		event = EventSessionEnd
	case in.Env.Engine == envelope.Pi && in.Env.CanonicalEvent == envelope.TurnEnd:
		// pi fires turn_end after every LLM response; only agent_settled
		// (below) is the run's final signal, so this turn is intermediate.
		event = EventTurnEnd
	case in.Env.Engine == envelope.Pi && in.Env.NativeEvent == "agent_settled":
		event = EventStop
	case in.Env.Engine == envelope.Codex && in.Env.NativeEvent == "SessionEnd":
		event = EventSessionEnd
	case in.Env.Engine == envelope.Cursor && in.Env.NativeEvent == "sessionEnd":
		event = EventSessionEnd
	default:
		event = canonicalEventMap[in.Env.CanonicalEvent]
	}
	return event, ev, agent, nil
}
