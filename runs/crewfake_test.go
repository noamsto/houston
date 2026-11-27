package runs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/noamsto/houston/crewview"
)

// fileCrew is the test double for the crew CLI's events view: a Runner that
// answers `crew dash --json --view events` in a checkout root from the
// fixture <bus>/events.jsonl dirs maps root to, the way the real command does
// (rows with their start offsets, cursor resume, a torn last line withheld,
// the last --limit rows with no cursor). The tests write plain fixtures;
// production code never reads a bus file.
func fileCrew(dirs map[string]string) crewview.Runner {
	return func(_ context.Context, dir string, args ...string) ([]byte, error) {
		if len(args) < 4 || args[0] != "dash" || args[3] != crewview.ViewEvents {
			return nil, fmt.Errorf("fileCrew: unexpected crew args %v", args)
		}
		var since string
		limit := 0
		for i := 4; i+1 < len(args); i += 2 {
			switch args[i] {
			case "--since":
				since = args[i+1]
			case "--limit":
				limit, _ = strconv.Atoi(args[i+1])
			}
		}
		bus := dirs[dir]
		if bus == "" {
			return nil, fmt.Errorf("fileCrew: no bus for %q", dir)
		}
		return eventsPage(filepath.Join(bus, "events.jsonl"), since, limit)
	}
}

type fileEvent struct {
	ID  string          `json:"id"`
	TS  int64           `json:"ts"`
	Row json.RawMessage `json:"row"`
}

func eventsPage(path, since string, limit int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		data = []byte{}
	}
	var rows []fileEvent
	var off int64
	end := int64(0)
	for {
		i := bytes.IndexByte(data[off:], '\n')
		if i < 0 {
			break
		}
		line := data[off : off+int64(i)]
		sum := sha256.Sum256(line)
		if json.Valid(line) {
			rows = append(rows, fileEvent{ID: fmt.Sprintf("%d-%s", off, hex.EncodeToString(sum[:])[:16]), Row: bytes.Clone(line)})
		}
		off += int64(i + 1)
		end = off
	}
	reset := false
	if since != "" {
		_, o, _ := strings.Cut(since, ":")
		start, _ := strconv.ParseInt(o, 10, 64)
		if start > end {
			reset = true
		} else {
			kept := rows[:0:0]
			for _, r := range rows {
				if n, _ := crewview.EventOffset(r.ID); n >= start {
					kept = append(kept, r)
				}
			}
			rows = kept
		}
	}
	if limit > 0 && len(rows) > limit && since == "" {
		rows = rows[len(rows)-limit:]
	}
	page := map[string]any{
		"schema_version": 1, "view": "events", "host": "box", "cursor": fmt.Sprintf("1:%d", end),
		"reset": reset, "more": false, "errors": []string{}, "events": nonNil(rows),
	}
	return json.Marshal(page)
}

func nonNil(r []fileEvent) []fileEvent {
	if r == nil {
		return []fileEvent{}
	}
	return r
}
