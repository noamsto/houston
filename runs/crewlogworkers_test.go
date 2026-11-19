package runs

import (
	"strings"
	"testing"

	"github.com/noamsto/houston/crewview"
)

func TestLogWorkers(t *testing.T) {
	ids := map[string]crewIdentity{
		"feat/a": {crew: "c1", codename: "otter", title: "T", tier: "standard", engine: "claude", model: "sonnet",
			state: "blocked", detail: "q", hasStatus: true, statusTS: 3_000_000, blockedSince: 2_000_000,
			watchdog: watchdogItem{kind: "watchdog", message: "quiet: x"}, watchdogTS: 3_000_000},
		"feat/b": {crew: "c2", state: "working", hasStatus: true},
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

	ws, _ = logWorkers(map[string]crewIdentity{
		"dispatch-only": {crew: "c1", state: "working"},
		"reaped":        {crew: "c1", state: "reaped", hasStatus: true, statusTS: 1000},
		"done":          {crew: "c1", state: "done", hasStatus: true, statusTS: 1000},
	}, 10_000, func(string) bool { return true })
	if len(ws) != 1 || ws[0].Branch != "done" || ws[0].Name != nil {
		t.Errorf("workers = %+v, want only the finished one — no status row and reaped are not listed", ws)
	}
}

func TestLogWorkersWatchdogOnlyWorker(t *testing.T) {
	ids := identityFromCrewLog(strings.NewReader(idDispatch + "\n" +
		`{"ts":5000000,"crew_id":"c1","from":"worker:feat/a#s1","kind":"status","body":{"state":"blocked","detail":"prompt: trust","source":"watchdog"}}` + "\n"))
	ws, items := logWorkers(ids, 10_000, func(string) bool { return true })
	if len(ws) != 1 || ws[0].State != "working" || ws[0].AgeSec != nil {
		t.Fatalf("workers = %+v, want one working worker without an age", ws)
	}
	if len(items) != 1 || items[0].Kind != "prompt" || items[0].Message != "prompt: trust" || items[0].AgeSec != 5000 {
		t.Errorf("items = %+v, want one prompt item aged 5000", items)
	}
}

func TestLogWorkersKeepsPRURLAfterDone(t *testing.T) {
	ids := identityFromCrewLog(strings.NewReader(idDispatch + "\n" +
		`{"ts":2000,"crew_id":"c1","from":"worker:feat/a#s1","kind":"status","body":{"state":"pr_open","pr_url":"https://github.com/o/r/pull/9"}}` + "\n" +
		`{"ts":3000,"crew_id":"c1","from":"worker:feat/a#s1","kind":"status","body":{"state":"done"}}` + "\n"))
	ws, _ := logWorkers(ids, 10_000, func(string) bool { return true })
	if len(ws) != 1 || deref(ws[0].PRURL) != "https://github.com/o/r/pull/9" {
		t.Errorf("workers = %+v, want the PR kept", ws)
	}
}
