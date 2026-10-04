package hub

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/noamsto/houston/chat"
	"github.com/noamsto/houston/hook"
)

// chat cannot import hook, so chat.ToolTitle duplicates hook.ToolHint; this
// pins the two in step.
func TestChatTitleMatchesHookToolHint(t *testing.T) {
	if !reflect.DeepEqual(chat.TitleKeys, hook.ToolHintKeys) {
		t.Fatalf("chat.TitleKeys = %v, hook.ToolHintKeys = %v", chat.TitleKeys, hook.ToolHintKeys)
	}

	long := strings.Repeat("x", 130)
	tests := []struct {
		tool  string
		input string
	}{
		{"Bash", `{"description":"run the tests","command":"go test ./..."}`},
		{"Bash", `{"command":"  go test ./...  "}`},
		{"Bash", `{"other":"x"}`},
		{"Bash", `{"description":"` + long + `"}`},
		{"Bash", `{"command":"` + long + `"}`},
		{"Read", `{"file_path":"/tmp/dir/main.go"}`},
		{"Read", `{"path":"/tmp/dir/other.go"}`},
		{"Read", `{"other":"x"}`},
		{"Edit", `{"file_path":"/tmp/dir/main.go","old_string":"a"}`},
		{"Edit", `{"path":"/tmp/dir/other.go"}`},
		{"Edit", `{"file_path":"/tmp/` + long + `"}`},
		{"Write", `{"file_path":"/tmp/dir/main.go"}`},
		{"Task", `{"path":"/tmp/x","command":"echo hi"}`},
		{"Task", `{"command":"echo hi","description":"say hi"}`},
		{"Grep", `{"pattern":"func main"}`},
		{"WebFetch", `{"url":"https://example.com"}`},
		{"Task", `{"description":"do a thing"}`},
		{"Agent", `{"prompt":"investigate the flake"}`},
		{"Agent", `{"prompt":"` + long + `"}`},
		{"Mystery", `{"other":"x"}`},
		{"Mystery", `{"file_path":""}`},
		{"bash", `{"command":"go test ./..."}`},
		{"bash", `{"description":"run","command":"x"}`},
		{"read", `{"path":"/tmp/dir/main.go"}`},
		{"edit", `{"path":"/tmp/dir/main.go","edits":[]}`},
		{"write", `{"path":"/tmp/dir/main.go","content":"x"}`},
		{"grep", `{"pattern":"func x","path":"."}`},
		{"grep", `{"path":"src"}`},
		{"find", `{"pattern":"*.go","path":"src"}`},
		{"find", `{"other":"x"}`},
		{"ls", `{"path":"src"}`},
		{"Bash", ``},
		{"Bash", `{not json`},
		{"Read", `[1,2]`},
	}
	for _, tt := range tests {
		raw := json.RawMessage(tt.input)
		if got, want := chat.ToolTitle(tt.tool, raw), hook.ToolHint(tt.tool, raw); got != want {
			t.Errorf("%s %s: chat.ToolTitle = %q, hook.ToolHint = %q", tt.tool, tt.input, got, want)
		}
	}
}
