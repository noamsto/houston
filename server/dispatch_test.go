package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noamsto/houston/tmux"
)

const (
	dispatchToken  = "dispatch-secret"
	dispatchOrigin = "http://good.example"
	dispatchHost   = "127.0.0.1:9090"

	// dispatchTestNewCrewID stands in for the real time.Now()/os.Getpid() mint
	// so tests can assert on the minted id and force a collision.
	dispatchTestNewCrewID = "1700000000-4242"
)

// stubDispatchRunner stands in for the real dispatch invocation. Its call
// count is the only way to prove a refusal happened before anything ran.
type stubDispatchRunner struct {
	mu     sync.Mutex
	calls  []dispatchExec
	result dispatchResult
}

func (s *stubDispatchRunner) run(_ context.Context, x dispatchExec) dispatchResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, x)
	return s.result
}

func (s *stubDispatchRunner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *stubDispatchRunner) last(t *testing.T) dispatchExec {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("runner was never called")
	}
	return s.calls[len(s.calls)-1]
}

// stubDispatchRepos wraps a canned repo list as the s.dispatchRepos field.
func stubDispatchRepos(repos []dispatchRepo, err error) func() ([]dispatchRepo, error) {
	return func() ([]dispatchRepo, error) { return repos, err }
}

// newDispatchTestRepo builds a dispatchRepo backed by a real temp directory
// with a crew/crews/<id> subdirectory per id, so crew-membership checks
// (which stat the filesystem) have something real to check.
func newDispatchTestRepo(t *testing.T, crewIDs ...string) dispatchRepo {
	t.Helper()
	dir := t.TempDir()
	crewsDir := filepath.Join(dir, "crew", "crews")
	if err := os.MkdirAll(crewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range crewIDs {
		if err := os.MkdirAll(filepath.Join(crewsDir, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dispatchRepo{Path: "/repo", Name: "repo", Crews: crewIDs, commonDir: dir}
}

// newDispatchServer wires the real gates the way New does, without touching
// disk or spawning tmux.
func newDispatchServer(t *testing.T, runner dispatchRunner, repos func() ([]dispatchRepo, error)) *Server {
	t.Helper()
	allowed := []string{dispatchOrigin}
	return &Server{
		auth:              &authGate{token: dispatchToken, enabled: true, allowedOrigins: allowed},
		hosts:             deriveHosts(nil, allowed),
		dispatchRunner:    runner,
		dispatchRepos:     repos,
		dispatchSlot:      make(chan struct{}, 1),
		dispatchTimeout:   5 * time.Second,
		dispatchNewCrewID: func() string { return dispatchTestNewCrewID },
	}
}

func validDispatchRequest(repo dispatchRepo, crew string) dispatchRequest {
	return dispatchRequest{
		Repo:   repo.Path,
		Title:  "add widget",
		Tier:   "standard",
		Engine: "claude",
		Model:  "sonnet",
		Effort: "high",
		Plan:   "required",
		Crew:   crew,
	}
}

func dispatchRequestJSON(t *testing.T, req dispatchRequest) string {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func dispatchHTTPRequest(method, body string) *http.Request {
	path := "/api/dispatch"
	if method == "GET" {
		path = "/api/dispatch/options"
	}
	req := httptest.NewRequest(method, "http://"+dispatchHost+path, strings.NewReader(body))
	req.Host = dispatchHost
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: authCookie, Value: dispatchToken})
	return req
}

func doDispatch(t *testing.T, s *Server, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeDispatchResponse(t *testing.T, rec *httptest.ResponseRecorder) dispatchResponse {
	t.Helper()
	var body dispatchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return body
}

// Every field check happens before the runner is ever invoked.
func TestDispatchValidationFailures(t *testing.T) {
	repo := newDispatchTestRepo(t, "1700000000-123")

	cases := []struct {
		name string
		mut  func(*dispatchRequest)
	}{
		{"bad tier", func(r *dispatchRequest) { r.Tier = "epic" }},
		{"bad effort", func(r *dispatchRequest) { r.Effort = "ultra" }},
		{"bad plan", func(r *dispatchRequest) { r.Plan = "maybe" }},
		{"bad engine", func(r *dispatchRequest) { r.Engine = "gpt" }},
		{"model from another engine", func(r *dispatchRequest) { r.Model = "gpt-5.6-sol" }},
		{"empty title", func(r *dispatchRequest) { r.Title = "   " }},
		{"title too long", func(r *dispatchRequest) { r.Title = strings.Repeat("a", dispatchTitleMaxRunes+1) }},
		{"title flag injection --draft", func(r *dispatchRequest) { r.Title = "--draft" }},
		{"title flag injection -x", func(r *dispatchRequest) { r.Title = "-x" }},
		{"title equals resume", func(r *dispatchRequest) { r.Title = "resume" }},
		{"title numeric issue shape", func(r *dispatchRequest) { r.Title = "123" }},
		{"title hash issue shape", func(r *dispatchRequest) { r.Title = "#12" }},
		{"title linear issue shape", func(r *dispatchRequest) { r.Title = "ENG-12" }},
		{"title control char", func(r *dispatchRequest) { r.Title = "a\nb" }},
		{"title bidi override", func(r *dispatchRequest) { r.Title = "a\u202eb" }},
		{"title no ascii alnum", func(r *dispatchRequest) { r.Title = "שלום" }},
		{"title punctuation only", func(r *dispatchRequest) { r.Title = "!!!" }},
		{"issue bad shape", func(r *dispatchRequest) { r.Issue = "12; rm" }},
		{"issue flag shape", func(r *dispatchRequest) { r.Issue = "--pr" }},
		{"crew missing", func(r *dispatchRequest) { r.Crew = "" }},
		{"crew bad shape", func(r *dispatchRequest) { r.Crew = "abc" }},
		{"crew wrong case", func(r *dispatchRequest) { r.Crew = "New" }},
		{"crew trailing space", func(r *dispatchRequest) { r.Crew = "new " }},
		{"crew looks like new but isn't", func(r *dispatchRequest) { r.Crew = "newcrew" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubDispatchRunner{}
			s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
			req := validDispatchRequest(repo, repo.Crews[0])
			tc.mut(&req)

			rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if n := stub.count(); n != 0 {
				t.Errorf("runner called %d times, want 0", n)
			}
			if body := decodeDispatchResponse(t, rec); body.Error == "" {
				t.Errorf("error message is empty")
			}
		})
	}
}

// Repo and crew membership are checked against the freshly computed known-repo
// set, never stat'd or joined before they match.
func TestDispatchRepoAndCrewMembership(t *testing.T) {
	repo := newDispatchTestRepo(t, "1700000000-123")
	otherRepo := newDispatchTestRepo(t, "1700000000-999")

	cases := []struct {
		name string
		mut  func(*dispatchRequest)
	}{
		{"unknown repo", func(r *dispatchRequest) { r.Repo = "/nope" }},
		{"repo with .. cleans to unknown", func(r *dispatchRequest) { r.Repo = repo.Path + "/../nope" }},
		{"crew directory missing", func(r *dispatchRequest) { r.Crew = "1700000000-000" }},
		{"crew belongs to another repo", func(r *dispatchRequest) { r.Crew = otherRepo.Crews[0] }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubDispatchRunner{}
			s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
			req := validDispatchRequest(repo, repo.Crews[0])
			tc.mut(&req)

			rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
			if rec.Code != http.StatusNotFound {
				t.Errorf("status %d, want 404 (body %s)", rec.Code, rec.Body.String())
			}
			if n := stub.count(); n != 0 {
				t.Errorf("runner called %d times, want 0", n)
			}
		})
	}
}

func TestDispatchBodyGates(t *testing.T) {
	repo := newDispatchTestRepo(t, "1700000000-123")

	t.Run("unknown JSON field", func(t *testing.T) {
		stub := &stubDispatchRunner{}
		s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
		rec := doDispatch(t, s, dispatchHTTPRequest("POST", `{"repo":"x","bogus":true}`))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", rec.Code)
		}
		if n := stub.count(); n != 0 {
			t.Errorf("runner called %d times, want 0", n)
		}
	})

	t.Run("oversize spec", func(t *testing.T) {
		stub := &stubDispatchRunner{}
		s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
		req := validDispatchRequest(repo, repo.Crews[0])
		req.Spec = strings.Repeat("a", maxDispatchSpec+1)
		rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status %d, want 413", rec.Code)
		}
		if n := stub.count(); n != 0 {
			t.Errorf("runner called %d times, want 0", n)
		}
	})

	t.Run("oversize body", func(t *testing.T) {
		stub := &stubDispatchRunner{}
		s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
		req := validDispatchRequest(repo, repo.Crews[0])
		req.Title = strings.Repeat("a", maxDispatchBody)
		rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status %d, want 413", rec.Code)
		}
		if n := stub.count(); n != 0 {
			t.Errorf("runner called %d times, want 0", n)
		}
	})

	// 405 is the mux's method pattern refusing the route, driven through the
	// full Handler() the way Step 5 wires it — not the bare handler func.
	t.Run("GET on the POST route", func(t *testing.T) {
		stub := &stubDispatchRunner{}
		s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
		req := httptest.NewRequest("GET", "http://"+dispatchHost+"/api/dispatch", nil)
		req.Host = dispatchHost
		req.AddCookie(&http.Cookie{Name: authCookie, Value: dispatchToken})
		rec := doDispatch(t, s, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("status %d, want 405", rec.Code)
		}
		if n := stub.count(); n != 0 {
			t.Errorf("runner called %d times, want 0", n)
		}
	})
}

