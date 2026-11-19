package runs

import (
	"testing"

	"github.com/noamsto/houston/crewview"
)

func TestLogWorkers(t *testing.T) {
	ids := map[string]crewIdentity{
		"feat/a": {crew: "c1", codename: "otter", title: "T", tier: "standard", engine: "claude", model: "sonnet",
			state: "blocked", detail: "q", statusTS: 3_000_000, blockedSince: 2_000_000,
			watchdog: watchdogItem{kind: "watchdog", message: "quiet: x"}},
		"feat/b": {crew: "c2", state: "working"},
		"feat/c": {state: "working"},
	}
	ws, items := logWorkers(ids, 10_000, func(crew string) bool { return crew == "c1" })
	if len(ws) != 1 {
		t.Fatalf("workers = %+v, want only feat/a", ws)
	}
	w := ws[0]
	if w.Crew != "c1" || w.Branch != "feat/a" || w.Origin != crewview.OriginLocal || w.State != "blocked" ||
		deref(w.Name) != "otter" || deref(w.Title) != "T" || deref(w.Tier) != "standard" || deref(w.Engine) != "claude" || deref(w.Model) != "sonnet" || deref(w.Detail) != "q" {
		t.Errorf("worker = %+v", w)
	}
	if w.AgeSec == nil || *w.AgeSec != 7000 || w.BlockedSince == nil || *w.BlockedSince != 2_000_000 {
		t.Errorf("age/blockedSince = %v/%v, want 7000/2000000", w.AgeSec, w.BlockedSince)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want blocked and watchdog", items)
	}
	if it := items[0]; it.ID != "blocked:c1:feat/a" || it.Kind != "blocked" || it.Message != "q" || it.AgeSec != 8000 || it.Action != nil || it.Origin != crewview.OriginLocal {
		t.Errorf("blocked item = %+v", it)
	}
	if it := items[1]; it.ID != "watchdog:c1:feat/a" || it.Message != "quiet: x" || it.AgeSec != 7000 {
		t.Errorf("watchdog item = %+v", it)
	}

	ws, _ = logWorkers(map[string]crewIdentity{"x": {crew: "c1", state: "working"}}, 10_000, func(string) bool { return true })
	if len(ws) != 1 || ws[0].AgeSec != nil || ws[0].Name != nil {
		t.Errorf("a worker with no status row = %+v, want no age and no name", ws)
	}
}
