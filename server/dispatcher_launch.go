package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/noamsto/houston/runs"
	"github.com/noamsto/houston/tmux"
)

const (
	maxDispatcherTasks  = 20
	maxTaskRunes        = 2000
	maxDispatcherPrompt = 8 << 10

	dispatcherLaunchDirName = "launch"
	launchFileMaxAge        = 10 * time.Minute

	tmuxTimeout     = 10 * time.Second
	paneTmuxTimeout = 2 * time.Second

	// launchFormat is what new-window/new-session print with -P, so the
	// handler learns where the dispatcher landed without a second lookup.
	launchFormat = "#{session_name}\t#{window_id}\t#{pane_id}"
)

// launchStrippedEnv are keys the wrapper drops before exec: the minted crew
// always wins over a tmux-global CREW_ID, and no worker or role identity or
// spec leaks into a dispatcher.
var launchStrippedEnv = []string{"CREW_ID", "CREW_WORKER_ID", "DISPATCH_SPEC", "CREW_ROLE_ID"}

// dispatcherRequest is the POST /api/dispatch/dispatcher body.
type dispatcherRequest struct {
	Repo   string   `json:"repo"`
	Tasks  []string `json:"tasks"`
	Engine string   `json:"engine"`
	Model  string   `json:"model"`
	Effort string   `json:"effort"`
}

type dispatcherResponse struct {
	Error   string `json:"error,omitempty"`
	Output  string `json:"output,omitempty"`
	Crew    string `json:"crew,omitempty"`
	Session string `json:"session,omitempty"`
	Window  string `json:"window,omitempty"`
	Pane    string `json:"pane,omitempty"`
	RunID   string `json:"run_id,omitempty"`
}

// dispatcherLaunchFile is how task text reaches the launcher: through a 0600
// file the wrapper reads, never through tmux argv, where a trailing ";"
// starts a new tmux command and "#" is format-expanded.
type dispatcherLaunchFile struct {
	Args   []string `json:"args"`
	CrewID string   `json:"crew_id"`
}

// tmuxRunner runs one tmux command with no shell, adding env (KEY=value) to
// houston's own environment. Stdout comes back trimmed; an error carries
// tmux's stderr.
type tmuxRunner func(ctx context.Context, args []string, env ...string) (string, error)

var errNoServerPath = errors.New("the tmux server has no global PATH")

// normalizeTask turns every run of whitespace (newlines included) into one
// space, so a multi-line textarea row becomes a one-line prompt.
func normalizeTask(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// validateDispatcher normalizes the tasks, drops empty ones, and checks every
// field. Repo membership is checked by the handler.
func validateDispatcher(req dispatcherRequest, engines []string) (dispatcherRequest, *dispatchError) {
	out := req

	var enabled []string
	for _, e := range dispatchEngineOrder {
		if slices.Contains(engines, e) {
			enabled = append(enabled, e)
		}
	}
	if !slices.Contains(enabled, out.Engine) {
		if len(enabled) == 0 {
			return out, &dispatchError{"engine", http.StatusBadRequest, "no dispatcher engine is enabled on this host"}
		}
		return out, &dispatchError{"engine", http.StatusBadRequest, "engine must be one of " + strings.Join(enabled, ", ")}
	}
	if out.Model != "" && !slices.Contains(dispatchModels[out.Engine], out.Model) {
		return out, &dispatchError{"model", http.StatusBadRequest, "model is not valid for engine " + out.Engine}
	}
	if out.Effort != "" && !slices.Contains(dispatchEfforts, out.Effort) {
		return out, &dispatchError{"effort", http.StatusBadRequest, "effort must be one of " + strings.Join(dispatchEfforts, ", ")}
	}
	if len(req.Tasks) > maxDispatcherTasks {
		return out, &dispatchError{"tasks", http.StatusBadRequest, "at most " + strconv.Itoa(maxDispatcherTasks) + " tasks"}
	}

	out.Tasks = make([]string, 0, len(req.Tasks))
	for _, raw := range req.Tasks {
		task := normalizeTask(raw)
		if task == "" {
			continue
		}
		n := "task " + strconv.Itoa(len(out.Tasks)+1)
		switch {
		case utf8.RuneCountInString(task) > maxTaskRunes:
			return out, &dispatchError{"tasks", http.StatusBadRequest, n + " must be at most " + strconv.Itoa(maxTaskRunes) + " characters"}
		case strings.ContainsFunc(task, isCcRune):
			return out, &dispatchError{"tasks", http.StatusBadRequest, n + " must not contain control characters"}
		case strings.HasPrefix(task, "-"):
			// The launcher consumes --agent/--model/--effort anywhere in argv.
			return out, &dispatchError{"tasks", http.StatusBadRequest, n + " must not start with -"}
		}
		out.Tasks = append(out.Tasks, task)
	}
	if len(composeDispatcherPrompt(out.Tasks)) > maxDispatcherPrompt {
		return out, &dispatchError{"tasks", http.StatusBadRequest, "tasks together must be at most 8 KiB"}
	}
	return out, nil
}

func isCcRune(r rune) bool { return unicode.Is(unicode.Cc, r) }

// composeDispatcherPrompt joins tasks into the launcher's first prompt. It
// stays one line because claude and pi also use it as the session name.
func composeDispatcherPrompt(tasks []string) string {
	switch len(tasks) {
	case 0:
		return ""
	case 1:
		return tasks[0]
	}
	var b strings.Builder
	b.WriteString(strconv.Itoa(len(tasks)) + " tasks:")
	for i, t := range tasks {
		b.WriteString(" (" + strconv.Itoa(i+1) + ") " + t)
	}
	return b.String()
}

// tmuxSafeArg reports whether tmux would take s literally: an element ending
// in ";" ends the tmux command, and "#" is format-expanded in -c.
func tmuxSafeArg(s string) bool {
	return !strings.ContainsRune(s, '#') && !strings.HasSuffix(s, ";") && !strings.ContainsFunc(s, isCcRune)
}

func sanitizeSessionName(name string) string {
	if name == "" {
		return "repo"
	}
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, name)
}

