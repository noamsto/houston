package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxDispatchBody       = 256 << 10
	maxDispatchSpec       = 64 << 10
	dispatchTitleMaxRunes = 200
	dispatchTimeout       = 120 * time.Second

	// dispatchNewCrew is the sentinel that tells handleDispatch to mint a
	// crew id itself rather than requiring one of dispatchCrewRe's shape.
	dispatchNewCrew = "new"
)

// dispatchTiers, dispatchEfforts and dispatchPlans are closed enums dispatch
// itself enforces; validateDispatch rejects anything else before it ever
// reaches argv.
var (
	dispatchTiers   = []string{"trivial", "standard", "deep"}
	dispatchEfforts = []string{"low", "medium", "high", "xhigh", "max"}
	dispatchPlans   = []string{"required", "provided"}

	// dispatchEngineOrder is the options endpoint's display order — a JSON
	// object (dispatchModels) has none of its own.
	dispatchEngineOrder = []string{"claude", "codex", "cursor", "pi"}

	// dispatchModels is the union of dispatch's own tier-map rows per engine;
	// update it when that map changes. Tier↔model fit is left to dispatch.
	dispatchModels = map[string][]string{
		"claude": {"opus", "sonnet", "haiku", "fable"},
		"codex":  {"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"},
		"cursor": {"kimi-k3-high", "cursor-grok-4.6-high", "cursor-grok-4.6-medium", "cursor-grok-4.6-low", "composer-2.5"},
		"pi": {
			"openrouter/deepseek/deepseek-v4-pro",
			"openrouter/deepseek/deepseek-v4.1-flash",
			"openrouter/deepseek/deepseek-v4-flash",
		},
	}

	// dispatchTierModels is the default model per engine+tier, mirroring
	// dispatch's own tier-map rows — the form's starting point only; tier↔model
	// fit is still dispatch's call, not enforced here. Keep in step with
	// dispatchModels when dispatch's tier map changes.
	dispatchTierModels = map[string]map[string]string{
		"claude": {"trivial": "haiku", "standard": "sonnet", "deep": "opus"},
		"codex":  {"trivial": "gpt-5.6-luna", "standard": "gpt-5.6-terra", "deep": "gpt-5.6-sol"},
		"cursor": {"trivial": "cursor-grok-4.6-low", "standard": "cursor-grok-4.6-medium", "deep": "kimi-k3-high"},
		"pi": {
			"trivial":  "openrouter/deepseek/deepseek-v4-flash",
			"standard": "openrouter/deepseek/deepseek-v4.1-flash",
			"deep":     "openrouter/deepseek/deepseek-v4-pro",
		},
	}
)

var (
	dispatchCrewRe  = regexp.MustCompile(`^[0-9]{1,20}-[0-9]{1,10}$`)
	dispatchIssueRe = regexp.MustCompile(`^([0-9]{1,9}|[A-Z]{2,10}-[0-9]{1,9})$`)
	// dispatchIssueShapedTitleRe matches what dispatch's own leading-options
	// loop would consume as an issue rather than a title.
	dispatchIssueShapedTitleRe = regexp.MustCompile(`^(#?[0-9]+|[A-Z]{2,}-[0-9]+)$`)
	dispatchWorkerIDRe         = regexp.MustCompile(`^worker_id:\s*(\S+)\s*$`)
	dispatchIssueURLRe         = regexp.MustCompile(`https://github\.com/\S+/issues/[0-9]+`)
)

// dispatchRequest is the POST /api/dispatch body.
type dispatchRequest struct {
	Repo   string `json:"repo"`
	Title  string `json:"title"`
	Spec   string `json:"spec"`
	Tier   string `json:"tier"`
	Engine string `json:"engine"`
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Plan   string `json:"plan"`
	Crew   string `json:"crew"`
	Issue  string `json:"issue"`
}

// dispatchRepo is one repo houston knows about, offered by the options
// endpoint and matched against on submit.
type dispatchRepo struct {
	Path  string   `json:"path"`
	Name  string   `json:"name"`
	Crews []string `json:"crews"` // newest first, never nil

	// commonDir is <repo>/.git (or a linked worktree's shared common dir),
	// used to check crew membership. Not exposed to the client.
	commonDir string
}

