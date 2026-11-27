package crewview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const eventsDoc = `{"schema_version":1,"view":"events","generated_at":5,"host":"box","cursor":"7:120","reset":false,"more":true,"errors":[],` +
	`"events":[{"id":"0-aaaaaaaaaaaaaaaa","host":"box","ts":1,"kind":"dispatch","from":"d","to":"","crew":"c1","branch":"feat/a","text":"t","row":{"ts":1,"kind":"dispatch"}}]}`

func TestDecodeEvents(t *testing.T) {
	p, err := DecodeEvents([]byte(eventsDoc))
	if err != nil {
		t.Fatal(err)
	}
	if p.Cursor != "7:120" || !p.More || p.Reset || len(p.Events) != 1 || p.Events[0].Branch != "feat/a" || string(p.Events[0].Row) != `{"ts":1,"kind":"dispatch"}` {
		t.Errorf("page = %+v", p)
	}
	for name, doc := range map[string]string{
		"other view":    strings.Replace(eventsDoc, `"view":"events"`, `"view":"overview"`, 1),
		"newer schema":  strings.Replace(eventsDoc, `"schema_version":1`, `"schema_version":2`, 1),
		"older schema":  strings.Replace(eventsDoc, `"schema_version":1`, `"schema_version":0`, 1),
		"not an object": `[]`,
	} {
		if _, err := DecodeEvents([]byte(doc)); err == nil {
			t.Errorf("%s: decoded, want an error", name)
		}
	}
	var u *UnsupportedError
	if _, err := DecodeEvents([]byte(strings.Replace(eventsDoc, `"schema_version":1`, `"schema_version":2`, 1))); !errors.As(err, &u) {
		t.Errorf("newer schema err = %v, want UnsupportedError", err)
	}
}

func TestEventOffset(t *testing.T) {
	for id, want := range map[string]int64{"0-ab": 0, "1234-ffff": 1234} {
		if got, ok := EventOffset(id); !ok || got != want {
			t.Errorf("EventOffset(%q) = %d, %v; want %d", id, got, ok, want)
		}
	}
	for _, id := range []string{"", "12", "x-1", "-1-2", "-5"} {
		if _, ok := EventOffset(id); ok {
			t.Errorf("EventOffset(%q) ok, want not", id)
		}
	}
}

type call struct {
	dir  string
	args []string
}

func fakeRunner(out string, err error, calls *[]call) Runner {
	return func(_ context.Context, dir string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{dir, args})
		return []byte(out), err
	}
}

func TestFetchEventsArgs(t *testing.T) {
	var calls []call
	run := fakeRunner(eventsDoc+"\n", nil, &calls)
	if _, err := FetchEvents(context.Background(), run, "/repo", "", 500); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchEvents(context.Background(), run, "/repo", "7:120", 0); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{"/repo", []string{"dash", "--json", "--view", "events", "--limit", "500"}},
		{"/repo", []string{"dash", "--json", "--view", "events", "--since", "7:120"}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	boom := errors.New("boom")
	if _, err := FetchEvents(context.Background(), fakeRunner("", boom, &calls), "/repo", "", 1); !errors.Is(err, boom) {
		t.Errorf("err = %v, want boom", err)
	}
}

func TestInitCrew(t *testing.T) {
	var calls []call
	res, err := InitCrew(context.Background(), fakeRunner(`{"id":"c1","dir":"/s/crews/c1","log":"/s/crews/c1/events.jsonl","created":true}`+"\n", nil, &calls), "/repo", "c1")
	if err != nil || res.Dir != "/s/crews/c1" {
		t.Fatalf("InitCrew = %+v, %v", res, err)
	}
	if want := []call{{"/repo", []string{"init", "c1"}}}; !reflect.DeepEqual(calls, want) {
		t.Errorf("calls = %v, want %v", calls, want)
	}
	if _, err := InitCrew(context.Background(), fakeRunner(`{"id":"c1","created":false}`, nil, &calls), "/repo", "c1"); !errors.Is(err, ErrCrewExists) {
		t.Errorf("existing crew err = %v, want ErrCrewExists", err)
	}
	if _, err := InitCrew(context.Background(), fakeRunner(`{"id":"other","created":true}`, nil, &calls), "/repo", "c1"); err == nil {
		t.Error("an answer for another crew was accepted")
	}
	if _, err := InitCrew(context.Background(), fakeRunner(`not json`, nil, &calls), "/repo", "c1"); err == nil {
		t.Error("garbage was accepted")
	}
}

func TestListCrews(t *testing.T) {
	var calls []call
	out := "crew_id\tlast_event_s\tfirst_event_s\tworkers\tpid\talive\n" +
		"1795715621-4143066\t52368\t53235\t2\t1831225\tyes\n" +
		"1795460927-1438739\t221504\t308020\t3\t—\t—\n" +
		"short\trow\n"
	rows, err := ListCrews(context.Background(), fakeRunner(out, nil, &calls), "/repo", true)
	if err != nil {
		t.Fatal(err)
	}
	want := []CrewRow{
		{ID: "1795715621-4143066", Workers: 2, PID: 1831225, Alive: true},
		{ID: "1795460927-1438739", Workers: 3},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %+v, want %+v", rows, want)
	}
	if wantCall := []call{{"/repo", []string{"crews", "--home"}}}; !reflect.DeepEqual(calls, wantCall) {
		t.Errorf("calls = %v, want %v", calls, wantCall)
	}
}

// Output that is neither crew's table nor empty is an error, not "no crews".
func TestListCrewsUnrecognisedOutput(t *testing.T) {
	var calls []call
	if _, err := ListCrews(context.Background(), fakeRunner("usage: crew crews [--mine]\n", nil, &calls), "/repo", false); err == nil {
		t.Error("unrecognised output was read as no crews")
	}
	if rows, err := ListCrews(context.Background(), fakeRunner("", nil, &calls), "/repo", false); err != nil || len(rows) != 0 {
		t.Errorf("empty output = %v, %v; want no crews", rows, err)
	}
	if rows, err := ListCrews(context.Background(), fakeRunner("crew_id\tlast_event_s\tfirst_event_s\tworkers\tpid\talive\n", nil, &calls), "/repo", false); err != nil || len(rows) != 0 {
		t.Errorf("header only = %v, %v; want no crews", rows, err)
	}
}