func dispatcherArgs(req dispatcherRequest) []string {
	args := []string{"--agent", req.Engine}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if prompt := composeDispatcherPrompt(req.Tasks); prompt != "" {
		args = append(args, prompt)
	}
	return args
}

func execTmux(ctx context.Context, args []string, env ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, tmuxTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "tmux", args...) //nolint:gosec // no shell; every element passed tmuxSafeArg or is a server constant
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("tmux %s: %w: %s", args[0], err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("tmux %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

func writeLaunchFile(dir string, lf dispatcherLaunchFile) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := json.Marshal(lf)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "dispatcher-*.json")
	if err != nil {
		return "", err
	}
	_, werr := f.Write(b)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// sweepLaunchDir removes launch files no wrapper ever read (say the houston
// binary was garbage-collected), so task text doesn't linger on disk.
func sweepLaunchDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() || time.Since(info.ModTime()) < launchFileMaxAge {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
			slog.Warn("dispatcher launch: stale launch file not removed", "error", err)
		}
	}
}

type launchState int

const (
	launchRegistered launchState = iota
	launchStarting
	launchDead
	launchGone
	launchUnchecked
)

// awaitDispatcher polls until the launcher registers (crew register writes
// <crew dir>/pid), its pane dies, or launchCheck passes. A dead pane lingers
// only because the wrapper sets remain-on-exit failed. detail is a dead
// pane's exit status, or why the pane could not be checked.
func (s *Server) awaitDispatcher(ctx context.Context, crewDir, pane string) (state launchState, detail string) {
	deadline := time.Now().Add(s.launchCheck)
	for {
		if _, err := os.Stat(filepath.Join(crewDir, "pid")); err == nil {
			return launchRegistered, ""
		}
		out, err := s.tmuxRun(ctx, []string{"display-message", "-p", "-t", pane, "#{pane_dead} #{pane_dead_status}"})
		if err != nil {
			if tmux.IsGoneMessage(err.Error()) {
				return launchGone, ""
			}
			return launchUnchecked, err.Error()
		}
		// A live server that no longer knows the pane exits 0 with every
		// pane field blank.
		dead, status, _ := strings.Cut(out, " ")
		switch dead {
		case "":
			return launchGone, ""
		case "1":
			return launchDead, status
		}
		if time.Now().After(deadline) {
			return launchStarting, ""
		}
		time.Sleep(s.launchPoll)
	}
}

