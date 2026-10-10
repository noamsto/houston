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
)

// The questions behind answer/testdata's plain-* and tab-* frames.
const (
	plainInput = `{"questions":[{"question":"Which color do you prefer?","header":"Color","multiSelect":false,
		"options":[{"label":"Red","description":"Warm"},{"label":"Blue","description":"Cool"},{"label":"Green","description":"Natural"}]}]}`
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
)

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

// tabbedOtherScreen walks the tabbed dialog through tabbedOtherAnswer.
func tabbedOtherScreen(t *testing.T, panes *fakeRunPanes) {
	screen(t, panes, "tab-q1",
		[3]string{"tab-q1", "3", "tab-q1-other-cursor"},
		[3]string{"tab-q1-other-cursor", `text:Teal, "deep"; 1`, "tab-q1-other-typed"},
		[3]string{"tab-q1-other-typed", "Enter", "tab-q2"},
		[3]string{"tab-q2", "2", "tab-q2-olives"},
		[3]string{"tab-q2-olives", "Down Down Down", "tab-q2-other-cursor"},
		[3]string{"tab-q2-other-cursor", "text:Pineapple 2", "tab-q2-other-typed"},
		[3]string{"tab-q2-other-typed", "Down", "tab-q2-submit"},
		[3]string{"tab-q2-submit", "Enter", "tab-review-other"},
		[3]string{"tab-review-other", "1", "plain-done"},
	)
}

var tabbedOtherAnswer = []answerEntry{
	{Question: 0, Text: `Teal, "deep"; 1`},
	{Question: 1, Options: []int{1}, Text: "Pineapple 2"},
}

func questionBody(t *testing.T, callID string, answers []answerEntry) string {
	t.Helper()
	return inputBody(t, map[string]any{"kind": "question", "toolCallId": callID, "answers": answers})
}

func choiceBody(ordinal int, frame string) string {
	b, _ := json.Marshal(map[string]any{"kind": "choice", "ordinal": ordinal, "frame": frame})
	return string(b)
}

func postAnswer(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doReply(t, s, replyRequest("POST", "/api/runs/"+termRunID+"/answer", body))
}

// keyLog renders the keys sent as answer.Plan's tests spell them.
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

func TestRunAnswerQuestionPlainOption(t *testing.T) {
	panes := &fakeRunPanes{}
	screen(t, panes, "plain-q", [3]string{"plain-q", "2", "plain-done"})
	s, _ := newAnswerServer(t, panes, "claude")

	rec := postAnswer(t, s, questionBody(t, plainCall, []answerEntry{{Question: 0, Options: []int{1}}}))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
	}
	if got, want := keyLog(panes), []string{"2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %q, want %q", got, want)
	}
}

// In the preview layout a digit only moves the cursor; Enter submits.
func TestRunAnswerQuestionPreview(t *testing.T) {
	answerSplit := []answerEntry{{Question: 0, Options: []int{2}}}

	t.Run("submits", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "prev-q",
			[3]string{"prev-q", "3", "prev-q-split"},
			[3]string{"prev-q-split", "Enter", "plain-done"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, questionBody(t, previewCall, answerSplit))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
		}
		if got, want := keyLog(panes), []string{"3", "Enter"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("keys %q, want %q", got, want)
		}
	})

	t.Run("tabbed with a non-preview tab", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "prevtab-q1",
			[3]string{"prevtab-q1", "2", "prevtab-q1-large"},
			[3]string{"prevtab-q1-large", "Enter", "prevtab-q2"},
			[3]string{"prevtab-q2", "1", "prevtab-review"},
			[3]string{"prevtab-review", "1", "plain-done"},
		)
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, questionBody(t, previewTabCall, []answerEntry{{Question: 0, Options: []int{1}}, {Question: 1, Options: []int{0}}}))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
		}
		if got, want := keyLog(panes), []string{"2", "Enter", "1", "1"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("keys %q, want %q", got, want)
		}
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

			wantRefusal(t, postAnswer(t, s, questionBody(t, previewCall, answerSplit)), http.StatusConflict, "prompt changed")
			if _, sent := panes.calls(); len(sent) != 0 {
				t.Fatalf("sent %+v on a changed dialog", sent)
			}
		})
	}

	t.Run("cursor lands on the wrong row", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "prev-q", [3]string{"prev-q", "3", "prev-q-list"})
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, questionBody(t, previewCall, answerSplit)), http.StatusConflict)
		if got, want := keyLog(panes), []string{"3"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("keys %q, want %q", got, want)
		}
	})

	t.Run("Other text has no row", func(t *testing.T) {
		panes := &fakeRunPanes{}
		screen(t, panes, "prev-q")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, questionBody(t, previewCall, []answerEntry{{Question: 0, Text: "Masonry"}}))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status %d, want 422 (%q)", rec.Code, rec.Body.String())
		}
		if _, sent := panes.calls(); len(sent) != 0 {
			t.Fatalf("sent %+v for an unplannable answer", sent)
		}
	})
}

