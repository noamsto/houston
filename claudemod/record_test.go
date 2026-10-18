package claudemod

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeValid(t *testing.T) {
	body := `{"v":1,"sid":"s","boot":"b","seq":3,"ts":99,"ev":"tool.end","tool":"Bash","id":"t1",
		"err":true,"task":"bg1","user_bg":true,"suggestions":2,
		"tasks":[{"id":"a","status":"running"}],"ask":[{"question":"q","multi":true,"options":["x","y"]}]}`
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

func TestDecodeMalformed(t *testing.T) {
	cases := map[string]string{
		"invalid json":   `{"v":1,`,
		"version 2":      `{"v":2,"sid":"s","boot":"b","seq":1,"ev":"stop"}`,
		"empty sid":      `{"v":1,"sid":"","boot":"b","seq":1,"ev":"stop"}`,
		"empty boot":     `{"v":1,"sid":"s","boot":"","seq":1,"ev":"stop"}`,
		"seq zero":       `{"v":1,"sid":"s","boot":"b","seq":0,"ev":"stop"}`,
		"unknown ev":     `{"v":1,"sid":"s","boot":"b","seq":1,"ev":"nope"}`,
		"trailing value": `{"v":1,"sid":"s","boot":"b","seq":1,"ev":"stop"}{}`,
		"empty body":     ``,
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

func TestDecodeTooLarge(t *testing.T) {
	body := `{"v":1,"sid":"s","boot":"b","seq":1,"ev":"stop","message":"` + strings.Repeat("x", MaxBody) + `"}`
	if _, err := Decode(strings.NewReader(body)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}
