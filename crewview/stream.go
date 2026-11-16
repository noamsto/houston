package crewview

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	StateStarting    = "starting"
	StateOK          = "ok"
	StateUnsupported = "unsupported"
	StateUnavailable = "unavailable"
)

// Status says whether a stream's view can be shown and, when not, why.
type Status struct {
	State  string
	Notice string
}

const (
	maxLine       = 4 << 20
	maxStderrLine = 4 << 10
	stopGrace     = 2 * time.Second
	// unreadableAfter consecutive undecodable lines drop the view.
	unreadableAfter    = 3
	maxDecodeNoticeLen = 200
)

const tooOldNotice = "crew is too old for houston: crew dash --view is missing (needs dispatcher#1580)"

// strippedEnv keeps houston's own crew/tmux identity out of crew dash. CREW_BIN
// matters most: the producer builds every action's argv prefix from it, and
// houston runs those argv later from its own environment.
var strippedEnv = []string{"CREW_BIN", "CREW_ID", "CREW_WORKER_ID", "TMUX_PANE", "DISPATCH_SPEC"}

// Stream keeps one `crew dash --json --view overview --watch` running in root
// and holds its latest overview line, restarting the process when it exits and
// periodically while the view has remote or peer rows (those change without
// touching the local bus, so the producer never re-emits for them).
type Stream struct {
	root string
	// env is the base environment for the child, minus strippedEnv; the
	// command's own Env entries are appended after it.
	env     []string
	command func(ctx context.Context, root string) *exec.Cmd

	backoffMin, backoffMax time.Duration
	// fresh is how long a view with remote or peer rows is trusted after its
	// last line before the process is restarted.
	fresh time.Duration
	// stale is how long a view outlives its last line once no process is live.
	stale time.Duration
	// startGrace is how long a process may run without a line before it
	// stops counting as live and is restarted.
	startGrace time.Duration

	cancel context.CancelFunc
	done   chan struct{}
	lines  chan struct{}

	mu       sync.Mutex
	latest   Overview
	has      bool
	remote   bool
	status   Status
	lastLine time.Time
	started  time.Time // when the current process started
	readRun  bool      // the current process wrote a stdout line
	lineRun  bool      // the current process wrote an accepted line
	warnRun  bool      // the current process's first undecodable line was logged
	live     bool      // a process is running or a freshness restart is underway
	exitNote string
	skipped  int
	badRun   int // undecodable lines since the last accepted one
}

func NewStream(crewPath, root string) *Stream {
	return &Stream{
		root: root,
		env:  os.Environ(),
		command: func(context.Context, string) *exec.Cmd {
			return exec.Command(crewPath, "dash", "--json", "--view", "overview", "--watch") //nolint:gosec // crewPath is resolved by houston at startup, not taken from a request
		},
		backoffMin: time.Second,
		backoffMax: 30 * time.Second,
		fresh:      60 * time.Second,
		stale:      30 * time.Second,
		startGrace: 120 * time.Second,
		status:     Status{State: StateStarting},
		done:       make(chan struct{}),
		lines:      make(chan struct{}, 1),
	}
}

// Start runs the stream until ctx is done or Stop is called.
func (s *Stream) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	go s.run(ctx)
}

// Stop ends the stream and waits for its process group to be gone.
func (s *Stream) Stop() {
	s.cancel()
	<-s.done
}

func (s *Stream) Root() string { return s.root }

// Latest returns the current overview, its status, and whether there is one to
// show. The overview's slices are shared with the stream: callers must not
// mutate them.
func (s *Stream) Latest() (Overview, Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.expireLocked(now)
	switch {
	case s.status.State == StateUnsupported || s.status.State == StateUnavailable:
		return Overview{}, s.status, false
	case s.has:
		return s.latest, s.status, true
	case s.live && !s.liveLocked(now):
		return Overview{}, Status{State: StateUnavailable, Notice: "crew view unavailable: " + s.noViewNote()}, false
	case s.exitNote != "":
		return Overview{}, Status{State: StateUnavailable, Notice: "crew view unavailable: " + s.exitNote}, false
	default:
		return Overview{}, Status{State: StateStarting}, false
	}
}

// expireLocked drops the view once no process is live and stale has passed
// since its last line. Dropping is permanent so a restarted process that has
// not written yet cannot bring the old view back.
func (s *Stream) expireLocked(now time.Time) {
	if s.has && !s.liveLocked(now) && now.Sub(s.lastLine) >= s.stale {
		s.latest, s.has, s.remote = Overview{}, false, false
	}
}

