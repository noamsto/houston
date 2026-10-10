package loadfixture

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CrewOptions sizes a crew fixture. Crews is the number of dispatcher crews,
// Workers the windowed workers per crew.
type CrewOptions struct {
	Crews   int
	Workers int
	Seed    int64
	Now     time.Time // default time.Now()
}

// CrewWorker is a worker that gets a tmux window: its bus state is never
// terminal, so a pane join needs no process-start evidence.
type CrewWorker struct {
	Crew     string
	Branch   string
	Codename string
	State    string // bus state of its latest status: pr_open or blocked
}

// CrewFixture describes what WriteCrews wrote.
type CrewFixture struct {
	Repo    string
	Crews   []string
	Workers []CrewWorker
	Events  int
}

// CrewManifest is written into the fixture dir for the tmux helper.
const CrewManifest = "crews.tsv"

// reapedPerCrew is the number of finished, windowless branches per crew.
const reapedPerCrew = 3

var codenames = strings.Fields(`atlas basalt cinder dune ember fjord garnet harbor ivory jasper kestrel lumen
marble nimbus onyx pebble quartz raven sable tundra umber vesper willow xenon yarrow zephyr
amber birch cedar delta elm flint glen heron iris juniper kelp larch moss north opal pine`)

// WriteCrews writes <dir>/repo as a git main checkout whose bus
// (.git/crew/events.jsonl) holds opts.Crews crews of opts.Workers windowed
// workers each, plus a few finished branches per crew, and <dir>/crews.tsv
// naming the dispatchers and windowed workers. The crews/<id>/pane and pid
// files are left to the tmux helper: they must postdate its tmux server.
func WriteCrews(dir string, opts CrewOptions) (CrewFixture, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	rnd := rand.New(rand.NewSource(opts.Seed)) //nolint:gosec // deterministic fixture, not security
	repo := filepath.Join(dir, "repo")
	fx := CrewFixture{Repo: repo}
	if err := initRepo(repo); err != nil {
		return fx, err
	}
	bus := filepath.Join(repo, ".git", "crew")
	if err := os.MkdirAll(filepath.Join(bus, "crews"), 0o755); err != nil { //nolint:gosec // test fixture dir
		return fx, err
	}

	g := &crewGen{rnd: rnd}
	ages := []time.Duration{2 * time.Minute, 5 * time.Minute, 20 * time.Minute, 40 * time.Minute}
	for c := range opts.Crews {
		crew := fmt.Sprintf("%d-%d", 1_700_000_000+opts.Seed*1000+int64(c), c+1)
		fx.Crews = append(fx.Crews, crew)
		for k := range opts.Workers {
			w := CrewWorker{Crew: crew, Branch: fmt.Sprintf("feat/%d-fixture-%d", 1000+c*100+k, k), Codename: g.codename()}
			w.State = "pr_open"
			if (c+k)%3 == 2 {
				w.State = "blocked"
			}
			age := ages[(c+k)%len(ages)] + time.Duration(rnd.Intn(60))*time.Second
			g.worker(w, opts.Now.Add(-age), c*opts.Workers+k)
			fx.Workers = append(fx.Workers, w)
		}
		for j := range reapedPerCrew {
			g.reaped(crew, fmt.Sprintf("feat/%d-fixture-r%d", 1000+c*100+50+j, j), opts.Now.Add(-time.Duration(2+j)*time.Hour))
		}
	}

	sort.SliceStable(g.events, func(i, j int) bool { return g.events[i].ts < g.events[j].ts })
	if err := writeEvents(filepath.Join(bus, "events.jsonl"), g.events); err != nil {
		return fx, err
	}
	fx.Events = len(g.events)
	return fx, writeManifest(filepath.Join(dir, CrewManifest), fx)
}

func initRepo(repo string) error {
	if err := os.MkdirAll(repo, 0o755); err != nil { //nolint:gosec // test fixture dir
		return err
	}
	env := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	for _, args := range [][]string{
		{"-c", "init.defaultBranch=main", "init", "--quiet"},
		{"commit", "--quiet", "--allow-empty", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...) //nolint:gosec // fixed argv
		cmd.Dir = repo
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w: %s", args[len(args)-1], err, out)
		}
	}
	return nil
}