// dispatchOptions is the GET /api/dispatch/options body — the form's source
// of truth so the UI never hardcodes an allowlist the server could disagree
// with.
type dispatchOptions struct {
	Repos       []dispatchRepo               `json:"repos"`
	Tiers       []string                     `json:"tiers"`
	Efforts     []string                     `json:"efforts"`
	Plans       []string                     `json:"plans"`
	Engines     map[string][]string          `json:"engines"`
	EngineOrder []string                     `json:"engine_order"`
	TierModels  map[string]map[string]string `json:"tier_models"`
}

// dispatchResponse covers every documented response shape: an error alone, a
// success (worker_id/branch/issue_url/output), or a 422/502/504 that carries
// output or a partial worker_id alongside the error.
type dispatchResponse struct {
	Error    string `json:"error,omitempty"`
	WorkerID string `json:"worker_id,omitempty"`
	Branch   string `json:"branch,omitempty"`
	IssueURL string `json:"issue_url,omitempty"`
	Output   string `json:"output,omitempty"`
	Crew     string `json:"crew,omitempty"`
}

// dispatchError is a validation failure. msg names the field and the reason
// because the UI shows it verbatim; field lets the log name what failed
// without repeating the rejected value.
type dispatchError struct {
	field string
	code  int
	msg   string
}

func (e *dispatchError) Error() string { return e.msg }

// validateDispatch trims the title, defaults plan, and checks every field
// against a closed enum or an anchored regex. Repo and crew membership are
// checked by the handler.
func validateDispatch(req dispatchRequest) (dispatchRequest, *dispatchError) {
	out := req
	out.Title = strings.TrimSpace(req.Title)
	if out.Plan == "" {
		out.Plan = "required"
	}

	if !slices.Contains(dispatchTiers, out.Tier) {
		return out, &dispatchError{"tier", http.StatusBadRequest, "tier must be one of " + strings.Join(dispatchTiers, ", ")}
	}
	if !slices.Contains(dispatchEfforts, out.Effort) {
		return out, &dispatchError{"effort", http.StatusBadRequest, "effort must be one of " + strings.Join(dispatchEfforts, ", ")}
	}
	if !slices.Contains(dispatchPlans, out.Plan) {
		return out, &dispatchError{"plan", http.StatusBadRequest, "plan must be one of " + strings.Join(dispatchPlans, ", ")}
	}
	models, ok := dispatchModels[out.Engine]
	if !ok {
		return out, &dispatchError{"engine", http.StatusBadRequest, "engine must be one of " + strings.Join(dispatchEngineOrder, ", ")}
	}
	if !slices.Contains(models, out.Model) {
		return out, &dispatchError{"model", http.StatusBadRequest, "model is not valid for engine " + out.Engine}
	}

	switch {
	case out.Title == "":
		return out, &dispatchError{"title", http.StatusBadRequest, "title must not be empty"}
	case utf8.RuneCountInString(out.Title) > dispatchTitleMaxRunes:
		return out, &dispatchError{"title", http.StatusBadRequest, "title must be at most " + strconv.Itoa(dispatchTitleMaxRunes) + " characters"}
	case strings.HasPrefix(out.Title, "-"):
		return out, &dispatchError{"title", http.StatusBadRequest, "title must not start with -"}
	case out.Title == "resume":
		return out, &dispatchError{"title", http.StatusBadRequest, `title must not equal "resume"`}
	case dispatchIssueShapedTitleRe.MatchString(out.Title):
		return out, &dispatchError{"title", http.StatusBadRequest, "title must not look like a GitHub issue or Linear id"}
	case dispatchHasControlRune(out.Title):
		return out, &dispatchError{"title", http.StatusBadRequest, "title must not contain control or formatting characters"}
	case !dispatchHasASCIIAlnum(out.Title):
		return out, &dispatchError{"title", http.StatusBadRequest, "title needs at least one Latin letter or digit for the branch name"}
	}

	if out.Issue != "" && !dispatchIssueRe.MatchString(out.Issue) {
		return out, &dispatchError{"issue", http.StatusBadRequest, "issue must be a GitHub issue number or a Linear id"}
	}
	// Required, not optional: the fallback would be houston's own inherited
	// $CREW_ID — whichever crew launched houston, possibly another repo's.
	if out.Crew != dispatchNewCrew && !dispatchCrewRe.MatchString(out.Crew) {
		return out, &dispatchError{"crew", http.StatusBadRequest, `crew is required and must be "new" or look like <unix>-<pid>`}
	}

	return out, nil
}