// liveLocked reports whether a process counts as live for staleness: one that
// has written no line stops counting once startGrace has passed, so a hung
// restart cannot keep the previous process's view alive.
func (s *Stream) liveLocked(now time.Time) bool {
	return s.live && (s.lineRun || now.Sub(s.started) < s.startGrace)
}

func (s *Stream) noViewNote() string {
	return fmt.Sprintf("crew dash produced no view for %ds", int(s.startGrace.Seconds()))
}

func rootGone(root string) bool {
	_, err := os.Stat(root)
	return errors.Is(err, fs.ErrNotExist)
}

type runResult struct {
	gotLine   bool
	tooOld    bool
	freshKill bool
}

func (s *Stream) run(ctx context.Context) {
	defer close(s.done)
	backoff := s.backoffMin
	for {
		r := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if r.gotLine {
			backoff = s.backoffMin
		}
		delay := backoff
		switch {
		case r.tooOld:
			delay = s.backoffMax
		case r.freshKill:
		default:
			backoff = min(backoff*2, s.backoffMax)
		}
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}

func (s *Stream) childEnv() []string {
	env := make([]string, 0, len(s.env))
	for _, kv := range s.env {
		key, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(strippedEnv, key) {
			env = append(env, kv)
		}
	}
	return env
}

func (s *Stream) runOnce(ctx context.Context) runResult {
	cmd := s.command(ctx, s.root)
	cmd.Dir = s.root
	cmd.Env = append(s.childEnv(), cmd.Env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	// Own pipes rather than StdoutPipe: Wait must not close the read ends
	// while the readers drain them.
	outR, errR, err := s.startWithPipes(cmd)
	if err != nil {
		slog.Warn("crewview: crew dash did not start", "dir", s.root, "err", err)
		note := err.Error()
		// exec reports a missing Dir as the binary being missing.
		if rootGone(s.root) {
			note = "working directory " + s.root + " is gone"
		}
		s.mu.Lock()
		s.live, s.exitNote = false, note
		s.mu.Unlock()
		return runResult{}
	}
	pid := cmd.Process.Pid

	s.mu.Lock()
	now := time.Now()
	s.expireLocked(now)
	s.live, s.started, s.readRun, s.lineRun, s.warnRun = true, now, false, false, false
	s.mu.Unlock()

	var readers sync.WaitGroup
	var stderrLine string
	var stderrAny bool
	readers.Add(2)
	go func() {
		defer readers.Done()
		stderrLine, stderrAny = firstLine(errR)
	}()
	go func() {
		defer readers.Done()
		s.readStdout(outR)
	}()
	exited := make(chan struct{})
	go func() {
		// The exit status is read from cmd.ProcessState.
		_ = cmd.Wait()
		close(exited)
	}()

	freshKill, hung := s.supervise(ctx, pid, exited)

	// Grandchildren left in the group would hold the pipes open; the group id
	// stays reserved while any member lives, so this cannot hit a stranger.
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	// A child that left the group could still hold a pipe open.
	_ = outR.SetReadDeadline(time.Now().Add(stopGrace))
	_ = errR.SetReadDeadline(time.Now().Add(stopGrace))
	readers.Wait()
	_ = outR.Close()
	_ = errR.Close()
	if !waitGroupGone(pid) {
		slog.Warn("crewview: crew dash process group outlived its stop", "dir", s.root, "pgid", pid)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	gotLine, readAny := s.lineRun, s.readRun
	if freshKill && ctx.Err() == nil {
		return runResult{gotLine: gotLine, freshKill: true}
	}
	s.live = false
	if ctx.Err() != nil {
		return runResult{}
	}
	if hung {
		slog.Warn("crewview: crew dash produced no view; restarting", "dir", s.root, "after", s.startGrace)
		return runResult{}
	}

	code := cmd.ProcessState.ExitCode()
	slog.Warn("crewview: crew dash exited", "dir", s.root, "code", code, "stderr", stderrLine)
	s.exitNote = stderrLine
	if s.exitNote == "" {
		s.exitNote = cmd.ProcessState.String()
	}
	tooOld := !readAny && code == 2 && (strings.Contains(stderrLine, "unknown argument") || !stderrAny)
	if tooOld {
		s.latest, s.has, s.remote = Overview{}, false, false
		s.status = Status{State: StateUnsupported, Notice: tooOldNotice}
	}
	return runResult{gotLine: gotLine, tooOld: tooOld}
}

func (s *Stream) startWithPipes(cmd *exec.Cmd) (outR, errR *os.File, err error) {
	outR, outW, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = outR.Close()
		_ = outW.Close()
		return nil, nil, err
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	_ = outW.Close()
	_ = errW.Close()
	if err != nil {
		_ = outR.Close()
		_ = errR.Close()
		return nil, nil, err
	}
	return outR, errR, nil
}

// supervise waits for the process to exit, stopping it on ctx, when a view
// with remote rows has gone fresh without a new line, or when the process has
// written no line within startGrace. It reports whether it stopped the process
// for freshness or for having written nothing.
func (s *Stream) supervise(ctx context.Context, pid int, exited <-chan struct{}) (freshKill, hung bool) {
	for {
		var freshC, hungC <-chan time.Time
		if at, ok := s.freshDeadline(); ok {
			freshC = time.After(time.Until(at))
		}
		if at, ok := s.hungDeadline(); ok {
			hungC = time.After(time.Until(at))
		}
		select {
		case <-exited:
			return false, false
		case <-ctx.Done():
			terminate(pid, exited)
			return false, false
		case <-s.lines:
		case <-freshC:
			if at, ok := s.freshDeadline(); ok && !time.Now().Before(at) {
				terminate(pid, exited)
				return true, false
			}
		case <-hungC:
			if at, ok := s.hungDeadline(); ok && !time.Now().Before(at) {
				s.mu.Lock()
				s.exitNote = s.noViewNote()
				s.mu.Unlock()
				terminate(pid, exited)
				return false, true
			}
		}
	}
}

// hungDeadline is when the current process is restarted for having written
// no line.
func (s *Stream) hungDeadline() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lineRun {
		return time.Time{}, false
	}
	return s.started.Add(s.startGrace), true
}

// freshDeadline is when the current process is restarted for freshness. Only
// a line from this process starts the clock, so a slow first emit is never
// cut short.
func (s *Stream) freshDeadline() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.has || !s.remote || !s.lineRun {
		return time.Time{}, false
	}
	return s.lastLine.Add(s.fresh), true
}

// terminate sends SIGTERM to the process group, then SIGKILL after stopGrace,
// and returns once the leader has been reaped.
func terminate(pid int, exited <-chan struct{}) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	t := time.NewTimer(stopGrace)
	defer t.Stop()
	select {
	case <-exited:
	case <-t.C:
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		<-exited
	}
}

