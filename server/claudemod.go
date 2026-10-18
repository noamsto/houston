package server

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleClaudeMod(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"sessions": s.claudeModStore.Snapshot()})
}
