package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/noamsto/houston/chat"
	"github.com/noamsto/houston/hub"
	"github.com/noamsto/houston/runs"
)

const (
	chatPingInterval  = 25 * time.Second
	chatCheckInterval = 2 * time.Second

	// maxChatToolText caps each of a tool detail's output, oldText and
	// newText, so one huge Read or Write result can't balloon the response.
	// An input over it (a Write's carries the whole file) is omitted rather
	// than cut, since a cut would no longer be JSON.
	maxChatToolText = 16 << 10

	// chatStartPage is how far back a cursorless stream starts when the
	// ring no longer holds seq 1: the newest page the hub will serve.
	chatStartPage = 100
)

// chatSource is what the chat routes need from the hub.
type chatSource interface {
	ChatPage(sessionID string, before uint64, limit int) (hub.ChatPage, error)
	ChatSince(sessionID, epoch string, after uint64) ([]chat.Update, bool, error)
	ChatEpoch(sessionID string) (string, error)
	ChatSubscribe(sessionID string) (<-chan struct{}, func(), error)
	ChatTool(sessionID, toolCallID string) (*chat.Update, error)
}

// chatRun resolves a run to its chat session, writing the refusal itself
// when there is none.
func (s *Server) chatRun(w http.ResponseWriter, r *http.Request) (runs.Run, bool) {
	id := r.PathValue("id")
	if s.runs == nil {
		chatRefusal(w, id, http.StatusServiceUnavailable, "run registry not started")
		return runs.Run{}, false
	}
	run, ok := s.findRun(id)
	if !ok {
		chatRefusal(w, id, http.StatusNotFound, "no such run")
		return runs.Run{}, false
	}
	if run.Session == "" || s.chat == nil {
		chatRefusal(w, id, http.StatusNotFound, "no chat")
		return runs.Run{}, false
	}
	return run, true
}

func chatRefusal(w http.ResponseWriter, id string, code int, detail string) {
	slog.Info("run chat refused", "id", id, "status", code, "outcome", detail)
	http.Error(w, detail, code)
}

// chatFailed writes the response for an error from the chat source.
func chatFailed(w http.ResponseWriter, id string, err error) {
	if errors.Is(err, hub.ErrNoChat) {
		chatRefusal(w, id, http.StatusNotFound, "no chat")
		return
	}
	slog.Warn("run chat failed", "id", id, "err", err)
	http.Error(w, "chat unavailable", http.StatusInternalServerError)
}

