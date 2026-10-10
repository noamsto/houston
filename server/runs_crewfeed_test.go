package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/noamsto/houston/crewfeed"
	"github.com/noamsto/houston/runs"
)

const feedCrew = "1700000000-42"

// feedLine is one bus line yielding a question entry for crew.
func feedLine(crew, branch, text string) string {
	return fmt.Sprintf(`{"ts":1,"crew_id":%q,"from":"worker:%s#s1-1","to":"dispatcher:%s","kind":"msg","body":%q}`+"\n", crew, branch, crew, text)
}

func writeFeedFile(t *testing.T, bus, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bus, "events.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFeedFile(t *testing.T, bus, data string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(bus, "events.jsonl"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func dispatcherDelta(bus, crew string) runs.Delta {
	return runs.Delta{Source: "crew", Key: "crew/" + bus + "/dispatcher", Run: runs.Run{
		Agent:   "claude",
		State:   runs.StateRunning,
		Role:    runs.RoleDispatcher,
		Crew:    &runs.CrewRef{Name: crew},
		CrewBus: bus,
	}}
}

// feedServer serves a dispatcher run over a real store reading bus, on a real
// listener so SSE frames flush. Lines are written to the bus file first; none
// writes no file at all. tune runs before the listener starts.
func feedServer(t *testing.T, lines []string, tune ...func(*Server)) (*Server, *httptest.Server, string, string) {
	t.Helper()
	bus := t.TempDir()
	if len(lines) > 0 {
		writeFeedFile(t, bus, strings.Join(lines, ""))
	}
	store := crewfeed.NewStore()
	store.Advance(bus)
	s := newReplyServer(t, nil, dispatcherDelta(bus, feedCrew))
	s.crewFeed = store
	for _, fn := range tune {
		fn(s)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, bus, "/api/runs/" + s.runs.Snapshot()[0].ID + "/crew/feed"
}

func getFeed(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	return doChat(t, s, path)
}

func decodePage(t *testing.T, rec *httptest.ResponseRecorder) crewfeed.Page {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %q, want 200", rec.Code, rec.Body)
	}
	var p crewfeed.Page
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return p
}

func feedTexts(es []crewfeed.Entry) string {
	parts := make([]string, len(es))
	for i, e := range es {
		parts[i] = e.Text
	}
	return strings.Join(parts, ",")
}

func decodeEntries(t *testing.T, f sseFrame) []crewfeed.Entry {
	t.Helper()
	if f.event != "entries" {
		t.Fatalf("frame %+v, want event: entries", f)
	}
	var es []crewfeed.Entry
	if err := json.Unmarshal([]byte(f.data), &es); err != nil {
		t.Fatalf("decode frame data: %v; %q", err, f.data)
	}
	if len(es) == 0 || f.id != es[len(es)-1].ID {
		t.Fatalf("frame id %q, want the last entry's id of %+v", f.id, es)
	}
	return es
}

func TestRunCrewFeedWithoutRegistryIs503(t *testing.T) {
	s := &Server{}
	for _, h := range []http.HandlerFunc{s.handleRunCrewFeed, s.handleRunCrewFeedStream} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x", nil)
		req.SetPathValue("id", "x")
		h(rec, req)
		wantRefusal(t, rec, http.StatusServiceUnavailable, "run registry not started")
	}
}

func TestRunCrewFeedLadder(t *testing.T) {
	bus := t.TempDir()
	store := crewfeed.NewStore()
	store.Advance(bus)

	worker := dispatcherDelta(bus, feedCrew)
	worker.Key, worker.Run.Role = "crew/"+bus+"/w", runs.RoleWorker
	solo := dispatcherDelta(bus, feedCrew)
	solo.Key, solo.Run.Role, solo.Run.Crew = "crew/"+bus+"/s", "", nil
	noBus := dispatcherDelta(bus, feedCrew)
	noBus.Key, noBus.Run.CrewBus = "crew/"+bus+"/n", ""
	noName := dispatcherDelta(bus, "")
	noName.Key = "crew/" + bus + "/m"

	for name, d := range map[string]runs.Delta{"worker": worker, "solo": solo, "no bus": noBus, "no crew name": noName} {
		t.Run(name, func(t *testing.T) {
			s := newReplyServer(t, nil, d)
			s.crewFeed = store
			base := "/api/runs/" + s.runs.Snapshot()[0].ID + "/crew/feed"
			wantRefusal(t, getFeed(t, s, base), http.StatusNotFound, "no crew")
			wantRefusal(t, getFeed(t, s, base+"/stream"), http.StatusNotFound, "no crew")
		})
	}

	t.Run("no store", func(t *testing.T) {
		s := newReplyServer(t, nil, dispatcherDelta(bus, feedCrew))
		wantRefusal(t, getFeed(t, s, "/api/runs/"+s.runs.Snapshot()[0].ID+"/crew/feed"), http.StatusNotFound, "no crew")
	})

	t.Run("no such run", func(t *testing.T) {
		s := newReplyServer(t, nil, dispatcherDelta(bus, feedCrew))
		s.crewFeed = store
		wantRefusal(t, getFeed(t, s, "/api/runs/nope/crew/feed"), http.StatusNotFound, "no such run")
		wantRefusal(t, getFeed(t, s, "/api/runs/nope/crew/feed/stream"), http.StatusNotFound, "no such run")
	})

	t.Run("bus never advanced", func(t *testing.T) {
		s := newReplyServer(t, nil, dispatcherDelta(t.TempDir(), feedCrew))
		s.crewFeed = store
		wantRefusal(t, getFeed(t, s, "/api/runs/"+s.runs.Snapshot()[0].ID+"/crew/feed"), http.StatusNotFound, "no crew")
	})
}