func TestDispatchHappyPath(t *testing.T) {
	repo := newDispatchTestRepo(t, "1700000000-123")

	cases := []struct {
		name     string
		issue    string
		wantArgv []string
	}{
		{
			"without issue", "",
			[]string{"dispatch", "standard", "sonnet", "--effort", "high", "--agent", "claude", "--plan", "required", "--crew-id", repo.Crews[0], "add widget"},
		},
		{
			"with issue", "42",
			[]string{"dispatch", "standard", "sonnet", "--effort", "high", "--agent", "claude", "--plan", "required", "--crew-id", repo.Crews[0], "42", "add widget"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubDispatchRunner{result: dispatchResult{Stdout: "worker_id: worker:feat/1-add-widget#s1\n"}}
			s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
			req := validDispatchRequest(repo, repo.Crews[0])
			req.Issue = tc.issue
			req.Spec = "do the thing"

			rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			body := decodeDispatchResponse(t, rec)
			if body.WorkerID != "worker:feat/1-add-widget#s1" {
				t.Errorf("worker_id = %q", body.WorkerID)
			}
			if body.Branch != "feat/1-add-widget" {
				t.Errorf("branch = %q", body.Branch)
			}
			if body.Crew != repo.Crews[0] {
				t.Errorf("crew = %q, want %q", body.Crew, repo.Crews[0])
			}

			got := stub.last(t)
			if !slices.Equal(got.Argv, tc.wantArgv) {
				t.Errorf("argv = %q, want %q", got.Argv, tc.wantArgv)
			}
			if got.Dir != repo.Path {
				t.Errorf("dir = %q, want %q", got.Dir, repo.Path)
			}
			if got.Spec != "do the thing" {
				t.Errorf("spec = %q, want %q", got.Spec, "do the thing")
			}
		})
	}
}

