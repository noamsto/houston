package server

import (
	"encoding/json"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/noamsto/houston/agents"
	"github.com/noamsto/houston/agents/generic"
	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

func tmuxOut(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("tmux", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newTrapSession builds the layout a bare-session target gets wrong: the run's
// pane is window 0 / pane 0 while another window is selected, so "<session>"
// alone would address the other window's pane. It returns both pane ids.
func newTrapSession(t *testing.T) (session, runPane, otherPane string) {
	t.Helper()
	session = "houston-runterm-" + strconv.Itoa(os.Getpid()) + "-" + strconv.FormatUint(rand.Uint64(), 36)
	// Two argv words make tmux exec the shell directly, bypassing the user's
	// default-shell and its startup files.
	tmuxOut(t, "new-session", "-d", "-s", session, "-x", "80", "-y", "24", "/bin/sh", "-i")
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", session).Run() })

	runPane = tmuxOut(t, "display-message", "-t", session, "-p", "#{pane_id}")
	// Force window 0 / pane 0 whatever the user's base-index settings are.
	if idx := tmuxOut(t, "display-message", "-t", runPane, "-p", "#{window_index}"); idx != "0" {
		tmuxOut(t, "move-window", "-s", session+":"+idx, "-t", session+":0")
	}
	tmuxOut(t, "set-option", "-w", "-t", session+":0", "pane-base-index", "0")

	otherPane = tmuxOut(t, "new-window", "-d", "-P", "-F", "#{pane_id}", "-t", session+":1", "/bin/sh", "-i")
	tmuxOut(t, "select-window", "-t", session+":1")

	resolved, err := tmux.NewClient().ResolvePane(runPane)
	if err != nil {
		t.Fatalf("resolve %s: %v", runPane, err)
	}
	if resolved.Window != 0 || resolved.Index != 0 {
		t.Fatalf("run pane resolved to %+v, want window 0 pane 0", resolved)
	}
	if bare := tmuxOut(t, "display-message", "-t", session, "-p", "#{pane_id}"); bare != otherPane {
		t.Fatalf("bare session target is %s, want the other window's %s", bare, otherPane)
	}
	return session, runPane, otherPane
}

func capturePaneText(t *testing.T, paneID string) string {
	t.Helper()
	return tmuxOut(t, "capture-pane", "-p", "-t", paneID)
}

func waitForPaneText(t *testing.T, paneID, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := capturePaneText(t, paneID)
		if strings.Contains(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane %s never showed %q; last capture:\n%s", paneID, want, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Input reaches the run's own pane through both routes, against real tmux.
func TestRunsTerminalE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	_, runPaneID, otherPaneID := newTrapSession(t)

	reg := runs.NewRegistry(runs.DefaultOrder)
	reg.Apply(runs.Delta{Source: "tmux", Key: runPaneID, Run: runs.Run{
		Agent: "claude",
		Tmux:  &runs.TmuxRef{PaneID: runPaneID},
	}})
	runID := reg.Snapshot()[0].ID

	srv := httptest.NewUnstartedServer(nil)
	origin := "http://" + srv.Listener.Addr().String()
	allowed := []string{origin}
	controlMgr := tmux.NewControlManager()
	t.Cleanup(controlMgr.Close)
	s := &Server{
		auth:       &authGate{token: replyToken, enabled: true, allowedOrigins: allowed},
		hosts:      deriveHosts(nil, allowed),
		runs:       reg,
		runPanes:   tmux.NewClient(),
		tmux:       tmux.NewClient(),
		controlMgr: controlMgr,
		registry:   agents.NewRegistry(generic.New()),
	}
	srv.Config.Handler = s.Handler()
	srv.Start()
	t.Cleanup(srv.Close)

	post := func(t *testing.T, body string) {
		t.Helper()
		req, err := http.NewRequest("POST", srv.URL+"/api/runs/"+runID+"/input", strings.NewReader(body))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.AddCookie(&http.Cookie{Name: authCookie, Value: replyToken})
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNoContent {
			t.Fatalf("status %d, want 204", res.StatusCode)
		}
	}

	// Each marker is typed with an empty '' in the middle, so only the
	// command's output, not its echoed command line, contains it whole.
	marker := strconv.FormatUint(rand.Uint64(), 36)

	t.Run("text reaches window 0 pane 0", func(t *testing.T) {
		post(t, `{"type":"text","text":"echo run-input-''`+marker+`"}`)
		waitForPaneText(t, runPaneID, "run-input-"+marker)
		if other := capturePaneText(t, otherPaneID); strings.Contains(other, "run-input-") {
			t.Fatalf("input landed in the selected window's pane too:\n%s", other)
		}
	})

	t.Run("key", func(t *testing.T) {
		post(t, `{"type":"key","key":"C-c"}`)
	})

	t.Run("websocket input", func(t *testing.T) {
		header := http.Header{}
		header.Set("Origin", origin)
		header.Set("Cookie", authCookie+"="+replyToken)
		wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/runs/" + runID + "/terminal"
		conn, res, err := websocket.DefaultDialer.Dial(wsURL, header)
		if err != nil {
			status := 0
			if res != nil {
				status = res.StatusCode
			}
			t.Fatalf("dial: %v (status %d)", err, status)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		var text strings.Builder
		readUntil := func(done func(msg WSMessage) bool) {
			t.Helper()
			for {
				_, raw, err := conn.ReadMessage()
				if err != nil {
					t.Fatalf("read: %v; received so far: %q", err, text.String())
				}
				var msg WSMessage
				if err := json.Unmarshal(raw, &msg); err != nil {
					t.Fatalf("decode %s: %v", raw, err)
				}
				if msg.Type == "seed" || msg.Type == "output" {
					var out WSOutput
					if err := json.Unmarshal(msg.Data, &out); err != nil {
						t.Fatalf("decode %s: %v", msg.Data, err)
					}
					text.WriteString(out.Data)
				}
				if done(msg) {
					return
				}
			}
		}

		readUntil(func(msg WSMessage) bool { return msg.Type == "seed" })
		if !strings.Contains(text.String(), "run-input-"+marker) {
			t.Fatalf("seed is not the run's pane: %q", text.String())
		}

		input, err := json.Marshal(WSMessage{Type: "input", Data: json.RawMessage(
			`{"data":"echo ws-input-''` + marker + `\r"}`)})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := conn.WriteMessage(websocket.TextMessage, input); err != nil {
			t.Fatalf("write: %v", err)
		}
		readUntil(func(WSMessage) bool { return strings.Contains(text.String(), "ws-input-"+marker) })
	})
}