func TestRunCrewFeedBusWithoutFileIsAnEmptyPage(t *testing.T) {
	s, _, _, path := feedServer(t, nil)
	p := decodePage(t, getFeed(t, s, path))
	if p.Epoch == "" || p.More || p.Entries == nil || len(p.Entries) != 0 {
		t.Fatalf("page = %+v, want an epoch and a non-nil empty entries array", p)
	}
	if body := getFeed(t, s, path).Body.String(); !strings.Contains(body, `"entries":[]`) {
		t.Fatalf("body %q, want entries as []", body)
	}
}

func TestRunCrewFeedPagesBackwardsAndOnlyForItsCrew(t *testing.T) {
	s, _, _, path := feedServer(t, []string{
		feedLine(feedCrew, "feat/a", "q1"),
		feedLine("1700000001-7", "feat/z", "other"),
		feedLine(feedCrew, "feat/a", "q2"),
		feedLine(feedCrew, "feat/a", "q3"),
		feedLine(feedCrew, "feat/a", "q4"),
		feedLine(feedCrew, "feat/a", "q5"),
	})

	p := decodePage(t, getFeed(t, s, path+"?limit=2"))
	if feedTexts(p.Entries) != "q4,q5" || !p.More {
		t.Fatalf("newest page = %s more=%v, want q4,q5 more", feedTexts(p.Entries), p.More)
	}
	if got := p.Entries[0]; got.Kind != crewfeed.KindQuestion || got.Branch != "feat/a" || !strings.HasPrefix(got.ID, p.Epoch+".") {
		t.Fatalf("entry %+v, want a question on feat/a with an id in %s", got, p.Epoch)
	}

	p2 := decodePage(t, getFeed(t, s, path+"?limit=2&before="+p.Entries[0].ID))
	if feedTexts(p2.Entries) != "q2,q3" || !p2.More {
		t.Fatalf("older page = %s more=%v, want q2,q3 more", feedTexts(p2.Entries), p2.More)
	}
	p3 := decodePage(t, getFeed(t, s, path+"?before="+p2.Entries[0].ID))
	if feedTexts(p3.Entries) != "q1" || p3.More {
		t.Fatalf("oldest page = %s more=%v, want q1", feedTexts(p3.Entries), p3.More)
	}

	// Nothing is older than the first byte, which the store alone would read
	// as "newest overall".
	p4 := decodePage(t, getFeed(t, s, path+"?before="+p.Epoch+".0"))
	if len(p4.Entries) != 0 || p4.More || p4.Epoch != p.Epoch {
		t.Fatalf("before offset 0 = %+v, want an empty page", p4)
	}
}

func TestRunCrewFeedRefusals(t *testing.T) {
	s, _, _, path := feedServer(t, []string{feedLine(feedCrew, "feat/a", "q1")})
	epoch := decodePage(t, getFeed(t, s, path)).Epoch

	for _, q := range []string{"?limit=x", "?limit=1.5"} {
		wantRefusal(t, getFeed(t, s, path+q), http.StatusBadRequest, "bad limit")
	}
	for _, c := range []string{"nodot", ".3", "ep.", "ep.x", "ep.-1", "ep.99999999999999999999"} {
		wantRefusal(t, getFeed(t, s, path+"?before="+c), http.StatusBadRequest, "malformed cursor")
	}
	for _, c := range []string{"old.5", "old.0"} {
		rec := getFeed(t, s, path+"?before="+c)
		if rec.Code != http.StatusConflict || strings.TrimSpace(rec.Body.String()) != `{"reset":true}` ||
			rec.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("before=%s: %d %q (%s), want 409 {\"reset\":true} as JSON", c, rec.Code, rec.Body, rec.Header().Get("Content-Type"))
		}
	}
	if rec := getFeed(t, s, path+"?before="+epoch+".5"); rec.Code != http.StatusOK {
		t.Fatalf("current epoch: %d, want 200", rec.Code)
	}
}

