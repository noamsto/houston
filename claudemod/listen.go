package claudemod

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Server serves the record route on a Unix socket.
type Server struct {
	ln   *net.UnixListener
	http *http.Server
	done chan struct{}
	path string
	sock os.FileInfo // the socket file linked at path, to tell it from a successor's
}

// Listen prepares path, listens on it and serves store's records in a
// goroutine. The parent directory is created 0700 if missing; a socket nobody
// answers is replaced; a live socket or a non-socket path is an error. The
// socket is bound in a private temp dir beside path, chmodded 0600 and linked
// into place, so it is never reachable at umask permissions. The link fails
// rather than replacing a socket another houston placed after clearStale.
func Listen(path string, store *Store) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := clearStale(path); err != nil {
		return nil, err
	}
	ln, sock, err := listenPrivate(path)
	if err != nil {
		return nil, err
	}
	s := &Server{
		ln:   ln,
		http: &http.Server{Handler: Handler(store), ReadHeaderTimeout: 5 * time.Second},
		done: make(chan struct{}),
		path: path,
		sock: sock,
	}
	go func() {
		defer close(s.done)
		if err := s.http.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("claude-mod serve", "error", err)
		}
	}()
	return s, nil
}

// listenPrivate also returns the socket file's identity: the link at path
// shares its inode.
func listenPrivate(path string) (*net.UnixListener, os.FileInfo, error) {
	tmp, err := os.MkdirTemp(filepath.Dir(path), ".claude-mod-")
	if err != nil {
		return nil, nil, fmt.Errorf("create private socket dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	tmpSock := filepath.Join(tmp, "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: tmpSock, Net: "unix"})
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	// The bound name is removed after the link; Close removes path instead.
	ln.SetUnlinkOnClose(false)
	if err := os.Chmod(tmpSock, 0o600); err != nil {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("chmod socket: %w", err)
	}
	sock, err := os.Lstat(tmpSock)
	if err != nil {
		_ = ln.Close()
		return nil, nil, fmt.Errorf("stat socket: %w", err)
	}
	if err := os.Link(tmpSock, path); err != nil {
		_ = ln.Close()
		if errors.Is(err, fs.ErrExist) {
			return nil, nil, fmt.Errorf("%s: already exists", path)
		}
		return nil, nil, fmt.Errorf("link socket into place: %w", err)
	}
	_ = os.Remove(tmpSock)
	return ln, sock, nil
}

func clearStale(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s exists and is not a socket", path)
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		_ = conn.Close()
		return fmt.Errorf("%s: already listening", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

// Close removes the socket, stops serving and waits for the serve goroutine.
// The socket goes first, and only while path is still ours: once the listener
// closes, another houston may take path for dead and link its own there.
func (s *Server) Close() error {
	err := s.removeSocket()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if shutErr := s.http.Shutdown(ctx); shutErr != nil {
		err = errors.Join(err, shutErr, s.http.Close())
	}
	<-s.done
	return err
}

func (s *Server) removeSocket() error {
	fi, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat socket: %w", err)
	}
	if !os.SameFile(fi, s.sock) {
		return nil
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove socket: %w", err)
	}
	return nil
}

// Handler serves POST /v1/record: 204 for an accepted record (a stale one
// included), 400 malformed, 413 oversize, 405 other methods, 404 other paths.
// It never writes a body.
func Handler(store *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path != "/v1/record":
			w.WriteHeader(http.StatusNotFound)
		case r.Method != http.MethodPost:
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
		default:
			serveRecord(store, w, r)
		}
	})
}

func serveRecord(store *Store, w http.ResponseWriter, r *http.Request) {
	rec, err := Decode(r.Body)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ErrTooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		slog.Debug("claude-mod reject", "status", status, "error", err)
		w.WriteHeader(status)
		return
	}
	store.Apply(rec)
	w.WriteHeader(http.StatusNoContent)
}
