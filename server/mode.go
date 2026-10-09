package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"

	"github.com/noamsto/houston/mode"
)

// ModeProbe looks for dispatch and crew on the PATH dispatch itself gets: the
// tmux server's global PATH ahead of houston's own.
func ModeProbe() mode.Probe {
	return mode.Probe{
		ServerPath: func(ctx context.Context) (string, error) { return serverGlobalPath(ctx, execTmux) },
		OwnPath:    os.Getenv("PATH"),
		Executable: mode.OnPath,
	}
}

func (s *Server) handleMode(w http.ResponseWriter, _ *http.Request) {
	m := mode.Dispatcher
	if s.mode == mode.Tmux {
		m = mode.Tmux
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]mode.Mode{"mode": m})
}