// launchSession picks where the dispatcher window goes: the session holding
// the most windows of this repo (ties by name), else an existing session
// named after the repo. exists is false when tmux must create it; err means
// tmux couldn't say whether it exists.
func (s *Server) launchSession(ctx context.Context, repo dispatchRepo) (session string, exists bool, err error) {
	wins, err := s.wsTmux.ListWindowOptions()
	if err != nil {
		slog.Warn("dispatcher launch: list windows failed", "error", err)
	}

	counts := make(map[string]int)
	ours := make(map[string]bool) // @git_root -> belongs to repo
	for _, win := range wins {
		if win.GitRoot == "" || !tmuxSafeArg(win.Session) {
			continue
		}
		mine, seen := ours[win.GitRoot]
		if !seen {
			path, ok := dispatchRepoPath(s.launchCommonDir, win.GitRoot)
			mine = ok && path == repo.Path
			ours[win.GitRoot] = mine
		}
		if mine {
			counts[win.Session]++
		}
	}
	best := ""
	for sess, n := range counts {
		if best == "" || n > counts[best] || (n == counts[best] && sess < best) {
			best = sess
		}
	}
	if best != "" {
		return best, true, nil
	}

	name := sanitizeSessionName(filepath.Base(repo.Path))
	_, err = s.tmuxRun(ctx, []string{"has-session", "-t", "=" + name})
	switch {
	case err == nil:
		return name, true, nil
	case strings.Contains(err.Error(), "can't find session"):
		return name, false, nil
	}
	return "", false, err
}

