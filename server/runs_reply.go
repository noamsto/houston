package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/noamsto/houston/runs"
)

const (
	// maxReplyText bounds the answer itself. crew elides a bus line past ~4KiB,
	// so anything materially larger is not going to survive delivery anyway.
	maxReplyText = 8 << 10
	// maxReplyBody bounds the JSON envelope, leaving room for escaping a
	// maxReplyText answer made entirely of escaped characters.
	maxReplyBody = 64 << 10

	replyTimeout = 15 * time.Second
)

// replyExec is one crew reply, fully resolved by the handler. The runner adds
// nothing and decides nothing.
type replyExec struct {
	Dir    string   // Run.Worktree — the working directory; never "" by the time a runner sees it
	CrewID string   // Run.Crew.Name — passed as CREW_ID=<CrewID> appended to os.Environ()
	Argv   []string // exactly {"crew", "reply", "worker:" + Run.Branch, text}; no shell, no "--"
}

type replyResult struct {
	Stderr   string
	ExitCode int   // 0 delivered; non-zero means crew refused → 409 with Stderr
	Err      error // the command could not run or finish: exec.ErrNotFound → 502, context.DeadlineExceeded → 504
}

type replyRunner func(ctx context.Context, x replyExec) replyResult

// execCrewReply is the real runner. The argv slice reaches execve untouched:
// no shell, and no "--" — crew reply takes its two operands positionally and
// does no option parsing, so a leading-dash answer is already literal and a
// "--" would be consumed as the recipient.
func execCrewReply(ctx context.Context, x replyExec) replyResult {
	cmd := exec.CommandContext(ctx, x.Argv[0], x.Argv[1:]...)
	cmd.Dir = x.Dir
	cmd.Env = append(os.Environ(), "CREW_ID="+x.CrewID)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := replyResult{Stderr: strings.TrimSpace(stderr.String())}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		// A timeout kills the child, so Run reports "signal: killed" — an
		// ExitError. Checking the context first is what keeps 504 from being
		// misread as crew's own refusal.
		res.Err = context.DeadlineExceeded
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		res.Err = err
	}
	return res
}

// handleRunReply delivers a crew answer to a blocked worker.
//
//	POST /api/runs/{id}/reply  {"text": "<answer>"} → 204
//
// This is the only route in houston that turns client input into a process
// argument, so the target is resolved entirely from the registry: a client can
// name a run, never a command, a branch, or a directory.
func (s *Server) handleRunReply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.runs == nil {
		http.Error(w, "run registry not started", http.StatusServiceUnavailable)
		return
	}

	run, ok := s.findRun(id)
	if !ok {
		replyOutcome(w, id, "", http.StatusNotFound, "no such run")
		return
	}

	switch {
	case run.Crew == nil || run.Crew.Name == "":
		replyOutcome(w, id, run.Branch, http.StatusBadRequest, "run has no crew")
		return
	case run.Branch == "":
		replyOutcome(w, id, run.Branch, http.StatusBadRequest, "run has no branch")
		return
	case run.Worktree == "":
		replyOutcome(w, id, run.Branch, http.StatusBadRequest, "run has no worktree")
		return
	case run.Question == nil:
		replyOutcome(w, id, run.Branch, http.StatusBadRequest, "run is not asking anything")
		return
	case run.Question.Via != "crew":
		// Answering a pane question over the bus posts to something nobody is
		// reading while the agent sits at its prompt — a false success.
		replyOutcome(w, id, run.Branch, http.StatusBadRequest, "question is not answered over the crew bus")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxReplyBody)
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			replyOutcome(w, id, run.Branch, http.StatusRequestEntityTooLarge, "answer too large")
			return
		}
		replyOutcome(w, id, run.Branch, http.StatusBadRequest, "malformed request body")
		return
	}
	if len(body.Text) > maxReplyText {
		replyOutcome(w, id, run.Branch, http.StatusRequestEntityTooLarge, "answer too large")
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		replyOutcome(w, id, run.Branch, http.StatusBadRequest, "empty answer")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), replyTimeout)
	defer cancel()

	res := s.replyRunner(ctx, replyExec{
		Dir:    run.Worktree,
		CrewID: run.Crew.Name,
		Argv:   []string{"crew", "reply", "worker:" + run.Branch, body.Text},
	})

	switch {
	case errors.Is(res.Err, context.DeadlineExceeded):
		replyOutcome(w, id, run.Branch, http.StatusGatewayTimeout, "crew reply timed out")
	case res.Err != nil:
		replyOutcome(w, id, run.Branch, http.StatusBadGateway, "could not run crew reply: "+res.Err.Error())
	case res.ExitCode != 0:
		// crew's own words, verbatim: 409 is the worker's refusal, not ours.
		reason := res.Stderr
		if reason == "" {
			reason = "crew reply refused"
		}
		replyOutcome(w, id, run.Branch, http.StatusConflict, reason)
	default:
		replyOutcome(w, id, run.Branch, http.StatusNoContent, "delivered")
	}
}

// findRun resolves a run by its externally visible ID. No registry method
// exists for this and none is needed — the snapshot is already the answer.
func (s *Server) findRun(id string) (runs.Run, bool) {
	for _, run := range s.runs.Snapshot() {
		if run.ID == id {
			return run, true
		}
	}
	return runs.Run{}, false
}

// replyOutcome logs the attempt and writes its result. 204 is the one case
// with no body, which http.Error cannot express.
func replyOutcome(w http.ResponseWriter, id, branch string, code int, detail string) {
	slog.Info("run reply", "id", id, "branch", branch, "status", code, "outcome", detail)
	if code == http.StatusNoContent {
		w.WriteHeader(code)
		return
	}
	http.Error(w, detail, code)
}
