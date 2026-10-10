package crewfeed

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

type folded struct {
	Crew  string
	Entry Entry
}

// foldAll feeds data to f line by line, the way a store reading the whole
// file would.
func foldAll(f *Fold, data []byte) []folded {
	var out []folded
	var off int64
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if crew, e, ok := f.Line(data[:i], off); ok {
			out = append(out, folded{crew, e})
		}
		off += int64(i + 1)
		data = data[i+1:]
	}
	return out
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/bus.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestFoldFixture(t *testing.T) {
	pr165 := &PR{Number: 165, URL: "https://github.com/o/r/pull/165"}
	want := []folded{
		{"c1", Entry{TS: 1000, Kind: "dispatch", Text: `Dispatched "Add widget" · deep · claude · opus`, Branch: "feat/a", Codename: "apollo"}},
		{"c2", Entry{TS: 1001, Kind: "dispatch", Text: `Dispatched "Fix gadget" · standard · pi · small`, Branch: "feat/b", Codename: "borealis"}},
		{"c1", Entry{TS: 1002, Kind: "status", Text: "starting", Branch: "feat/a", State: "running"}},
		{"c1", Entry{TS: 1004, Kind: "status", Text: "watchdog: quiet", Branch: "feat/a", State: "running"}},
		{"c1", Entry{TS: 1006, Kind: "status", Text: "which database?", Branch: "feat/a", State: "blocked"}},
		{"c1", Entry{TS: 1008, Kind: "question", Text: "Which database should this use? sqlite or postgres", Branch: "feat/a"}},
		{"c1", Entry{TS: 1009, Kind: "reply", Text: "Use sqlite.", Branch: "feat/a"}},
		{"c1", Entry{TS: 1010, Kind: "status", Text: "which database?", Branch: "feat/a", State: "blocked"}},
		{"c1", Entry{TS: 1011, Kind: "status", Text: "which database now?", Branch: "feat/a", State: "blocked"}},
		{"c1", Entry{TS: 1012, Kind: "follow-ups", Text: "Follow-ups: - tidy the README", Branch: "feat/a"}},
		{"c1", Entry{TS: 1013, Kind: "pr", Text: "PR #165 checks PENDING → SUCCESS", PR: pr165}},
		{"c1", Entry{TS: 1014, Kind: "pr", Text: "PR #164 merged", PR: &PR{Number: 164, URL: "https://github.com/o/r/pull/164"}}},
		{"c1", Entry{TS: 1015, Kind: "status", Text: "ready for review", Branch: "feat/a", State: "pr_open", PR: pr165}},
		{"c2", Entry{TS: 1023, Kind: "question", Text: "question without crew id", Branch: "feat/b"}},
		{"c1", Entry{TS: 1024, Kind: "reap", Text: "Reaped (PR #252 MERGED)", Branch: "feat/a", PR: &PR{Number: 252, URL: "https://github.com/o/r/pull/252"}}},
		{"c2", Entry{TS: 1025, Kind: "reap", Text: "Released session (done)", Branch: "feat/b"}},
		{"c2", Entry{TS: 1026, Kind: "reap", Text: "Reclaimed windows (done)", Branch: "feat/b"}},
		{"c2", Entry{TS: 1028, Kind: "resume", Text: "Resumed · pi · small", Branch: "feat/b"}},
	}
	got := foldAll(&Fold{}, readFixture(t))
	if len(got) != len(want) {
		t.Fatalf("got %d entries, want %d:\n%+v", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("entry %d:\n got  %+v\n want %+v", i, got[i], want[i])
		}
	}
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func fold1(t *testing.T, f *Fold, line string) (string, Entry, bool) {
	t.Helper()
	return f.Line([]byte(line), 0)
}

func TestExclusions(t *testing.T) {
	setup := `{"crew_id":"c1","kind":"dispatch","branch":"feat/a","title":"t"}`
	lines := map[string]string{
		"claim":               `{"crew_id":"c1","from":"worker:feat/a#s1-1","kind":"claim","body":{"x":1}}`,
		"claim-issue":         `{"crew_id":"c1","kind":"claim-issue","branch":"feat/a"}`,
		"role status":         `{"crew_id":"c1","from":"role:feat/a:critic","kind":"status","body":{"state":"running"}}`,
		"non-worker status":   `{"crew_id":"c1","from":"dispatcher:c1","kind":"status","body":{"state":"running"}}`,
		"msg to role":         `{"crew_id":"c1","from":"worker:feat/a#s1-1","to":"role:feat/a","kind":"msg","body":"x"}`,
		"msg from role":       `{"crew_id":"c1","from":"role:feat/a","to":"worker:feat/a#s1-1","kind":"msg","body":"x"}`,
		"msg to metrics":      `{"crew_id":"c1","from":"worker:feat/a#s1-1","to":"metrics:c1","kind":"msg","body":"x"}`,
		"msg to review":       `{"crew_id":"c1","from":"worker:feat/a#s1-1","to":"review:c1","kind":"msg","body":"x"}`,
		"msg to retro":        `{"crew_id":"c1","from":"dispatcher:c1","to":"retro:c1","kind":"msg","body":"x"}`,
		"unknown kind":        `{"crew_id":"c1","kind":"heartbeat"}`,
		"unattributable reap": `{"kind":"reap","branch":"feat/zzz","pr_state":"MERGED"}`,
		"unattributable msg":  `{"from":"worker:feat/zzz#s1-1","to":"dispatcher:c9","kind":"msg","body":"x"}`,
		"empty status state":  `{"crew_id":"c1","from":"worker:feat/a#s1-1","kind":"status","body":{"state":""}}`,
		"msg body not string": `{"crew_id":"c1","from":"worker:feat/a#s1-1","to":"dispatcher:c1","kind":"msg","body":{"a":1}}`,
		"pr-watch bad json":   `{"crew_id":"c1","from":"pr-watch:1","to":"dispatcher:c1","kind":"msg","body":"not json"}`,
		"pr-watch no change":  `{"crew_id":"c1","from":"pr-watch:1","to":"dispatcher:c1","kind":"msg","body":"{\"pr\":1,\"changed\":[\"title\"]}"}`,
		"pr-watch reopened":   `{"crew_id":"c1","from":"pr-watch:1","to":"dispatcher:c1","kind":"msg","body":"{\"pr\":1,\"changed\":[\"state\"],\"state\":{\"state\":\"OPEN\"}}"}`,
		"pr-watch no number":  `{"crew_id":"c1","from":"pr-watch:1","to":"dispatcher:c1","kind":"msg","body":"{\"changed\":[\"checks\"],\"state\":{\"checks\":\"FAILURE\"}}"}`,
		"empty follow-ups":    `{"crew_id":"c1","from":"worker:feat/a#s1-1","to":"dispatcher:c1","kind":"msg","body":"follow-ups (untracked):  "}`,
		"unparseable":         `{"crew_id":"c1","kind":"dispatch","branch":`,
		"not json":            `hello`,
		"empty line":          ``,
	}
	for name, line := range lines {
		t.Run(name, func(t *testing.T) {
			var f Fold
			fold1(t, &f, setup)
			if crew, e, ok := fold1(t, &f, line); ok {
				t.Fatalf("got entry %q %+v, want none", crew, e)
			}
		})
	}
}

func TestPRWatchStates(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"closed":      {`{"pr":7,"url":"https://github.com/o/r/pull/7","changed":["state"],"state":{"state":"CLOSED"}}`, "PR #7 closed"},
		"merged wins": {`{"pr":7,"changed":["checks","state"],"state":{"state":"MERGED","checks":"SUCCESS"},"was":{"checks":"PENDING"}}`, "PR #7 merged"},
		"no was":      {`{"pr":7,"changed":["checks"],"state":{"checks":"FAILURE"}}`, "PR #7 checks FAILURE"},
		"number from url": {`{"url":"https://github.com/o/r/pull/8","changed":["checks"],"state":{"checks":"FAILURE"},"was":{"checks":"PENDING"}}`,
			"PR #8 checks PENDING → FAILURE"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var f Fold
			line := fmt.Sprintf(`{"crew_id":"c1","from":"pr-watch:7","to":"dispatcher:c1","kind":"msg","body":%s}`, quote(c.body))
			crew, e, ok := fold1(t, &f, line)
			if !ok || crew != "c1" || e.Kind != "pr" || e.Text != c.want {
				t.Fatalf("got %q %+v ok=%v, want text %q", crew, e, ok, c.want)
			}
		})
	}
}