func writeEvents(path string, events []crewEvent) error {
	f, err := os.Create(path) //nolint:gosec // fixture path chosen by the caller
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	w := bufio.NewWriter(f)
	for _, e := range events {
		b, err := json.Marshal(e.row)
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}

func writeManifest(path string, fx CrewFixture) error {
	var b strings.Builder
	for _, crew := range fx.Crews {
		fmt.Fprintf(&b, "dispatcher\t%s\n", crew)
	}
	for _, w := range fx.Workers {
		fmt.Fprintf(&b, "worker\t%s\t%s\t%s\t%s\n", w.Crew, w.Branch, w.Codename, w.State)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644) //nolint:gosec // test fixture
}

type crewEvent struct {
	ts  int64
	row map[string]any
}

type crewGen struct {
	rnd    *rand.Rand
	events []crewEvent
	names  int
}

func (g *crewGen) codename() string {
	i := g.names
	g.names++
	if i < len(codenames) {
		return codenames[i]
	}
	return fmt.Sprintf("%s%d", codenames[i%len(codenames)], i/len(codenames))
}

func (g *crewGen) add(at time.Time, e map[string]any) {
	e["ts"] = at.UnixMilli()
	g.events = append(g.events, crewEvent{ts: at.UnixMilli(), row: e})
}

func (g *crewGen) dispatch(crew, branch, name string, at time.Time, n int) {
	g.add(at, map[string]any{
		"kind": "dispatch", "crew_id": crew, "from": "dispatcher:" + crew, "to": "worker:" + branch,
		"branch": branch, "title": fmt.Sprintf("Fixture task %d: %s", n, words(g.rnd, 4)), "tier": []string{"quick", "standard", "deep"}[n%3],
		"engine": "claude", "model": []string{"haiku", "sonnet", "opus"}[n%3], "effort": "medium", "name": name,
		"color": fmt.Sprintf("colour%d", 17+n%200), "session": "fx", "engine_session": nil,
	})
}

func (g *crewGen) status(crew, branch string, at time.Time, state, detail, prURL string) {
	body := map[string]any{"state": state, "detail": detail}
	if prURL != "" {
		body["pr_url"] = prURL
	}
	g.add(at, map[string]any{
		"kind": "status", "crew_id": crew, "to": "dispatcher:" + crew,
		"from": fmt.Sprintf("worker:%s#s%d-%d", branch, at.Unix()-60, 4000+g.rnd.Intn(1000)), "branch": branch, "body": body,
	})
}

func (g *crewGen) msg(crew, from, to string, at time.Time, body string) {
	g.add(at, map[string]any{"kind": "msg", "crew_id": crew, "from": from, "to": to, "body": body})
}

func (g *crewGen) prWatch(crew string, pr int, at time.Time, from, to string) {
	body, _ := json.Marshal(map[string]any{
		"pr": pr, "url": prURL(pr), "changed": []string{"checks"},
		"state": map[string]any{"state": "OPEN", "checks": to}, "was": map[string]any{"checks": from},
	})
	g.msg(crew, fmt.Sprintf("pr-watch:%d", pr), "dispatcher:"+crew, at, string(body))
}

func prURL(n int) string { return fmt.Sprintf("https://github.com/fixture/repo/pull/%d", n) }

// worker writes a windowed worker's history ending in w.State at final.
func (g *crewGen) worker(w CrewWorker, final time.Time, n int) {
	pr := 5000 + n
	id := fmt.Sprintf("worker:%s#s%d-%d", w.Branch, final.Add(-30*time.Minute).Unix(), 4000+n)
	disp := "dispatcher:" + w.Crew
	g.dispatch(w.Crew, w.Branch, w.Codename, final.Add(-30*time.Minute), n)
	g.status(w.Crew, w.Branch, final.Add(-29*time.Minute), "working", "reading "+words(g.rnd, 2), "")
	g.msg(w.Crew, id, disp, final.Add(-12*time.Minute), fmt.Sprintf("Question: should %s use %s?", vocab[n%len(vocab)], vocab[(n+7)%len(vocab)]))
	g.msg(w.Crew, disp, id, final.Add(-10*time.Minute), "Use "+vocab[(n+7)%len(vocab)]+", keep it small.")
	g.msg(w.Crew, id, disp, final.Add(-8*time.Minute), "follow-ups (untracked): tidy "+vocab[(n+3)%len(vocab)]+"; add a test for "+vocab[(n+5)%len(vocab)])
	g.status(w.Crew, w.Branch, final.Add(-5*time.Minute), "working", "editing "+words(g.rnd, 2), "")
	if w.State == "blocked" {
		g.status(w.Crew, w.Branch, final, "blocked", fmt.Sprintf("Which of %s or %s should %s keep?", vocab[n%len(vocab)], vocab[(n+9)%len(vocab)], vocab[(n+2)%len(vocab)]), "")
		return
	}
	g.status(w.Crew, w.Branch, final, "pr_open", "PR open, waiting on checks", prURL(pr))
	g.prWatch(w.Crew, pr, final.Add(time.Minute), "PENDING", "SUCCESS")
}

// reaped writes a finished branch that has no window: dispatch, done, reap.
func (g *crewGen) reaped(crew, branch string, done time.Time) {
	n := len(g.events)
	pr := 7000 + n
	g.dispatch(crew, branch, g.codename(), done.Add(-time.Hour), n)
	g.status(crew, branch, done.Add(-50*time.Minute), "working", "building "+words(g.rnd, 2), "")
	g.status(crew, branch, done, "done", "merged and done", prURL(pr))
	g.add(done.Add(10*time.Minute), map[string]any{"kind": "reap", "branch": branch, "pr": prURL(pr), "pr_state": "MERGED"})
}
