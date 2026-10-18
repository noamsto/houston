package peer

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/noamsto/houston/runs"
)

// InstanceHeader carries a houston's per-process instance id on its runs
// stream, so a peer list that includes this host is recognised as self.
const InstanceHeader = "X-Houston-Instance"

// ScopeLocal is the ?scope= value a peer dials with: the peer re-exports only
// its own local runs, never the runs it mirrors from its peers.
const ScopeLocal = "local"

// Health states.
const (
	StateConnecting  = "connecting"
	StateConnected   = "connected"
	StateUnreachable = "unreachable"
)

// Error classes. Health carries only these, never an error string.
const (
	ClassToken    = "token"
	ClassDNS      = "dns"
	ClassRefused  = "refused"
	ClassTimeout  = "timeout"
	ClassTLS      = "tls"
	ClassAuth     = "auth"
	ClassRedirect = "redirect"
	ClassHTTP     = "http"
	ClassProtocol = "protocol"
	ClassSilent   = "silent"
	ClassClosed   = "closed"
	ClassSelf     = "self"
	ClassInsecure = "insecure"
	ClassOther    = "other"
)

const maxToken = 4 << 10

// maxPeerRuns bounds how many runs one peer can list here, so a broken peer
// cannot grow this registry and every browser's stream without limit.
const maxPeerRuns = 2000

// maxID allows long ids: a windowless crew run's id encodes its bus path and
// branch. The bound is not in idRe because RE2 caps a repeat count at 1000.
const maxID = 1024

var idRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var errInsecureHop = errors.New("plain http connection is neither loopback nor over the tailnet")

type Options struct {
	Self       string        // this houston's instance id; a peer answering with it is "self"
	Caps       runs.Caps     // caps this houston can forward; a peer run keeps only those it also reports
	MinBackoff time.Duration // default 1s
	MaxBackoff time.Duration // default 30s
	Silence    time.Duration // default 60s

	DialTimeout   time.Duration // default 10s
	HeaderTimeout time.Duration // Fast requests' response-header timeout, default 15s
	LongCeiling   time.Duration // bound on a Long or Detached request, default 5m
}

type Health struct {
	Name     string `json:"name"`
	Local    bool   `json:"local,omitempty"`
	State    string `json:"health"`              // connecting | connected | unreachable
	Error    string `json:"error,omitempty"`     // error class, never err.Error()
	LastSeen int64  `json:"last_seen,omitempty"` // unix seconds, last event heard
}

// Source publishes one peer's local runs as peer layers. Each run is keyed
// runs.PeerKey(name, id), stamped with the peer's name and stripped of
// everything that would address this host's tmux or offer an action.
type Source struct {
	cfg    Config
	opts   Options
	client *http.Client
	slow   *http.Client                                                      // client without a response-header timeout, for requests that answer late
	dial   func(ctx context.Context, network, addr string) (net.Conn, error) // the transport's dial, reused by DialTerminal
	hopOK  func(local, remote net.Addr) bool                                 // plainHopOK; a field so tests can stand in a route
	dialer *net.Dialer

	mu     sync.Mutex
	health Health

	// known is the peer's run set as last emitted, by peer run id; stale is
	// whether those emissions carried Stale. Both are owned by Run's
	// goroutine.
	known map[string]runs.Run
	stale bool

	// warned holds the warnings already logged on this connection, so a
	// peer sending many bad runs logs each reason once. Owned by Run's
	// goroutine.
	warned map[string]bool
}

