package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DoctorReport is the structured output of Doctor.
type DoctorReport struct {
	SettingsPath       string    `json:"settings_path"`
	Binary             string    `json:"binary"`
	StateDir           string    `json:"state_dir"`
	StateDirOK         bool      `json:"state_dir_ok"`
	LiveSessions       int       `json:"live_sessions"`
	DiscoveredSessions int       `json:"discovered_sessions"`
	ClaudeProjectsDir  string    `json:"claude_projects_dir"`
	StaleThreshold     int       `json:"stale_threshold_seconds"`
	Findings           []Finding `json:"findings"`
	LoopbackOK         bool      `json:"loopback_ok"`
	LoopbackError      string    `json:"loopback_error,omitempty"`
}

// Doctor gathers diagnostic info and returns a report. It does not print;
// callers decide how to render (see Print below).
func Doctor(stateDir string) (*DoctorReport, error) {
	settingsPath, err := SettingsPath()
	if err != nil {
		return nil, err
	}
	binary, _ := os.Executable()

	rep := &DoctorReport{
		SettingsPath:   settingsPath,
		Binary:         binary,
		StateDir:       stateDir,
		StaleThreshold: 60,
	}

	// 1. Hook installation.
	findings, err := Check(settingsPath, binary)
	if err != nil {
		return nil, err
	}
	rep.Findings = findings

	// 2. State dir writable.
	claudeDir := filepath.Join(stateDir, "claude")
	if err := os.MkdirAll(claudeDir, 0o755); err == nil {
		probe := filepath.Join(claudeDir, ".houston-doctor-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err == nil {
			rep.StateDirOK = true
			_ = os.Remove(probe)
		}
	}

	// 3. Count live sessions (state files updated within threshold).
	if entries, err := os.ReadDir(claudeDir); err == nil {
		cutoff := time.Now().Add(-time.Duration(rep.StaleThreshold) * time.Second).Unix()
		for _, e := range entries {
			if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
				continue
			}
			s, err := Read(filepath.Join(claudeDir, e.Name()))
			if err == nil && s.UpdatedAt >= cutoff {
				rep.LiveSessions++
			}
		}
	}

	// 4. Discovered sessions — Claude transcripts recently written even if no
	//    hook has fired yet. This is how houston picks up sessions that were
	//    already running when it started.
	if home, err := os.UserHomeDir(); err == nil {
		rep.ClaudeProjectsDir = filepath.Join(home, ".claude", "projects")
		rep.DiscoveredSessions = countRecentTranscripts(rep.ClaudeProjectsDir, 24*time.Hour)
	}

	// 5. Loopback: pipe a fake SessionStart event through Dispatch against a
	//    temp dir to confirm the binary itself can process events end-to-end.
	rep.LoopbackOK, rep.LoopbackError = loopback()

	return rep, nil
}

// countRecentTranscripts walks the Claude projects dir and counts .jsonl files
// modified within window. Returns 0 for missing dir or any I/O error.
func countRecentTranscripts(root string, window time.Duration) int {
	cutoff := time.Now().Add(-window)
	projects, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		files, err := os.ReadDir(filepath.Join(root, p.Name()))
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || filepath.Ext(f.Name()) != ".jsonl" {
				continue
			}
			if info, err := f.Info(); err == nil && info.ModTime().After(cutoff) {
				n++
			}
		}
	}
	return n
}

func loopback() (bool, string) {
	tmp, err := os.MkdirTemp("", "houston-doctor-*")
	if err != nil {
		return false, err.Error()
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	payload := map[string]any{
		"hook_event_name": EventSessionStart,
		"session_id":      "doctor-probe",
		"transcript_path": filepath.Join(tmp, "nope.jsonl"),
		"cwd":             tmp,
		"source":          "doctor",
	}
	b, _ := json.Marshal(payload)
	if err := Dispatch(EventSessionStart, tmp, strings.NewReader(string(b))); err != nil {
		return false, err.Error()
	}
	s, err := Read(Path(tmp, "doctor-probe"))
	if err != nil {
		return false, err.Error()
	}
	if s.State != StateStarting {
		return false, fmt.Sprintf("loopback state = %q, want %q", s.State, StateStarting)
	}
	return true, ""
}

// Print renders the report as human-readable output.
func Print(w io.Writer, r *DoctorReport) {
	fmt.Fprintf(w, "\nhouston doctor\n")
	fmt.Fprintf(w, "──────────────────────────────────────────────\n")
	fmt.Fprintf(w, "  binary         %s\n", dash(r.Binary))
	fmt.Fprintf(w, "  settings file  %s\n", r.SettingsPath)
	fmt.Fprintf(w, "  state dir      %s %s\n", r.StateDir, okMark(r.StateDirOK))
	fmt.Fprintf(w, "  loopback       %s", okMark(r.LoopbackOK))
	if !r.LoopbackOK && r.LoopbackError != "" {
		fmt.Fprintf(w, "  (%s)", r.LoopbackError)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "  live sessions  %d (state files updated in last %ds)\n", r.LiveSessions, r.StaleThreshold)
	if r.ClaudeProjectsDir != "" {
		fmt.Fprintf(w, "  discovered     %d (transcripts in %s active in last 24h)\n", r.DiscoveredSessions, r.ClaudeProjectsDir)
	}

	fmt.Fprintf(w, "\nHooks\n")
	fmt.Fprintf(w, "──────────────────────────────────────────────\n")
	anyMissing := false
	for _, f := range r.Findings {
		mark := okMark(f.Installed)
		fmt.Fprintf(w, "  %-20s %s", f.Event, mark)
		switch {
		case f.Installed:
			fmt.Fprintf(w, "  %s", f.Target)
		case f.OtherHook:
			fmt.Fprintf(w, "  (another hook is registered; houston not wired up)")
			anyMissing = true
		default:
			fmt.Fprintf(w, "  (missing)")
			anyMissing = true
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w)
	if anyMissing {
		fmt.Fprintf(w, "Fix with:  houston hooks install\n")
		fmt.Fprintf(w, "Preview:   houston hooks install --dry-run\n\n")
	} else {
		fmt.Fprintf(w, "All must-have hooks are installed.\n\n")
	}
}

func okMark(b bool) string {
	if b {
		return "[ok]"
	}
	return "[--]"
}

func dash(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
