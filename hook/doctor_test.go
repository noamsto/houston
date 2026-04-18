package hook

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestDoctorLoopbackAgainstEmptyStateDir(t *testing.T) {
	rep, err := Doctor(t.TempDir())
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if !rep.LoopbackOK {
		t.Errorf("LoopbackOK = false, err=%q", rep.LoopbackError)
	}
	if !rep.StateDirOK {
		t.Errorf("StateDirOK = false for fresh tmpdir")
	}
	if rep.LiveSessions != 0 {
		t.Errorf("LiveSessions = %d, want 0 for fresh tmpdir", rep.LiveSessions)
	}
	if len(rep.Findings) != len(MustHaveEvents) {
		t.Errorf("Findings count = %d, want %d", len(rep.Findings), len(MustHaveEvents))
	}
}

func TestDoctorCountsLiveSessions(t *testing.T) {
	dir := t.TempDir()
	// Fresh session (just now).
	if err := Write(Path(dir, "live"), SessionState{
		SessionID: "live", State: StateThinking, UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatalf("Write live: %v", err)
	}
	// Stale session (10 min old).
	if err := Write(Path(dir, "stale"), SessionState{
		SessionID: "stale", State: StateThinking, UpdatedAt: time.Now().Add(-10 * time.Minute).Unix(),
	}); err != nil {
		t.Fatalf("Write stale: %v", err)
	}
	rep, err := Doctor(dir)
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if rep.LiveSessions != 1 {
		t.Errorf("LiveSessions = %d, want 1", rep.LiveSessions)
	}
}

func TestDoctorPrintRendersMissingAndOk(t *testing.T) {
	rep := &DoctorReport{
		SettingsPath: "/tmp/settings.json",
		Binary:       "/tmp/houston",
		StateDir:     "/tmp/state",
		StateDirOK:   true,
		LoopbackOK:   true,
		Findings: []Finding{
			{Event: EventStop, Installed: true, Target: "/tmp/houston hook"},
			{Event: EventPreToolUse, Installed: false},
		},
	}
	var buf bytes.Buffer
	Print(&buf, rep)
	out := buf.String()

	if !strings.Contains(out, "Stop") {
		t.Errorf("missing Stop row: %s", out)
	}
	if !strings.Contains(out, "PreToolUse") {
		t.Errorf("missing PreToolUse row: %s", out)
	}
	if !strings.Contains(out, "missing") {
		t.Errorf("expected 'missing' hint: %s", out)
	}
	if !strings.Contains(out, "houston hooks install") {
		t.Errorf("expected fix hint: %s", out)
	}
}