// waitGroupGone reports whether no process is left in group pgid within
// stopGrace. SIGKILL is asynchronous and a dying member closes its pipes before
// it is gone, so EOF on them does not mean the group has exited.
func waitGroupGone(pgid int) bool {
	deadline := time.Now().Add(stopGrace)
	for syscall.Kill(-pgid, 0) != syscall.ESRCH {
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(time.Millisecond)
	}
	return true
}

// readStdout feeds each line to accept. A line over maxLine is discarded
// through its newline without being held whole.
func (s *Stream) readStdout(r io.Reader) {
	br := bufio.NewReaderSize(r, 64<<10)
	var buf []byte
	over := false
	for {
		chunk, err := br.ReadSlice('\n')
		if !over {
			if len(buf)+len(chunk) > maxLine+1 {
				over, buf = true, buf[:0]
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		switch line := bytes.TrimSpace(buf); {
		case over:
			s.skip()
			over = false
		case len(line) > 0:
			s.accept(line)
		}
		buf = buf[:0]
		if err != nil {
			return
		}
	}
}

func (s *Stream) skip() {
	s.mu.Lock()
	s.readRun = true
	s.skipped++
	s.mu.Unlock()
}

func (s *Stream) accept(line []byte) {
	ov, err := Decode(line)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readRun = true
	var ue *UnsupportedError
	switch {
	case err == nil:
		s.latest, s.has, s.remote = ov, true, hasRemote(ov)
		s.status = Status{State: StateOK}
		s.lastLine, s.lineRun, s.badRun = time.Now(), true, 0
		select {
		case s.lines <- struct{}{}:
		default:
		}
	case errors.As(err, &ue):
		s.latest, s.has, s.remote = Overview{}, false, false
		s.status = Status{State: StateUnsupported, Notice: ue.Error()}
	default:
		s.skipped++
		s.badRun++
		if !s.warnRun {
			s.warnRun = true
			// The error's text can quote the line, so only its type and offset
			// are logged.
			slog.Warn("crewview: undecodable crew dash line", "dir", s.root, "err_type", fmt.Sprintf("%T", errors.Unwrap(err)), "offset", decodeOffset(err))
		}
		if s.badRun >= unreadableAfter {
			msg := strings.TrimPrefix(err.Error(), "crewview: ")
			if len(msg) > maxDecodeNoticeLen {
				msg = msg[:maxDecodeNoticeLen]
			}
			s.latest, s.has, s.remote = Overview{}, false, false
			s.status = Status{State: StateUnavailable, Notice: "crew view unreadable: " + msg}
		}
	}
}

func decodeOffset(err error) int64 {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return se.Offset
	}
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return te.Offset
	}
	return -1
}

