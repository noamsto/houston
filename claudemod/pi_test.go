package claudemod

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/noamsto/houston/hook"
)

func piRec(seq int64, ev string, mut func(*Record)) Record {
	r := Record{V: 1, SID: "p", Boot: "b", Seq: seq, TS: ts0 + seq*1000, Ev: ev, Engine: EnginePi}
	if mut != nil {
		mut(&r)
	}
	return r
}

func piTool(id string) func(*Record) {
	return func(r *Record) { r.ID, r.Tool = id, "bash" }
}

func piSession(t *testing.T, st *Store) Session {
	t.Helper()
	s, ok := st.Session("p")
	if !ok {
		t.Fatal("no session p")
	}
	return s
}

func piApply(st *Store, seq int64, ev string, mut func(*Record)) {
	st.Apply(piRec(seq, ev, mut))
}

func permutations(n int) [][]int {
	if n == 0 {
		return [][]int{{}}
	}
	var out [][]int
	for _, p := range permutations(n - 1) {
		for i := 0; i <= len(p); i++ {
			q := append(append(append([]int{}, p[:i]...), n-1), p[i:]...)
			out = append(out, q)
		}
	}
	return out
}

func TestPiLateRecordNeverReopensAfterSettle(t *testing.T) {
	recs := []Record{
		piRec(1, evTurnStart, nil),
		piRec(2, evStepStart, nil),
		piRec(3, evToolStart, piTool("a")),
		piRec(4, evToolEnd, piTool("a")),
		piRec(5, evStepStart, nil),
		piRec(6, evTurnComplete, nil),
	}
	perms := permutations(len(recs))
	if len(perms) != 720 {
		t.Fatalf("perms = %d", len(perms))
	}
	for _, p := range perms {
		st := NewStore()
		for _, i := range p {
			st.Apply(recs[i])
		}
		s := piSession(t, st)
		if s.State != hook.StateWaiting || len(s.InFlight) != 0 {
			t.Fatalf("order %v: state = %q, in flight = %v", p, s.State, s.InFlight)
		}
	}
}

func TestPiHigherSeqAfterSettle(t *testing.T) {
	st := NewStore()
	piApply(st, 1, evTurnStart, nil)
	piApply(st, 2, evTurnComplete, nil)
	piApply(st, 3, evStepStart, nil)
	if got := piSession(t, st).State; got != hook.StateWaiting {
		t.Fatalf("after step.start state = %q, want waiting", got)
	}
	piApply(st, 4, evToolStart, piTool("a"))
	if got := piSession(t, st).State; got != hook.StateWaiting {
		t.Fatalf("after tool.start state = %q, want waiting", got)
	}
	piApply(st, 5, evToolEnd, piTool("zzz"))
	if got := piSession(t, st).State; got != hook.StateWaiting {
		t.Fatalf("after tool.end state = %q, want waiting", got)
	}
}

func TestPiAbortSettles(t *testing.T) {
	st := NewStore()
	piApply(st, 1, evTurnStart, nil)
	piApply(st, 2, evToolStart, piTool("a"))
	piApply(st, 3, evTurnComplete, func(r *Record) { r.Reason = "aborted" })
	s := piSession(t, st)
	if s.State != hook.StateWaiting || len(s.InFlight) != 0 || !s.Known {
		t.Fatalf("state = %q, in flight = %v, known = %v", s.State, s.InFlight, s.Known)
	}
}

func TestPiRetryKeepsTurn(t *testing.T) {
	st := NewStore()
	piApply(st, 1, evTurnStart, nil)
	piApply(st, 2, evToolStart, piTool("a"))
	piApply(st, 3, evTurnStart, nil)
	s := piSession(t, st)
	if s.Turns != 1 || s.State != hook.StateThinking || len(s.InFlight) != 0 {
		t.Fatalf("turns = %d, state = %q, in flight = %v", s.Turns, s.State, s.InFlight)
	}
	piApply(st, 4, evTurnComplete, nil)
	piApply(st, 5, evTurnStart, nil)
	if got := piSession(t, st).Turns; got != 2 {
		t.Fatalf("turns = %d, want 2", got)
	}
}

