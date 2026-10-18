package claudemod

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDecodeValid(t *testing.T) {
	body := `{"v":1,"sid":"s","boot":"b","seq":3,"ts":99,"ev":"tool.end","tool":"Bash","id":"t1",
		"err":true,"task":"bg1","user_bg":true,"suggestions":2,
		"tasks":[{"id":"a","status":"running"}],"ask":[{"question":"q","multi":true,"options":[{"label":"x"},{"label":"y","preview":true}]}]}`
	rec, err := Decode(strings.NewReader(body))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if rec.SID != "s" || rec.Boot != "b" || rec.Seq != 3 || rec.TS != 99 || rec.Ev != "tool.end" ||
		rec.Tool != "Bash" || rec.ID != "t1" || !rec.Err || rec.Task != "bg1" || !rec.UserBG ||
		rec.Suggestions != 2 || len(rec.Tasks) != 1 || rec.Tasks[0].Status != "running" ||
		len(rec.Ask) != 1 || !rec.Ask[0].Multi || len(rec.Ask[0].Options) != 2 {
		t.Fatalf("decoded wrong: %+v", rec)
	}
}

func TestDecodeNewFields(t *testing.T) {
	body := `{"v":1,"sid":"s","boot":"b","seq":1,"ts":5,"ev":"tool.start","cwd":"/r","transcript":"/t.jsonl",
		"pane":"%3","tmux_server":"123","last":"done","stop_task":"bg2","timeout_ms":60000,
		"input":{"description":"d","command":"c","file_path":"/f","path":"/p","pattern":"pt","url":"http://u",
		"prompt":"pr","questions":[{"header":"h","question":"q?"}]},
		"tasks":[{"id":"a","description":"desc","tool_use":"tu1"}]}`
	rec, err := Decode(strings.NewReader(body))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	want := ToolInput{Description: "d", Command: "c", FilePath: "/f", Path: "/p", Pattern: "pt",
		URL: "http://u", Prompt: "pr", Questions: []AskQuestion{{Header: "h", Question: "q?"}}}
	if rec.CWD != "/r" || rec.Transcript != "/t.jsonl" || rec.Pane != "%3" || rec.TmuxServer != "123" ||
		rec.Last != "done" || rec.StopTask != "bg2" || rec.TimeoutMS != 60000 ||
		rec.Input == nil || !reflect.DeepEqual(*rec.Input, want) ||
		len(rec.Tasks) != 1 || rec.Tasks[0].Description != "desc" || rec.Tasks[0].ToolUse != "tu1" {
		t.Fatalf("decoded wrong: %+v", rec)
	}
}

func TestDecodeStopTasks(t *testing.T) {
	empty, err := Decode(strings.NewReader(`{"v":1,"sid":"s","boot":"b","seq":1,"ts":1,"ev":"stop","tasks":[]}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if empty.Tasks == nil || len(empty.Tasks) != 0 {
		t.Fatalf("tasks:[] decoded to %#v, want non-nil empty", empty.Tasks)
	}
	absent, err := Decode(strings.NewReader(`{"v":1,"sid":"s","boot":"b","seq":1,"ts":1,"ev":"stop"}`))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if absent.Tasks != nil {
		t.Fatalf("absent tasks decoded to %#v, want nil", absent.Tasks)
	}
}

func TestDecodeMalformed(t *testing.T) {
	cases := map[string]string{
		"invalid json":   `{"v":1,`,
		"version 2":      `{"v":2,"sid":"s","boot":"b","seq":1,"ts":1,"ev":"stop"}`,
		"empty sid":      `{"v":1,"sid":"","boot":"b","seq":1,"ts":1,"ev":"stop"}`,
		"empty boot":     `{"v":1,"sid":"s","boot":"","seq":1,"ts":1,"ev":"stop"}`,
		"seq zero":       `{"v":1,"sid":"s","boot":"b","seq":0,"ts":1,"ev":"stop"}`,
		"ts zero":        `{"v":1,"sid":"s","boot":"b","seq":1,"ts":0,"ev":"stop"}`,
		"ts missing":     `{"v":1,"sid":"s","boot":"b","seq":1,"ev":"stop"}`,
		"ts negative":    `{"v":1,"sid":"s","boot":"b","seq":1,"ts":-5,"ev":"stop"}`,
		"unknown ev":     `{"v":1,"sid":"s","boot":"b","seq":1,"ts":1,"ev":"nope"}`,
		"trailing value": `{"v":1,"sid":"s","boot":"b","seq":1,"ts":1,"ev":"stop"}{}`,
		"empty body":     ``,
		"long sid":       `{"v":1,"sid":"` + strings.Repeat("s", 129) + `","boot":"b","seq":1,"ts":1,"ev":"stop"}`,
		"long boot":      `{"v":1,"sid":"s","boot":"` + strings.Repeat("b", 65) + `","seq":1,"ts":1,"ev":"stop"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Decode(strings.NewReader(body))
			if err == nil {
				t.Fatal("want error")
			}
			if errors.Is(err, ErrTooLarge) {
				t.Fatalf("want a malformed error, got %v", err)
			}
		})
	}
}

func TestDecodeLongestSIDAndBoot(t *testing.T) {
	body := `{"v":1,"sid":"` + strings.Repeat("s", 128) + `","boot":"` + strings.Repeat("b", 64) + `","seq":1,"ts":1,"ev":"heartbeat"}`
	if _, err := Decode(strings.NewReader(body)); err != nil {
		t.Fatalf("Decode: %v", err)
	}
}

func TestDecodeTooLarge(t *testing.T) {
	body := `{"v":1,"sid":"s","boot":"b","seq":1,"ts":1,"ev":"stop","message":"` + strings.Repeat("x", MaxBody) + `"}`
	if _, err := Decode(strings.NewReader(body)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}
