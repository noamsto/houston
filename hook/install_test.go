package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if _, err := Install(path, "/usr/local/bin/houston", false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("settings.json not valid json: %v\n%s", err, b)
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("missing hooks key")
	}
	for _, ev := range MustHaveEvents {
		if _, ok := hooks[ev]; !ok {
			t.Errorf("hooks.%s not installed", ev)
		}
	}
}

func TestInstallPreservesExistingKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	existing := `{
  "theme": "dark",
  "alwaysThinkingEnabled": true,
  "permissions": {"allow": ["Bash(ls *)"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "/opt/audit.sh"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	if _, err := Install(path, "/opt/houston", false); err != nil {
		t.Fatalf("Install: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("parse: %v\n%s", err, b)
	}
	if parsed["theme"] != "dark" {
		t.Errorf("theme wiped: %v", parsed["theme"])
	}
	if parsed["alwaysThinkingEnabled"] != true {
		t.Errorf("alwaysThinkingEnabled wiped")
	}
	if _, ok := parsed["permissions"]; !ok {
		t.Errorf("permissions wiped")
	}

	// Existing non-houston audit hook on PreToolUse should still be there.
	if !strings.Contains(string(b), "/opt/audit.sh") {
		t.Errorf("non-houston hook dropped: %s", b)
	}
	// New houston entry on PreToolUse should be present.
	if !strings.Contains(string(b), "/opt/houston hook") {
		t.Errorf("houston hook not written: %s", b)
	}
}

func TestInstallIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if _, err := Install(path, "/opt/houston", false); err != nil {
		t.Fatalf("Install 1: %v", err)
	}
	first, _ := os.ReadFile(path)
	if _, err := Install(path, "/opt/houston", false); err != nil {
		t.Fatalf("Install 2: %v", err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) {
		t.Errorf("Install is not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	// Ensure there's only ONE houston entry per event.
	var parsed struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(second, &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}
	for ev, entries := range parsed.Hooks {
		count := 0
		for _, e := range entries {
			for _, c := range e.Hooks {
				if strings.Contains(c.Command, "/opt/houston hook") {
					count++
				}
			}
		}
		if count != 1 {
			t.Errorf("event %s has %d houston entries, want 1", ev, count)
		}
	}
}

func TestCheckReportsMissingAndInstalled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// Install only Notification manually, leave the rest missing.
	seed := `{
  "hooks": {
    "Notification": [
      {"hooks": [{"type":"command","command":"/opt/houston hook"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	findings, err := Check(path, "/opt/houston")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	byName := map[string]Finding{}
	for _, f := range findings {
		byName[f.Event] = f
	}
	if !byName[EventNotification].Installed {
		t.Errorf("Notification should be installed")
	}
	if byName[EventStop].Installed {
		t.Errorf("Stop should be missing")
	}
}

func TestCheckFlagsOtherHook(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	seed := `{
  "hooks": {
    "Stop": [
      {"hooks": [{"type":"command","command":"/opt/other-tool.sh"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	findings, err := Check(path, "/opt/houston")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, f := range findings {
		if f.Event != EventStop {
			continue
		}
		if f.Installed {
			t.Errorf("Stop should not be marked installed when only other-tool is registered")
		}
		if !f.OtherHook {
			t.Errorf("Stop should be flagged as having OtherHook")
		}
	}
}

func TestInstallAcceptsLegacyBashScript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	// User has the old bash script wired up.
	seed := `{
  "hooks": {
    "Notification": [
      {"hooks": [{"type":"command","command":"/home/me/scripts/claude-hook.sh"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	findings, err := Check(path, "/opt/houston")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	for _, f := range findings {
		if f.Event == EventNotification && !f.Installed {
			t.Errorf("Notification with legacy bash script should be recognized as installed")
		}
	}
}

func TestInstallDryRunDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	out, err := Install(path, "/opt/houston", true)
	if err != nil {
		t.Fatalf("Install dry-run: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("dry-run should not create file, err=%v", err)
	}
	if len(out) == 0 {
		t.Errorf("dry-run should return preview bytes")
	}
}
