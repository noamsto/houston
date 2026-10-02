package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type reposAPIFixture struct {
	s    *Server
	root string
}

func newReposAPIFixture(t *testing.T) reposAPIFixture {
	t.Helper()
	root := realTempDir(t)
	s := newDispatchServer(t, nil, stubDispatchRepos(nil, nil))
	s.repoReg = newRepoRegistry(filepath.Join(realTempDir(t), "repos.json"), []string{root}, gitCommonDir)
	return reposAPIFixture{s: s, root: root}
}

func reposRequest(method, target, body string, withToken bool) *http.Request {
	req := httptest.NewRequest(method, "http://"+dispatchHost+target, strings.NewReader(body))
	req.Host = dispatchHost
	req.Header.Set("Content-Type", "application/json")
	if withToken {
		req.AddCookie(&http.Cookie{Name: authCookie, Value: dispatchToken})
	}
	return req
}

func (f reposAPIFixture) do(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doDispatch(t, f.s, reposRequest(method, target, body, true))
}

func pathBody(path string) string {
	b, _ := json.Marshal(map[string]string{"path": path})
	return string(b)
}

func decodeRaw(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("unmarshal %q: %v", rec.Body.String(), err)
	}
	return raw
}

func TestReposAPIListEmptyArraysNeverNull(t *testing.T) {
	f := newReposAPIFixture(t)

	rec := f.do(t, "GET", "/api/repos", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	raw := decodeRaw(t, rec)
	if got := string(raw["repos"]); got != "[]" {
		t.Errorf("repos = %s, want []", got)
	}
	var roots []string
	if err := json.Unmarshal(raw["roots"], &roots); err != nil || !slices.Equal(roots, []string{f.root}) {
		t.Errorf("roots = %s, want [%s]", raw["roots"], f.root)
	}
}

func TestReposAPIAddListRemove(t *testing.T) {
	requireGit(t)
	f := newReposAPIFixture(t)
	repo := gitInit(t, filepath.Join(f.root, "proj"))

	rec := f.do(t, "POST", "/api/repos", pathBody(repo))
	if rec.Code != http.StatusOK {
		t.Fatalf("add status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var added struct{ Path, Name string }
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	if added.Path != repo || added.Name != "proj" {
		t.Errorf("added = %+v, want {%s proj}", added, repo)
	}

	rec = f.do(t, "GET", "/api/repos", "")
	var list struct{ Repos []repoEntry }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if want := []repoEntry{{Path: repo, Name: "proj", Valid: true}}; !slices.Equal(list.Repos, want) {
		t.Errorf("repos = %+v, want %+v", list.Repos, want)
	}

	if rec = f.do(t, "DELETE", "/api/repos", pathBody(repo)); rec.Code != http.StatusNoContent {
		t.Fatalf("remove status %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	rec = f.do(t, "DELETE", "/api/repos", pathBody(repo))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second remove status %d, want 404", rec.Code)
	}
	if body := decodeDispatchResponse(t, rec); body.Error == "" {
		t.Error("error message is empty")
	}
}

func TestReposAPIAddRejections(t *testing.T) {
	f := newReposAPIFixture(t)
	outside := realTempDir(t)

	cases := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"unknown field", "POST", `{"path":"/x","extra":1}`, http.StatusBadRequest},
		{"malformed", "POST", `{"path":`, http.StatusBadRequest},
		{"relative", "POST", pathBody("proj"), http.StatusBadRequest},
		{"outside roots", "POST", pathBody(outside), http.StatusUnprocessableEntity},
		{"too large", "POST", pathBody(strings.Repeat("a", 5<<10)), http.StatusRequestEntityTooLarge},
		{"delete unknown field", "DELETE", `{"path":"/x","extra":1}`, http.StatusBadRequest},
		{"delete malformed", "DELETE", `nope`, http.StatusBadRequest},
		{"delete too large", "DELETE", pathBody(strings.Repeat("a", 5<<10)), http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.do(t, tc.method, "/api/repos", tc.body)
			if rec.Code != tc.want {
				t.Errorf("status %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
			if body := decodeDispatchResponse(t, rec); body.Error == "" {
				t.Error("error message is empty")
			}
		})
	}
}

func TestReposAPICandidates(t *testing.T) {
	f := newReposAPIFixture(t)
	alpha := fakeRepo(t, filepath.Join(f.root, "alpha"))
	fakeRepo(t, filepath.Join(f.root, "beta"))

	type result struct {
		Roots      []string
		Candidates []repoCandidate
		Truncated  bool
	}
	get := func(q string) result {
		t.Helper()
		rec := f.do(t, "GET", "/api/repos/candidates?q="+url.QueryEscape(q), "")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var res result
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := get("ALP")
	if want := []repoCandidate{{Path: alpha, Name: "alpha"}}; !slices.Equal(res.Candidates, want) {
		t.Errorf("candidates = %+v, want %+v", res.Candidates, want)
	}
	if !slices.Equal(res.Roots, []string{f.root}) {
		t.Errorf("roots = %q, want [%s]", res.Roots, f.root)
	}
	if res.Truncated {
		t.Error("truncated = true, want false")
	}
	if all := get(""); len(all.Candidates) != 2 {
		t.Errorf("empty q candidates = %+v, want 2", all.Candidates)
	}

	rec := f.do(t, "GET", "/api/repos/candidates?q=zzz", "")
	if got := string(decodeRaw(t, rec)["candidates"]); got != "[]" {
		t.Errorf("candidates = %s, want []", got)
	}
}

func TestReposAPICandidatesRejectsBadQuery(t *testing.T) {
	f := newReposAPIFixture(t)
	cases := map[string]string{
		"control char": "a\nb",
		"bidi":         "a\u202eb",
		"too long":     strings.Repeat("a", 201),
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, "GET", "/api/repos/candidates?q="+url.QueryEscape(q), "")
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
	if rec := f.do(t, "GET", "/api/repos/candidates?q="+strings.Repeat("é", 200), ""); rec.Code != http.StatusOK {
		t.Errorf("200 runes: status %d, want 200", rec.Code)
	}
}

func TestReposAPIRequiresToken(t *testing.T) {
	f := newReposAPIFixture(t)
	routes := []struct{ method, target, body string }{
		{"GET", "/api/repos", ""},
		{"POST", "/api/repos", pathBody("/x")},
		{"DELETE", "/api/repos", pathBody("/x")},
		{"GET", "/api/repos/candidates", ""},
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.target, func(t *testing.T) {
			rec := doDispatch(t, f.s, reposRequest(rt.method, rt.target, rt.body, false))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status %d, want 401", rec.Code)
			}
		})
	}
}