func dispatchHasControlRune(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Cc, unicode.Cf) {
			return true
		}
	}
	return false
}

func dispatchHasASCIIAlnum(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return true
		}
	}
	return false
}

// listDispatchRepos computes the known-repo set fresh per request from the
// same tmux snapshot /api/workspace rolls up: every distinct @git_root
// resolved to its git common dir, collapsing a repo's worktrees to one entry.
func listDispatchRepos(lister workspaceLister, commonDir func(root string) (string, error)) ([]dispatchRepo, error) {
	wins, err := lister.ListWindowOptions()
	if err != nil {
		return nil, err
	}

	seenRoots := make(map[string]bool)
	byPath := make(map[string]string) // repo path -> common dir
	for _, win := range wins {
		root := win.GitRoot
		if root == "" || seenRoots[root] {
			continue
		}
		seenRoots[root] = true

		dir, err := commonDir(root)
		if err != nil {
			continue
		}
		if filepath.Base(dir) != ".git" {
			// Bare repos and anything else without a normal "<repo>/.git"
			// layout have no directory dispatch could run against.
			continue
		}
		repo := filepath.Dir(dir)
		if _, ok := byPath[repo]; ok {
			continue
		}
		byPath[repo] = dir
	}

	repos := make([]dispatchRepo, 0, len(byPath))
	for repo, dir := range byPath {
		repos = append(repos, dispatchRepo{
			Path:      repo,
			Name:      filepath.Base(repo),
			Crews:     listDispatchCrews(dir),
			commonDir: dir,
		})
	}
	sort.Slice(repos, func(i, j int) bool {
		if repos[i].Name != repos[j].Name {
			return repos[i].Name < repos[j].Name
		}
		return repos[i].Path < repos[j].Path
	})
	return repos, nil
}

// listDispatchCrews lists <commonDir>/crew/crews/*, newest first by the
// numeric prefix of the <unix>-<pid> id. A missing directory is not an
// error — it just means the repo has no crew yet.
func listDispatchCrews(commonDir string) []string {
	entries, err := os.ReadDir(filepath.Join(commonDir, "crew", "crews"))
	if err != nil {
		return []string{}
	}

	crews := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() && dispatchCrewRe.MatchString(e.Name()) {
			crews = append(crews, e.Name())
		}
	}
	sort.Slice(crews, func(i, j int) bool {
		ni, nj := dispatchCrewUnixPrefix(crews[i]), dispatchCrewUnixPrefix(crews[j])
		if ni != nj {
			return ni > nj
		}
		return crews[i] > crews[j]
	})
	return crews
}

func dispatchCrewUnixPrefix(id string) int64 {
	i := strings.IndexByte(id, '-')
	if i < 0 {
		return 0
	}
	n, _ := strconv.ParseInt(id[:i], 10, 64)
	return n
}

// handleDispatchOptions serves the form's source of truth.
//
//	GET /api/dispatch/options → dispatchOptions
func (s *Server) handleDispatchOptions(w http.ResponseWriter, _ *http.Request) {
	repos, err := s.dispatchRepos()
	if err != nil {
		slog.Error("dispatch options: list repos failed", "error", err)
		writeDispatchJSON(w, http.StatusBadGateway, dispatchResponse{Error: "could not list repos: " + err.Error()})
		return
	}

	writeDispatchJSON(w, http.StatusOK, dispatchOptions{
		Repos:       repos,
		Tiers:       dispatchTiers,
		Efforts:     dispatchEfforts,
		Plans:       dispatchPlans,
		Engines:     dispatchModels,
		EngineOrder: dispatchEngineOrder,
		TierModels:  dispatchTierModels,
	})
}

