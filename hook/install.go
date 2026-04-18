package hook

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
)

// SettingsPath returns ~/.claude/settings.json.
func SettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// hookEntry is the shape Claude Code expects under settings.hooks.<event>.
type hookEntry struct {
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []command `json:"hooks"`
}

type command struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// DesiredCommand is the command string that should appear in settings for each
// must-have event, using the given houston binary path.
func DesiredCommand(binary string) string {
	return fmt.Sprintf("%s hook", binary)
}

// Check inspects the settings file and reports which must-have hooks are
// wired up to point at the given binary (or any command that contains
// "houston hook" — legacy tolerant).
//
// It returns one Finding per must-have event, in declaration order.
type Finding struct {
	Event     string // e.g. "PreToolUse"
	Installed bool   // any entry found that references houston
	Target    string // full command string of the matching entry, if any
	OtherHook bool   // true when some other non-houston hook is registered for this event
}

func Check(settingsPath, expectedBinary string) ([]Finding, error) {
	settings, err := loadSettings(settingsPath)
	if err != nil {
		return nil, err
	}

	findings := make([]Finding, 0, len(MustHaveEvents))
	for _, ev := range MustHaveEvents {
		f := Finding{Event: ev}
		entries := settings.Hooks[ev]
		for _, entry := range entries {
			for _, cmd := range entry.Hooks {
				if cmd.Type != "command" {
					continue
				}
				if matchesHouston(cmd.Command, expectedBinary) {
					f.Installed = true
					f.Target = cmd.Command
				} else {
					f.OtherHook = true
				}
			}
		}
		findings = append(findings, f)
	}
	return findings, nil
}

func matchesHouston(cmd, binary string) bool {
	// Accept any command string that mentions our binary or the legacy bash helper.
	// Keeps us tolerant to users who wrap the command.
	if binary != "" && containsToken(cmd, binary) {
		return true
	}
	return containsToken(cmd, "houston hook") || containsToken(cmd, "claude-hook.sh")
}

func containsToken(s, tok string) bool {
	return tok != "" && len(s) >= len(tok) && indexOf(s, tok) >= 0
}

// small helper to avoid pulling strings pkg here (kept self-contained).
func indexOf(s, sub string) int {
	n, m := len(s), len(sub)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}

// Install writes settings.hooks.<event> entries for each must-have event,
// pointing at `<binary> hook`. Existing houston entries are replaced;
// non-houston entries for the same event are preserved alongside.
//
// When dryRun is true the resulting file is not written; Install returns
// the pretty-printed bytes it would have written.
func Install(settingsPath, binary string, dryRun bool) ([]byte, error) {
	settings, err := loadSettings(settingsPath)
	if err != nil {
		return nil, err
	}
	if settings.Hooks == nil {
		settings.Hooks = map[string][]hookEntry{}
	}

	for _, ev := range MustHaveEvents {
		// Keep non-houston entries verbatim, drop existing houston ones.
		kept := settings.Hooks[ev][:0]
		for _, entry := range settings.Hooks[ev] {
			filtered := entry
			filtered.Hooks = filtered.Hooks[:0]
			for _, c := range entry.Hooks {
				if !matchesHouston(c.Command, binary) {
					filtered.Hooks = append(filtered.Hooks, c)
				}
			}
			if len(filtered.Hooks) > 0 {
				kept = append(kept, filtered)
			}
		}
		kept = append(kept, hookEntry{
			Hooks: []command{{
				Type:    "command",
				Command: DesiredCommand(binary),
				Timeout: 5,
			}},
		})
		settings.Hooks[ev] = kept
	}

	buf, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, err
	}
	buf = append(buf, '\n')
	if dryRun {
		return buf, nil
	}
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return nil, err
	}
	if err := atomicWrite(settingsPath, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// --- private: settings IO ---

type claudeSettings struct {
	Hooks map[string][]hookEntry `json:"hooks,omitempty"`
	// Preserve other keys so we don't eat the user's theme, permissions, etc.
	Extra map[string]json.RawMessage `json:"-"`
}

func loadSettings(path string) (*claudeSettings, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &claudeSettings{Hooks: map[string][]hookEntry{}}, nil
	}
	if err != nil {
		return nil, err
	}

	// Round-trip via RawMessage map to preserve foreign keys.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	cs := &claudeSettings{Extra: map[string]json.RawMessage{}}
	for k, v := range raw {
		if k == "hooks" {
			if err := json.Unmarshal(v, &cs.Hooks); err != nil {
				return nil, fmt.Errorf("parse hooks section: %w", err)
			}
			continue
		}
		cs.Extra[k] = v
	}
	if cs.Hooks == nil {
		cs.Hooks = map[string][]hookEntry{}
	}
	return cs, nil
}

func (s claudeSettings) MarshalJSON() ([]byte, error) {
	out := make(map[string]json.RawMessage, len(s.Extra)+1)
	maps.Copy(out, s.Extra)
	if len(s.Hooks) > 0 {
		b, err := json.Marshal(s.Hooks)
		if err != nil {
			return nil, err
		}
		out["hooks"] = b
	}
	return json.Marshal(out)
}

func atomicWrite(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}