// handleDispatcherLaunch starts a dispatcher in a new tmux window.
//
//	POST /api/dispatch/dispatcher {"repo","tasks","engine","model","effort"}
//
// tmux runs `houston launch-dispatcher <file>`; the launcher's flags and the
// task text travel in that file, so nothing user-typed reaches tmux argv.
func (s *Server) handleDispatcherLaunch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	llog := &launchLog{}
	defer llog.emit(start)
	reply := func(code int, body dispatcherResponse) {
		llog.status = code
		writeDispatchJSON(w, code, body)
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDispatchBody)
	var req dispatcherRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		code, msg := http.StatusBadRequest, "malformed request body"
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			code, msg = http.StatusRequestEntityTooLarge, "request body too large"
		}
		reply(code, dispatcherResponse{Error: msg})
		return
	}

	// Fail closed: under houston.service, houston's own PATH has no
	// dispatcher, claude or pi, so neither the engine list nor the pane may
	// fall back to it.
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
	valid, derr := validateDispatcher(req, engines)
	if derr != nil {
		llog.field = derr.field
		reply(derr.code, dispatcherResponse{Error: derr.msg})
		return
	}

	repos, err := s.dispatchRepos()
	if err != nil {
		reply(http.StatusBadGateway, dispatcherResponse{Error: "could not list repos: " + err.Error()})
		return
	}
	repoPath := filepath.Clean(valid.Repo)
	var repo dispatchRepo
	found := false
	for _, cand := range repos {
		if cand.Path == repoPath {
			repo, found = cand, true
			break
		}
	}
	if !found {
		reply(http.StatusNotFound, dispatcherResponse{Error: "repo is not a known repo"})
		return
	}
	llog.setValidated(valid, repo.Path)

	if !tmuxSafeArg(repo.Path) {
		reply(http.StatusUnprocessableEntity, dispatcherResponse{Error: "repo path contains characters tmux would interpret"})
		return
	}
	if s.houstonExe == "" || !tmuxSafeArg(s.houstonExe) || !tmuxSafeArg(s.launchDir) {
		reply(http.StatusInternalServerError, dispatcherResponse{Error: "houston's own executable or state path cannot be passed to tmux"})
		return
	}

	select {
	case s.launchSlot <- struct{}{}:
		defer func() { <-s.launchSlot }()
	default:
		reply(http.StatusTooManyRequests, dispatcherResponse{Error: "another dispatcher launch is already running"})
		return
	}
	// Files kept on a 502 would otherwise wait for the next houston start.
	sweepLaunchDir(s.launchDir)

	id := s.dispatchNewCrewID()
	crewDir, merr := mintDispatchCrew(repo.commonDir, id)
	if merr != nil {
		reply(merr.code, dispatcherResponse{Error: merr.msg})
		return
	}
	llog.crew = id

	file, err := writeLaunchFile(s.launchDir, dispatcherLaunchFile{Args: dispatcherArgs(valid), CrewID: id})
	if err != nil {
		_ = os.Remove(crewDir)
		reply(http.StatusInternalServerError, dispatcherResponse{Error: "could not write launch file: " + err.Error()})
		return
	}

	// Detached from the request: a dropped phone connection must not abort a
	// half-made window or skip the cleanup below.
	ctx := context.WithoutCancel(r.Context())

	session, exists, err := s.launchSession(ctx, repo)
	if err != nil {
		_ = os.Remove(file)
		_ = os.Remove(crewDir)
		reply(http.StatusServiceUnavailable, dispatcherResponse{Error: "tmux unavailable: " + err.Error()})
		return
	}
	// -E: tmux would otherwise copy houston's (service) values of every
	// update-environment variable into the new session, or mark the absent
	// ones removed, shadowing the server's global SSH_AUTH_SOCK, DISPLAY etc.
	argv := []string{"new-session", "-d", "-E", "-P", "-F", launchFormat, "-s", session}
	if exists {
		// -d: never switch the session's current window. The launcher stamps
		// its own window by pane id (`-t "$TMUX_PANE"`), and houston pre-stamps
		// the new window the same way, so the window carries its tags without
		// becoming current and switching a client that is viewing the session.
		argv = []string{"new-window", "-d", "-P", "-F", launchFormat, "-t", "=" + session + ":"}
	}
	argv = append(argv, "-n", "dispatcher", "-c", repo.Path, "--", s.houstonExe, "launch-dispatcher", file)

	// tmux hands a pane created by a session-less client that client's PATH,
	// over the global environment and even over -e PATH.
	out, err := s.tmuxRun(ctx, argv, "PATH="+serverPath)
	if err != nil {
		_ = os.Remove(file)
		_ = os.Remove(crewDir)
		reply(http.StatusBadGateway, dispatcherResponse{Error: "tmux could not start the dispatcher window: " + err.Error()})
		return
	}
	parts := strings.Split(out, "\t")
	if len(parts) != 3 {
		reply(http.StatusBadGateway, dispatcherResponse{Error: "tmux started a window but printed no pane for it", Output: out, Crew: id})
		return
	}
	created, window, pane := parts[0], parts[1], parts[2]
	llog.pane = pane

	state, detail := s.awaitDispatcher(ctx, crewDir, pane)
	switch state {
	case launchDead:
		output, err := s.tmuxRun(ctx, []string{"capture-pane", "-p", "-J", "-t", pane, "-S", "-40"})
		if err != nil {
			slog.Warn("dispatcher launch: capture failed", "pane", pane, "error", err)
		}
		kill := []string{"kill-window", "-t", window}
		if !exists {
			kill = []string{"kill-session", "-t", "=" + created}
		}
		if _, err := s.tmuxRun(ctx, kill); err != nil {
			slog.Warn("dispatcher launch: cleanup failed", "pane", pane, "error", err)
		}
		msg := "dispatcher exited"
		if detail != "" {
			msg += " with status " + detail
		}
		reply(http.StatusUnprocessableEntity, dispatcherResponse{Error: msg, Output: output, Crew: removeLaunchLeftovers(file, crewDir, id)})
	case launchGone:
		reply(http.StatusUnprocessableEntity, dispatcherResponse{Error: "dispatcher exited immediately", Crew: removeLaunchLeftovers(file, crewDir, id)})
	case launchUnchecked:
		// The window may still be running the launcher, which may not have
		// read its file yet: keep both, and the crew.
		reply(http.StatusBadGateway, dispatcherResponse{Error: "could not check the dispatcher pane: " + detail, Crew: id, Session: created, Window: window, Pane: pane})
	default:
		if _, err := s.repoReg.Add(repo.Path); err != nil {
			slog.Debug("dispatcher launch: repo not remembered", "repo", repo.Path, "reason", err)
		}
		reply(http.StatusOK, dispatcherResponse{Crew: id, Session: created, Window: window, Pane: pane, RunID: runs.PaneRunID(pane)})
	}
}

// removeLaunchLeftovers drops the launch file (the wrapper may never have
// read it) and the crew dir if the launcher left it empty, returning the crew
// id only when its dir still exists.
func removeLaunchLeftovers(file, crewDir, id string) string {
	_ = os.Remove(file)
	if err := os.Remove(crewDir); err == nil || errors.Is(err, os.ErrNotExist) {
		return ""
	}
	return id
}

// launchLog accumulates one request's outcome for a single slog.Info call.
// Task text is never logged, only its count and the prompt's size.
type launchLog struct {
	validated bool
	field     string

	repo, engine, model, effort, crew, pane string
	tasks, promptBytes, status              int
}