func TestPiStepStartRecovers(t *testing.T) {
	st := NewStore()
	piApply(st, 1, evStepStart, nil)
	s := piSession(t, st)
	if s.State != hook.StateThinking || !s.Known {
		t.Fatalf("state = %q, known = %v", s.State, s.Known)
	}
}

func TestPiToolPhases(t *testing.T) {
	st := NewStore()
	piApply(st, 1, evTurnStart, nil)
	piApply(st, 2, evToolStart, piTool("a"))
	piApply(st, 3, evToolStart, piTool("b"))
	piApply(st, 4, evToolEnd, piTool("a"))
	if got := piSession(t, st).State; got != hook.StateToolRunning {
		t.Fatalf("state = %q, want tool-running", got)
	}
	piApply(st, 5, evToolEnd, piTool("b"))
	if got := piSession(t, st).State; got != hook.StateThinking {
		t.Fatalf("state = %q, want thinking", got)
	}
}

func TestPiEnded(t *testing.T) {
	st := NewStore()
	piApply(st, 1, evSessionStart, nil)
	piApply(st, 2, evTurnStart, nil)
	piApply(st, 3, evToolStart, piTool("a"))
	piApply(st, 4, evSessionEnd, nil)
	piApply(st, 5, evTurnComplete, nil)
	piApply(st, 6, evToolEnd, piTool("a"))
	if got := piSession(t, st).State; got != hook.StateEnded {
		t.Fatalf("state = %q, want ended", got)
	}
}

func TestPiIgnoresClaudeOnlyEvents(t *testing.T) {
	st := NewStore()
	piApply(st, 1, evTurnStart, nil)
	piApply(st, 2, evPermission, func(r *Record) { r.Tool = "X" })
	piApply(st, 3, evNotification, func(r *Record) { r.Type = "permission_prompt" })
	piApply(st, 4, evToolCheck, func(r *Record) { r.Decision = "ask" })
	piApply(st, 5, evStop, func(r *Record) { r.Tasks = []Task{} })
	s := piSession(t, st)
	if s.State != hook.StateThinking || s.BGKnown || s.StopSeen {
		t.Fatalf("state = %q, bgKnown = %v, stopSeen = %v", s.State, s.BGKnown, s.StopSeen)
	}
}

func TestPiEngineOnSession(t *testing.T) {
	st := NewStore()
	piApply(st, 1, evTurnStart, nil)
	if got := piSession(t, st).Engine; got != "pi" {
		t.Fatalf("engine = %q, want pi", got)
	}
	st2 := NewStore()
	r := piRec(1, evTurnStart, nil)
	r.Engine = ""
	st2.Apply(r)
	if got := piSession(t, st2).Engine; got != "" {
		t.Fatalf("engine = %q, want empty", got)
	}
}

func TestPiGoldenRecordsDecode(t *testing.T) {
	raw, err := os.ReadFile("../contrib/pi-mod/testdata/records.json")
	if err != nil {
		t.Fatal(err)
	}
	var recs []json.RawMessage
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	st := NewStore()
	h := Handler(st)
	for i, rec := range recs {
		body, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/record", bytes.NewReader(body)))
		if w.Code != http.StatusNoContent {
			t.Fatalf("record %d: status = %d, want 204", i, w.Code)
		}
	}
	s, ok := st.Session("sid-1")
	if !ok {
		t.Fatal("no session sid-1")
	}
	if s.State != hook.StateEnded || s.Engine != "pi" || s.CWD != "/tmp/p" ||
		s.Transcript != "/tmp/p/s.jsonl" || s.Pane != "%7" || s.TmuxServer != "4242" || s.Applied != len(recs) {
		t.Fatalf("session = %+v, want ended pi /tmp/p /tmp/p/s.jsonl %%7 4242 applied %d", s, len(recs))
	}
}
