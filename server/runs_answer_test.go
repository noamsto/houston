package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/answer"
	"github.com/noamsto/houston/chat"
	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

const (
	plainCall      = "call-plain"
	tabbedCall     = "call-tabbed"
	previewCall    = "call-preview"
	previewTabCall = "call-preview-tabbed"
	m3Call         = "call-m3"
)

// The questions behind answer/testdata's plain-* and tab-* frames.
const (
	plainInput = `{"questions":[{"question":"Which color do you prefer?","header":"Color","multiSelect":false,
		"options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"},{"label":"Green","description":"Natural"}]}]}`
	// Also the questions behind the 2.1.310 v2-*, v3-* and 2.1.310-normal frames.
	tabbedInput = `{"questions":[
		{"question":"Which color do you prefer?","header":"Color","multiSelect":false,
		 "options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]},
		{"question":"Which toppings do you want?","header":"Toppings","multiSelect":true,
		 "options":[{"label":"Cheese","description":"Melty"},{"label":"Olives","description":"Salty"},{"label":"Basil","description":"Fresh"}]}]}`
	// The questions behind answer/testdata's prev-q* and prevtab-* frames.
	previewInput = `{"questions":[{"question":"Which layout do you prefer?","header":"Layout","multiSelect":false,
		"options":[{"label":"Grid","description":"Cards in rows","preview":"+--+ +--+\n|A | |B |"},
		{"label":"List","description":"One per line","preview":"+------+\n| item |"},
		{"label":"Split","description":"Two panes","preview":"+-----+------+\n| Nav | Main |"}]}]}`
	previewTabInput = `{"questions":[
		{"question":"Pick a size?","header":"Size","multiSelect":false,
		 "options":[{"label":"Small","description":"Compact","preview":"+---+\n| s |"},{"label":"Large","description":"Roomy","preview":"+---+\n| L |"}]},
		{"question":"Pick a speed?","header":"Speed","multiSelect":false,
		 "options":[{"label":"Slow","description":"Careful"},{"label":"Fast","description":"Quick"}]}]}`
	// The questions behind the 2.1.310 m3-* frames.
	m3Input = `{"questions":[
		{"question":"Which color do you prefer?","header":"Color","multiSelect":false,
		 "options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"}]},
		{"question":"Which toppings do you want?","header":"Toppings","multiSelect":true,
		 "options":[{"label":"Cheese","description":"Melty"},{"label":"Olives","description":"Salty"},{"label":"Basil","description":"Fresh"}]},
		{"question":"Pick a size?","header":"Size","multiSelect":false,
		 "options":[{"label":"Small","description":"Compact","preview":"+-----+\n|PREVIEW-S|\n+-----+"},
		 {"label":"Large","description":"Roomy","preview":"+-----+\n|PREVIEW-L|\n+-----+"}]}]}`
)

// m3Review is what m3-review shows.
var m3Review = []string{"Red", "Cheese, Basil, extra pepper", "Large"}

func askCall(id, input string) *chat.Update {
	return &chat.Update{
		SessionUpdate: chat.SessionUpdateToolCall,
		ToolCallID:    id,
		Status:        chat.StatusPending,
		RawInput:      json.RawMessage(input),
		Meta:          map[string]any{"tool": "AskUserQuestion"},
	}
}

// answerDeltas is a Claude run in pane %42 whose hooks layer names chatSID.
func answerDeltas(agent string) []runs.Delta {
	tmuxLayer := termDelta()
	tmuxLayer.Run.Agent = agent
	return []runs.Delta{tmuxLayer, {Source: "hooks", Key: "%42", Run: runs.Run{Agent: agent, Session: chatSID}}}
}

func newAnswerServer(t *testing.T, panes *fakeRunPanes, agent string) (*Server, *fakeChat) {
	t.Helper()
	s := newRunTerminalServer(t, panes, answerDeltas(agent)...)
	src := newFakeChat(0)
	src.tools[plainCall] = askCall(plainCall, plainInput)
	src.tools[tabbedCall] = askCall(tabbedCall, tabbedInput)
	src.tools[previewCall] = askCall(previewCall, previewInput)
	src.tools[previewTabCall] = askCall(previewTabCall, previewTabInput)
	src.tools[m3Call] = askCall(m3Call, m3Input)
	s.chat = src
	s.answerPoll = time.Millisecond
	s.answerWait = 100 * time.Millisecond
	return s, src
}

func screenFrame(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "answer", "testdata", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// screen scripts panes to start on frame from and follow transitions, each
// {from, keys, to}; every frame named is loaded from answer/testdata.
func screen(t *testing.T, panes *fakeRunPanes, from string, transitions ...[3]string) {
	t.Helper()
	panes.frames = map[string]string{from: screenFrame(t, from)}
	panes.frame = from
	panes.next = map[string]string{}
	for _, tr := range transitions {
		for _, name := range []string{tr[0], tr[2]} {
			panes.frames[name] = screenFrame(t, name)
		}
		panes.next[tr[0]+"|"+tr[1]] = tr[2]
	}
}

// plainOtherScreen walks the 2.1.310 plain dialog through typing "dark teal"
// into its Other field.
func plainOtherScreen(t *testing.T, panes *fakeRunPanes) {
	screen(t, panes, "2.1.310/plain-q",
		[3]string{"2.1.310/plain-q", "Down Down Down", "2.1.310/plain-other-cursor"},
		[3]string{"2.1.310/plain-other-cursor", "text:dark teal", "2.1.310/plain-other-typed"},
		[3]string{"2.1.310/plain-other-typed", "Enter", "plain-done"},
	)
}

var plainOtherKeys = []string{"Down", "Down", "Down", "text:dark teal", "Enter"}

func stateBody(callID string) string {
	b, _ := json.Marshal(map[string]any{"kind": "state", "toolCallId": callID})
	return string(b)
}

func stepBody(callID string, index int, a answerEntry) string {
	b, _ := json.Marshal(map[string]any{"kind": "step", "toolCallId": callID, "index": index, "answer": a})
	return string(b)
}

func backBody(callID string, index int) string {
	b, _ := json.Marshal(map[string]any{"kind": "back", "toolCallId": callID, "index": index})
	return string(b)
}

func submitBody(callID string, review []string) string {
	b, _ := json.Marshal(map[string]any{"kind": "submit", "toolCallId": callID, "review": review})
	return string(b)
}

func choiceBody(ordinal int, frame string) string {
	b, _ := json.Marshal(map[string]any{"kind": "choice", "ordinal": ordinal, "frame": frame})
	return string(b)
}

func postAnswer(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/answer", body))
}

// keyLog renders the keys sent as the answer package's plan tests spell them.
func keyLog(panes *fakeRunPanes) []string {
	_, sent := panes.calls()
	log := []string{}
	for _, in := range sent {
		if in.special {
			log = append(log, in.keys)
			continue
		}
		if in.enter {
			log = append(log, "text+enter:"+in.keys)
			continue
		}
		log = append(log, "text:"+in.keys)
	}
	return log
}

func wantKeys(t *testing.T, panes *fakeRunPanes, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := keyLog(panes); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %q, want %q", got, want)
	}
}