func TestDispatchPlanDefaultsToRequired(t *testing.T) {
	repo := newDispatchTestRepo(t, "1700000000-123")
	stub := &stubDispatchRunner{result: dispatchResult{Stdout: "worker_id: worker:feat/1-x#s1\n"}}
	s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
	req := validDispatchRequest(repo, repo.Crews[0])
	req.Plan = ""

	rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	got := stub.last(t)
	i := slices.Index(got.Argv, "--plan")
	if i < 0 || i+1 >= len(got.Argv) || got.Argv[i+1] != "required" {
		t.Errorf("argv %q missing --plan required", got.Argv)
	}
}

func TestDispatchExecOutcomes(t *testing.T) {
	repo := newDispatchTestRepo(t, "1700000000-123")

	cases := []struct {
		name   string
		result dispatchResult
		want   int
		check  func(t *testing.T, body dispatchResponse)
	}{
		{
			"non-zero exit", dispatchResult{ExitCode: 3, Stderr: "tier/model mismatch", Stdout: "some output"},
			http.StatusUnprocessableEntity,
			func(t *testing.T, b dispatchResponse) {
				if b.Error != "tier/model mismatch" {
					t.Errorf("error = %q", b.Error)
				}
				if b.Output != "some output" {
					t.Errorf("output = %q", b.Output)
				}
			},
		},
		{
			"missing binary", dispatchResult{Err: &exec.Error{Name: "dispatch", Err: exec.ErrNotFound}},
			http.StatusBadGateway,
			func(t *testing.T, b dispatchResponse) {
				if !strings.Contains(b.Error, "dispatch") {
					t.Errorf("error = %q", b.Error)
				}
			},
		},
		{
			"exit 0 without worker_id", dispatchResult{Stdout: "nothing useful"},
			http.StatusBadGateway,
			func(t *testing.T, b dispatchResponse) {
				if b.Output != "nothing useful" {
					t.Errorf("output = %q", b.Output)
				}
			},
		},
		{
			"timeout without worker_id", dispatchResult{Err: context.DeadlineExceeded},
			http.StatusGatewayTimeout,
			func(t *testing.T, b dispatchResponse) {
				if b.WorkerID != "" {
					t.Errorf("worker_id = %q, want empty", b.WorkerID)
				}
			},
		},
		{
			"timeout with worker_id already printed",
			dispatchResult{Err: context.DeadlineExceeded, Stdout: "worker_id: worker:feat/2-x#s2\n"},
			http.StatusGatewayTimeout,
			func(t *testing.T, b dispatchResponse) {
				if b.WorkerID != "worker:feat/2-x#s2" {
					t.Errorf("worker_id = %q", b.WorkerID)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubDispatchRunner{result: tc.result}
			s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
			req := validDispatchRequest(repo, repo.Crews[0])

			rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			tc.check(t, decodeDispatchResponse(t, rec))
		})
	}
}

func TestDispatchConcurrencyCap(t *testing.T) {
	repo := newDispatchTestRepo(t, "1700000000-123")
	stub := &stubDispatchRunner{}
	s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
	s.dispatchSlot <- struct{}{} // pre-fill: simulates another dispatch in flight

	req := validDispatchRequest(repo, repo.Crews[0])
	rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429 (body %s)", rec.Code, rec.Body.String())
	}
	if n := stub.count(); n != 0 {
		t.Errorf("runner called %d times, want 0", n)
	}
}