func TestRunAnswerQuestionTabbedWithOther(t *testing.T) {
	panes := &fakeRunPanes{}
	tabbedOtherScreen(t, panes)
	s, _ := newAnswerServer(t, panes, "claude-code")

	rec := postAnswer(t, s, questionBody(t, tabbedCall, tabbedOtherAnswer))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
	}
	want := []string{
		"3", `text:Teal, "deep"; 1`, "Enter",
		"2", "Down", "Down", "Down", "text:Pineapple 2", "Down", "Enter",
		"1",
	}
	if got := keyLog(panes); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %q, want %q", got, want)
	}
	if panes.frame != "plain-done" {
		t.Errorf("screen ended on %s, want the submitted dialog gone", panes.frame)
	}
}

func TestRunAnswerQuestionStopsWhenThePaneMovesOn(t *testing.T) {
	panes := &fakeRunPanes{injectAfter: 1}
	tabbedOtherScreen(t, panes)
	panes.injectFrame = "perm-bash"
	panes.frames["perm-bash"] = screenFrame(t, "perm-bash")
	s, _ := newAnswerServer(t, panes, "claude")

	rec := postAnswer(t, s, questionBody(t, tabbedCall, tabbedOtherAnswer))
	wantPartial(t, rec, http.StatusConflict)
	if got, want := keyLog(panes), []string{"3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %q, want %q", got, want)
	}
}

func TestRunAnswerQuestionCaptureFailsMidway(t *testing.T) {
	panes := &fakeRunPanes{captureErr: errors.New("no server running"), captureErrFrom: 1}
	tabbedOtherScreen(t, panes)
	s, _ := newAnswerServer(t, panes, "claude")

	rec := postAnswer(t, s, questionBody(t, tabbedCall, tabbedOtherAnswer))
	wantPartial(t, rec, http.StatusServiceUnavailable)
	if got, want := keyLog(panes), []string{"3"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %q, want %q", got, want)
	}
}

func TestRunAnswerQuestionFirstFrameMismatch(t *testing.T) {
	for _, frame := range []string{"tab-q1", "plain-done", "perm-bash", "scrollback-negative"} {
		t.Run(frame, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, frame)
			s, _ := newAnswerServer(t, panes, "claude")

			rec := postAnswer(t, s, questionBody(t, plainCall, []answerEntry{{Question: 0, Options: []int{1}}}))
			wantRefusal(t, rec, http.StatusConflict, "prompt changed")
			if _, sent := panes.calls(); len(sent) != 0 {
				t.Fatalf("sent %+v on a mismatched first frame", sent)
			}
		})
	}
}

func TestRunAnswerQuestionNarrowPane(t *testing.T) {
	panes := &fakeRunPanes{width: 61}
	screen(t, panes, "plain-q")
	s, _ := newAnswerServer(t, panes, "claude")

	rec := postAnswer(t, s, questionBody(t, plainCall, []answerEntry{{Question: 0, Options: []int{1}}}))
	wantRefusal(t, rec, http.StatusConflict, "prompt changed")
	if _, sent := panes.calls(); len(sent) != 0 {
		t.Fatalf("sent %+v with no full-width rule", sent)
	}
}

func TestRunAnswerCaptureFailsFirst(t *testing.T) {
	panes := &fakeRunPanes{captureErr: errors.New("no server running")}
	s, _ := newAnswerServer(t, panes, "claude")

	rec := postAnswer(t, s, questionBody(t, plainCall, []answerEntry{{Question: 0, Options: []int{1}}}))
	wantRefusal(t, rec, http.StatusServiceUnavailable, "tmux unavailable")
	if _, sent := panes.calls(); len(sent) != 0 {
		t.Fatalf("sent %+v without a capture", sent)
	}
}

func TestRunAnswerBusy(t *testing.T) {
	for _, body := range []string{
		`{"kind":"question","toolCallId":"` + plainCall + `","answers":[{"question":0,"options":[1]}]}`,
		choiceBody(1, strings.Repeat("a", 64)),
	} {
		panes := &fakeRunPanes{}
		screen(t, panes, "plain-q", [3]string{"plain-q", "2", "plain-done"})
		s, _ := newAnswerServer(t, panes, "claude")
		mu, _ := s.answerLocks.LoadOrStore("%42", new(sync.Mutex))
		mu.(*sync.Mutex).Lock()

		rec := postAnswer(t, s, body)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status %d, want 429 (%q)", rec.Code, rec.Body.String())
		}
		if _, sent := panes.calls(); len(sent) != 0 {
			t.Fatalf("sent %+v while another answer held the pane", sent)
		}
	}
}