func wantPartial(t *testing.T, rec *httptest.ResponseRecorder, code int) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status %d, want %d (%q)", rec.Code, code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type %q, want application/json", ct)
	}
	var body struct {
		Partial bool `json:"partial"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Partial {
		t.Fatalf("body %q, want {\"partial\":true}", rec.Body.String())
	}
}

func wantNoContent(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
	}
}

// wantState decodes a 200 state body. The raw fields come back too, so a test
// can tell an absent field from an empty one.
func wantState(t *testing.T, rec *httptest.ResponseRecorder) (questionState, map[string]json.RawMessage) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (%q)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type %q, want application/json", ct)
	}
	var st questionState
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return st, raw
}

// wantTab checks a state body shows tab step with options and text committed.
func wantTab(t *testing.T, rec *httptest.ResponseRecorder, step int, options []int, text string) questionState {
	t.Helper()
	st, raw := wantState(t, rec)
	if st.Step != step || st.Options == nil || !reflect.DeepEqual(*st.Options, options) || st.Text != text {
		t.Fatalf("state %s, want step %d options %v text %q", rec.Body.String(), step, options, text)
	}
	if _, ok := raw["review"]; ok {
		t.Errorf("tab state %s has a review", rec.Body.String())
	}
	return st
}

func TestRunAnswerState(t *testing.T) {
	t.Run("tab with options and Other text", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q2-other-typed")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, stateBody(m3Call))
		st := wantTab(t, rec, 1, []int{0, 2}, "extra pepper")
		if !st.Tabbed || st.Count != 3 || len(st.Questions) != 3 {
			t.Fatalf("state %s", rec.Body.String())
		}
		want := stateQuestion{Question: "Which toppings do you want?", Header: "Toppings", MultiSelect: true, Options: []stateOption{
			{"Cheese", "Melty"}, {"Olives", "Salty"}, {"Basil", "Fresh"},
		}}
		if !reflect.DeepEqual(st.Questions[1], want) {
			t.Errorf("question 1 = %+v, want %+v", st.Questions[1], want)
		}
		if q := st.Questions[2]; !q.Preview || q.MultiSelect || q.Header != "Size" || len(q.Options) != 2 {
			t.Errorf("question 2 = %+v, want the preview question", q)
		}
		if strings.Contains(rec.Body.String(), "PREVIEW") {
			t.Errorf("state %s carries previews", rec.Body.String())
		}
		wantKeys(t, panes)
	})

	t.Run("fresh tab sends empty options and no text", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, stateBody(m3Call))
		wantTab(t, rec, 0, []int{}, "")
		_, raw := wantState(t, rec)
		if string(raw["options"]) != "[]" {
			t.Errorf("options %s, want []", raw["options"])
		}
		if _, ok := raw["text"]; ok {
			t.Errorf("state %s has text", rec.Body.String())
		}
	})

	t.Run("re-entered tab shows the committed option", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1-back-left")
		s, _ := newAnswerServer(t, panes, "claude")

		wantTab(t, postAnswer(t, s, stateBody(m3Call)), 0, []int{1}, "")
	})

	t.Run("review", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-review")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, stateBody(m3Call))
		st, raw := wantState(t, rec)
		if st.Step != 3 || st.Count != 3 || !reflect.DeepEqual(st.Review, m3Review) {
			t.Fatalf("state %s, want the review", rec.Body.String())
		}
		if _, ok := raw["options"]; ok {
			t.Errorf("review state %s has options", rec.Body.String())
		}
	})

	t.Run("plain", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/plain-q")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, stateBody(plainCall))
		st := wantTab(t, rec, 0, []int{}, "")
		if st.Tabbed || st.Count != 1 || len(st.Questions[0].Options) != 3 {
			t.Fatalf("state %s, want a plain dialog", rec.Body.String())
		}
	})

	for _, frame := range []string{"plain-done", "perm-bash", "2.1.310/m3-q1"} {
		t.Run("no question/"+frame, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, frame)
			s, _ := newAnswerServer(t, panes, "claude")
			wantRefusal(t, postAnswer(t, s, stateBody(plainCall)), http.StatusNotFound, "no question")
		})
	}

	t.Run("answers while a step holds the lock", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1")
		s, _ := newAnswerServer(t, panes, "claude")
		mu, _ := s.answerLocks.LoadOrStore("%42", new(sync.Mutex))
		mu.(*sync.Mutex).Lock()

		wantTab(t, postAnswer(t, s, stateBody(m3Call)), 0, []int{}, "")
	})

	t.Run("capture fails", func(t *testing.T) {
		panes := &fakeRunPanes{captureErr: errors.New("no server running")}
		s, _ := newAnswerServer(t, panes, "claude")
		wantRefusal(t, postAnswer(t, s, stateBody(m3Call)), http.StatusServiceUnavailable, "tmux unavailable")
	})

	t.Run("logs at Debug", func(t *testing.T) {
		logs := captureServerLogs(t)
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1")
		s, _ := newAnswerServer(t, panes, "claude")

		wantTab(t, postAnswer(t, s, stateBody(m3Call)), 0, []int{}, "")
		for _, line := range strings.Split(logs.String(), "\n") {
			if strings.Contains(line, "kind=state") && !strings.Contains(line, "level=DEBUG") {
				t.Errorf("state logged above Debug: %s", line)
			}
		}
		if !strings.Contains(logs.String(), "level=DEBUG msg=\"run answer\"") {
			t.Errorf("no Debug line for the state read in %q", logs.String())
		}
	})
}

func TestRunAnswerStepSingle(t *testing.T) {
	for _, frame := range []string{"plain-q", "2.1.299/plain-q", "2.1.299-named/plain-q", "2.1.310/plain-q"} {
		t.Run("plain submits/"+frame, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, frame, [3]string{frame, "2", "plain-done"})
			s, _ := newAnswerServer(t, panes, "claude")

			wantNoContent(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Options: []int{1}})))
			wantKeys(t, panes, "2")
		})
	}

	t.Run("tabbed lands on the next tab", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1", [3]string{"2.1.310/m3-q1", "2", "2.1.310/m3-q2-after-blue"})
		s, _ := newAnswerServer(t, panes, "claude")

		st := wantTab(t, postAnswer(t, s, stepBody(m3Call, 0, answerEntry{Options: []int{1}})), 1, []int{}, "")
		if !st.Tabbed || st.Count != 3 {
			t.Errorf("state %+v", st)
		}
		wantKeys(t, panes, "2")
	})

	t.Run("preview tab lands on the review", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q3",
			[3]string{"2.1.310/m3-q3", "2", "2.1.310/m3-q3-large"},
			[3]string{"2.1.310/m3-q3-large", "Enter", "2.1.310/m3-review"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, stepBody(m3Call, 2, answerEntry{Options: []int{1}}))
		st, _ := wantState(t, rec)
		if st.Step != 3 || !reflect.DeepEqual(st.Review, m3Review) || st.Options != nil {
			t.Fatalf("state %s, want the review", rec.Body.String())
		}
		wantKeys(t, panes, "2", "Enter")
	})

	t.Run("landing never comes", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1")
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(m3Call, 0, answerEntry{Options: []int{1}})), http.StatusConflict)
		wantKeys(t, panes, "2")
	})
}

// Other is reached with Down and cleared before typing, never with the Other
// digit, which commits whatever the field still holds; the vim INSERT footer
// shows while the field has focus.
func TestRunAnswerStepOtherLands(t *testing.T) {
	t.Run("single over committed Other text", func(t *testing.T) {
		logs := captureServerLogs(t)
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1-back-other-answered",
			[3]string{"2.1.310/m3-q1-back-other-answered", "Down Down", "2.1.310/m3-q1-down-to-other-answered"},
			[3]string{"2.1.310/m3-q1-down-to-other-answered", "C-u C-u C-u", "2.1.310/m3-q1-other-cleared"},
			[3]string{"2.1.310/m3-q1-other-cleared", "text:navy", "2.1.310/m3-q1-other-navy"},
			[3]string{"2.1.310/m3-q1-other-navy", "Enter", "2.1.310/m3-q2-after-navy"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		wantTab(t, postAnswer(t, s, stepBody(m3Call, 0, answerEntry{Text: "navy"})), 1, []int{2}, "no onions")
		wantKeys(t, panes, "Down", "Down", "C-u", "C-u", "C-u", "text:navy", "Enter")
		if strings.Contains(logs.String(), "navy") {
			t.Errorf("answer text in the logs: %q", logs.String())
		}
		if !strings.Contains(logs.String(), "level=INFO msg=\"run answer\"") || !strings.Contains(logs.String(), "kind=step") {
			t.Errorf("no Info line for the step in %q", logs.String())
		}
	})

	t.Run("multi", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q2-cheese-basil",
			[3]string{"2.1.310/m3-q2-cheese-basil", "Down Down Down", "2.1.310/m3-q2-other-cursor"},
			[3]string{"2.1.310/m3-q2-other-cursor", "text:extra pepper", "2.1.310/m3-q2-other-typed"},
			[3]string{"2.1.310/m3-q2-other-typed", "Down", "2.1.310/m3-q2-other-next"},
			[3]string{"2.1.310/m3-q2-other-next", "Enter", "2.1.310/m3-q3"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		wantTab(t, postAnswer(t, s, stepBody(m3Call, 1, answerEntry{Options: []int{2, 0}, Text: "extra pepper"})), 2, []int{}, "")
		wantKeys(t, panes, "Down", "Down", "Down", "text:extra pepper", "Down", "Enter")
	})

	t.Run("plain", func(t *testing.T) {
		panes := &fakeRunPanes{}
		plainOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantNoContent(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Text: "  dark teal "})))
		wantKeys(t, panes, plainOtherKeys...)
	})
}

// Back shows the pane's committed answer, and a change after it leaves
// exactly the new selection.
func TestRunAnswerBackThenChange(t *testing.T) {
	panes := &fakeRunPanes{}
	screen(t, panes, "2.1.310/m3-q2-after-blue",
		[3]string{"2.1.310/m3-q2-after-blue", "Left", "2.1.310/m3-q1-back-left"},
		[3]string{"2.1.310/m3-q1-back-left", "1", "2.1.310/m3-q2-after-red"},
		[3]string{"2.1.310/m3-q2-after-red", "Left", "2.1.310/m3-q1-back-btab"},
	)
	s, _ := newAnswerServer(t, panes, "claude")

	wantTab(t, postAnswer(t, s, backBody(m3Call, 1)), 0, []int{1}, "")
	wantTab(t, postAnswer(t, s, stepBody(m3Call, 0, answerEntry{Options: []int{0}})), 1, []int{}, "")
	wantTab(t, postAnswer(t, s, backBody(m3Call, 1)), 0, []int{0}, "")
	wantKeys(t, panes, "Left", "1", "Left")
}

func TestRunAnswerBack(t *testing.T) {
	t.Run("from the review", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-review", [3]string{"2.1.310/m3-review", "Left", "2.1.310/m3-q3-back-from-review"})
		s, _ := newAnswerServer(t, panes, "claude")

		wantTab(t, postAnswer(t, s, backBody(m3Call, 3)), 2, []int{1}, "")
		wantKeys(t, panes, "Left")
	})

	t.Run("pane on another tab", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1")
		s, _ := newAnswerServer(t, panes, "claude")

		wantRefusal(t, postAnswer(t, s, backBody(m3Call, 1)), http.StatusConflict, "prompt changed")
		wantKeys(t, panes)
	})

	t.Run("plain dialog", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/plain-q")
		s, _ := newAnswerServer(t, panes, "claude")

		wantRefusal(t, postAnswer(t, s, backBody(plainCall, 1)), http.StatusUnprocessableEntity, "a plain dialog has no tabs")
		wantKeys(t, panes)
	})
}

func TestRunAnswerStepWrongTab(t *testing.T) {
	for name, body := range map[string]string{
		"another tab": stepBody(m3Call, 1, answerEntry{Options: []int{0}}),
		"the review":  stepBody(m3Call, 2, answerEntry{Options: []int{0}}),
	} {
		t.Run(name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			frame := "2.1.310/m3-q1"
			if name == "the review" {
				frame = "2.1.310/m3-review"
			}
			screen(t, panes, frame)
			s, _ := newAnswerServer(t, panes, "claude")

			wantRefusal(t, postAnswer(t, s, body), http.StatusConflict, "prompt changed")
			wantKeys(t, panes)
		})
	}
}

func TestRunAnswerStepPartial(t *testing.T) {
	t.Run("screen stalls after Down Down", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q1-back-other-answered",
			[3]string{"2.1.310/m3-q1-back-other-answered", "Down Down", "2.1.310/m3-q1-down-to-other-answered"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(m3Call, 0, answerEntry{Text: "navy"})), http.StatusConflict)
		wantKeys(t, panes, "Down", "Down", "C-u", "C-u", "C-u")
	})

	t.Run("pane moves on", func(t *testing.T) {
		panes := &fakeRunPanes{injectAfter: 1}
		plainOtherScreen(t, panes)
		panes.injectFrame = "perm-bash"
		panes.frames["perm-bash"] = screenFrame(t, "perm-bash")
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Text: "dark teal"})), http.StatusConflict)
		wantKeys(t, panes, "Down", "Down", "Down")
	})

	t.Run("capture fails midway", func(t *testing.T) {
		// Capture 0 reads the view and 1 is the executor's first check.
		panes := &fakeRunPanes{captureErr: errors.New("no server running"), captureErrFrom: 2}
		plainOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Text: "dark teal"})), http.StatusServiceUnavailable)
		wantKeys(t, panes, "Down", "Down", "Down")
	})
}

func TestRunAnswerSubmit(t *testing.T) {
	t.Run("submits", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-review", [3]string{"2.1.310/m3-review", "1", "plain-done"})
		s, _ := newAnswerServer(t, panes, "claude")

		wantNoContent(t, postAnswer(t, s, submitBody(m3Call, m3Review)))
		wantKeys(t, panes, "1")
	})

	t.Run("review the client saw differs", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-review", [3]string{"2.1.310/m3-review", "1", "plain-done"})
		s, _ := newAnswerServer(t, panes, "claude")

		wantRefusal(t, postAnswer(t, s, submitBody(m3Call, []string{"Blue", m3Review[1], m3Review[2]})), http.StatusConflict, "prompt changed")
		wantKeys(t, panes)
	})

	t.Run("cursor on Cancel", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/v3-review-cancel",
			[3]string{"2.1.310/v3-review-cancel", "Up", "2.1.310/v3-review-up"},
			[3]string{"2.1.310/v3-review-up", "1", "plain-done"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		wantNoContent(t, postAnswer(t, s, submitBody(tabbedCall, []string{"Red", "Olives"})))
		wantKeys(t, panes, "Up", "1")
	})

	t.Run("pane on a tab", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/m3-q3")
		s, _ := newAnswerServer(t, panes, "claude")

		wantRefusal(t, postAnswer(t, s, submitBody(m3Call, m3Review)), http.StatusConflict, "prompt changed")
		wantKeys(t, panes)
	})

	t.Run("plain dialog", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310/plain-q")
		s, _ := newAnswerServer(t, panes, "claude")

		wantRefusal(t, postAnswer(t, s, submitBody(plainCall, []string{"Red"})), http.StatusUnprocessableEntity, "a plain dialog has no tabs")
	})
}

// A session started with `claude --name`, in normal editor mode, ends its
// dialog with the input box's named top rule.
func TestRunAnswerStepNamedSession(t *testing.T) {
	t.Run("Other, then back", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310-normal/q1",
			[3]string{"2.1.310-normal/q1", "Down Down", "2.1.310-normal/q1-other-cursor"},
			[3]string{"2.1.310-normal/q1-other-cursor", "text:teal", "2.1.310-normal/q1-other-typed"},
			[3]string{"2.1.310-normal/q1-other-typed", "Enter", "2.1.310-normal/q2"},
			[3]string{"2.1.310-normal/q2", "Left", "2.1.310-normal/q1-back"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		wantTab(t, postAnswer(t, s, stepBody(tabbedCall, 0, answerEntry{Text: "teal"})), 1, []int{}, "")
		wantTab(t, postAnswer(t, s, backBody(tabbedCall, 1)), 0, []int{}, "teal")
		wantKeys(t, panes, "Down", "Down", "text:teal", "Enter", "Left")
	})

	t.Run("submit", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "2.1.310-normal/review", [3]string{"2.1.310-normal/review", "1", "plain-done"})
		s, _ := newAnswerServer(t, panes, "claude")

		wantNoContent(t, postAnswer(t, s, submitBody(tabbedCall, []string{"navy", "ham"})))
		wantKeys(t, panes, "1")
	})
}

// In the preview layout a digit only moves the cursor; Enter commits.
func TestRunAnswerStepPreview(t *testing.T) {
	split := answerEntry{Options: []int{2}}

	t.Run("submits", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "prev-q",
			[3]string{"prev-q", "3", "prev-q-split"},
			[3]string{"prev-q-split", "Enter", "plain-done"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		wantNoContent(t, postAnswer(t, s, stepBody(previewCall, 0, split)))
		wantKeys(t, panes, "3", "Enter")
	})

	t.Run("tabbed with a non-preview tab", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "prevtab-q1",
			[3]string{"prevtab-q1", "2", "prevtab-q1-large"},
			[3]string{"prevtab-q1-large", "Enter", "prevtab-q2"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		wantTab(t, postAnswer(t, s, stepBody(previewTabCall, 0, answerEntry{Options: []int{1}})), 1, []int{}, "")
		wantKeys(t, panes, "2", "Enter")
	})

	nonPreview := strings.NewReplacer(
		"Which color do you prefer?", "Which layout do you prefer?",
		"Red", "Grid", "Warm", "Cards in rows", "Blue", "List", "Cool", "One per line", "Green", "Split", "Natural", "Two panes",
	)
	for name, edit := range map[string]func(frames map[string]string){
		"label changed": func(f map[string]string) {
			f["prev-q"] = strings.Replace(f["prev-q"], "2. List ", "2. Lists", 1)
		},
		"options reordered": func(f map[string]string) {
			f["prev-q"] = strings.NewReplacer("2. List ", "2. Split", "3. Split", "3. List ").Replace(f["prev-q"])
		},
		"same question without previews": func(f map[string]string) {
			f["prev-q"] = nonPreview.Replace(screenFrame(t, "plain-q"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, "prev-q")
			edit(panes.frames)
			s, _ := newAnswerServer(t, panes, "claude")

			wantRefusal(t, postAnswer(t, s, stepBody(previewCall, 0, split)), http.StatusConflict, "prompt changed")
			wantKeys(t, panes)
		})
	}

	t.Run("cursor lands on the wrong row", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "prev-q", [3]string{"prev-q", "3", "prev-q-list"})
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(previewCall, 0, split)), http.StatusConflict)
		wantKeys(t, panes, "3")
	})

	t.Run("Other text has no row", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "prev-q")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, stepBody(previewCall, 0, answerEntry{Text: "Masonry"}))
		if rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), "Masonry") {
			t.Fatalf("got %d %q, want 422 without the text", rec.Code, rec.Body.String())
		}
		wantKeys(t, panes)
	})
}

func TestRunAnswerStepFirstFrameMismatch(t *testing.T) {
	for _, frame := range []string{"tab-q1", "plain-done", "perm-bash", "scrollback-negative", "stale-negative"} {
		t.Run(frame, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, frame)
			s, _ := newAnswerServer(t, panes, "claude")

			wantRefusal(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Options: []int{1}})), http.StatusConflict, "prompt changed")
			wantKeys(t, panes)
		})
	}

	for _, base := range []string{"2.1.299/plain-q", "2.1.299-named/plain-q"} {
		t.Run("changed option "+base, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, base)
			panes.frames[base] = strings.Replace(panes.frames[base], "     Cool", "     Very cool", 1)
			s, _ := newAnswerServer(t, panes, "claude")

			wantRefusal(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Options: []int{1}})), http.StatusConflict, "prompt changed")
			wantKeys(t, panes)
		})
	}
}

func TestRunAnswerStepNarrowPane(t *testing.T) {
	panes := &fakeRunPanes{width: 61}
	screen(t, panes, "plain-q")
	s, _ := newAnswerServer(t, panes, "claude")

	wantRefusal(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Options: []int{1}})), http.StatusConflict, "prompt changed")
	wantKeys(t, panes)
}

func TestRunAnswerCaptureFailsFirst(t *testing.T) {
	for name, body := range map[string]string{
		"step":   stepBody(plainCall, 0, answerEntry{Options: []int{1}}),
		"back":   backBody(m3Call, 1),
		"submit": submitBody(m3Call, m3Review),
	} {
		t.Run(name, func(t *testing.T) {
			panes := &fakeRunPanes{captureErr: errors.New("no server running")}
			s, _ := newAnswerServer(t, panes, "claude")

			wantRefusal(t, postAnswer(t, s, body), http.StatusServiceUnavailable, "tmux unavailable")
			wantKeys(t, panes)
		})
	}
}

func TestRunAnswerBusy(t *testing.T) {
	for name, body := range map[string]string{
		"step":   stepBody(plainCall, 0, answerEntry{Options: []int{1}}),
		"back":   backBody(m3Call, 1),
		"submit": submitBody(m3Call, m3Review),
		"choice": choiceBody(1, strings.Repeat("a", 64)),
	} {
		t.Run(name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, "plain-q", [3]string{"plain-q", "2", "plain-done"})
			s, _ := newAnswerServer(t, panes, "claude")
			mu, _ := s.answerLocks.LoadOrStore("%42", new(sync.Mutex))
			mu.(*sync.Mutex).Lock()

			rec := postAnswer(t, s, body)
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status %d, want 429 (%q)", rec.Code, rec.Body.String())
			}
			wantKeys(t, panes)
		})
	}
}

func TestRunAnswerReleasesTheLock(t *testing.T) {
	panes := &fakeRunPanes{}
	screen(t, panes, "plain-q")
	s, _ := newAnswerServer(t, panes, "claude")
	body := stepBody(plainCall, 0, answerEntry{Options: []int{1}})

	// The screen never leaves plain-q, so each answer is delivered in full.
	for range 2 {
		wantNoContent(t, postAnswer(t, s, body))
	}
	// A refused step releases it too.
	wantRefusal(t, postAnswer(t, s, backBody(plainCall, 1)), http.StatusUnprocessableEntity, "a plain dialog has no tabs")
	wantRefusal(t, postAnswer(t, s, stepBody(tabbedCall, 0, answerEntry{Options: []int{1}})), http.StatusConflict, "prompt changed")
	wantNoContent(t, postAnswer(t, s, body))
}

func TestRunAnswerSendFailure(t *testing.T) {
	t.Run("nothing delivered", func(t *testing.T) {
		panes := &fakeRunPanes{sendErr: errors.New("no server running")}
		screen(t, panes, "plain-q")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Options: []int{1}}))
		if rec.Code != http.StatusInternalServerError || !strings.HasPrefix(rec.Body.String(), "failed to send: ") {
			t.Fatalf("got %d %q, want 500 failed to send", rec.Code, rec.Body.String())
		}
	})
	t.Run("first key partly delivered", func(t *testing.T) {
		panes := &fakeRunPanes{sendErr: &tmux.DeliveryPartialError{Err: errors.New("boom")}}
		plainOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Text: "dark teal"})), http.StatusBadGateway)
	})
	t.Run("after a delivered key", func(t *testing.T) {
		panes := &fakeRunPanes{sendErr: errors.New("no server running"), sendErrFrom: 1}
		plainOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Text: "dark teal"})), http.StatusBadGateway)
		wantKeys(t, panes, "Down", "Down")
	})
}

func TestRunAnswerRejectsBadBodies(t *testing.T) {
	color := func(e answerEntry) string { return stepBody(tabbedCall, 0, e) }
	toppings := func(e answerEntry) string { return stepBody(tabbedCall, 1, e) }
	frame := strings.Repeat("a", 64)

	cases := []struct {
		name string
		body string
	}{
		{"malformed json", `{"kind":"step",`},
		{"unknown field", `{"kind":"step","toolCallId":"` + tabbedCall + `","index":0,"answer":{"options":[0]},"extra":1}`},
		{"retired answers field", `{"kind":"question","toolCallId":"` + tabbedCall + `","answers":[]}`},
		{"unknown answer field", `{"kind":"step","toolCallId":"` + tabbedCall + `","index":0,"answer":{"question":0,"options":[0]}}`},
		{"unknown kind", `{"kind":"keys"}`},
		{"retired question kind", `{"kind":"question","toolCallId":"` + tabbedCall + `"}`},
		{"missing kind", `{"ordinal":1}`},
		{"negative index", stepBody(tabbedCall, -1, answerEntry{Options: []int{0}})},
		{"index past the questions", stepBody(tabbedCall, 2, answerEntry{Options: []int{0}})},
		{"missing answer", `{"kind":"step","toolCallId":"` + tabbedCall + `","index":0}`},
		{"option out of range", color(answerEntry{Options: []int{2}})},
		{"negative option", color(answerEntry{Options: []int{-1}})},
		{"duplicate option", toppings(answerEntry{Options: []int{1, 1}})},
		{"single with two options", color(answerEntry{Options: []int{0, 1}})},
		{"single with option and text", color(answerEntry{Options: []int{0}, Text: "Teal"})},
		{"single empty", color(answerEntry{})},
		{"single blank text", color(answerEntry{Text: " \t "})},
		{"multi empty", toppings(answerEntry{})},
		{"newline in text", color(answerEntry{Text: "a\nb"})},
		{"escape in text", color(answerEntry{Text: "a\x1b[Ab"})},
		{"tab in text", color(answerEntry{Text: "a\tb"})},
		{"commit mark in text", color(answerEntry{Text: "teal ✔"})},
		{"commit mark in multi text", toppings(answerEntry{Options: []int{0}, Text: "✔"})},
		{"text too long", color(answerEntry{Text: strings.Repeat("א", maxAnswerText+1)})},
		{"invalid utf-8", `{"kind":"step","toolCallId":"` + tabbedCall + `","index":0,"answer":{"text":"a` + "\xff" + `"}}`},
		{"back from the first tab", backBody(tabbedCall, 0)},
		{"back past the review", backBody(tabbedCall, 3)},
		{"no review", `{"kind":"submit","toolCallId":"` + tabbedCall + `"}`},
		{"too few review answers", submitBody(tabbedCall, []string{"Red"})},
		{"too many review answers", submitBody(tabbedCall, []string{"Red", "Olives", "x"})},
		{"review answer too long", submitBody(tabbedCall, []string{"Red", strings.Repeat("א", maxReviewText+1)})},
		{"review answer with a control character", submitBody(tabbedCall, []string{"Red\n", "Olives"})},
		{"ordinal zero", choiceBody(0, frame)},
		{"ordinal ten", choiceBody(10, frame)},
		{"short frame", choiceBody(1, frame[1:])},
		{"uppercase frame", choiceBody(1, strings.Repeat("A", 64))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, "2.1.310/v2-q1")
			s, _ := newAnswerServer(t, panes, "claude")

			rec := postAnswer(t, s, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (%q)", rec.Code, rec.Body.String())
			}
			for _, quoted := range []string{"Teal", "teal", "Red", "Olives", "א"} {
				if strings.Contains(rec.Body.String(), quoted) {
					t.Errorf("refusal %q quotes the client's text", rec.Body.String())
				}
			}
			wantKeys(t, panes)
		})
	}
}

// Text that passes validation reaches the pane check, which here finds no
// dialog: 409 with nothing typed.
func TestRunAnswerAcceptsText(t *testing.T) {
	for name, text := range map[string]string{
		"bidi mark":            "כחול\u200f",
		"zwj emoji":            "👩\u200d💻",
		"padded":               "  Teal  ",
		"at the cap":           strings.Repeat("א", maxAnswerText),
		"leading digit":        "2 more",
		"shell-ish chars":      `$(rm -rf /); "x"`,
		"check mark lookalike": "done ✓",
	} {
		t.Run(name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			s, _ := newAnswerServer(t, panes, "claude")

			wantRefusal(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Text: text})), http.StatusConflict, "prompt changed")
		})
	}
}

func TestRunAnswerTooLarge(t *testing.T) {
	panes := &fakeRunPanes{}
	s, _ := newAnswerServer(t, panes, "claude")

	body := io.MultiReader(
		strings.NewReader(`{"kind":"step","toolCallId":"`),
		io.LimitReader(repeatByte('a'), maxAnswerBody+1),
	)
	req := httptest.NewRequest("POST", "http://"+replyHost+"/api/runs/"+termRunID+"/answer", body)
	req.Host = replyHost
	req.AddCookie(&http.Cookie{Name: authCookie, Value: replyToken})

	rec := doReply(t, s, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", rec.Code)
	}
}

func TestRunAnswerRefusals(t *testing.T) {
	cases := []struct {
		name   string
		agent  string
		callID string
		setup  func(*Server, *fakeChat)
		want   int
		body   string
	}{
		{"pi run", "pi", plainCall, nil, http.StatusUnprocessableEntity, "only a Claude run can be answered"},
		{"no chat source", "claude", plainCall, func(s *Server, _ *fakeChat) { s.chat = nil }, http.StatusNotFound, "no chat"},
		{"unknown tool call", "claude", "nope", nil, http.StatusNotFound, "no such tool call"},
		{"chat source fails", "claude", plainCall, func(_ *Server, c *fakeChat) { c.err = errors.New("disk") }, http.StatusInternalServerError, "chat unavailable"},
		{"not a question", "claude", plainCall, func(_ *Server, c *fakeChat) {
			c.tools[plainCall].Meta = map[string]any{"tool": "Bash"}
		}, http.StatusBadRequest, "tool call is not a question"},
		{"completed call", "claude", plainCall, func(_ *Server, c *fakeChat) {
			c.tools[plainCall].Status = chat.StatusCompleted
		}, http.StatusConflict, "already answered"},
		{"failed call", "claude", plainCall, func(_ *Server, c *fakeChat) {
			c.tools[plainCall].Status = chat.StatusFailed
		}, http.StatusConflict, "already answered"},
		{"unreadable input", "claude", plainCall, func(_ *Server, c *fakeChat) {
			c.tools[plainCall].RawInput = json.RawMessage(`{"questions":[]}`)
		}, http.StatusUnprocessableEntity, "cannot read the questions"},
		{"questions repeat", "claude", plainCall, func(_ *Server, c *fakeChat) {
			q := `{"question":"Which color do you prefer?","options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"},{"label":"Green","description":"Natural"}]}`
			c.tools[plainCall].RawInput = json.RawMessage(`{"questions":[` + q + `,` + q + `]}`)
		}, http.StatusUnprocessableEntity, "questions repeat"},
	}
	bodies := map[string]func(callID string) string{
		"state": stateBody,
		"step":  func(callID string) string { return stepBody(callID, 0, answerEntry{Options: []int{1}}) },
	}
	for _, tc := range cases {
		for kind, body := range bodies {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				panes := &fakeRunPanes{}
				screen(t, panes, "plain-q", [3]string{"plain-q", "2", "plain-done"})
				s, src := newAnswerServer(t, panes, tc.agent)
				if tc.setup != nil {
					tc.setup(s, src)
				}

				wantRefusal(t, postAnswer(t, s, body(tc.callID)), tc.want, tc.body)
				wantKeys(t, panes)
			})
		}
	}

	t.Run("run without a session", func(t *testing.T) {
		panes := &fakeRunPanes{}
		s := newRunTerminalServer(t, panes, termDelta())
		s.chat = newFakeChat(0)

		wantRefusal(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Options: []int{1}})), http.StatusNotFound, "no chat")
	})
}