func TestRunCrewFeedStreamMalformedCursorIs400(t *testing.T) {
	s, _, _, path := feedServer(t, []string{feedLine(feedCrew, "feat/a", "q1")})
	path += "/stream"
	for _, c := range []string{"nodot", ".3", "ep.", "ep.x", "ep.-1"} {
		rec := getFeed(t, s, path+"?after="+c)
		if rec.Code != http.StatusBadRequest || rec.Header().Get("Content-Type") == "text/event-stream" {
			t.Errorf("after=%q: %d %q (%s), want 400 before SSE", c, rec.Code, rec.Body, rec.Header().Get("Content-Type"))
		}
		req := replyRequest("GET", path, "")
		req.Header.Set("Last-Event-ID", c)
		rec = httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("Last-Event-ID %q: %d, want 400", c, rec.Code)
		}
	}
}

func fastCheck(s *Server) { s.crewFeedCheck = 20 * time.Millisecond }

func TestRunCrewFeedStreamResumesAfterCursorAndFollowsAppends(t *testing.T) {
	s, ts, bus, path := feedServer(t, []string{
		feedLine(feedCrew, "feat/a", "q1"),
		feedLine(feedCrew, "feat/a", "q2"),
		feedLine(feedCrew, "feat/a", "q3"),
	}, fastCheck)
	all := decodePage(t, getFeed(t, s, path)).Entries

	c := openChatStream(t, ts, path+"/stream?after="+all[0].ID, "")
	if c.resp.StatusCode != 200 {
		t.Fatalf("status %d", c.resp.StatusCode)
	}
	for k, v := range map[string]string{"Content-Type": "text/event-stream", "Cache-Control": "no-cache", "X-Accel-Buffering": "no"} {
		if got := c.resp.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if got := decodeEntries(t, c.next(t)); feedTexts(got) != "q2,q3" {
		t.Fatalf("first batch %s, want q2,q3", feedTexts(got))
	}

	appendFeedFile(t, bus, feedLine(feedCrew, "feat/a", "q4")+feedLine("1700000001-7", "feat/z", "other")+feedLine(feedCrew, "feat/a", "q5"))
	s.crewFeed.Advance(bus)
	if got := decodeEntries(t, c.next(t)); feedTexts(got) != "q4,q5" {
		t.Fatalf("appended batch %s, want q4,q5", feedTexts(got))
	}
}

func TestRunCrewFeedStreamLastEventIDWinsOverAfter(t *testing.T) {
	s, ts, bus, path := feedServer(t, []string{
		feedLine(feedCrew, "feat/a", "q1"),
		feedLine(feedCrew, "feat/a", "q2"),
		feedLine(feedCrew, "feat/a", "q3"),
	}, fastCheck)
	all := decodePage(t, getFeed(t, s, path)).Entries

	c := openChatStream(t, ts, path+"/stream?after="+all[0].ID, all[1].ID)
	if got := decodeEntries(t, c.next(t)); feedTexts(got) != "q3" {
		t.Fatalf("resumed batch %s, want q3 only", feedTexts(got))
	}
	appendFeedFile(t, bus, feedLine(feedCrew, "feat/a", "q4"))
	s.crewFeed.Advance(bus)
	if got := decodeEntries(t, c.next(t)); feedTexts(got) != "q4" {
		t.Fatalf("appended batch %s, want q4", feedTexts(got))
	}
}

func TestRunCrewFeedStreamFromEpochIncludesTheEntryAtOffsetZero(t *testing.T) {
	s, ts, bus, path := feedServer(t, []string{
		feedLine(feedCrew, "feat/a", "q1"),
		feedLine(feedCrew, "feat/a", "q2"),
	}, fastCheck)
	epoch, _ := s.crewFeed.Epoch(bus)

	c := openChatStream(t, ts, path+"/stream?from="+epoch, "")
	got := decodeEntries(t, c.next(t))
	if feedTexts(got) != "q1,q2" || got[0].ID != epoch+".0" {
		t.Fatalf("batch %s (first id %s), want q1,q2 starting at %s.0", feedTexts(got), got[0].ID, epoch)
	}

	// The same cursor shape a client holding only the epoch would send as
	// after= skips offset 0.
	c2 := openChatStream(t, ts, path+"/stream?after="+epoch+".0", "")
	if got := decodeEntries(t, c2.next(t)); feedTexts(got) != "q2" {
		t.Fatalf("after=%s.0 batch %s, want q2", epoch, feedTexts(got))
	}
}

func TestRunCrewFeedStreamWithoutCursorSendsOnlyLaterEntries(t *testing.T) {
	s, ts, bus, path := feedServer(t, []string{
		feedLine(feedCrew, "feat/a", "q1"),
		feedLine(feedCrew, "feat/a", "q2"),
	}, fastCheck)
	c := openChatStream(t, ts, path+"/stream", "")

	appendFeedFile(t, bus, feedLine(feedCrew, "feat/a", "q3"))
	s.crewFeed.Advance(bus)
	if got := decodeEntries(t, c.next(t)); feedTexts(got) != "q3" {
		t.Fatalf("batch %s, want only q3", feedTexts(got))
	}
}

func TestRunCrewFeedStreamWithoutCursorOnAnCrewWithNoEntriesSendsEveryEntryThatFollows(t *testing.T) {
	s, ts, bus, path := feedServer(t, []string{feedLine("1700000001-7", "feat/z", "other")}, fastCheck)
	c := openChatStream(t, ts, path+"/stream", "")

	appendFeedFile(t, bus, feedLine(feedCrew, "feat/a", "q1"))
	s.crewFeed.Advance(bus)
	if got := decodeEntries(t, c.next(t)); feedTexts(got) != "q1" {
		t.Fatalf("batch %s, want q1", feedTexts(got))
	}
}

func TestRunCrewFeedStreamResets(t *testing.T) {
	t.Run("foreign epoch", func(t *testing.T) {
		_, ts, _, path := feedServer(t, []string{feedLine(feedCrew, "feat/a", "q1")})
		openChatStream(t, ts, path+"/stream?after=old.0", "").wantReset(t)
		openChatStream(t, ts, path+"/stream?from=old", "").wantReset(t)
	})

	t.Run("cursor past the file", func(t *testing.T) {
		s, ts, bus, path := feedServer(t, []string{feedLine(feedCrew, "feat/a", "q1")})
		epoch, _ := s.crewFeed.Epoch(bus)
		openChatStream(t, ts, path+"/stream?after="+epoch+".9999", "").wantReset(t)
	})

	t.Run("bus file replaced while open", func(t *testing.T) {
		s, ts, bus, path := feedServer(t, []string{feedLine(feedCrew, "feat/a", "q1")}, fastCheck)
		epoch, _ := s.crewFeed.Epoch(bus)
		c := openChatStream(t, ts, path+"/stream?from="+epoch, "")
		decodeEntries(t, c.next(t))

		writeFeedFile(t, bus, feedLine(feedCrew, "feat/b", "fresh"))
		s.crewFeed.Advance(bus)
		c.wantReset(t)
	})

	t.Run("crew changes while open", func(t *testing.T) {
		s, ts, bus, path := feedServer(t, []string{feedLine(feedCrew, "feat/a", "q1")}, fastCheck)
		epoch, _ := s.crewFeed.Epoch(bus)
		c := openChatStream(t, ts, path+"/stream?from="+epoch, "")
		decodeEntries(t, c.next(t))

		s.runs.Apply(dispatcherDelta(bus, "1700000001-7"))
		c.wantReset(t)
	})

	t.Run("bus changes while open", func(t *testing.T) {
		s, ts, bus, path := feedServer(t, []string{feedLine(feedCrew, "feat/a", "q1")}, fastCheck)
		epoch, _ := s.crewFeed.Epoch(bus)
		c := openChatStream(t, ts, path+"/stream?from="+epoch, "")
		decodeEntries(t, c.next(t))

		moved := dispatcherDelta(t.TempDir(), feedCrew)
		moved.Key = "crew/" + bus + "/dispatcher"
		s.runs.Apply(moved)
		c.wantReset(t)
	})

	t.Run("run gone while open", func(t *testing.T) {
		s, ts, bus, path := feedServer(t, []string{feedLine(feedCrew, "feat/a", "q1")}, fastCheck)
		epoch, _ := s.crewFeed.Epoch(bus)
		c := openChatStream(t, ts, path+"/stream?from="+epoch, "")
		decodeEntries(t, c.next(t))

		d := dispatcherDelta(bus, feedCrew)
		d.Gone = true
		s.runs.Apply(d)
		c.wantReset(t)
	})
}

func TestRunCrewFeedStreamPings(t *testing.T) {
	_, ts, _, path := feedServer(t, nil, func(s *Server) { s.crewFeedPing = 20 * time.Millisecond })
	c := openChatStream(t, ts, path+"/stream", "")
	deadline := time.After(3 * time.Second)
	for {
		select {
		case fr, ok := <-c.frames:
			if !ok {
				t.Fatal("stream ended before a ping")
			}
			if fr.event == "ping" {
				return
			}
		case <-deadline:
			t.Fatal("no ping within 3s")
		}
	}
}