func New(cfg Config, opts Options) *Source {
	if opts.MinBackoff <= 0 {
		opts.MinBackoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = 30 * time.Second
	}
	opts.MaxBackoff = max(opts.MaxBackoff, opts.MinBackoff)
	if opts.Silence <= 0 {
		opts.Silence = 60 * time.Second
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.HeaderTimeout <= 0 {
		opts.HeaderTimeout = 15 * time.Second
	}
	if opts.LongCeiling <= 0 {
		opts.LongCeiling = 5 * time.Minute
	}
	s := &Source{
		cfg:    cfg,
		opts:   opts,
		hopOK:  plainHopOK,
		dialer: &net.Dialer{Timeout: opts.DialTimeout, KeepAlive: 30 * time.Second},
		health: Health{Name: cfg.Name, State: StateConnecting},
		known:  map[string]runs.Run{},
		warned: map[string]bool{},
	}
	transport := &http.Transport{
		// Not ProxyFromEnvironment: an HTTP_PROXY would receive a plain-http
		// tailnet request, bearer header included.
		Proxy:                 nil,
		DialContext:           s.dialer.DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: opts.HeaderTimeout,
		ForceAttemptHTTP2:     true,
		IdleConnTimeout:       90 * time.Second,
	}
	if strings.HasPrefix(cfg.URL, "http:") {
		transport.DialContext = s.dialPlain
	}
	s.dial = transport.DialContext
	// The token must never follow a redirect to another URL.
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	s.client = &http.Client{Transport: transport, CheckRedirect: noRedirect}
	// A dispatch answers only once it finished, so the ceiling bounds it
	// instead of the header timeout. No idle reuse: a Detached request counts
	// as written only on a fresh connection, so a stale pooled one that fails
	// after the write cannot fake an unknown outcome.
	slowTr := transport.Clone()
	slowTr.ResponseHeaderTimeout = 0
	slowTr.DisableKeepAlives = true
	s.slow = &http.Client{Transport: slowTr, CheckRedirect: noRedirect}
	return s
}

// dialPlain refuses a plain-http connection that leaves both this machine and
// the tailnet before anything is written on it, so the bearer token never
// crosses a cleartext hop.
func (s *Source) dialPlain(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := s.dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	if !s.hopOK(conn.LocalAddr(), conn.RemoteAddr()) {
		_ = conn.Close()
		return nil, errInsecureHop
	}
	return conn, nil
}

func (s *Source) Name() string { return "peer:" + s.cfg.Name }

// Host is the name peer runs carry in Run.Host.
func (s *Source) Host() string { return s.cfg.Name }

// ValidID reports whether id is safe to splice into a peer URL path.
func ValidID(id string) bool { return len(id) <= maxID && idRe.MatchString(id) }

func (s *Source) Health() Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health
}