func promptFrameOf(t *testing.T, name string) string {
	t.Helper()
	p, ok := answer.PermissionPrompt(screenFrame(t, name), 60)
	if !ok {
		t.Fatalf("%s holds no permission prompt", name)
	}
	return p.Frame
}

func TestRunAnswerChoice(t *testing.T) {
	cases := []struct {
		name    string
		ordinal int
		frame   string
		want    int
		body    string
		keys    []string
	}{
		{"presses the ordinal", 2, "perm-bash", http.StatusNoContent, "", []string{"2"}},
		{"last choice", 4, "perm-bash", http.StatusNoContent, "", []string{"4"}},
		{"stale frame", 1, "perm-bash-cmd2", http.StatusConflict, "prompt changed", []string{}},
		{"ordinal past the choices", 5, "perm-bash", http.StatusBadRequest, "no such choice", []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, "perm-bash")
			s, _ := newAnswerServer(t, panes, "claude")

			rec := postAnswer(t, s, choiceBody(tc.ordinal, promptFrameOf(t, tc.frame)))
			if rec.Code != tc.want || strings.TrimSpace(rec.Body.String()) != tc.body {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tc.want, tc.body)
			}
			if got := keyLog(panes); !reflect.DeepEqual(got, tc.keys) {
				t.Fatalf("keys %q, want %q", got, tc.keys)
			}
		})
	}

	t.Run("narrow pane", func(t *testing.T) {
		panes := &fakeRunPanes{width: 61}
		screen(t, panes, "perm-bash")
		s, _ := newAnswerServer(t, panes, "claude")

		wantRefusal(t, postAnswer(t, s, choiceBody(1, promptFrameOf(t, "perm-bash"))), http.StatusConflict, "prompt changed")
		if _, sent := panes.calls(); len(sent) != 0 {
			t.Fatalf("sent %+v with no full-width rule", sent)
		}
	})

	t.Run("no prompt on screen", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "perm-after")
		s, _ := newAnswerServer(t, panes, "claude")

		wantRefusal(t, postAnswer(t, s, choiceBody(1, promptFrameOf(t, "perm-bash"))), http.StatusConflict, "prompt changed")
		if _, sent := panes.calls(); len(sent) != 0 {
			t.Fatalf("sent %+v with no prompt on screen", sent)
		}
	})
}

