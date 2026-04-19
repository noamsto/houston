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

// synthesizeFromTranscript tails a transcript and infers current state.
func synthesizeFromTranscript(path string, info fs.FileInfo, cwd string) (hook.SessionState, bool) {
	sessionID := strings.TrimSuffix(info.Name(), ".jsonl")

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

	var (
		unmatchedTool *TranscriptEvent
		lastAssistant *TranscriptEvent
		toolResults   = map[string]bool{}
	)
	for i := range events {
		ev := &events[i]
		if ev.Type == EventTypeToolResult && ev.ToolUseID != "" {
			toolResults[ev.ToolUseID] = true
		}
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := &events[i]
		switch ev.Type {
		case EventTypeToolUse:
			if unmatchedTool == nil && !toolResults[ev.ToolUseID] {
				unmatchedTool = ev
			}
		case EventTypeText, EventTypeThinking:
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
		state.State = hook.StateToolRunning
		state.Tool = unmatchedTool.ToolName
		state.ToolInputHint = unmatchedTool.Text
	case unmatchedTool != nil, age > 12*time.Hour:
		state.State = hook.StateEnded
	default:
		state.State = hook.StateWaiting
	}
	return state, true
}

// decodeProjectDirName reverses Claude's `-home-me-foo` path encoding.
// Ambiguous for paths containing `-` — good enough for display until a hook
// fire writes the real CWD.
func decodeProjectDirName(name string) string {
	if !strings.HasPrefix(name, "-") {
		return name
	}
	return strings.ReplaceAll(name, "-", "/")
}