// TestDispatchNewCrew drives crew:"new" through every runner outcome on a
// repo with no crew/ directory at all, to prove the MkdirAll rather than just
// the leaf Mkdir.
func TestDispatchNewCrew(t *testing.T) {
	cases := []struct {
		name        string
		result      dispatchResult
		wantStatus  int
		wantDirKept bool
		wantCrewSet bool
	}{
		{"success", dispatchResult{Stdout: "worker_id: worker:feat/1-x#s1\n"}, http.StatusOK, true, true},
		{"exit 1", dispatchResult{ExitCode: 1, Stderr: "nope"}, http.StatusUnprocessableEntity, false, true},
		{"runner err", dispatchResult{Err: &exec.Error{Name: "dispatch", Err: exec.ErrNotFound}}, http.StatusBadGateway, false, false},
		{"deadline", dispatchResult{Err: context.DeadlineExceeded}, http.StatusGatewayTimeout, true, true},
		{"exit 0 no worker_id", dispatchResult{Stdout: "nothing useful"}, http.StatusBadGateway, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newDispatchTestRepo(t)
			if err := os.RemoveAll(filepath.Join(repo.commonDir, "crew")); err != nil {
				t.Fatal(err)
			}

			stub := &stubDispatchRunner{result: tc.result}
			s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
			req := validDispatchRequest(repo, dispatchNewCrew)

			rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}

			mintedDir := filepath.Join(repo.commonDir, "crew", "crews", dispatchTestNewCrewID)
			_, err := os.Stat(mintedDir)
			if exists := err == nil; exists != tc.wantDirKept {
				t.Errorf("minted dir exists = %v, want %v", exists, tc.wantDirKept)
			}

			body := decodeDispatchResponse(t, rec)
			switch {
			case tc.wantCrewSet && body.Crew != dispatchTestNewCrewID:
				t.Errorf("crew = %q, want %q", body.Crew, dispatchTestNewCrewID)
			case !tc.wantCrewSet && body.Crew != "":
				t.Errorf("crew = %q, want empty", body.Crew)
			}

			if n := stub.count(); n != 1 {
				t.Fatalf("runner called %d times, want 1", n)
			}
			got := stub.last(t)
			i := slices.Index(got.Argv, "--crew-id")
			if i < 0 || i+1 >= len(got.Argv) || got.Argv[i+1] != dispatchTestNewCrewID {
				t.Errorf("argv %q missing --crew-id %s", got.Argv, dispatchTestNewCrewID)
			}
			if slices.Contains(got.Argv, dispatchNewCrew) {
				t.Errorf("argv %q must never contain %q", got.Argv, dispatchNewCrew)
			}
		})
	}
}