func TestAttributionViaBranch(t *testing.T) {
	var f Fold
	fold1(t, &f, `{"crew_id":"c1","kind":"dispatch","branch":"feat/a","title":"t"}`)
	fold1(t, &f, `{"crew_id":"c2","kind":"dispatch","branch":"feat/b","title":"t"}`)
	crew, _, ok := fold1(t, &f, `{"kind":"reap","branch":"feat/a","pr_state":"MERGED"}`)
	if !ok || crew != "c1" {
		t.Fatalf("reap: crew %q ok=%v, want c1", crew, ok)
	}
	crew, _, ok = fold1(t, &f, `{"kind":"release","branch":"feat/b","state":"done"}`)
	if !ok || crew != "c2" {
		t.Fatalf("release: crew %q ok=%v, want c2", crew, ok)
	}
	// The newest dispatch or resume for a branch wins.
	fold1(t, &f, `{"crew_id":"c3","kind":"resume","branch":"feat/a","engine":"claude"}`)
	crew, _, ok = fold1(t, &f, `{"kind":"reap","branch":"feat/a"}`)
	if !ok || crew != "c3" {
		t.Fatalf("reap after resume: crew %q ok=%v, want c3", crew, ok)
	}
	// A record's own crew_id beats the branch map.
	crew, _, _ = fold1(t, &f, `{"crew_id":"c9","kind":"reclaim","branch":"feat/a","state":"done"}`)
	if crew != "c9" {
		t.Fatalf("reclaim: crew %q, want c9", crew)
	}
}

