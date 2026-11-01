package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/noamsto/houston/answer"
	"github.com/noamsto/houston/chat"
	"github.com/noamsto/houston/tmux"
)

const (
	maxAnswerBody = 64 << 10
	// maxAnswerText bounds one Other answer, which is typed into the dialog's
	// single-line field.
	maxAnswerText = 500
	// maxReviewText bounds one review answer: chosen labels plus Other text.
	maxReviewText = 2000
	// answerCaptureLines reaches past a tall dialog, so its top rule is in
	// the capture.
	answerCaptureLines = 200

	answerPollInterval = 100 * time.Millisecond
	answerWaitTimeout  = 3 * time.Second
)

var promptFrame = regexp.MustCompile(`^[0-9a-f]{64}$`)

type answerRequest struct {
	Kind string `json:"kind"`

	// state, step, back, submit
	ToolCallID string       `json:"toolCallId"`
	Index      int          `json:"index"`
	Answer     *answerEntry `json:"answer"`
	Review     []string     `json:"review"`

	// choice
	Ordinal int    `json:"ordinal"`
	Frame   string `json:"frame"`
}

type answerEntry struct {
	Options []int  `json:"options"`
	Text    string `json:"text"`
}

type promptResponse struct {
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
	Detail   string   `json:"detail"`
	Frame    string   `json:"frame"`
}

// questionState is what the pane shows of an AskUserQuestion call, with the
// server's own parse of its questions, so the client's indexes always match
// the server's.
type questionState struct {
	Tabbed    bool            `json:"tabbed"`
	Step      int             `json:"step"`
	Count     int             `json:"count"`
	Questions []stateQuestion `json:"questions"`
	// Options is set on a tab, never on the review; a pointer, so a tab with
	// nothing committed still sends [].
	Options *[]int   `json:"options,omitempty"`
	Text    string   `json:"text,omitempty"`
	Review  []string `json:"review,omitempty"`
}

// stateQuestion leaves out the option previews, which can be large.
type stateQuestion struct {
	Question    string        `json:"question"`
	Header      string        `json:"header,omitempty"`
	MultiSelect bool          `json:"multiSelect"`
	Preview     bool          `json:"preview"`
	Options     []stateOption `json:"options"`
}

type stateOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

func newQuestionState(qs []answer.Question, v answer.View) questionState {
	st := questionState{Tabbed: answer.Tabbed(qs), Step: v.Step, Count: len(qs), Questions: make([]stateQuestion, len(qs))}
	for i, q := range qs {
		opts := make([]stateOption, len(q.Options))
		for k, o := range q.Options {
			opts[k] = stateOption{Label: o.Label, Description: o.Description}
		}
		st.Questions[i] = stateQuestion{Question: q.Text, Header: q.Header, MultiSelect: q.MultiSelect, Preview: q.PreviewLayout(), Options: opts}
	}
	if v.Step == len(qs) {
		st.Review = v.Review
		return st
	}
	opts := append([]int{}, v.Options...)
	st.Options, st.Text = &opts, v.Text
	return st
}

// answerAttempt is what one answer request logs: never the text, labels,
// questions or frames, which are the user's and the pane's. level is Info
// unless the request is a poll.
type answerAttempt struct {
	id, pane, kind string
	steps          int
	level          slog.Level
}

// answerOutcome is how an answer request ended. partial means some key
// reached the pane before it stopped.
type answerOutcome struct {
	code    int
	detail  string
	partial bool
}

func (a answerAttempt) finish(w http.ResponseWriter, code int, detail string) {
	a.write(w, answerOutcome{code: code, detail: detail})
}

func (a answerAttempt) log(out answerOutcome) {
	slog.Log(context.Background(), a.level, "run answer", "id", a.id, "pane", a.pane, "kind", a.kind, "steps", a.steps, "status", out.code, "outcome", out.detail, "partial", out.partial)
}

func (a answerAttempt) write(w http.ResponseWriter, out answerOutcome) {
	a.log(out)
	switch {
	case out.code == http.StatusNoContent:
		w.WriteHeader(out.code)
	case out.partial:
		// JSON, so the client can tell it from a refusal that typed nothing.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(out.code)
		_, _ = w.Write([]byte("{\"partial\":true}\n"))
	default:
		http.Error(w, out.detail, out.code)
	}
}