func TestRunAnswerReleasesTheLock(t *testing.T) {
	panes := &fakeRunPanes{}
	screen(t, panes, "plain-q")
	s, _ := newAnswerServer(t, panes, "claude")
	body := questionBody(t, plainCall, []answerEntry{{Question: 0, Options: []int{1}}})

	// The screen never leaves plain-q, so each answer is delivered in full.
	for range 2 {
		if rec := postAnswer(t, s, body); rec.Code != http.StatusNoContent {
			t.Fatalf("status %d, want 204 (%q)", rec.Code, rec.Body.String())
		}
	}
}

func TestRunAnswerSendFailure(t *testing.T) {
	t.Run("nothing delivered", func(t *testing.T) {
		panes := &fakeRunPanes{sendErr: errors.New("no server running")}
		screen(t, panes, "plain-q")
		s, _ := newAnswerServer(t, panes, "claude")

		rec := postAnswer(t, s, questionBody(t, plainCall, []answerEntry{{Question: 0, Options: []int{1}}}))
		if rec.Code != http.StatusInternalServerError || !strings.HasPrefix(rec.Body.String(), "failed to send: ") {
			t.Fatalf("got %d %q, want 500 failed to send", rec.Code, rec.Body.String())
		}
	})
	t.Run("first key partly delivered", func(t *testing.T) {
		panes := &fakeRunPanes{sendErr: &tmux.DeliveryPartialError{Err: errors.New("boom")}}
		tabbedOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, questionBody(t, tabbedCall, tabbedOtherAnswer)), http.StatusBadGateway)
	})
	t.Run("after a delivered key", func(t *testing.T) {
		panes := &fakeRunPanes{sendErr: errors.New("no server running"), sendErrFrom: 1}
		tabbedOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, questionBody(t, tabbedCall, tabbedOtherAnswer)), http.StatusBadGateway)
		if got, want := keyLog(panes), []string{"3", `text:Teal, "deep"; 1`}; !reflect.DeepEqual(got, want) {
			t.Fatalf("keys %q, want %q", got, want)
		}
	})
}

func TestRunAnswerRejectsBadBodies(t *testing.T) {
	q := func(answers ...answerEntry) string { return questionBody(t, tabbedCall, answers) }
	color := func(e answerEntry) answerEntry { e.Question = 0; return e }
	toppings := answerEntry{Question: 1, Options: []int{0}}
	frame := strings.Repeat("a", 64)

	cases := []struct {
		name string
		body string
	}{
		{"malformed json", `{"kind":"question",`},
		{"unknown field", `{"kind":"question","toolCallId":"` + tabbedCall + `","answers":[],"extra":1}`},
		{"unknown kind", `{"kind":"keys"}`},
		{"missing kind", `{"ordinal":1}`},
		{"no answers", q()},
		{"too few answers", q(color(answerEntry{Options: []int{0}}))},
		{"too many answers", q(color(answerEntry{Options: []int{0}}), toppings, answerEntry{Question: 2, Options: []int{0}})},
		{"wrong order", q(answerEntry{Question: 1, Options: []int{0}}, answerEntry{Question: 0, Options: []int{0}})},
		{"option out of range", q(color(answerEntry{Options: []int{2}}), toppings)},
		{"negative option", q(color(answerEntry{Options: []int{-1}}), toppings)},
		{"duplicate option", q(color(answerEntry{Options: []int{0}}), answerEntry{Question: 1, Options: []int{1, 1}})},
		{"single with two options", q(color(answerEntry{Options: []int{0, 1}}), toppings)},
		{"single with option and text", q(color(answerEntry{Options: []int{0}, Text: "Teal"}), toppings)},
		{"single empty", q(color(answerEntry{}), toppings)},
		{"single blank text", q(color(answerEntry{Text: " \t "}), toppings)},
		{"multi empty", q(color(answerEntry{Options: []int{0}}), answerEntry{Question: 1})},
		{"newline in text", q(color(answerEntry{Text: "a\nb"}), toppings)},
		{"escape in text", q(color(answerEntry{Text: "a\x1b[Ab"}), toppings)},
		{"tab in text", q(color(answerEntry{Text: "a\tb"}), toppings)},
		{"text too long", q(color(answerEntry{Text: strings.Repeat("א", maxAnswerText+1)}), toppings)},
		{"invalid utf-8", `{"kind":"question","toolCallId":"` + tabbedCall + `","answers":[{"question":0,"text":"a` + "\xff" + `"},{"question":1,"options":[0]}]}`},
		{"ordinal zero", choiceBody(0, frame)},
		{"ordinal ten", choiceBody(10, frame)},
		{"short frame", choiceBody(1, frame[1:])},
		{"uppercase frame", choiceBody(1, strings.Repeat("A", 64))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			tabbedOtherScreen(t, panes)
			s, _ := newAnswerServer(t, panes, "claude")

			rec := postAnswer(t, s, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (%q)", rec.Code, rec.Body.String())
			}
			if _, sent := panes.calls(); len(sent) != 0 {
				t.Fatalf("sent %+v for a rejected body", sent)
			}
		})
	}
}