// A branch no git ref could be (over 255 bytes, or holding a control rune)
// drops its record, whichever field names it, and is never learned for
// attribution.
func TestInvalidBranchDropsTheRecord(t *testing.T) {
	long := "feat/" + strings.Repeat("x", 251)
	for name, branch := range map[string]string{"too long": long, "control rune": "feat/a\x07b", "newline": "feat/a\nb"} {
		t.Run(name, func(t *testing.T) {
			b := quote(branch)
			w := quote("worker:" + branch + "#s1-1")
			lines := []string{
				`{"crew_id":"c1","kind":"dispatch","branch":` + b + `,"title":"t"}`,
				`{"crew_id":"c1","kind":"resume","branch":` + b + `,"engine":"claude"}`,
				`{"crew_id":"c1","kind":"reap","branch":` + b + `}`,
				`{"crew_id":"c1","kind":"reclaim","branch":` + b + `}`,
				`{"crew_id":"c1","kind":"release","branch":` + b + `}`,
				`{"crew_id":"c1","from":` + w + `,"kind":"status","body":{"state":"running"}}`,
				`{"crew_id":"c1","from":` + w + `,"to":"dispatcher:c1","kind":"msg","body":"q?"}`,
				`{"crew_id":"c1","from":"dispatcher:c1","to":` + w + `,"kind":"msg","body":"a"}`,
			}
			var f Fold
			for _, line := range lines {
				if _, e, ok := fold1(t, &f, line); ok {
					t.Errorf("%s: got entry %+v, want none", line, e)
				}
			}
			if len(f.branchCrew) != 0 || len(f.branches) != 0 {
				t.Errorf("fold remembered the branch: %d crews, %d branches", len(f.branchCrew), len(f.branches))
			}
		})
	}

	var f Fold
	limit := "feat/" + strings.Repeat("x", 250)
	if _, e, ok := fold1(t, &f, `{"crew_id":"c1","kind":"dispatch","branch":`+quote(limit)+`}`); !ok || e.Branch != limit {
		t.Fatalf("a 255-byte branch: %+v ok=%v, want kept as is", e, ok)
	}
}