// Run dials the peer until ctx is cancelled, reconnecting with backoff. It
// returns only ctx.Err().
func (s *Source) Run(ctx context.Context, out chan<- runs.Delta) error {
	backoff := s.opts.MinBackoff
	for {
		snapshot, class, err := s.connect(ctx, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if snapshot {
			backoff = s.opts.MinBackoff
		}
		s.setUnreachable(class, err)
		if err := s.markStale(ctx, out); err != nil {
			return err
		}

		t := time.NewTimer(jitter(backoff))
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		backoff = min(backoff*2, s.opts.MaxBackoff)
	}
}

// connect runs one connection to its end and reports whether a snapshot
// arrived, and the class of what ended it.
func (s *Source) connect(ctx context.Context, out chan<- runs.Delta) (snapshot bool, class string, err error) {
	clear(s.warned)
	token, err := readToken(s.cfg.TokenFile)
	if err != nil {
		return false, ClassToken, err
	}

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, s.cfg.URL+"/api/runs/stream?scope="+ScopeLocal, nil)
	if err != nil {
		return false, ClassOther, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := s.client.Do(req)
	if err != nil {
		return false, classify(err), err
	}
	defer resp.Body.Close()
	if class, err := s.checkResponse(resp); err != nil {
		return false, class, err
	}

	var silent atomic.Bool
	timer := time.AfterFunc(s.opts.Silence, func() {
		silent.Store(true)
		cancel()
	})
	defer timer.Stop()

	rd := &sseReader{r: bufio.NewReaderSize(resp.Body, 64<<10), touch: func() { timer.Reset(s.opts.Silence) }}
	for {
		ev, err := rd.next()
		switch {
		case err == nil:
		case silent.Load():
			return snapshot, ClassSilent, err
		case errors.Is(err, errTooLong):
			return snapshot, ClassProtocol, err
		default:
			return snapshot, classify(err), err
		}
		s.heard()
		switch ev.name {
		case "snapshot":
			if err := s.applySnapshot(ctx, out, ev.data); err != nil {
				return snapshot, ClassProtocol, err
			}
			snapshot = true
			s.setConnected()
		case "update":
			if err := s.applyUpdate(ctx, out, ev.data); err != nil {
				return snapshot, ClassProtocol, err
			}
		}
	}
}

func (s *Source) checkResponse(resp *http.Response) (string, error) {
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return ClassRedirect, fmt.Errorf("peer answered %d", resp.StatusCode)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return ClassAuth, fmt.Errorf("peer answered %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return ClassHTTP, fmt.Errorf("peer answered %d", resp.StatusCode)
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "text/event-stream" {
		return ClassProtocol, errors.New("peer answered a non-event-stream content type")
	}
	if s.opts.Self != "" && resp.Header.Get(InstanceHeader) == s.opts.Self {
		return ClassSelf, errors.New("peer is this houston")
	}
	return "", nil
}

// applySnapshot replaces the peer's set: every run is (re-)emitted fresh and
// every known id the snapshot lacks goes Gone. A "null" body is an empty set.
func (s *Source) applySnapshot(ctx context.Context, out chan<- runs.Delta, data []byte) error {
	var list []runs.Run
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	next := make(map[string]runs.Run, len(list))
	order := make([]string, 0, len(list))
	for _, r := range list {
		id, r, ok := s.sanitise(r)
		if !ok {
			continue
		}
		if _, dup := next[id]; !dup {
			if len(order) >= maxPeerRuns {
				s.warnLimit()
				continue
			}
			order = append(order, id)
		}
		next[id] = r
	}
	for _, id := range order {
		if err := s.emit(ctx, out, runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey(s.cfg.Name, id), Run: next[id]}); err != nil {
			return err
		}
	}
	for id := range s.known {
		if _, ok := next[id]; ok {
			continue
		}
		if err := s.emit(ctx, out, runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey(s.cfg.Name, id), Gone: true}); err != nil {
			return err
		}
	}
	s.known = next
	s.stale = false
	return nil
}

func (s *Source) applyUpdate(ctx context.Context, out chan<- runs.Delta, data []byte) error {
	var r runs.Run
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	if r.Removed {
		if _, ok := s.known[r.ID]; !ok || r.Host != "" {
			return nil
		}
		delete(s.known, r.ID)
		return s.emit(ctx, out, runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey(s.cfg.Name, r.ID), Gone: true})
	}
	id, r, ok := s.sanitise(r)
	if !ok {
		return nil
	}
	if _, ok := s.known[id]; !ok && len(s.known) >= maxPeerRuns {
		s.warnLimit()
		return nil
	}
	s.known[id] = r
	return s.emit(ctx, out, runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey(s.cfg.Name, id), Run: r})
}

// sanitise returns the peer's id for r and the run as this host lists it. A
// run already carrying a host is one the peer mirrors from elsewhere (an older
// peer that ignored scope=local): dropping it is the loop guard.
func (s *Source) sanitise(r runs.Run) (string, runs.Run, bool) {
	if r.Host != "" {
		s.warnDropped("carries a host")
		return "", runs.Run{}, false
	}
	if !ValidID(r.ID) {
		s.warnDropped("invalid id")
		return "", runs.Run{}, false
	}
	id := r.ID
	r.ID = ""
	r.Host = s.cfg.Name
	// A peer pane id addresses nothing here and would collide with a local
	// pane; the peer's own Stale describes its tmux connection, not ours.
	r.Tmux = nil
	r.Caps = runs.Caps{
		Terminal: r.Caps.Terminal && s.opts.Caps.Terminal,
		Reply:    r.Caps.Reply && s.opts.Caps.Reply,
		Kill:     r.Caps.Kill && s.opts.Caps.Kill,
		Chat:     r.Caps.Chat && s.opts.Caps.Chat,
	}
	r.Stale = false
	r.DraftKey = ""
	r.Removed = false
	return id, r, true
}

