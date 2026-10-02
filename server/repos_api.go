package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"unicode/utf8"
)

const (
	maxRepoBody   = 4 << 10
	maxRepoQuery  = 200
	repoBadQuery  = "q must be at most 200 characters with no control characters"
	repoBadBody   = "malformed request body"
	repoBodyLarge = "request body too large"
)

type repoPathRequest struct {
	Path string `json:"path"`
}

// handleReposList serves the registry as stored, invalid entries included, so
// the UI can offer to remove one that has gone missing.
//
//	GET /api/repos → {"roots","repos"}
func (s *Server) handleReposList(w http.ResponseWriter, _ *http.Request) {
	writeDispatchJSON(w, http.StatusOK, map[string]any{
		"roots": s.repoReg.Roots(),
		"repos": s.repoReg.Entries(),
	})
}

// handleReposAdd registers a main checkout under a configured root.
//
//	POST /api/repos {"path"} → {"path","name"}
func (s *Server) handleReposAdd(w http.ResponseWriter, r *http.Request) {
	path, ok := decodeRepoPath(w, r)
	if !ok {
		return
	}
	resolved, err := s.repoReg.Add(path)
	if err != nil {
		writeRepoError(w, err)
		return
	}
	writeDispatchJSON(w, http.StatusOK, map[string]string{"path": resolved, "name": filepath.Base(resolved)})
}

// handleReposRemove forgets a registered repo; the repo itself is untouched.
//
//	DELETE /api/repos {"path"} → 204
func (s *Server) handleReposRemove(w http.ResponseWriter, r *http.Request) {
	path, ok := decodeRepoPath(w, r)
	if !ok {
		return
	}
	if err := s.repoReg.Remove(path); err != nil {
		writeRepoError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRepoCandidates lists main checkouts under the roots that match q.
//
//	GET /api/repos/candidates?q= → {"roots","candidates","truncated"}
func (s *Server) handleRepoCandidates(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if utf8.RuneCountInString(q) > maxRepoQuery || dispatchHasControlRune(q) {
		writeDispatchJSON(w, http.StatusBadRequest, dispatchResponse{Error: repoBadQuery})
		return
	}
	cands, truncated := s.repoReg.Candidates(q)
	writeDispatchJSON(w, http.StatusOK, map[string]any{
		"roots":      s.repoReg.Roots(),
		"candidates": cands,
		"truncated":  truncated,
	})
}

func decodeRepoPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRepoBody)
	var req repoPathRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		code, msg := http.StatusBadRequest, repoBadBody
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			code, msg = http.StatusRequestEntityTooLarge, repoBodyLarge
		}
		writeDispatchJSON(w, code, dispatchResponse{Error: msg})
		return "", false
	}
	return req.Path, true
}

// writeRepoError answers a registry error with its own status; anything that
// isn't a *repoError is an internal fault whose detail stays in the log.
func writeRepoError(w http.ResponseWriter, err error) {
	var re *repoError
	if !errors.As(err, &re) {
		writeDispatchJSON(w, http.StatusInternalServerError, dispatchResponse{Error: "internal error"})
		return
	}
	writeDispatchJSON(w, re.code, dispatchResponse{Error: re.msg})
}
