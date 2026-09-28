package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"strconv"

	"github.com/noamsto/houston/chat"
)

// ErrNoChat is returned by the Chat* methods for a session the hub doesn't
// know, or whose agent has no chat.Reader.
var ErrNoChat = errors.New("hub: no chat for session")

// chatRingSize bounds the updates a session keeps in memory; older pages are
// served by re-reading the transcript.
const chatRingSize = 500

// chatState is one session's chat stream. Seq is an update's ordinal in the
// transcript's update stream (1-based), so a re-read from byte 0 lands on the
// same seqs the ring assigned; gen bumps whenever ordinals restart.
type chatState struct {
	reader chat.Reader
	path   string
	cursor chat.Cursor
	gen    uint64
	total  uint64        // Seq of the newest update
	ring   []chat.Update // newest ≤ chatRingSize updates, seq-ascending
	subs   map[chan struct{}]struct{}
}

// ChatPage is a run of consecutive updates, oldest first.
type ChatPage struct {
	Epoch   string        `json:"epoch"`
	Updates []chat.Update `json:"updates"`
	More    bool          `json:"more"`
}

func chatEpoch(sessionID string, gen uint64) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + strconv.FormatUint(gen, 10)))
	return hex.EncodeToString(sum[:])[:12]
}

// chatLocked returns the session's chat state, creating it on first use, or
// nil when its agent has no reader. Called under h.mu.
func (s *Session) chatLocked() *chatState {
	if s.chat != nil {
		return s.chat
	}
	r := chat.For(s.view.Agent)
	if r == nil {
		return nil
	}
	s.chat = &chatState{reader: r, path: s.transcriptPath, subs: map[chan struct{}]struct{}{}}
	return s.chat
}

// setPath restarts the stream when the session's transcript moves (e.g. a
// hook rewrite naming a new file). Called under h.mu.
func (c *chatState) setPath(path string) {
	if c.path == path {
		return
	}
	c.restart(path)
	c.notify()
}

func (c *chatState) restart(path string) {
	c.path = path
	c.cursor = chat.Cursor{}
	c.gen++
	c.total = 0
	c.ring = nil
}

// notify never blocks: each channel has room for one pending signal, and
// subscribers pull by seq, so coalesced signals lose nothing.
func (c *chatState) notify() {
	for ch := range c.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (c *chatState) closeSubs() {
	for ch := range c.subs {
		delete(c.subs, ch)
		close(ch)
	}
}

// first is the oldest seq the ring holds, or total+1 when it is empty.
func (c *chatState) first() uint64 {
	if len(c.ring) == 0 {
		return c.total + 1
	}
	return c.ring[0].Seq
}

// slice copies the ring's updates with seq in [from, to); first() <= from.
func (c *chatState) slice(from, to uint64) []chat.Update {
	f := c.first()
	return append(make([]chat.Update, 0, to-from), c.ring[from-f:to-f]...)
}

func (c *chatState) page(sessionID string, from, to uint64) ChatPage {
	return ChatPage{Epoch: chatEpoch(sessionID, c.gen), Updates: c.slice(from, to), More: from > 1 && from < to}
}

// pageFrom is the oldest seq of a limit-sized page ending before before.
func pageFrom(before uint64, limit int) uint64 {
	if before > uint64(limit) {
		return before - uint64(limit)
	}
	return 1
}

// chatFor resolves a session's chat state. Called under h.mu.
func (h *Hub) chatFor(sessionID string) (*Session, *chatState, error) {
	sess, ok := h.sessions[sessionID]
	if !ok {
		return nil, nil, ErrNoChat
	}
	c := sess.chatLocked()
	if c == nil {
		return nil, nil, ErrNoChat
	}
	return sess, c, nil
}

// refreshChat reads the transcript's new complete lines into the ring. The
// read runs outside h.mu; its result is dropped if the stream restarted or
// advanced meanwhile.
func (h *Hub) refreshChat(sessionID string) {
	h.mu.Lock()
	sess, ok := h.sessions[sessionID]
	if !ok || sess.transcriptPath == "" {
		h.mu.Unlock()
		return
	}
	c := sess.chatLocked()
	if c == nil {
		h.mu.Unlock()
		return
	}
	reader, path, cursor, gen := c.reader, c.path, c.cursor, c.gen
	h.mu.Unlock()

	updates, next, reset, err := reader.Read(path, cursor)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			h.log.Debug("read chat transcript", "path", path, "err", err)
		}
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[sessionID] != sess || c.gen != gen || c.path != path || c.cursor.Offset != cursor.Offset {
		return
	}
	if reset {
		c.restart(path)
	}
	c.cursor = next
	if len(updates) == 0 && !reset {
		return
	}
	for _, u := range updates {
		c.total++
		u.Seq = c.total
		c.ring = append(c.ring, u)
	}
	if n := len(c.ring) - chatRingSize; n > 0 {
		c.ring = c.ring[n:]
	}
	c.notify()
}

