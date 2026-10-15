package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/noamsto/houston/answer"
	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

// launcherSettings are the engine, model and effort a running dispatcher was
// launched with. An empty field means the launcher was not given that flag.
type launcherSettings struct {
	Engine string
	Model  string
	Effort string
}

// parseLauncherArgv reads the settings back from a dispatcher process's argv.
// It mirrors the launcher's own flag loop: the launcher parses flags anywhere
// after its name, so a task text between flags must be skipped, not treated as
// the end of the flags. The last occurrence of a flag wins.
func parseLauncherArgv(argv []string) launcherSettings {
	var s launcherSettings
	start := -1
	for i, a := range argv {
		if filepath.Base(a) == "dispatcher" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return s
	}
	for i := start; i < len(argv); i++ {
		switch argv[i] {
		case "--agent", "--model", "--effort", "--autocompact":
			if i+1 >= len(argv) {
				return s
			}
			i++
			switch argv[i-1] {
			case "--agent":
				s.Engine = argv[i]
			case "--model":
				s.Model = argv[i]
			case "--effort":
				s.Effort = argv[i]
			}
		}
	}
	return s
}

// restartRequest is the body of a dispatcher restart. A nil field inherits the
// running dispatcher's setting; a non-nil empty string clears it.
type restartRequest struct {
	Engine *string `json:"engine"`
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
	Force  bool    `json:"force"`
}

// restartSettings merges the request over the inherited launcher settings.
// Models are per-engine, so an engine change does not inherit the model.
// runAgent stands in for an engine the launcher was not told explicitly.
func restartSettings(req restartRequest, inh launcherSettings, runAgent string) dispatcherRequest {
	base := inh.Engine
	if base == "" {
		base = runAgent
	}
	engine := base
	if req.Engine != nil {
		engine = *req.Engine
	}
	engineChanged := engine != base

	model := inh.Model
	switch {
	case req.Model != nil:
		model = *req.Model
	case engineChanged:
		model = ""
	}
	effort := inh.Effort
	if req.Effort != nil {
		effort = *req.Effort
	}
	return dispatcherRequest{Engine: engine, Model: model, Effort: effort}
}

// restartBusy reports whether restarting the run would cut off work in
// progress. A turn that ended on a question is the agent waiting, not
// working; any other blocked run has a dialog or AskUserQuestion on screen
// that a restart would discard.
func restartBusy(run runs.Run) bool {
	switch run.State {
	case runs.StateThinking, runs.StateRunning, runs.StateCompacting:
		return true
	case runs.StateBlocked:
		return run.Question == nil || run.Question.Kind != runs.QuestionTurn
	case runs.StateReview, runs.StateDone, runs.StateFailed, runs.StateIdle:
		return false
	}
	return false
}

const restartBusyMsg = "the dispatcher is in the middle of a turn — restart with force to interrupt it"