func TestRunPrompt(t *testing.T) {
	get := func(s *Server) *httptest.ResponseRecorder {
		return doReply(t, s, replyRequest("GET", "/api/runs/"+termRunID+"/prompt", ""))
	}

	t.Run("permission dialog", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "perm-bash")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := get(s)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200 (%q)", rec.Code, rec.Body.String())
		}
		var got promptResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode %q: %v", rec.Body.String(), err)
		}
		if got.Question != "Do you want to proceed?" || len(got.Choices) != 4 || got.Choices[0] != "Yes" || got.Detail == "" {
			t.Errorf("prompt %+v", got)
		}
		if got.Frame != promptFrameOf(t, "perm-bash") {
			t.Errorf("frame %q, want the fixture's", got.Frame)
		}
		if _, sent := panes.calls(); len(sent) != 0 {
			t.Errorf("sent %+v on a read", sent)
		}
	})

	for _, frame := range []string{"plain-q", "perm-after", "perm-inactive"} {
		t.Run("no prompt/"+frame, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, frame)
			s, _ := newAnswerServer(t, panes, "claude")
			wantRefusal(t, get(s), http.StatusNotFound, "no prompt")
		})
	}

	t.Run("narrow pane", func(t *testing.T) {
		panes := &fakeRunPanes{width: 61}
		screen(t, panes, "perm-bash")
		s, _ := newAnswerServer(t, panes, "claude")
		wantRefusal(t, get(s), http.StatusNotFound, "no prompt")
	})

	t.Run("pi run", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "perm-bash")
		s, _ := newAnswerServer(t, panes, "pi")
		wantRefusal(t, get(s), http.StatusUnprocessableEntity, "only a Claude run can be answered")
	})

	t.Run("capture fails", func(t *testing.T) {
		panes := &fakeRunPanes{captureErr: errors.New("no server running")}
		s, _ := newAnswerServer(t, panes, "claude")
		wantRefusal(t, get(s), http.StatusServiceUnavailable, "tmux unavailable")
	})
}