func TestReapText(t *testing.T) {
	cases := map[string]string{
		`{"crew_id":"c1","kind":"reap","branch":"b","pr":"https://github.com/o/r/pull/3","pr_state":"MERGED"}`: "Reaped (PR #3 MERGED)",
		`{"crew_id":"c1","kind":"reap","branch":"b","pr":"https://github.com/o/r/pull/3"}`:                     "Reaped (PR #3)",
		`{"crew_id":"c1","kind":"reap","branch":"b","pr":"javascript:alert(1)","pr_state":"CLOSED"}`:           "Reaped (CLOSED)",
		`{"crew_id":"c1","kind":"reap","branch":"b","pr":{"n":1}}`:                                             "Reaped",
		`{"crew_id":"c1","kind":"reclaim","branch":"b"}`:                                                       "Reclaimed windows",
		`{"crew_id":"c1","kind":"release","branch":"b","state":"done"}`:                                        "Released session (done)",
	}
	for line, want := range cases {
		var f Fold
		_, e, ok := fold1(t, &f, line)
		if !ok || e.Kind != "reap" || e.Text != want {
			t.Errorf("%s:\n got %+v ok=%v, want text %q", line, e, ok, want)
		}
	}
}

func statusLine(src, state, detail string) string {
	body := fmt.Sprintf(`{"state":%s,"detail":%s`, quote(state), quote(detail))
	if src != "" {
		body += fmt.Sprintf(`,"source":%s`, quote(src))
	}
	return `{"crew_id":"c1","from":"worker:feat/a#s1-1","kind":"status","body":` + body + `}}`
}

func TestWorkerAndWatchdogMasking(t *testing.T) {
	steps := []struct {
		src, state, detail string
		want               bool
	}{
		{"", "running", "a", true},
		{"watchdog", "running", "quiet: x", true}, // not masked by the worker's running
		{"", "running", "b", false},
		{"watchdog", "running", "quota: y", false},
		{"", "blocked", "q", true},
		{"watchdog", "running", "quiet: z", false}, // not unmasked by the worker's change
		{"watchdog", "failed", "dead: gone", true},
		{"", "running", "c", true},
	}
	var f Fold
	for i, s := range steps {
		_, _, ok := fold1(t, &f, statusLine(s.src, s.state, s.detail))
		if ok != s.want {
			t.Errorf("step %d (%s %s): emitted=%v, want %v", i, s.src, s.state, ok, s.want)
		}
	}
}

func TestReaskedBlocked(t *testing.T) {
	reply := `{"crew_id":"c1","from":"dispatcher:c1","to":"worker:feat/a#s1-1","kind":"msg","body":"answer"}`
	otherReply := `{"crew_id":"c1","from":"dispatcher:c1","to":"worker:feat/b#s1-1","kind":"msg","body":"answer"}`
	var f Fold
	emit := func(line string) bool { _, _, ok := fold1(t, &f, line); return ok }

	if !emit(statusLine("", "blocked", "q1")) {
		t.Fatal("first blocked not emitted")
	}
	if emit(statusLine("", "blocked", "q1")) {
		t.Fatal("same blocked detail re-emitted")
	}
	if !emit(statusLine("", "blocked", "q2")) {
		t.Fatal("blocked with new detail not emitted")
	}
	emit(otherReply)
	if emit(statusLine("", "blocked", "q2")) {
		t.Fatal("a reply to another branch re-opened the question")
	}
	emit(reply)
	if !emit(statusLine("", "blocked", "q2")) {
		t.Fatal("same blocked detail after a reply not emitted")
	}
	if emit(statusLine("", "blocked", "q2")) {
		t.Fatal("reply credited twice")
	}
	// A watchdog blocked never touches the non-watchdog blocked memory.
	emit(statusLine("watchdog", "blocked", "prompt: x"))
	if emit(statusLine("", "blocked", "q2")) {
		t.Fatal("watchdog blocked reset the worker blocked memory")
	}
	// Whitespace-only differences are not a new question.
	emit(statusLine("", "blocked", "q  3"))
	if emit(statusLine("", "blocked", "q\n3")) {
		t.Fatal("whitespace variant treated as new detail")
	}
}