func hasRemote(ov Overview) bool {
	for _, w := range ov.Workers {
		if w.Origin == OriginRemote || w.Origin == OriginPeer {
			return true
		}
	}
	for _, it := range ov.Attention {
		if it.Origin == OriginRemote || it.Origin == OriginPeer {
			return true
		}
	}
	return false
}

// firstLine returns stderr's first line, capped at maxStderrLine, and whether
// stderr had any bytes; it drains the rest.
func firstLine(r io.Reader) (string, bool) {
	br := bufio.NewReaderSize(r, maxStderrLine)
	b, _ := br.ReadSlice('\n')
	line := strings.TrimSpace(string(b))
	n, _ := io.Copy(io.Discard, br)
	return line, len(b) > 0 || n > 0
}

// Manager keeps one Stream per crew bus.
type Manager struct {
	ctx       context.Context
	newStream func(root string) *Stream

	mu      sync.Mutex
	streams map[string]*Stream
	// retiring holds cancelled streams until their run loop ends, so Close
	// can wait for their processes.
	retiring []*Stream
}

func NewManager(ctx context.Context, crewPath string) *Manager {
	return &Manager{
		ctx:       ctx,
		newStream: func(root string) *Stream { return NewStream(crewPath, root) },
		streams:   map[string]*Stream{},
	}
}

// Ensure starts a stream for bus in root unless one is already running. A
// running stream keeps its root while that directory exists; once it is gone
// (a reaped worker's worktree) the stream is replaced by one in root.
func (m *Manager) Ensure(bus, root string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	if old, ok := m.streams[bus]; ok {
		if old.root == root || !rootGone(old.root) {
			return
		}
		m.retireLocked(old)
	}
	s := m.newStream(root)
	s.Start(m.ctx)
	m.streams[bus] = s
}

// Retain stops the streams of buses not in keep. It does not wait for their
// processes to exit; Close does.
func (m *Manager) Retain(keep map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	for bus, s := range m.streams {
		if !keep[bus] {
			m.retireLocked(s)
			delete(m.streams, bus)
		}
	}
}

func (m *Manager) retireLocked(s *Stream) {
	s.cancel()
	m.retiring = append(m.retiring, s)
}

func (m *Manager) pruneLocked() {
	m.retiring = slices.DeleteFunc(m.retiring, func(s *Stream) bool {
		select {
		case <-s.done:
			return true
		default:
			return false
		}
	})
}

// Root is the root bus's stream runs in; false for an unknown bus.
func (m *Manager) Root(bus string) (string, bool) {
	if s := m.stream(bus); s != nil {
		return s.Root(), true
	}
	return "", false
}

// Close stops every stream, retired ones included, and waits for their
// processes to be gone.
func (m *Manager) Close() {
	m.mu.Lock()
	all := m.retiring
	for _, s := range m.streams {
		all = append(all, s)
	}
	m.streams, m.retiring = map[string]*Stream{}, nil
	m.mu.Unlock()
	// Cancel all first so the processes' stop graces run concurrently.
	for _, s := range all {
		s.cancel()
	}
	for _, s := range all {
		s.Stop()
	}
}

// Latest is the bus's Stream.Latest; an unknown bus has no view and a zero
// Status.
func (m *Manager) Latest(bus string) (Overview, Status, bool) {
	if s := m.stream(bus); s != nil {
		return s.Latest()
	}
	return Overview{}, Status{}, false
}

func (m *Manager) stream(bus string) *Stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.streams[bus]
}
