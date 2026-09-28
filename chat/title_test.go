package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolTitle(t *testing.T) {
	tests := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{"bash description over command", "Bash", `{"description":"run the tests","command":"go test ./..."}`, "run the tests"},
		{"bash command only", "Bash", `{"command":"go test ./..."}`, "go test ./..."},
		{"bash neither", "Bash", `{"foo":"bar"}`, ""},
		{"read file_path basename", "Read", `{"file_path":"/tmp/dir/main.go"}`, "main.go"},
		{"read path fallback", "Read", `{"path":"/tmp/dir/other.go"}`, "other.go"},
		{"edit file_path basename", "Edit", `{"file_path":"/tmp/dir/main.go","old_string":"a","new_string":"b"}`, "main.go"},
		{"write generic full path", "Write", `{"file_path":"/tmp/dir/main.go","content":"x"}`, "/tmp/dir/main.go"},
		{"multiedit generic full path", "MultiEdit", `{"file_path":"/tmp/dir/main.go","edits":[]}`, "/tmp/dir/main.go"},
		{"generic key order path over command", "Task", `{"path":"/tmp/x","command":"echo hi"}`, "/tmp/x"},
		{"generic key order command over description", "Task", `{"command":"echo hi","description":"say hi"}`, "echo hi"},
		{"generic falls to description", "Task", `{"description":"do a thing"}`, "do a thing"},
		{"generic falls to prompt", "Agent", `{"prompt":"investigate the flake"}`, "investigate the flake"},
		{"unknown tool no matching key", "Mystery", `{"other":"x"}`, ""},
		{"empty input", "Bash", ``, ""},
		{"bad json", "Bash", `{not json`, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToolTitle(tt.tool, json.RawMessage(tt.input))
			if got != tt.want {
				t.Errorf("ToolTitle(%q, %s) = %q, want %q", tt.tool, tt.input, got, tt.want)
			}
		})
	}
}

func TestToolTitleTruncates(t *testing.T) {
	long := strings.Repeat("a", 200)
	input, err := json.Marshal(map[string]string{"command": long})
	if err != nil {
		t.Fatal(err)
	}
	got := ToolTitle("Bash", input)
	if len([]rune(got)) != 120 {
		t.Fatalf("len(got) = %d, want 120", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("got = %q, want suffix …", got)
	}
	if got != long[:119]+"…" {
		t.Fatalf("got = %q, want %q", got, long[:119]+"…")
	}
}

func TestToolKind(t *testing.T) {
	tests := []struct {
		tool string
		want string
	}{
		{"Read", KindRead},
		{"Edit", KindEdit},
		{"Write", KindEdit},
		{"MultiEdit", KindEdit},
		{"NotebookEdit", KindEdit},
		{"Grep", KindSearch},
		{"Glob", KindSearch},
		{"Bash", KindExecute},
		{"WebFetch", KindFetch},
		{"WebSearch", KindFetch},
		{"Task", KindOther},
		{"Agent", KindOther},
		{"SomethingElse", KindOther},
	}
	for _, tt := range tests {
		if got := ToolKind(tt.tool); got != tt.want {
			t.Errorf("ToolKind(%q) = %q, want %q", tt.tool, got, tt.want)
		}
	}
}