func TestStatusText(t *testing.T) {
	cases := []struct {
		name, line, want string
	}{
		{"no detail", statusLine("", "blocked", ""), "blocked"},
		{"detail", statusLine("", "pr_open", "ready"), "ready"},
		{"detail sanitised", statusLine("", "blocked", "  which\n\tdb?\u0007 "), "which db?"},
		{"detail capped", statusLine("", "blocked", strings.Repeat("a", 250)), strings.Repeat("a", 200) + "…"},
		{"blank detail", statusLine("", "running", " \n "), "running"},
		{"watchdog listed", statusLine("watchdog", "running", "turn-stall: 12m"), "watchdog: turn-stall"},
		{"watchdog no colon", statusLine("watchdog", "running", "just words"), "watchdog"},
		{"watchdog uppercase prefix", statusLine("watchdog", "running", "Quiet: x"), "watchdog"},
		{"watchdog long prefix", statusLine("watchdog", "running", "abcdefghijklmnopq: x"), "watchdog"},
		{"watchdog prefix with space", statusLine("watchdog", "running", "two words: x"), "watchdog"},
		{"watchdog empty detail", statusLine("watchdog", "running", ""), "watchdog"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var f Fold
			_, e, ok := fold1(t, &f, c.line)
			if !ok || e.Text != c.want {
				t.Fatalf("got %+v ok=%v, want text %q", e, ok, c.want)
			}
		})
	}
}

func TestSanitising(t *testing.T) {
	long := strings.Repeat("é", 200)
	cases := []struct {
		name, in, want string
	}{
		{"newlines and tabs", "a\nb\t\tc\r\n d", "a b c d"},
		{"trim", "  \n a \n ", "a"},
		{"control runes dropped", "a\x00b\x07c\u007fd\u0085e\u009ff", "abcdef"},
		{"unicode space collapses", "a\u00a0\u2003b", "a b"},
		{"exact cap", long, long},
		{"cut multibyte", long + "é", long + "…"},
		{"cut after space", long + " é", long + "…"},
		{"cut multibyte wide", strings.Repeat("世", 250), strings.Repeat("世", 200) + "…"},
		{"cut drops trailing space", strings.Repeat("a", 199) + " b", strings.Repeat("a", 199) + "…"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			line := fmt.Sprintf(`{"crew_id":"c1","from":"dispatcher:c1","to":"worker:feat/a#s1-1","kind":"msg","body":%s}`, quote(c.in))
			var f Fold
			_, e, ok := fold1(t, &f, line)
			if !ok || e.Text != c.want {
				t.Fatalf("got %q ok=%v, want %q", e.Text, ok, c.want)
			}
		})
	}
}

func TestSanitisingAppliesToEveryBusField(t *testing.T) {
	var f Fold
	_, e, ok := fold1(t, &f, `{"crew_id":"c1","kind":"dispatch","branch":"b","title":"a\nb\u0007","name":"x\ty","tier":"d\ne"}`)
	if !ok || e.Text != `Dispatched "a b" · d e` || e.Codename != "x y" {
		t.Fatalf("got %+v", e)
	}
}