// capture-pane shows the live screen under copy mode, so every check passes
// while copy mode would swallow the keys.
func TestRunAnswerCopyMode(t *testing.T) {
	bodies := map[string]func(t *testing.T) (string, string){
		"step": func(t *testing.T) (string, string) {
			return "plain-q", stepBody(plainCall, 0, answerEntry{Options: []int{1}})
		},
		"choice": func(t *testing.T) (string, string) {
			return "perm-bash", choiceBody(2, promptFrameOf(t, "perm-bash"))
		},
	}
	for name, body := range bodies {
		t.Run(name+"/in copy mode", func(t *testing.T) {
			frame, b := body(t)
			panes := &fakeRunPanes{inMode: true}
			screen(t, panes, frame)
			s, _ := newAnswerServer(t, panes, "claude")

			wantRefusal(t, postAnswer(t, s, b), http.StatusConflict, "pane is in copy mode")
			if _, sent := panes.calls(); len(sent) != 0 {
				t.Fatalf("sent %+v to a pane in copy mode", sent)
			}
		})
		t.Run(name+"/probe fails", func(t *testing.T) {
			frame, b := body(t)
			panes := &fakeRunPanes{modeErr: errors.New("no server running")}
			screen(t, panes, frame)
			s, _ := newAnswerServer(t, panes, "claude")

			wantRefusal(t, postAnswer(t, s, b), http.StatusServiceUnavailable, "tmux unavailable")
			if _, sent := panes.calls(); len(sent) != 0 {
				t.Fatalf("sent %+v without a mode probe", sent)
			}
		})
	}

	t.Run("step/enters copy mode midway", func(t *testing.T) {
		panes := &fakeRunPanes{modeAfter: 1}
		plainOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Text: "dark teal"})), http.StatusConflict)
		wantKeys(t, panes, "Down", "Down", "Down")
	})
	t.Run("step/probe fails midway", func(t *testing.T) {
		panes := &fakeRunPanes{modeErr: errors.New("no server running"), modeErrFrom: 1}
		plainOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, stepBody(plainCall, 0, answerEntry{Text: "dark teal"})), http.StatusServiceUnavailable)
		wantKeys(t, panes, "Down", "Down", "Down")
	})
	// The landing check sends no keys, so it needs no probe: a pane that
	// enters copy mode on the last key still lands.
	t.Run("step/no probe before the landing", func(t *testing.T) {
		panes := &fakeRunPanes{modeAfter: 1}
		screen(t, panes, "2.1.310/m3-q1", [3]string{"2.1.310/m3-q1", "2", "2.1.310/m3-q2-after-blue"})
		s, _ := newAnswerServer(t, panes, "claude")

		wantTab(t, postAnswer(t, s, stepBody(m3Call, 0, answerEntry{Options: []int{1}})), 1, []int{}, "")
	})
}