// handleRunRestart ends a dispatcher run's engine session and starts a new
// dispatcher on the same crew and repo, keeping its engine, model and effort
// unless the body overrides them.
//
//	POST /api/runs/{id}/restart {"engine"?,"model"?,"effort"?,"force"?}
//
// The crew, bus, repo and old pid come from server state only; the body only
// picks settings, each checked against the same allowlists as a launch.
func (s *Server) handleRunRestart(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	id := r.PathValue("id")
	rlog := &restartLog{id: id}
	defer rlog.emit(start)
	oldEnded := false
	reply := func(code int, body dispatcherResponse) {
		rlog.status = code
		if body.Crew == "" {
			body.Crew = rlog.crew
		}
		body.OldEnded = oldEnded
		writeDispatchJSON(w, code, body)
	}

	if s.runs == nil {
		reply(http.StatusServiceUnavailable, dispatcherResponse{Error: "run registry not started"})
		return
	}
	run, ok := s.findRun(id)
	if !ok {
		reply(http.StatusNotFound, dispatcherResponse{Error: "no such run"})
		return
	}
	if run.Role != runs.RoleDispatcher {
		reply(http.StatusNotFound, dispatcherResponse{Error: "run is not a dispatcher"})
		return
	}
	if run.Crew == nil || run.Crew.Name == "" || run.CrewBus == "" {
		reply(http.StatusNotFound, dispatcherResponse{Error: "dispatcher has no crew"})
		return
	}
	if !dispatchCrewRe.MatchString(run.Crew.Name) {
		slog.Debug("dispatcher restart: crew id is not of crew's shape", "id", id)
		reply(http.StatusNotFound, dispatcherResponse{Error: "dispatcher has no crew"})
		return
	}
	crew := run.Crew.Name
	rlog.crew = crew

	r.Body = http.MaxBytesReader(w, r.Body, maxDispatchBody)
	var req restartRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		code, msg := http.StatusBadRequest, "malformed request body"
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			code, msg = http.StatusRequestEntityTooLarge, "request body too large"
		}
		reply(code, dispatcherResponse{Error: msg})
		return
	}

	serverPath, err := s.tmuxServerPath(r.Context())
	if err != nil {
		msg := err.Error()
		if !errors.Is(err, errNoServerPath) {
			msg = "tmux unavailable: " + msg
		}
		reply(http.StatusServiceUnavailable, dispatcherResponse{Error: msg})
		return
	}
	engines, err := s.dispatchEngines(r.Context(), serverPath)
	if err != nil {
		reply(http.StatusBadGateway, dispatcherResponse{Error: "could not list dispatcher engines: " + err.Error()})
		return
	}
	models, modelsErr := s.dispatchModelSet(r.Context(), serverPath)
	if modelsErr != nil {
		slog.Warn("dispatcher restart: model lookup failed, using built-in models", "error", modelsErr)
	}

	crewDir := filepath.Join(run.CrewBus, "crews", crew)
	oldPID := readPIDText(filepath.Join(crewDir, "pid"))
	pid, err := strconv.Atoi(oldPID)
	if err != nil {
		pid = 0
	}
	var inherited launcherSettings
	if pid > 0 {
		if argv, ok := s.launcherArgv(pid); ok {
			inherited = parseLauncherArgv(argv)
		}
	}
	valid, derr := validateDispatcher(restartSettings(req, inherited, run.Agent), engines, models)
	if derr != nil {
		reply(derr.code, dispatcherResponse{Error: derr.msg})
		return
	}
	rlog.setValidated(valid, req.Force)

	repos, err := s.dispatchRepos()
	if err != nil {
		reply(http.StatusBadGateway, dispatcherResponse{Error: "could not list repos: " + err.Error()})
		return
	}
	// CrewBus comes from the window's @git_root as tmux reports it, while a
	// known repo's commonDir is symlink-resolved.
	commonDir := filepath.Clean(filepath.Dir(run.CrewBus))
	if real, err := filepath.EvalSymlinks(commonDir); err == nil {
		commonDir = real
	}
	var repo dispatchRepo
	found := false
	for _, cand := range repos {
		if filepath.Clean(cand.commonDir) == commonDir {
			repo, found = cand, true
			break
		}
	}
	if !found {
		reply(http.StatusNotFound, dispatcherResponse{Error: "repo is not a known repo"})
		return
	}
	rlog.repo = repo.Path
	if fi, err := os.Stat(crewDir); err != nil || !fi.IsDir() {
		reply(http.StatusNotFound, dispatcherResponse{Error: "crew is not a crew of this repo"})
		return
	}
	// Without the old pid the wrapper cannot hold the new launcher back, so it
	// would register at once and an aborted kill would leave the crew on the
	// killed new pane while the old dispatcher runs on.
	if pid <= 0 {
		reply(http.StatusConflict, dispatcherResponse{Error: "the crew records no dispatcher pid — restart it from a terminal"})
		return
	}

	if !tmuxSafeArg(repo.Path) {
		reply(http.StatusUnprocessableEntity, dispatcherResponse{Error: "repo path contains characters tmux would interpret"})
		return
	}
	if s.houstonExe == "" || !tmuxSafeArg(s.houstonExe) || !tmuxSafeArg(s.launchDir) {
		reply(http.StatusInternalServerError, dispatcherResponse{Error: "houston's own executable or state path cannot be passed to tmux"})
		return
	}

	pane, code, detail := s.resolveRunPane(run)
	if code != 0 {
		reply(code, dispatcherResponse{Error: detail})
		return
	}
	if run.Tmux == nil {
		reply(http.StatusConflict, dispatcherResponse{Error: "run has no terminal"})
		return
	}
	oldPaneID, session := run.Tmux.PaneID, run.Tmux.Session
	oldServer := pane.Server
	if oldServer == "" {
		oldServer = run.Tmux.Server
	}
	rlog.oldPane = oldPaneID
	busy := dispatcherResponse{Error: restartBusyMsg, Busy: true}

	if !req.Force {
		if restartBusy(run) {
			reply(http.StatusConflict, busy)
			return
		}
		if isClaudeAgent(run.Agent) {
			capture, width, err := s.runPanes.CapturePaneWidth(pane, answerCaptureLines)
			if err != nil {
				slog.Warn("dispatcher restart: capture failed", "id", id, "pane", oldPaneID, "error", err)
				reply(http.StatusServiceUnavailable, dispatcherResponse{Error: "tmux unavailable"})
				return
			}
			if _, open := answer.PermissionPrompt(capture, width); open {
				reply(http.StatusConflict, busy)
				return
			}
		}
	}

	if !s.launcherFound(serverPath) {
		reply(http.StatusBadGateway, dispatcherResponse{Error: "the dispatcher launcher is not on the tmux server's PATH"})
		return
	}

	select {
	case s.launchSlot <- struct{}{}:
		defer func() { <-s.launchSlot }()
	default:
		reply(http.StatusTooManyRequests, dispatcherResponse{Error: "another dispatcher launch is already running"})
		return
	}
	sweepLaunchDir(s.launchDir)

	file, err := writeLaunchFile(s.launchDir, dispatcherLaunchFile{Args: dispatcherArgs(valid), CrewID: crew, WaitPID: pid})
	if err != nil {
		reply(http.StatusInternalServerError, dispatcherResponse{Error: "could not write launch file: " + err.Error()})
		return
	}

	// The new window opens first, so a window that fails to open never leaves
	// the crew without its dispatcher; the wrapper's wait_pid holds the new
	// launcher back from registering until the old one is dead, for up to
	// launchWaitLimit, which outlasts the resolve + kill-pane below. kill-pane,
	// not kill-window, ends only the engine pane (closing a single-pane
	// window). After the kill houston awaits the new registration for
	// launchCheck plus restartSlack for the old process's exit.
	end := func(ctx context.Context) *restartAbort {
		if !req.Force {
			// A run missing from the registry is no evidence its pane is gone;
			// it only skips this re-check.
			if cur, ok := s.findRun(id); ok && restartBusy(cur) {
				return &restartAbort{http.StatusConflict, busy}
			}
		}
		p, err := s.runPanes.ResolvePane(oldPaneID)
		if errors.Is(err, tmux.ErrPaneNotFound) {
			oldEnded = true
			return nil
		}
		if err != nil {
			slog.Warn("dispatcher restart: resolve old pane failed", "id", id, "pane", oldPaneID, "error", err)
			return &restartAbort{http.StatusServiceUnavailable, dispatcherResponse{Error: "tmux unavailable"}}
		}
		if tmux.ServerMismatch(oldServer, p.Server) {
			return &restartAbort{http.StatusConflict, dispatcherResponse{Error: "terminal pane belongs to a different tmux server"}}
		}
		if _, err := s.tmuxRun(ctx, []string{"kill-pane", "-t", oldPaneID}); err != nil && !tmux.IsGoneMessage(err.Error()) {
			return &restartAbort{http.StatusBadGateway, dispatcherResponse{Error: "could not end the old dispatcher session: " + err.Error()}}
		}
		oldEnded = true
		return nil
	}
	// A launcher still waiting on an old pid that outlived its pane cannot
	// register; reporting it as merely starting would hide that.
	oldOutlived := func(context.Context) *restartAbort {
		if !s.pidAlive(pid) {
			return nil
		}
		return &restartAbort{http.StatusBadGateway, dispatcherResponse{Error: "the old dispatcher process is still running after its pane was ended — the new launcher could not register; stop pid " + strconv.Itoa(pid) + " and relaunch with CREW_ID=" + crew + " dispatcher"}}
	}

	// Detached from the request: a dropped phone connection must not abort a
	// half-made window or skip its cleanup.
	ctx := context.WithoutCancel(r.Context())
	code, body, newPane := s.openDispatcherWindow(ctx, windowLaunch{
		repo: repo, serverPath: serverPath, crewID: crew, crewDir: crewDir,
		prevPID: oldPID, file: file, session: session,
		wait: s.launchCheck + s.restartSlack, afterOpen: end, onStarting: oldOutlived,
	})
	rlog.newPane = newPane
	reply(code, body)
}

// restartLog accumulates one restart's outcome for a single slog.Info call.
// Settings are logged only once validated.
type restartLog struct {
	id, crew, repo, oldPane, newPane string
	status                           int

	validated             bool
	engine, model, effort string
	force                 bool
}

func (l *restartLog) setValidated(req dispatcherRequest, force bool) {
	l.validated = true
	l.engine, l.model, l.effort, l.force = req.Engine, req.Model, req.Effort, force
}

func (l *restartLog) emit(start time.Time) {
	args := []any{"id", l.id, "status", l.status, "duration", time.Since(start)}
	if l.crew != "" {
		args = append(args, "crew", l.crew)
	}
	if l.validated {
		args = append(args, "engine", l.engine, "model", l.model, "effort", l.effort, "force", l.force)
	}
	if l.repo != "" {
		args = append(args, "repo", l.repo)
	}
	if l.oldPane != "" {
		args = append(args, "old_pane", l.oldPane)
	}
	if l.newPane != "" {
		args = append(args, "new_pane", l.newPane)
	}
	slog.Info("dispatcher restart", args...)
}