// handleDispatch launches a worker through the host's dispatch CLI.
//
//	POST /api/dispatch {"repo","title","spec","tier","engine","model","effort","plan","crew","issue"}
//
// Every value that reaches argv is either a server constant or matched a
// closed enum / anchored regex in validateDispatch; the only free text
// (title) is a single argv element that cannot start with "-" and cannot
// equal "resume".
func (s *Server) handleDispatch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	dlog := &dispatchLog{}
	defer dlog.emit(start)

	r.Body = http.MaxBytesReader(w, r.Body, maxDispatchBody)
	var req dispatchRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		code, msg := http.StatusBadRequest, "malformed request body"
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			code, msg = http.StatusRequestEntityTooLarge, "request body too large"
		}
		dlog.status = code
		writeDispatchJSON(w, code, dispatchResponse{Error: msg})
		return
	}
	dlog.specBytes = len(req.Spec)

	if len(req.Spec) > maxDispatchSpec {
		dlog.status = http.StatusRequestEntityTooLarge
		writeDispatchJSON(w, http.StatusRequestEntityTooLarge, dispatchResponse{Error: "spec too large"})
		return
	}

	valid, derr := validateDispatch(req)
	if derr != nil {
		dlog.field = derr.field
		dlog.status = derr.code
		writeDispatchJSON(w, derr.code, dispatchResponse{Error: derr.msg})
		return
	}

	repos, err := s.dispatchRepos()
	if err != nil {
		dlog.status = http.StatusBadGateway
		writeDispatchJSON(w, http.StatusBadGateway, dispatchResponse{Error: "could not list repos: " + err.Error()})
		return
	}

	repoPath := filepath.Clean(valid.Repo)
	repoIdx := slices.IndexFunc(repos, func(r dispatchRepo) bool { return r.Path == repoPath })
	if repoIdx < 0 {
		dlog.status = http.StatusNotFound
		writeDispatchJSON(w, http.StatusNotFound, dispatchResponse{Error: "repo is not a known repo"})
		return
	}
	repo := repos[repoIdx]

	// Everything below is validated (bounded, enum, or anchored-regex) and
	// repo is now a known path, so it's safe to log in full.
	dlog.setValidated(valid, repo.Path)

	if valid.Crew != dispatchNewCrew {
		crewDir := filepath.Join(repo.commonDir, "crew", "crews", valid.Crew)
		if info, err := os.Stat(crewDir); err != nil || !info.IsDir() {
			dlog.status = http.StatusNotFound
			writeDispatchJSON(w, http.StatusNotFound, dispatchResponse{Error: "crew is not a crew of this repo"})
			return
		}
	}

	select {
	case s.dispatchSlot <- struct{}{}:
		defer func() { <-s.dispatchSlot }()
	default:
		dlog.status = http.StatusTooManyRequests
		writeDispatchJSON(w, http.StatusTooManyRequests, dispatchResponse{Error: "another dispatch is already running"})
		return
	}

	// minted is the crew leaf this request created, if any — removed on a
	// failure that means dispatch never ran or produced no worker.
	var minted string
	if valid.Crew == dispatchNewCrew {
		id := s.dispatchNewCrewID()
		crewsDir := filepath.Join(repo.commonDir, "crew", "crews")
		if err := os.MkdirAll(crewsDir, 0o755); err != nil {
			dlog.status = http.StatusInternalServerError
			writeDispatchJSON(w, http.StatusInternalServerError, dispatchResponse{Error: "could not create crew: " + err.Error()})
			return
		}
		if err := os.Mkdir(filepath.Join(crewsDir, id), 0o755); err != nil {
			if errors.Is(err, fs.ErrExist) {
				dlog.status = http.StatusConflict
				writeDispatchJSON(w, http.StatusConflict, dispatchResponse{Error: "a new crew was just started in this second — retry"})
				return
			}
			dlog.status = http.StatusInternalServerError
			writeDispatchJSON(w, http.StatusInternalServerError, dispatchResponse{Error: "could not create crew: " + err.Error()})
			return
		}
		minted = filepath.Join(crewsDir, id)
		valid.Crew = id
		dlog.crew = id
	}

	// Detached from the request: a phone dropping its connection mid-dispatch
	// must not kill dispatch half-way through scaffolding a worktree/issue.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), s.dispatchTimeout)
	defer cancel()

	argv := []string{"dispatch", valid.Tier, valid.Model, "--effort", valid.Effort, "--agent", valid.Engine, "--plan", valid.Plan, "--crew-id", valid.Crew}
	if valid.Issue != "" {
		argv = append(argv, valid.Issue)
	}
	argv = append(argv, valid.Title)

	res := s.dispatchRunner(ctx, dispatchExec{Dir: repo.Path, Argv: argv, Spec: valid.Spec})
	dlog.exitCode = res.ExitCode

	switch {
	case errors.Is(res.Err, context.DeadlineExceeded):
		body := dispatchResponse{
			Error: "dispatch timed out — the worker window may exist without an agent or stall-watch and may need `dispatch resume` from that worktree",
			Crew:  valid.Crew,
		}
		if id := dispatchWorkerID(res.Stdout); id != "" {
			body.WorkerID = id
			dlog.workerID = id
		}
		dlog.status = http.StatusGatewayTimeout
		writeDispatchJSON(w, http.StatusGatewayTimeout, body)
	case res.Err != nil:
		if minted != "" {
			_ = os.Remove(minted)
		}
		dlog.status = http.StatusBadGateway
		writeDispatchJSON(w, http.StatusBadGateway, dispatchResponse{Error: "could not run dispatch: " + res.Err.Error()})
	case res.ExitCode != 0:
		if minted != "" {
			_ = os.Remove(minted)
		}
		errMsg := res.Stderr
		if errMsg == "" {
			errMsg = "dispatch failed"
		}
		dlog.status = http.StatusUnprocessableEntity
		writeDispatchJSON(w, http.StatusUnprocessableEntity, dispatchResponse{Error: errMsg, Output: res.Stdout, Crew: valid.Crew})
	default:
		id := dispatchWorkerID(res.Stdout)
		if id == "" {
			dlog.status = http.StatusBadGateway
			writeDispatchJSON(w, http.StatusBadGateway, dispatchResponse{Error: "dispatch exited 0 without printing a worker_id", Output: res.Stdout})
			return
		}
		dlog.workerID = id
		dlog.status = http.StatusOK
		writeDispatchJSON(w, http.StatusOK, dispatchResponse{
			WorkerID: id,
			Branch:   dispatchBranch(id),
			IssueURL: dispatchIssueURL(res.Stdout, res.Stderr),
			Output:   res.Stdout,
			Crew:     valid.Crew,
		})
	}
}