func (l *launchLog) setValidated(req dispatcherRequest, repoPath string) {
	l.validated = true
	l.repo, l.engine, l.model, l.effort = repoPath, req.Engine, req.Model, req.Effort
	l.tasks, l.promptBytes = len(req.Tasks), len(composeDispatcherPrompt(req.Tasks))
}

func (l *launchLog) emit(start time.Time) {
	args := []any{"status", l.status, "duration", time.Since(start)}
	switch {
	case l.validated:
		args = append(args,
			"repo", l.repo, "engine", l.engine, "model", l.model, "effort", l.effort,
			"tasks", l.tasks, "prompt_bytes", l.promptBytes, "crew", l.crew, "pane", l.pane,
		)
	case l.field != "":
		args = append(args, "field", l.field)
	}
	slog.Info("dispatcher launch", args...)
}

// ExecDispatcherLaunch is `houston launch-dispatcher <file>`, run by tmux in
// the new pane with the tmux server's environment. It resolves `dispatcher`
// on that PATH and replaces itself with it, so it returns only on failure.
func ExecDispatcherLaunch(file string) error {
	pane := os.Getenv("TMUX_PANE")
	// Keep a failed launch's pane (and its error) for houston's startup check
	// and the user; a clean exit still closes it.
	paneTmux(pane, "set-option", "-w", "-t", pane, "remain-on-exit", "failed")

	b, err := os.ReadFile(file) //nolint:gosec // the path houston itself passed in tmux argv
	if err != nil {
		return err
	}
	if err := os.Remove(file); err != nil {
		return err
	}
	var lf dispatcherLaunchFile
	if err := json.Unmarshal(b, &lf); err != nil {
		return fmt.Errorf("parse %s: %w", file, err)
	}
	launcher, err := exec.LookPath("dispatcher")
	if err != nil {
		return fmt.Errorf("dispatcher launcher not found on the tmux server's PATH: %w", err)
	}

	// Stamp by pane id: houston creates the window with -d, so it is not the
	// session's current window, and an untargeted stamp would land on the
	// client's. The launcher targets its own window too (dispatcher#655); a
	// launcher predating #655 stamps untargeted and would relabel whichever
	// window is current, so houston and #655 ship in the same bump. This
	// pre-stamp is the belt-and-braces that keeps the new window tagged.
	paneTmux(pane, "set-option", "-w", "-t", pane, "@crew_name", "dispatcher")
	paneTmux(pane, "set-option", "-w", "-t", pane, "@crew_color", "colour99")

	return syscall.Exec(launcher, append([]string{launcher}, lf.Args...), launchEnv(os.Environ(), lf.CrewID)) //nolint:gosec // the launcher on the tmux server's own PATH
}

// paneTmux runs one best-effort tmux command about the wrapper's own pane; it
// does nothing outside tmux.
func paneTmux(pane string, args ...string) {
	if pane == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), paneTmuxTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, "tmux", args...).Run() //nolint:gosec // fixed argv; pane is tmux's own $TMUX_PANE
}

func launchEnv(environ []string, crewID string) []string {
	env := make([]string, 0, len(environ)+1)
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(launchStrippedEnv, key) {
			env = append(env, kv)
		}
	}
	return append(env, "CREW_ID="+crewID)
}

// tmuxServerPath is the tmux server's global PATH. The value is never logged.
func (s *Server) tmuxServerPath(ctx context.Context) (string, error) {
	out, err := s.tmuxRun(ctx, []string{"show-environment", "-g", "PATH"})
	if err != nil {
		if strings.Contains(err.Error(), "unknown variable") {
			return "", errNoServerPath
		}
		return "", err
	}
	path, ok := strings.CutPrefix(out, "PATH=")
	if !ok || path == "" {
		return "", errNoServerPath
	}
	return path, nil
}

// dispatchServerPath is tmuxServerPath for the dispatch CLI, which keeps
// houston's own PATH after it: when tmux can't say, it warns and returns "",
// leaving dispatch with houston's PATH alone.
func (s *Server) dispatchServerPath(ctx context.Context, endpoint string) string {
	path, err := s.tmuxServerPath(ctx)
	if err != nil {
		slog.Warn(endpoint+": tmux server PATH unavailable, dispatch gets houston's alone", "error", err)
		return ""
	}
	return path
}