// handleRunChat returns one page of a run's chat, oldest first.
//
//	GET /api/runs/{id}/chat?before=<seq>&limit=<n> → hub.ChatPage
func (s *Server) handleRunChat(w http.ResponseWriter, r *http.Request) {
	run, ok := s.chatRun(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var before uint64
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			chatRefusal(w, run.ID, http.StatusBadRequest, "bad before")
			return
		}
		before = n
	}
	var limit int
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			chatRefusal(w, run.ID, http.StatusBadRequest, "bad limit")
			return
		}
		limit = n
	}

	page, err := s.chat.ChatPage(run.Session, before, limit)
	if err != nil {
		chatFailed(w, run.ID, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(page)
}

// parseChatCursor splits "<epoch>.<seq>" on its last dot.
func parseChatCursor(c string) (string, uint64, bool) {
	i := strings.LastIndexByte(c, '.')
	if i <= 0 {
		return "", 0, false
	}
	seq, err := strconv.ParseUint(c[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return c[:i], seq, true
}

// chatStart is where a cursorless stream begins: the whole ring when it
// still starts at seq 1, else the newest page, so the client never gets a
// batch it can't place after an older one it lacks.
func (s *Server) chatStart(sessionID string) (string, uint64, error) {
	epoch, err := s.chat.ChatEpoch(sessionID)
	if err != nil {
		return "", 0, err
	}
	if _, ok, err := s.chat.ChatSince(sessionID, epoch, 0); err != nil || ok {
		return epoch, 0, err
	}
	page, err := s.chat.ChatPage(sessionID, 0, chatStartPage)
	if err != nil || len(page.Updates) == 0 {
		return page.Epoch, 0, err
	}
	return page.Epoch, page.Updates[0].Seq - 1, nil
}

// handleRunChatStream emits a run's chat updates over SSE, resuming after
// the cursor. Each batch's id is the cursor to resume after it; "reset"
// means the cursor can no longer be served and ends the stream.
//
//	GET /api/runs/{id}/chat/stream?after=<epoch>.<seq> → text/event-stream
func (s *Server) handleRunChatStream(w http.ResponseWriter, r *http.Request) {
	run, ok := s.chatRun(w, r)
	if !ok {
		return
	}
	cursor := r.Header.Get("Last-Event-ID")
	if cursor == "" {
		cursor = r.URL.Query().Get("after")
	}
	var epoch string
	var after uint64
	if cursor != "" {
		if epoch, after, ok = parseChatCursor(cursor); !ok {
			chatRefusal(w, run.ID, http.StatusBadRequest, "malformed cursor")
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe before the first read, so an append in between signals the
	// channel instead of being missed.
	notify, unsubscribe, err := s.chat.ChatSubscribe(run.Session)
	if err != nil {
		chatFailed(w, run.ID, err)
		return
	}
	defer unsubscribe()
	if cursor == "" {
		if epoch, after, err = s.chatStart(run.Session); err != nil {
			chatFailed(w, run.ID, err)
			return
		}
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")

	reset := func() {
		fmt.Fprint(w, "event: reset\ndata: {}\n\n")
		flusher.Flush()
	}
	// pull sends everything after the cursor; false ends the stream.
	pull := func() bool {
		ups, ok, err := s.chat.ChatSince(run.Session, epoch, after)
		if err != nil && !errors.Is(err, hub.ErrNoChat) {
			slog.Warn("run chat stream", "id", run.ID, "err", err)
		}
		if err != nil || !ok {
			reset()
			return false
		}
		if len(ups) == 0 {
			return true
		}
		b, err := json.Marshal(ups)
		if err != nil {
			slog.Warn("run chat stream marshal", "id", run.ID, "err", err)
			return false
		}
		last := ups[len(ups)-1].Seq
		if _, err := fmt.Fprintf(w, "id: %s.%d\nevent: updates\ndata: %s\n\n", epoch, last, b); err != nil {
			return false
		}
		flusher.Flush()
		after = last
		return true
	}

	if !pull() {
		return
	}
	// Commit the headers even when there was nothing to send yet.
	flusher.Flush()

	ping := time.NewTicker(orDefault(s.chatPing, chatPingInterval))
	defer ping.Stop()
	check := time.NewTicker(orDefault(s.chatCheck, chatCheckInterval))
	defer check.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-notify:
			if !ok {
				reset()
				return
			}
			if !pull() {
				return
			}
		case <-check.C:
			// A new session on the run, or a restart the hub never
			// signalled, both invalidate the cursor.
			cur, ok := s.findRun(run.ID)
			if !ok || cur.Session != run.Session {
				reset()
				return
			}
			if e, err := s.chat.ChatEpoch(run.Session); err != nil || e != epoch {
				reset()
				return
			}
		case <-ping.C:
			if _, err := fmt.Fprint(w, "event: ping\ndata: \n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func orDefault(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

type chatToolDetail struct {
	ToolCallID   string          `json:"toolCallId"`
	Name         string          `json:"name"`
	Title        string          `json:"title"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	Input        json.RawMessage `json:"input,omitempty"`
	Output       string          `json:"output"`
	Truncated    bool            `json:"truncated"`
	InputOmitted bool            `json:"inputOmitted,omitempty"`
	Diff         *chatToolDiff   `json:"diff,omitempty"`
}

type chatToolDiff struct {
	Path    string `json:"path"`
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// handleRunChatTool returns one tool call's input and result.
//
//	GET /api/runs/{id}/chat/tool/{callId} → chatToolDetail
func (s *Server) handleRunChatTool(w http.ResponseWriter, r *http.Request) {
	run, ok := s.chatRun(w, r)
	if !ok {
		return
	}
	u, err := s.chat.ChatTool(run.Session, r.PathValue("callId"))
	if errors.Is(err, chat.ErrToolNotFound) {
		chatRefusal(w, run.ID, http.StatusNotFound, "no such tool call")
		return
	}
	if err != nil {
		chatFailed(w, run.ID, err)
		return
	}

	d := chatToolDetail{
		ToolCallID: u.ToolCallID,
		Title:      u.Title,
		Kind:       u.Kind,
		Status:     u.Status,
	}
	if len(u.RawInput) > maxChatToolText {
		d.InputOmitted = true
	} else {
		d.Input = u.RawInput
	}
	d.Name, _ = u.Meta["tool"].(string)
	capText := func(s string) string {
		if len(s) <= maxChatToolText {
			return s
		}
		d.Truncated = true
		cut := maxChatToolText
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut]
	}
	var out []string
	for _, c := range u.Content {
		switch {
		case c.Type == "diff" && d.Diff == nil:
			d.Diff = &chatToolDiff{Path: c.Path, OldText: capText(c.OldText), NewText: capText(c.NewText)}
		case c.Content != nil && c.Content.Type == "text":
			out = append(out, c.Content.Text)
		}
	}
	d.Output = capText(strings.Join(out, "\n"))

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}
