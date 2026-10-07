package server

import (
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var safeNameRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,63}$`)

var imageBaseRE = regexp.MustCompile(`^houston-[0-9]+-[A-Za-z0-9._-]+$`)

func TestRunInputImageSanitizesName(t *testing.T) {
	tests := []struct {
		name   string
		upload string
		suffix string
	}{
		{"newline", "a\nb.png", "-a_b.png"},
		{"carriage return", "c\rd.png", "-c_d.png"},
		{"escape and nul", "e\x1bf\x00.png", "-e_f_.png"},
		{"parent dirs", "../../g.png", "-g.png"},
		{"dotdot with newline", "a/../b\nc.png", "-b_c.png"},
		{"leading dash", "-rf.png", "-_rf.png"},
		{"non-ascii", "日本.png", "-__.png"},
		{"long", strings.Repeat("x", 300) + ".png", "-" + strings.Repeat("x", 60) + ".png"},
		{"space", "screen shot 1.png", "-screen_shot_1.png"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			s := newRunTerminalServer(t, panes, termDelta())

			body := inputBody(t, map[string]any{
				"type": "image",
				"text": "caption",
				"images": []map[string]string{{
					"name": tc.upload,
					"type": "image/png",
					"data": base64.StdEncoding.EncodeToString([]byte("\x89PNG not really")),
				}},
			})
			rec := doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/input", body))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
			}

			_, sent := panes.calls()
			if len(sent) != 1 {
				t.Fatalf("sent %+v, want one message", sent)
			}
			msg := sent[0].keys
			path, ok := strings.CutSuffix(msg, " caption")
			if !ok {
				t.Fatalf("message %q does not end with the caption", msg)
			}
			t.Cleanup(func() {
				// Only remove what the handler could have made.
				if filepath.Dir(path) == "/tmp" && strings.HasPrefix(path, "/tmp/houston-") {
					_ = os.Remove(path)
				}
			})

			for i := range len(path) {
				if path[i] < 0x21 || path[i] > 0x7e {
					t.Errorf("path %q has byte 0x%02x outside 0x21..0x7e at %d", path, path[i], i)
					break
				}
			}
			if dir := filepath.Dir(path); dir != "/tmp" {
				t.Errorf("path %q in %q, want /tmp", path, dir)
			}
			base := filepath.Base(path)
			if !imageBaseRE.MatchString(base) {
				t.Errorf("basename %q does not match %s", base, imageBaseRE)
			}
			if limit := len("houston-") + 10 + 1 + 64; len(base) > limit {
				t.Errorf("basename %q is %d bytes, want <= %d", base, len(base), limit)
			}
			if !strings.HasSuffix(base, tc.suffix) {
				t.Errorf("basename %q, want suffix %q", base, tc.suffix)
			}
		})
	}
}

func TestSafeImageName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"space", "screen shot 1.png", "screen_shot_1.png"},
		{"newline", "a\nb.png", "a_b.png"},
		{"carriage return", "a\rb.png", "a_b.png"},
		{"nul", "a\x00b.png", "a_b.png"},
		{"escape sequence", "a\x1b[31m.png", "a__31m.png"},
		{"tab", "a\tb.png", "a_b.png"},
		{"parent dirs", "../../evil.png", "evil.png"},
		{"dotdot with newline", "a/../b\nc.png", "b_c.png"},
		{"leading dash", "-rf.png", "_rf.png"},
		{"dotdot", "..", "__"},
		{"empty", "", "_"},
		{"dot only ext", ".png", "_png"},
		{"dotfile", ".bashrc", "_bashrc"},
		{"non-ascii", "日本.png", "__.png"},
		{"star", "a*b.png", "a_b.png"},
		{"long", strings.Repeat("x", 300) + ".png", strings.Repeat("x", 60) + ".png"},
		{"long ext", "a.verylongextension", "a.verylongextension"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := safeImageName(tc.in)
			if got != tc.want {
				t.Errorf("safeImageName(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if !safeNameRE.MatchString(got) {
				t.Errorf("safeImageName(%q) = %q, does not match %s", tc.in, got, safeNameRE)
			}
		})
	}
}
