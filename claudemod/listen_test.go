package claudemod

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/noamsto/houston/hook"
)

// socketPath returns a socket path in a short temp dir (sun_path is ~108 bytes).
func socketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cm")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "sock")
}

func unixClient(path string) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
}

func startServer(t *testing.T) (*Server, *Store, *http.Client) {
	t.Helper()
	path := socketPath(t)
	store := NewStore()
	srv, err := Listen(path, store)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv, store, unixClient(path)
}

func do(t *testing.T, c *http.Client, method, path, body string) int {
	t.Helper()
	req, err := http.NewRequest(method, "http://houston"+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestSocketMode(t *testing.T) {
	srv, _, _ := startServer(t)
	fi, err := os.Stat(srv.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
}

func TestPostRoundTrip(t *testing.T) {
	_, store, c := startServer(t)
	status := do(t, c, http.MethodPost, "/v1/record", `{"v":1,"sid":"s","boot":"b","seq":1,"ev":"turn.start"}`)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", status)
	}
	snap := store.Snapshot()
	if len(snap) != 1 || snap[0].SID != "s" || snap[0].State != hook.StateThinking {
		t.Fatalf("snapshot = %+v", snap)
	}
	// A duplicate is accepted too, and counted stale.
	status = do(t, c, http.MethodPost, "/v1/record", `{"v":1,"sid":"s","boot":"b","seq":1,"ev":"turn.start"}`)
	if status != http.StatusNoContent || store.Snapshot()[0].Stale != 1 {
		t.Fatalf("duplicate: status = %d, snapshot = %+v", status, store.Snapshot())
	}
}

func TestStatusCodes(t *testing.T) {
	_, store, c := startServer(t)
	oversize := `{"v":1,"sid":"s","boot":"b","seq":1,"ev":"stop","message":"` + strings.Repeat("x", MaxBody) + `"}`
	cases := []struct {
		name, method, path, body string
		want                     int
	}{
		{"malformed", http.MethodPost, "/v1/record", `{"v":2}`, http.StatusBadRequest},
		{"oversize", http.MethodPost, "/v1/record", oversize, http.StatusRequestEntityTooLarge},
		{"get", http.MethodGet, "/v1/record", "", http.StatusMethodNotAllowed},
		{"other path", http.MethodPost, "/v1/other", "{}", http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := do(t, c, tc.method, tc.path, tc.body); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
	if n := len(store.Snapshot()); n != 0 {
		t.Fatalf("rejected requests created %d sessions", n)
	}
}

func TestListenReplacesDeadSocket(t *testing.T) {
	path := socketPath(t)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	// Closing a net listener unlinks the file; keep it, as a crash would.
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = ln.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("leftover socket missing: %v", err)
	}
	srv, err := Listen(path, NewStore())
	if err != nil {
		t.Fatalf("Listen over a dead socket: %v", err)
	}
	_ = srv.Close()
}

func TestListenRefusesLiveSocket(t *testing.T) {
	srv, _, _ := startServer(t)
	if _, err := Listen(srv.path, NewStore()); err == nil {
		t.Fatal("second Listen on a live socket succeeded")
	}
	if _, err := os.Lstat(srv.path); err != nil {
		t.Fatalf("live socket removed: %v", err)
	}
}

func TestListenRefusesRegularFile(t *testing.T) {
	path := socketPath(t)
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(path, NewStore()); err == nil {
		t.Fatal("Listen over a regular file succeeded")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "keep" {
		t.Fatalf("file changed: %q, %v", b, err)
	}
}

func TestListenCreatesParentDir(t *testing.T) {
	path := filepath.Join(filepath.Dir(socketPath(t)), "sub", "sock")
	srv, err := Listen(path, NewStore())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("parent dir: %v, %v", fi, err)
	}
}

func TestCloseRemovesSocket(t *testing.T) {
	srv, _, _ := startServer(t)
	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Lstat(srv.path); !os.IsNotExist(err) {
		t.Fatalf("socket still there: %v", err)
	}
}

func TestListenLeavesOnlyTheSocket(t *testing.T) {
	srv, _, _ := startServer(t)
	entries, err := os.ReadDir(filepath.Dir(srv.path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(srv.path) {
		t.Fatalf("parent dir holds %v, want only the socket", entries)
	}
	fi, err := os.Lstat(srv.path)
	if err != nil || fi.Mode()&os.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket: %v, %v", fi, err)
	}
}
