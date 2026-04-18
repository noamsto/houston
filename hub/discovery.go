package hub

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/noamsto/houston/hook"
)

// DefaultClaudeProjectsDir returns ~/.claude/projects.
func DefaultClaudeProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// DefaultDiscoveryWindow is how far back we consider a transcript "live".
// Sessions with no transcript activity in this window are ignored entirely.
const DefaultDiscoveryWindow = 24 * time.Hour

// DiscoverClaudeSessions scans projectsDir for transcript files modified
// within window and synthesizes a SessionState for each. Used to bootstrap
// agent cards when houston starts while Claude sessions are already running
// (no recent hook fire, but the transcript is on disk).
//
// Nothing is written to disk. The caller seeds the returned states into the hub.
func DiscoverClaudeSessions(projectsDir string, window time.Duration) ([]hook.SessionState, error) {
	if projectsDir == "" {
		return nil, nil
	}
	cutoff := time.Now().Add(-window)

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []hook.SessionState
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		projectDir := filepath.Join(projectsDir, e.Name())
		cwd := decodeProjectDirName(e.Name())

		files, err := os.ReadDir(projectDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			info, err := f.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				continue
			}
			s, ok := synthesizeFromTranscript(filepath.Join(projectDir, f.Name()), info, cwd)
			if ok {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// synthesizeFromTranscript reads the tail of a transcript and infers current
// state. Returns (zero, false) if the file is empty or unparseable.
func synthesizeFromTranscript(path string, info fs.FileInfo, cwd string) (hook.SessionState, bool) {
	sessionID := strings.TrimSuffix(info.Name(), ".jsonl")

	// Read the last ~64KB. For typical transcripts this captures several turns;
	// for pathological huge ones we'd still see the tail which is what matters.
	const tailBytes = 64 * 1024
	size := info.Size()
	offset := int64(0)
	if size > tailBytes {
		offset = size - tailBytes
	}
	events, _, err := ReadTranscriptFrom(path, offset)
	if err != nil || len(events) == 0 {
		return hook.SessionState{}, false
	}

	state := hook.SessionState{
		SessionID:      sessionID,
		TranscriptPath: path,
		CWD:            cwd,
		UpdatedAt:      info.ModTime().Unix(),
		Since:          info.ModTime().Unix(),
	}

	// Find the last unmatched tool_use (running tool at shutdown) and the
	// most recent assistant text or thinking block.
	var (
		unmatchedTool *TranscriptEvent
		lastAssistant *TranscriptEvent
		toolResults   = map[string]bool{} // tool_use_id → has_result
	)

	// First pass: collect tool_result IDs.
	for i := range events {
		ev := &events[i]
		if ev.Type == "tool_result" && ev.ToolUseID != "" {
			toolResults[ev.ToolUseID] = true
		}
	}

	// Second pass: find last unmatched tool_use + last assistant text.
	for i := len(events) - 1; i >= 0; i-- {
		ev := &events[i]
		switch ev.Type {
		case "tool_use":
			if unmatchedTool == nil && !toolResults[ev.ToolUseID] {
				unmatchedTool = ev
			}
		case "text", "thinking":
			if ev.Role == "assistant" && lastAssistant == nil {
				lastAssistant = ev
			}
		}
		if unmatchedTool != nil && lastAssistant != nil {
			break
		}
	}

	age := time.Since(info.ModTime())

	switch {
	case unmatchedTool != nil && age < 30*time.Second:
		// Tool call with no result, file just written → tool is executing.
		state.State = hook.StateToolRunning
		state.Tool = unmatchedTool.ToolName
		state.ToolInputHint = unmatchedTool.Text
	case unmatchedTool != nil:
		// Tool call with no result but stale → session likely ended with a
		// tool in flight. Mark it ended rather than invent a running tool.
		state.State = hook.StateEnded
	case age > 12*time.Hour:
		// Old session. Treat as ended (still visible in UI as greyed-out).
		state.State = hook.StateEnded
	default:
		// Most-recent event was an assistant message and no tool is pending —
		// Claude is waiting on the user.
		state.State = hook.StateWaiting
	}
	return state, true
}

// decodeProjectDirName reverses Claude Code's path encoding (best-effort).
// It encodes `/home/me/foo-bar` as `-home-me-foo-bar`, which is ambiguous —
// we can't tell `/foo-bar` from `/foo/bar` after the fact. This is good enough
// for display. A later hook fire writes the real CWD.
func decodeProjectDirName(name string) string {
	if !strings.HasPrefix(name, "-") {
		return name
	}
	return strings.ReplaceAll(name, "-", "/")
}
