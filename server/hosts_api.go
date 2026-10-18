package server

import (
	"encoding/json"
	"net/http"

	"github.com/noamsto/houston/peer"
)

// handleHosts lists this host first, then each configured peer with its
// connection health.
//
//	GET /api/hosts → {"hosts": [peer.Health…]}
func (s *Server) handleHosts(w http.ResponseWriter, _ *http.Request) {
	hosts := make([]peer.Health, 0, 1+len(s.peers))
	hosts = append(hosts, peer.Health{Name: "local", Local: true, State: peer.StateConnected})
	for _, p := range s.peers {
		hosts = append(hosts, p.Health())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Hosts []peer.Health `json:"hosts"`
	}{hosts})
}