// ChatPage returns up to limit updates with seq < before, oldest first
// (before 0 = the newest page). limit is clamped to [1, 100]; 0 means 50.
// Pages older than the ring are served by re-reading the transcript.
func (h *Hub) ChatPage(sessionID string, before uint64, limit int) (ChatPage, error) {
	switch {
	case limit == 0:
		limit = 50
	case limit < 1:
		limit = 1
	case limit > 100:
		limit = 100
	}

	h.mu.Lock()
	sess, c, err := h.chatFor(sessionID)
	if err != nil {
		h.mu.Unlock()
		return ChatPage{}, err
	}
	if before == 0 || before > c.total+1 {
		before = c.total + 1
	}
	from := pageFrom(before, limit)
	if from >= c.first() {
		p := c.page(sessionID, from, before)
		h.mu.Unlock()
		return p, nil
	}
	reader, path, gen, total := c.reader, c.path, c.gen, c.total
	h.mu.Unlock()

	all, _, _, err := reader.Read(path, chat.Cursor{})
	if err != nil {
		return ChatPage{}, err
	}
	// The reader is chunking-independent, so the first total updates of a
	// full re-read are exactly those the ring numbered 1..total.
	if uint64(len(all)) > total {
		all = all[:total]
	}
	ups := make([]chat.Update, 0, before-from)
	for seq := from; seq < before && seq <= uint64(len(all)); seq++ {
		u := all[seq-1]
		u.Seq = seq
		ups = append(ups, u)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[sessionID] != sess {
		return ChatPage{}, ErrNoChat
	}
	if c.gen != gen || c.path != path {
		// The stream restarted under the re-read; its newest page is
		// always inside the ring (limit ≤ 100 < chatRingSize).
		to := c.total + 1
		return c.page(sessionID, max(c.first(), pageFrom(to, limit)), to), nil
	}
	return ChatPage{Epoch: chatEpoch(sessionID, gen), Updates: ups, More: len(ups) > 0 && ups[0].Seq > 1}, nil
}

// ChatSince returns the ring's updates with seq > after. ok is false when
// epoch is not the current one or after is not servable from the ring
// (newer than the newest, or older than the oldest-1); the caller then
// restarts from a fresh page.
func (h *Hub) ChatSince(sessionID, epoch string, after uint64) ([]chat.Update, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, c, err := h.chatFor(sessionID)
	if err != nil {
		return nil, false, err
	}
	if epoch != chatEpoch(sessionID, c.gen) || after > c.total || after+1 < c.first() {
		return nil, false, nil
	}
	return c.slice(after+1, c.total+1), true, nil
}

// ChatEpoch returns the session's current stream epoch.
func (h *Hub) ChatEpoch(sessionID string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, c, err := h.chatFor(sessionID)
	if err != nil {
		return "", err
	}
	return chatEpoch(sessionID, c.gen), nil
}

// ChatSubscribe returns a channel signalled (coalescing, never blocking the
// hub) whenever the session's stream gains updates or restarts, and closed
// when the session goes away. The returned func unsubscribes; it is
// idempotent and safe after the channel was closed.
func (h *Hub) ChatSubscribe(sessionID string) (<-chan struct{}, func(), error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, c, err := h.chatFor(sessionID)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan struct{}, 1)
	c.subs[ch] = struct{}{}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
	}, nil
}

// ChatTool returns the full detail of one tool call; chat.ErrToolNotFound
// when the transcript has no such call.
func (h *Hub) ChatTool(sessionID, toolCallID string) (*chat.Update, error) {
	h.mu.Lock()
	_, c, err := h.chatFor(sessionID)
	if err != nil {
		h.mu.Unlock()
		return nil, err
	}
	reader, path := c.reader, c.path
	h.mu.Unlock()

	if path == "" {
		return nil, chat.ErrToolNotFound
	}
	return reader.Tool(path, toolCallID)
}