// TestDispatchNewCrewCollision proves a same-second re-mint is refused
// without ever invoking the runner, rather than silently reusing the dir.
func TestDispatchNewCrewCollision(t *testing.T) {
	repo := newDispatchTestRepo(t, dispatchTestNewCrewID)
	stub := &stubDispatchRunner{}
	s := newDispatchServer(t, stub.run, stubDispatchRepos([]dispatchRepo{repo}, nil))
	req := validDispatchRequest(repo, dispatchNewCrew)

	rec := doDispatch(t, s, dispatchHTTPRequest("POST", dispatchRequestJSON(t, req)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if n := stub.count(); n != 0 {
		t.Errorf("runner called %d times, want 0", n)
	}
}

// TestDispatchTierModelsConsistent keeps dispatchTierModels in step with
// dispatchModels: every default must itself be a valid model for its engine.
func TestDispatchTierModelsConsistent(t *testing.T) {
	for _, engine := range dispatchEngineOrder {
		for _, tier := range dispatchTiers {
			model, ok := dispatchTierModels[engine][tier]
			if !ok || model == "" {
				t.Errorf("dispatchTierModels[%q][%q] missing", engine, tier)
				continue
			}
			if !slices.Contains(dispatchModels[engine], model) {
				t.Errorf("dispatchTierModels[%q][%q] = %q not in dispatchModels[%q]", engine, tier, model, engine)
			}
		}
	}
}

func TestHandleDispatchOptions(t *testing.T) {
	repo := newDispatchTestRepo(t, "1700000000-123")
	s := newDispatchServer(t, nil, stubDispatchRepos([]dispatchRepo{repo}, nil))

	req := httptest.NewRequest("GET", "http://"+dispatchHost+"/api/dispatch/options", nil)
	req.Host = dispatchHost
	req.AddCookie(&http.Cookie{Name: authCookie, Value: dispatchToken})
	rec := doDispatch(t, s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	var opts dispatchOptions
	if err := json.Unmarshal(rec.Body.Bytes(), &opts); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(opts.Repos) != 1 || opts.Repos[0].Path != repo.Path {
		t.Errorf("repos = %+v", opts.Repos)
	}
	if len(opts.Tiers) == 0 || len(opts.Efforts) == 0 || len(opts.Plans) == 0 {
		t.Errorf("expected non-empty tiers/efforts/plans")
	}
	if len(opts.EngineOrder) == 0 || len(opts.Engines) == 0 {
		t.Errorf("expected non-empty engines/engine_order")
	}
	if got := opts.TierModels["claude"]["standard"]; got != "sonnet" {
		t.Errorf("tier_models.claude.standard = %q, want %q", got, "sonnet")
	}
}

func TestHandleDispatchOptionsRepoListFailure(t *testing.T) {
	s := newDispatchServer(t, nil, stubDispatchRepos(nil, errors.New("tmux unreachable")))

	req := httptest.NewRequest("GET", "http://"+dispatchHost+"/api/dispatch/options", nil)
	req.Host = dispatchHost
	req.AddCookie(&http.Cookie{Name: authCookie, Value: dispatchToken})
	rec := doDispatch(t, s, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
}

// listDispatchRepos collapses a repo's worktrees to one entry, skips bare or
// unresolvable roots, and lists crews newest-first by numeric prefix.
func TestListDispatchRepos(t *testing.T) {
	tmp := t.TempDir()
	mainRoot := filepath.Join(tmp, "repo")
	worktreeRoot := filepath.Join(tmp, "repo-wt")
	commonDir := filepath.Join(mainRoot, ".git")
	crewsDir := filepath.Join(commonDir, "crew", "crews")
	if err := os.MkdirAll(crewsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"1700000002-1", "1700000001-1", "not-a-crew-id"} {
		if err := os.MkdirAll(filepath.Join(crewsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bareRoot := filepath.Join(tmp, "bare")

	lister := &fakeWorkspaceLister{wins: []tmux.WindowOptions{
		{GitRoot: mainRoot},
		{GitRoot: worktreeRoot}, // same repo (shared common dir) -> collapses
		{GitRoot: bareRoot},     // unresolvable -> skipped
		{GitRoot: ""},           // no repo -> skipped
	}}
	lookup := func(root string) (string, error) {
		if root == mainRoot || root == worktreeRoot {
			return commonDir, nil
		}
		return "", errors.New("not a repo")
	}

	repos, err := listDispatchRepos(lister, lookup)
	if err != nil {
		t.Fatalf("listDispatchRepos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("repos = %+v, want 1", repos)
	}
	if repos[0].Path != mainRoot {
		t.Errorf("path = %q, want %q", repos[0].Path, mainRoot)
	}
	if repos[0].Name != "repo" {
		t.Errorf("name = %q, want %q", repos[0].Name, "repo")
	}
	wantCrews := []string{"1700000002-1", "1700000001-1"}
	if !slices.Equal(repos[0].Crews, wantCrews) {
		t.Errorf("crews = %q, want %q", repos[0].Crews, wantCrews)
	}
}

func TestListDispatchReposMissingCrewsDirIsEmptyNotNil(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "repo")
	commonDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}

	lister := &fakeWorkspaceLister{wins: []tmux.WindowOptions{{GitRoot: root}}}
	lookup := func(string) (string, error) { return commonDir, nil }

	repos, err := listDispatchRepos(lister, lookup)
	if err != nil {
		t.Fatalf("listDispatchRepos: %v", err)
	}
	if repos[0].Crews == nil {
		t.Fatal("crews is nil, want an empty non-nil slice")
	}
	if len(repos[0].Crews) != 0 {
		t.Errorf("crews = %q, want empty", repos[0].Crews)
	}
}

func TestListDispatchReposListerError(t *testing.T) {
	lister := &fakeWorkspaceLister{winErr: errors.New("tmux unreachable")}
	_, err := listDispatchRepos(lister, gitCommonDir)
	if err == nil {
		t.Fatal("expected an error when the lister fails")
	}
}