func (s *Source) warnDropped(reason string) {
	if s.firstOnConn("dropped: " + reason) {
		slog.Warn("peer run dropped", "peer", s.cfg.Name, "reason", reason)
	}
}

func (s *Source) warnLimit() {
	if s.firstOnConn("limit") {
		slog.Warn("peer run limit reached", "peer", s.cfg.Name, "limit", maxPeerRuns)
	}
}

// firstOnConn reports whether key is new on this connection, and records it.
func (s *Source) firstOnConn(key string) bool {
	if s.warned[key] {
		return false
	}
	s.warned[key] = true
	return true
}

// markStale re-emits every known run as stale, once per outage: they stay
// listed with their last known values until the next snapshot.
func (s *Source) markStale(ctx context.Context, out chan<- runs.Delta) error {
	if s.stale || len(s.known) == 0 {
		return nil
	}
	s.stale = true
	for id, r := range s.known {
		r.Stale = true
		r.Caps = runs.Caps{} // a stale run cannot be acted on
		s.known[id] = r
		if err := s.emit(ctx, out, runs.Delta{Source: runs.PeerLayer, Key: runs.PeerKey(s.cfg.Name, id), Run: r}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) emit(ctx context.Context, out chan<- runs.Delta, d runs.Delta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case out <- d:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Source) heard() {
	now := time.Now().Unix()
	s.mu.Lock()
	s.health.LastSeen = now
	s.mu.Unlock()
}

func (s *Source) setConnected() {
	s.mu.Lock()
	was := s.health.State
	s.health.State = StateConnected
	s.health.Error = ""
	s.mu.Unlock()
	if was != StateConnected {
		slog.Info("peer connected", "peer", s.cfg.Name)
	}
}

func (s *Source) setUnreachable(class string, err error) {
	s.mu.Lock()
	changed := s.health.State != StateUnreachable || s.health.Error != class
	s.health.State = StateUnreachable
	s.health.Error = class
	s.mu.Unlock()
	if changed {
		slog.Warn("peer unreachable", "peer", s.cfg.Name, "error", class)
	}
	slog.Debug("peer connection ended", "peer", s.cfg.Name, "error", class, "err", err)
}

// readToken reads the bearer token afresh, so rotating the file needs no
// restart. The value never appears in an error.
func readToken(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: the operator names the token file
	if err != nil {
		return "", errors.New("token file unreadable")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxToken+1))
	if err != nil {
		return "", errors.New("token file unreadable")
	}
	if len(b) > maxToken {
		return "", errors.New("token file larger than 4 KiB")
	}
	b = bytes.TrimSpace(b)
	if len(b) == 0 {
		return "", errors.New("token file empty")
	}
	for _, c := range b {
		if c < 0x21 || c > 0x7e {
			return "", errors.New("token holds a byte outside printable ASCII")
		}
	}
	return string(b), nil
}

func classify(err error) string {
	var (
		dnsErr   *net.DNSError
		recErr   tls.RecordHeaderError
		verErr   *tls.CertificateVerificationError
		authErr  x509.UnknownAuthorityError
		hostErr  x509.HostnameError
		invalErr x509.CertificateInvalidError
		netErr   net.Error
	)
	switch {
	case errors.Is(err, errInsecureHop):
		return ClassInsecure
	case errors.As(err, &dnsErr):
		return ClassDNS
	case errors.Is(err, syscall.ECONNREFUSED):
		return ClassRefused
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return ClassClosed
	case errors.As(err, &recErr), errors.As(err, &verErr), errors.As(err, &authErr),
		errors.As(err, &hostErr), errors.As(err, &invalErr):
		return ClassTLS
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return ClassTimeout
	}
	return ClassOther
}

func jitter(d time.Duration) time.Duration {
	f := 0.8 + 0.4*rand.Float64() //nolint:gosec // G404: retry jitter, not security
	return time.Duration(float64(d) * f)
}