func (a answerAttempt) writeState(w http.ResponseWriter, qs []answer.Question, v answer.View) {
	a.log(answerOutcome{code: http.StatusOK, detail: "state"})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(newQuestionState(qs, v))
}

func isClaudeAgent(agent string) bool { return agent == "claude" || agent == "claude-code" }

// handleRunAnswer answers the dialog open in a Claude run's pane by typing
// what a human would, one question tab at a time, checking the pane before
// every key batch.
//
//	POST /api/runs/{id}/answer
//	  {"kind":"state","toolCallId":"…"} → 200 questionState · 404 "no question"
//	  {"kind":"step","toolCallId":"…","index":0,"answer":{"options":[1],"text":"…"}}
//	    → 200 questionState (tabbed) · 204 (plain, submitted)
//	  {"kind":"back","toolCallId":"…","index":1} → 200 questionState
//	  {"kind":"submit","toolCallId":"…","review":["…"]} → 204
//	  {"kind":"choice","ordinal":2,"frame":"<hex>"} → 204
//	→ 409 "prompt changed" | "pane is in copy mode" (nothing typed)
//	  · 409/502/503 {"partial":true} · 429 busy
func (s *Server) handleRunAnswer(w http.ResponseWriter, r *http.Request) {
	run, pane, ok := s.runPane(w, r)
	if !ok {
		return
	}
	att := answerAttempt{id: run.ID, pane: pane.Target()}

	r.Body = http.MaxBytesReader(w, r.Body, maxAnswerBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			att.finish(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		att.finish(w, http.StatusBadRequest, "malformed request body")
		return
	}
	// encoding/json turns invalid UTF-8 into U+FFFD, so it is refused here,
	// before decoding hides it.
	if !utf8.Valid(body) {
		att.finish(w, http.StatusBadRequest, "request body is not valid UTF-8")
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var in answerRequest
	if err := dec.Decode(&in); err != nil {
		att.finish(w, http.StatusBadRequest, "malformed request body")
		return
	}

	if !isClaudeAgent(run.Agent) {
		att.finish(w, http.StatusUnprocessableEntity, "only a Claude run can be answered")
		return
	}

	switch in.Kind {
	case "state":
		// The card polls state, so it logs below Info and takes no lock: a
		// poll never makes a step answer 429.
		att.kind, att.level = in.Kind, slog.LevelDebug
		s.answerState(w, att, run.Session, pane, in.ToolCallID)
	case "step", "back", "submit":
		att.kind = in.Kind
		s.answerTab(r.Context(), w, att, run.Session, pane, in)
	case "choice":
		att.kind = in.Kind
		if in.Ordinal < 1 || in.Ordinal > 9 {
			att.finish(w, http.StatusBadRequest, "ordinal must be 1..9")
			return
		}
		if !promptFrame.MatchString(in.Frame) {
			att.finish(w, http.StatusBadRequest, "malformed frame")
			return
		}
		att.steps = 1
		unlock, ok := s.lockAnswer(pane)
		if !ok {
			att.finish(w, http.StatusTooManyRequests, "another answer is in progress")
			return
		}
		defer unlock()
		att.write(w, s.typeChoice(r.Context(), pane, in.Ordinal, in.Frame))
	default:
		att.finish(w, http.StatusBadRequest, "unknown answer kind")
	}
}

// answerState reports what the pane shows of the call. It types nothing.
func (s *Server) answerState(w http.ResponseWriter, att answerAttempt, session string, pane tmux.Pane, toolCallID string) {
	qs, code, detail := s.questionCall(session, toolCallID)
	if qs == nil {
		att.finish(w, code, detail)
		return
	}
	capture, width, err := s.runPanes.CapturePaneWidth(pane, answerCaptureLines)
	if err != nil {
		slog.Warn("run answer: capture failed", "pane", pane.Target(), "err", err)
		att.write(w, tmuxUnavailable(false))
		return
	}
	v, ok := answer.ReadView(qs, capture, width)
	if !ok {
		att.finish(w, http.StatusNotFound, "no question")
		return
	}
	att.writeState(w, qs, v)
}

// answerTab runs one step, back or submit on the call's dialog, planned from
// the view the pane shows while the pane's answer lock is held.
func (s *Server) answerTab(ctx context.Context, w http.ResponseWriter, att answerAttempt, session string, pane tmux.Pane, in answerRequest) {
	qs, code, detail := s.questionCall(session, in.ToolCallID)
	if qs == nil {
		att.finish(w, code, detail)
		return
	}
	// tab is the tab the pane must show: len(qs) for the review.
	tab := in.Index
	var a answer.Answer
	switch in.Kind {
	case "step":
		if in.Index < 0 || in.Index >= len(qs) {
			att.finish(w, http.StatusBadRequest, "index out of range")
			return
		}
		if in.Answer == nil {
			att.finish(w, http.StatusBadRequest, "missing answer")
			return
		}
		var reason string
		if a, reason = toAnswer(qs[in.Index], *in.Answer); reason != "" {
			att.finish(w, http.StatusBadRequest, reason)
			return
		}
	case "back":
		if in.Index < 1 || in.Index > len(qs) {
			att.finish(w, http.StatusBadRequest, "index out of range")
			return
		}
	case "submit":
		if reason := checkReview(qs, in.Review); reason != "" {
			att.finish(w, http.StatusBadRequest, reason)
			return
		}
		tab = len(qs)
	}
	if in.Kind != "step" && !answer.Tabbed(qs) {
		att.finish(w, http.StatusUnprocessableEntity, "a plain dialog has no tabs")
		return
	}

	unlock, ok := s.lockAnswer(pane)
	if !ok {
		att.finish(w, http.StatusTooManyRequests, "another answer is in progress")
		return
	}
	defer unlock()
	capture, width, err := s.runPanes.CapturePaneWidth(pane, answerCaptureLines)
	if err != nil {
		slog.Warn("run answer: capture failed", "pane", pane.Target(), "err", err)
		att.write(w, tmuxUnavailable(false))
		return
	}
	v, ok := answer.ReadView(qs, capture, width)
	if !ok || v.Step != tab {
		att.finish(w, http.StatusConflict, "prompt changed")
		return
	}
	var steps []answer.Step
	switch in.Kind {
	case "step":
		steps, err = answer.PlanStep(qs, in.Index, a, v)
	case "back":
		steps, err = answer.PlanBack(qs, in.Index, v)
	default:
		steps, err = answer.PlanSubmit(qs, in.Review, v)
	}
	if err != nil {
		// Plan errors name indexes and rules, never the client's text.
		att.finish(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	att.steps = len(steps)
	out, capture, width := s.typeSteps(ctx, pane, steps)
	if out.code != http.StatusNoContent || in.Kind == "submit" || !answer.Tabbed(qs) {
		att.write(w, out)
		return
	}
	// A tabbed step or back ends on a landing check, which passes only on a
	// capture ReadView reads.
	v, _ = answer.ReadView(qs, capture, width)
	att.writeState(w, qs, v)
}

// questionCall reads the AskUserQuestion call from the transcript. On refusal
// it returns nil questions with the status and reason.
func (s *Server) questionCall(session, toolCallID string) ([]answer.Question, int, string) {
	if session == "" || s.chat == nil {
		return nil, http.StatusNotFound, "no chat"
	}
	u, err := s.chat.ChatTool(session, toolCallID)
	if errors.Is(err, chat.ErrToolNotFound) {
		return nil, http.StatusNotFound, "no such tool call"
	}
	if err != nil {
		slog.Warn("run answer: read tool call failed", "err", err)
		return nil, http.StatusInternalServerError, "chat unavailable"
	}
	if name, _ := u.Meta["tool"].(string); name != "AskUserQuestion" {
		return nil, http.StatusBadRequest, "tool call is not a question"
	}
	if u.Status == chat.StatusCompleted || u.Status == chat.StatusFailed {
		return nil, http.StatusConflict, "already answered"
	}
	qs, err := answer.ParseQuestions(u.RawInput)
	if err != nil {
		return nil, http.StatusUnprocessableEntity, "cannot read the questions"
	}
	if answer.Repeats(qs) {
		return nil, http.StatusUnprocessableEntity, "questions repeat"
	}
	return qs, 0, ""
}

// toAnswer checks the client's answer to q. A non-empty reason refuses it; it
// never quotes the client's text.
func toAnswer(q answer.Question, e answerEntry) (answer.Answer, string) {
	for j, o := range e.Options {
		if o < 0 || o >= len(q.Options) {
			return answer.Answer{}, "option out of range"
		}
		if slices.Contains(e.Options[:j], o) {
			return answer.Answer{}, "option chosen twice"
		}
	}
	text := strings.TrimSpace(e.Text)
	if strings.ContainsFunc(text, unicode.IsControl) {
		return answer.Answer{}, "text has a control character"
	}
	if utf8.RuneCountInString(text) > maxAnswerText {
		return answer.Answer{}, fmt.Sprintf("text longer than %d characters", maxAnswerText)
	}
	// The pane marks a tab's committed answer with ✔, so typed text must not
	// hold one.
	if strings.ContainsRune(text, '✔') {
		return answer.Answer{}, "text has the commit mark"
	}
	if answer.IsOtherPlaceholder(text) {
		return answer.Answer{}, "text reads as the Other placeholder"
	}
	hasText := text != ""
	if q.MultiSelect {
		if len(e.Options) == 0 && !hasText {
			return answer.Answer{}, "empty answer"
		}
	} else if len(e.Options) > 1 || (len(e.Options) == 1) == hasText {
		return answer.Answer{}, "pick one option or type an answer"
	}
	return answer.Answer{Options: e.Options, Text: text}, ""
}

// checkReview checks the review answers the client saw, one per question. A
// non-empty reason refuses them; it never quotes them.
func checkReview(qs []answer.Question, review []string) string {
	if len(review) != len(qs) {
		return fmt.Sprintf("%d review answers for %d questions", len(review), len(qs))
	}
	for i, r := range review {
		if utf8.RuneCountInString(r) > maxReviewText {
			return fmt.Sprintf("review answer %d longer than %d characters", i, maxReviewText)
		}
		if strings.ContainsFunc(r, unicode.IsControl) {
			return fmt.Sprintf("review answer %d has a control character", i)
		}
	}
	return ""
}

// lockAnswer takes the pane's answer lock without waiting.
func (s *Server) lockAnswer(pane tmux.Pane) (func(), bool) {
	v, _ := s.answerLocks.LoadOrStore(pane.Target(), new(sync.Mutex))
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}

// typeSteps runs an answer plan: the first capture must pass steps[0].Expect
// or nothing is typed, and after each batch the pane must reach the next
// step's Expect before anything more is typed. It returns the capture and
// width that passed the last Expect.
func (s *Server) typeSteps(ctx context.Context, pane tmux.Pane, steps []answer.Step) (answerOutcome, string, int) {
	capture, width, err := s.runPanes.CapturePaneWidth(pane, answerCaptureLines)
	if err != nil {
		slog.Warn("run answer: capture failed", "pane", pane.Target(), "err", err)
		return tmuxUnavailable(false), "", 0
	}
	if !steps[0].Expect(capture, width) {
		return answerOutcome{code: http.StatusConflict, detail: "prompt changed"}, "", 0
	}
	sent := false
	for i, step := range steps {
		if i > 0 {
			var out answerOutcome
			if capture, width, out = s.awaitStep(ctx, pane, step.Expect); out.code != 0 {
				return out, "", 0
			}
		}
		if len(step.Keys) == 0 {
			continue
		}
		if out, ok := s.paneTakesKeys(pane, sent); !ok {
			return out, "", 0
		}
		for _, k := range step.Keys {
			if out, ok := s.sendAnswerKey(ctx, pane, k, sent); !ok {
				return out, "", 0
			}
			sent = true
		}
	}
	return answerOutcome{code: http.StatusNoContent, detail: "delivered"}, capture, width
}

// awaitStep polls the pane, after keys reached it, until expect passes,
// returning the capture that passed it, or how the answer ends when it gives
// up.
func (s *Server) awaitStep(ctx context.Context, pane tmux.Pane, expect answer.Check) (string, int, answerOutcome) {
	poll := time.NewTicker(orDefault(s.answerPoll, answerPollInterval))
	defer poll.Stop()
	deadline := time.NewTimer(orDefault(s.answerWait, answerWaitTimeout))
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", 0, answerOutcome{code: http.StatusConflict, detail: "request cancelled", partial: true}
		case <-deadline.C:
			return "", 0, answerOutcome{code: http.StatusConflict, detail: "pane did not reach the next step", partial: true}
		case <-poll.C:
		}
		capture, width, err := s.runPanes.CapturePaneWidth(pane, answerCaptureLines)
		if err != nil {
			slog.Warn("run answer: capture failed", "pane", pane.Target(), "err", err)
			return "", 0, tmuxUnavailable(true)
		}
		if expect(capture, width) {
			return capture, width, answerOutcome{}
		}
	}
}

// paneTakesKeys checks the pane is in no tmux mode: capture-pane shows the
// live screen under copy mode, so every check passes while copy mode would
// swallow the keys. sent says whether an earlier key reached the pane.
func (s *Server) paneTakesKeys(pane tmux.Pane, sent bool) (answerOutcome, bool) {
	inMode, err := s.runPanes.PaneInMode(pane)
	if err != nil {
		slog.Warn("run answer: mode probe failed", "pane", pane.Target(), "err", err)
		return tmuxUnavailable(sent), false
	}
	if inMode {
		return answerOutcome{code: http.StatusConflict, detail: "pane is in copy mode", partial: sent}, false
	}
	return answerOutcome{}, true
}

func tmuxUnavailable(sent bool) answerOutcome {
	return answerOutcome{code: http.StatusServiceUnavailable, detail: "tmux unavailable", partial: sent}
}

// sendAnswerKey sends one key. sent says whether an earlier key reached the
// pane, which turns any failure into a partial one.
func (s *Server) sendAnswerKey(ctx context.Context, pane tmux.Pane, k answer.Key, sent bool) (answerOutcome, bool) {
	if ctx.Err() != nil {
		return answerOutcome{code: http.StatusConflict, detail: "request cancelled", partial: sent}, false
	}
	var err error
	if k.Special != "" {
		err = s.runPanes.SendSpecialKey(pane, k.Special)
	} else {
		err = s.runPanes.SendKeys(ctx, pane, k.Text, false)
	}
	if err == nil {
		return answerOutcome{}, true
	}
	var partial *tmux.DeliveryPartialError
	if sent || errors.As(err, &partial) {
		return answerOutcome{code: http.StatusBadGateway, detail: "partial send", partial: true}, false
	}
	return answerOutcome{code: http.StatusInternalServerError, detail: "failed to send: " + err.Error()}, false
}

// typeChoice presses ordinal on the permission dialog the client saw, and only
// while the pane still shows that exact dialog.
func (s *Server) typeChoice(ctx context.Context, pane tmux.Pane, ordinal int, frame string) answerOutcome {
	capture, width, err := s.runPanes.CapturePaneWidth(pane, answerCaptureLines)
	if err != nil {
		slog.Warn("run answer: capture failed", "pane", pane.Target(), "err", err)
		return tmuxUnavailable(false)
	}
	p, ok := answer.PermissionPrompt(capture, width)
	if !ok || p.Frame != frame {
		return answerOutcome{code: http.StatusConflict, detail: "prompt changed"}
	}
	if ordinal > len(p.Choices) {
		return answerOutcome{code: http.StatusBadRequest, detail: "no such choice"}
	}
	if out, ok := s.paneTakesKeys(pane, false); !ok {
		return out
	}
	if out, ok := s.sendAnswerKey(ctx, pane, answer.Key{Special: strconv.Itoa(ordinal)}, false); !ok {
		return out
	}
	return answerOutcome{code: http.StatusNoContent, detail: "delivered"}
}

// handleRunPrompt returns the permission-style dialog open in a Claude run's
// pane. The UI polls it, so only failures log above Debug.
//
//	GET /api/runs/{id}/prompt → promptResponse · 404 "no prompt"
func (s *Server) handleRunPrompt(w http.ResponseWriter, r *http.Request) {
	run, pane, ok := s.runPane(w, r)
	if !ok {
		return
	}
	if !isClaudeAgent(run.Agent) {
		terminalRefusal(w, run.ID, http.StatusUnprocessableEntity, "only a Claude run can be answered")
		return
	}
	capture, width, err := s.runPanes.CapturePaneWidth(pane, answerCaptureLines)
	if err != nil {
		slog.Warn("run prompt: capture failed", "id", run.ID, "pane", pane.Target(), "err", err)
		http.Error(w, "tmux unavailable", http.StatusServiceUnavailable)
		return
	}
	p, ok := answer.PermissionPrompt(capture, width)
	if !ok {
		slog.Debug("run prompt", "id", run.ID, "pane", pane.Target(), "status", http.StatusNotFound)
		http.Error(w, "no prompt", http.StatusNotFound)
		return
	}
	slog.Debug("run prompt", "id", run.ID, "pane", pane.Target(), "status", http.StatusOK, "choices", len(p.Choices))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(promptResponse{Question: p.Question, Choices: p.Choices, Detail: p.Detail, Frame: p.Frame})
}