func TestPRURL(t *testing.T) {
	cases := map[string]bool{
		"https://github.com/o/r/pull/12":                     true,
		"https://github.com/some-org/my.repo_x/pull/1":       true,
		"http://github.com/o/r/pull/12":                      false,
		"https://gitlab.com/o/r/pull/12":                     false,
		"https://github.com.evil.example/o/r/pull/12":        false,
		"https://github.com/o/r/pull/12?x=1":                 false,
		"https://github.com/o/r/pull/12#c":                   false,
		"https://github.com/o/r/pull/12/files":               false,
		"https://github.com/o/r/pull/":                       false,
		"https://github.com/o/r/pull/x":                      false,
		"https://github.com/o/r/pull/12\n":                   false,
		"javascript:alert(1)//https://github.com/o/r/pull/1": false,
		"": false,
	}
	for url, valid := range cases {
		t.Run(url, func(t *testing.T) {
			var f Fold
			line := fmt.Sprintf(`{"crew_id":"c1","from":"worker:feat/a#s1-1","kind":"status","body":{"state":"pr_open","pr_url":%s}}`, quote(url))
			_, e, ok := fold1(t, &f, line)
			if !ok {
				t.Fatal("status not emitted")
			}
			if valid != (e.PR != nil) {
				t.Fatalf("pr=%+v, valid want %v", e.PR, valid)
			}
			if valid && (e.PR.URL != url || e.PR.Number == 0) {
				t.Fatalf("pr=%+v", e.PR)
			}
		})
	}
}

func TestUnparseableLineKeepsState(t *testing.T) {
	var f Fold
	fold1(t, &f, `{"crew_id":"c1","kind":"dispatch","branch":"feat/a","title":"t"}`)
	fold1(t, &f, statusLine("", "running", "x"))
	if _, _, ok := fold1(t, &f, `{"broken`); ok {
		t.Fatal("unparseable line yielded an entry")
	}
	if _, _, ok := fold1(t, &f, statusLine("", "running", "y")); ok {
		t.Fatal("state lost across an unparseable line")
	}
	if crew, _, ok := fold1(t, &f, `{"kind":"reap","branch":"feat/a"}`); !ok || crew != "c1" {
		t.Fatalf("branch map lost across an unparseable line: %q %v", crew, ok)
	}
}

// Entries are a pure function of the lines: a fresh Fold per run and any way
// of cutting the byte stream into reads give identical entries and offsets.
func TestChunkingIndependent(t *testing.T) {
	data := readFixture(t)
	want := foldAll(&Fold{}, data)
	if len(want) == 0 {
		t.Fatal("fixture yielded nothing")
	}

	t.Run("fresh fold", func(t *testing.T) {
		if got := foldAll(&Fold{}, data); !reflect.DeepEqual(got, want) {
			t.Fatalf("second pass differs:\n%+v\n%+v", got, want)
		}
	})

	for _, size := range []int{1, 2, 3, 7, 64, 4096} {
		t.Run(fmt.Sprintf("reader %d", size), func(t *testing.T) {
			r := &chunkReader{data: data, n: size}
			// Mirror a tail: read in arbitrary chunks, hand over only
			// complete lines with their start offsets.
			br := bufio.NewReaderSize(r, 16)
			var f Fold
			var got []folded
			var off int64
			for {
				line, err := br.ReadBytes('\n')
				if len(line) > 0 && line[len(line)-1] == '\n' {
					if crew, e, ok := f.Line(line[:len(line)-1], off); ok {
						got = append(got, folded{crew, e})
					}
				}
				off += int64(len(line))
				if err != nil {
					break
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %d entries, want %d", len(got), len(want))
			}
		})
	}

	t.Run("resumed fold", func(t *testing.T) {
		// Splitting at every line boundary and folding the halves in order
		// with the same Fold is the same as one pass.
		lines := bytes.SplitAfter(data, []byte("\n"))
		for cut := range lines {
			var f Fold
			var got []folded
			got = append(got, foldAll(&f, bytes.Join(lines[:cut], nil))...)
			got = append(got, foldAll(&f, bytes.Join(lines[cut:], nil))...)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("cut at line %d: got %d entries, want %d", cut, len(got), len(want))
			}
		}
	})
}

type chunkReader struct {
	data []byte
	n    int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if len(c.data) == 0 {
		return 0, io.EOF
	}
	n := min(c.n, len(p), len(c.data))
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}