// Text that passes validation reaches the executor, which here finds no
// dialog: 409 with nothing typed.
func TestRunAnswerAcceptsText(t *testing.T) {
	for name, text := range map[string]string{
		"bidi mark":       "כחול\u200f",
		"zwj emoji":       "👩\u200d💻",
		"padded":          "  Teal  ",
		"at the cap":      strings.Repeat("א", maxAnswerText),
		"leading digit":   "2 more",
		"shell-ish chars": `$(rm -rf /); "x"`,
	} {
		t.Run(name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			s, _ := newAnswerServer(t, panes, "claude")

			rec := postAnswer(t, s, questionBody(t, plainCall, []answerEntry{{Question: 0, Text: text}}))
			wantRefusal(t, rec, http.StatusConflict, "prompt changed")
		})
	}
}

func TestRunAnswerTooLarge(t *testing.T) {
	panes := &fakeRunPanes{}
	s, _ := newAnswerServer(t, panes, "claude")

	body := io.MultiReader(
		strings.NewReader(`{"kind":"question","toolCallId":"`),
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

func TestRunAnswerQuestionRefusals(t *testing.T) {
	plainAnswer := []answerEntry{{Question: 0, Options: []int{1}}}
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
		{"nine options", "claude", plainCall, func(_ *Server, c *fakeChat) {
			c.tools[plainCall].RawInput = json.RawMessage(`{"questions":[{"question":"Pick?","options":[
				{"label":"a"},{"label":"b"},{"label":"c"},{"label":"d"},{"label":"e"},
				{"label":"f"},{"label":"g"},{"label":"h"},{"label":"i"}]}]}`)
		}, http.StatusUnprocessableEntity, "question 0: 9 options, want 1..8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			panes := &fakeRunPanes{}
			screen(t, panes, "plain-q", [3]string{"plain-q", "2", "plain-done"})
			s, src := newAnswerServer(t, panes, tc.agent)
			if tc.setup != nil {
				tc.setup(s, src)
			}

			wantRefusal(t, postAnswer(t, s, questionBody(t, tc.callID, plainAnswer)), tc.want, tc.body)
			if _, sent := panes.calls(); len(sent) != 0 {
				t.Fatalf("sent %+v on a refused answer", sent)
			}
		})
	}

	t.Run("run without a session", func(t *testing.T) {
		panes := &fakeRunPanes{}
		s := newRunTerminalServer(t, panes, termDelta())
		s.chat = newFakeChat(0)

		wantRefusal(t, postAnswer(t, s, questionBody(t, plainCall, plainAnswer)), http.StatusNotFound, "no chat")
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
		"question": func(t *testing.T) (string, string) {
			return "plain-q", questionBody(t, plainCall, []answerEntry{{Question: 0, Options: []int{1}}})
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

	t.Run("question/enters copy mode midway", func(t *testing.T) {
		panes := &fakeRunPanes{modeAfter: 1}
		tabbedOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, questionBody(t, tabbedCall, tabbedOtherAnswer)), http.StatusConflict)
		if got, want := keyLog(panes), []string{"3"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("keys %q, want %q", got, want)
		}
	})
	t.Run("question/probe fails midway", func(t *testing.T) {
		panes := &fakeRunPanes{modeErr: errors.New("no server running"), modeErrFrom: 1}
		tabbedOtherScreen(t, panes)
		s, _ := newAnswerServer(t, panes, "claude")

		wantPartial(t, postAnswer(t, s, questionBody(t, tabbedCall, tabbedOtherAnswer)), http.StatusServiceUnavailable)
		if got, want := keyLog(panes), []string{"3"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("keys %q, want %q", got, want)
		}
	})
}
