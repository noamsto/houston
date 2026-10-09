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
	// answerCaptureLines reaches past a tall dialog, so its top rule is in
	// the capture.
	answerCaptureLines = 200

	answerPollInterval = 100 * time.Millisecond
	answerWaitTimeout  = 3 * time.Second
)

var promptFrame = regexp.MustCompile(`^[0-9a-f]{64}$`)

type answerRequest struct {
	Kind string `json:"kind"`

	// question
	ToolCallID string        `json:"toolCallId"`
	Answers    []answerEntry `json:"answers"`

	// choice
	Ordinal int    `json:"ordinal"`
	Frame   string `json:"frame"`
}

type answerEntry struct {
	Question int    `json:"question"`
	Options  []int  `json:"options"`
	Text     string `json:"text"`
}

type promptResponse struct {
	Question string   `json:"question"`
	Choices  []string `json:"choices"`
	Detail   string   `json:"detail"`
	Frame    string   `json:"frame"`
}

// answerAttempt is what one answer request logs: never the text, labels,
// questions or frames, which are the user's and the pane's.
type answerAttempt struct {
	id, pane, kind string
	steps          int
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

func (a answerAttempt) write(w http.ResponseWriter, out answerOutcome) {
	slog.Info("run answer", "id", a.id, "pane", a.pane, "kind", a.kind, "steps", a.steps, "status", out.code, "outcome", out.detail, "partial", out.partial)
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

func isClaudeAgent(agent string) bool { return agent == "claude" || agent == "claude-code" }

// handleRunAnswer answers the dialog open in a Claude run's pane by typing
// what a human would, checking the pane before every key batch.
//
//	POST /api/runs/{id}/answer
//	  {"kind":"question","toolCallId":"…","answers":[{"question":0,"options":[1],"text":"…"}]}
//	  {"kind":"choice","ordinal":2,"frame":"<hex>"}
//	→ 204 · 409 "prompt changed" | "pane is in copy mode" (nothing typed)
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
	case "question":
		att.kind = in.Kind
		steps, code, detail := s.questionSteps(run.Session, in)
		if steps == nil {
			att.finish(w, code, detail)
			return
		}
		att.steps = len(steps)
		unlock, ok := s.lockAnswer(pane)
		if !ok {
			att.finish(w, http.StatusTooManyRequests, "another answer is in progress")
			return
		}
		defer unlock()
		att.write(w, s.typeSteps(r.Context(), pane, steps))
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

// questionSteps reads the AskUserQuestion call from the transcript and plans
// the keys that give it the client's answers. On refusal it returns nil steps
// with the status and reason.
func (s *Server) questionSteps(session string, in answerRequest) ([]answer.Step, int, string) {
	if session == "" || s.chat == nil {
		return nil, http.StatusNotFound, "no chat"
	}
	u, err := s.chat.ChatTool(session, in.ToolCallID)
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
	as, reason := toAnswers(qs, in.Answers)
	if reason != "" {
		return nil, http.StatusBadRequest, reason
	}
	steps, err := answer.Plan(qs, as)
	if err != nil {
		return nil, http.StatusUnprocessableEntity, err.Error()
	}
	return steps, 0, ""
}

// toAnswers checks the client's entries against the transcript's questions.
// A non-empty reason refuses them; it never quotes the client's text.
func toAnswers(qs []answer.Question, entries []answerEntry) ([]answer.Answer, string) {
	if len(entries) != len(qs) {
		return nil, fmt.Sprintf("%d answers for %d questions", len(entries), len(qs))
	}
	as := make([]answer.Answer, 0, len(qs))
	for i, e := range entries {
		if e.Question != i {
			return nil, fmt.Sprintf("answer %d is not for question %d", i, i)
		}
		q := qs[i]
		for j, o := range e.Options {
			if o < 0 || o >= len(q.Options) {
				return nil, fmt.Sprintf("question %d: option out of range", i)
			}
			if slices.Contains(e.Options[:j], o) {
				return nil, fmt.Sprintf("question %d: option chosen twice", i)
			}
		}
		text := strings.TrimSpace(e.Text)
		if strings.ContainsFunc(text, unicode.IsControl) {
			return nil, fmt.Sprintf("question %d: text has a control character", i)
		}
		if utf8.RuneCountInString(text) > maxAnswerText {
			return nil, fmt.Sprintf("question %d: text longer than %d characters", i, maxAnswerText)
		}
		hasText := text != ""
		if q.MultiSelect {
			if len(e.Options) == 0 && !hasText {
				return nil, fmt.Sprintf("question %d: empty answer", i)
			}
		} else if len(e.Options) > 1 || (len(e.Options) == 1) == hasText {
			return nil, fmt.Sprintf("question %d: pick one option or type an answer", i)
		}
		as = append(as, answer.Answer{Options: e.Options, Text: text})
	}
	return as, ""
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
// step's Expect before anything more is typed.
func (s *Server) typeSteps(ctx context.Context, pane tmux.Pane, steps []answer.Step) answerOutcome {
	capture, err := s.runPanes.CapturePane(pane, answerCaptureLines)
	if err != nil {
		slog.Warn("run answer: capture failed", "pane", pane.Target(), "err", err)
		return tmuxUnavailable(false)
	}
	if !steps[0].Expect(capture) {
		return answerOutcome{code: http.StatusConflict, detail: "prompt changed"}
	}
	sent := false
	for i, step := range steps {
		if i > 0 {
			if out, ok := s.awaitStep(ctx, pane, step.Expect); !ok {
				return out
			}
		}
		if out, ok := s.paneTakesKeys(pane, sent); !ok {
			return out
		}
		for _, k := range step.Keys {
			if out, ok := s.sendAnswerKey(ctx, pane, k, sent); !ok {
				return out
			}
			sent = true
		}
	}
	return answerOutcome{code: http.StatusNoContent, detail: "delivered"}
}

// awaitStep polls the pane, after keys reached it, until expect passes,
// returning how the answer ends when it gives up.
func (s *Server) awaitStep(ctx context.Context, pane tmux.Pane, expect answer.Check) (answerOutcome, bool) {
	poll := time.NewTicker(orDefault(s.answerPoll, answerPollInterval))
	defer poll.Stop()
	deadline := time.NewTimer(orDefault(s.answerWait, answerWaitTimeout))
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return answerOutcome{code: http.StatusConflict, detail: "request cancelled", partial: true}, false
		case <-deadline.C:
			return answerOutcome{code: http.StatusConflict, detail: "pane did not reach the next step", partial: true}, false
		case <-poll.C:
		}
		capture, err := s.runPanes.CapturePane(pane, answerCaptureLines)
		if err != nil {
			slog.Warn("run answer: capture failed", "pane", pane.Target(), "err", err)
			return tmuxUnavailable(true), false
		}
		if expect(capture) {
			return answerOutcome{}, true
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
	capture, err := s.runPanes.CapturePane(pane, answerCaptureLines)
	if err != nil {
		slog.Warn("run answer: capture failed", "pane", pane.Target(), "err", err)
		return tmuxUnavailable(false)
	}
	p, ok := answer.PermissionPrompt(capture)
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
	capture, err := s.runPanes.CapturePane(pane, answerCaptureLines)
	if err != nil {
		slog.Warn("run prompt: capture failed", "id", run.ID, "pane", pane.Target(), "err", err)
		http.Error(w, "tmux unavailable", http.StatusServiceUnavailable)
		return
	}
	p, ok := answer.PermissionPrompt(capture)
	if !ok {
		slog.Debug("run prompt", "id", run.ID, "pane", pane.Target(), "status", http.StatusNotFound)
		http.Error(w, "no prompt", http.StatusNotFound)
		return
	}
	slog.Debug("run prompt", "id", run.ID, "pane", pane.Target(), "status", http.StatusOK, "choices", len(p.Choices))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(promptResponse{Question: p.Question, Choices: p.Choices, Detail: p.Detail, Frame: p.Frame})
}