// dispatchWorkerID returns the first stdout line matching `worker_id: …`,
// dispatch's own success marker.
func dispatchWorkerID(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if m := dispatchWorkerIDRe.FindStringSubmatch(line); m != nil {
			return m[1]
		}
	}
	return ""
}

// dispatchBranch strips worker_id down to its branch: "worker:<branch>#<session>".
func dispatchBranch(workerID string) string {
	b := strings.TrimPrefix(workerID, "worker:")
	if i := strings.IndexByte(b, '#'); i >= 0 {
		b = b[:i]
	}
	return b
}

// dispatchIssueURL reports the first issue URL dispatch printed, if any — it
// mints one via `gh issue create` only when no issue was passed, and never
// prints one itself otherwise.
func dispatchIssueURL(stdout, stderr string) string {
	if m := dispatchIssueURLRe.FindString(stdout); m != "" {
		return m
	}
	return dispatchIssueURLRe.FindString(stderr)
}

func writeDispatchJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// dispatchLog accumulates one request's outcome for a single slog.Info call.
// Request fields are set only after validation and the repo match, so a
// rejected value never reaches the log.
type dispatchLog struct {
	validated bool
	field     string

	repo, tier, engine, model, effort, plan, crew, issue, title string
	specBytes, status, exitCode                                 int
	workerID                                                    string
}

func (l *dispatchLog) setValidated(req dispatchRequest, repoPath string) {
	l.validated = true
	l.repo, l.tier, l.engine, l.model = repoPath, req.Tier, req.Engine, req.Model
	l.effort, l.plan, l.crew, l.issue, l.title = req.Effort, req.Plan, req.Crew, req.Issue, req.Title
}

func (l *dispatchLog) emit(start time.Time) {
	args := []any{"status", l.status, "spec_bytes", l.specBytes, "duration", time.Since(start)}
	switch {
	case l.validated:
		args = append(args,
			"repo", l.repo, "tier", l.tier, "engine", l.engine, "model", l.model,
			"effort", l.effort, "plan", l.plan, "crew", l.crew, "issue", l.issue,
			"title", l.title, "worker_id", l.workerID, "exit_code", l.exitCode,
		)
	case l.field != "":
		args = append(args, "field", l.field)
	}
	slog.Info("dispatch", args...)
}
