package claudemod

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Server serves the record route on a Unix socket.
type Server struct {
	ln   net.Listener
	http *http.Server
	done chan struct{}
	path string
}

// Listen prepares path, listens on it and serves store's records in a
// goroutine. The parent directory is created 0700 if missing; a socket nobody
// answers is replaced; a live socket or a non-socket path is an error.
func Listen(path string, store *Store) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := clearStale(path); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	s := &Server{
		ln:   ln,
		http: &http.Server{Handler: Handler(store), ReadHeaderTimeout: 5 * time.Second},
		done: make(chan struct{}),
		path: path,
	}
	go func() {
		defer close(s.done)
		if err := s.http.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("claude-mod serve", "error", err)
		}
	}()
	return s, nil
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

// Close stops serving, waits for the serve goroutine and removes the socket.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := s.http.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, s.http.Close())
	}
	<-s.done
	if rmErr := os.Remove(s.path); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
		err = errors.Join(err, rmErr)
	}
	return err
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
