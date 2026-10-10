package runs

import (
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/noamsto/houston/tmux"
)

// procProbe is the dispatcher join's view of the host process table — an
// interface so tests stay hermetic.
type procProbe interface {
	// Alive reports whether pid exists (signal 0; EPERM counts as alive).
	Alive(pid int) bool
	// Start is pid's unix start time; 0 means unknown.
	Start(pid int) int64
	// Descends reports whether pid is root or one of its descendants.
	Descends(pid, root int) (bool, error)
}

// pidRecycleSlack mirrors the crew CLI's _pid_recycled: a process that started
// more than this after its pid file was written is not the process that wrote
// it.
const pidRecycleSlack = 2 * time.Second

// joinDispatchers maps each crew of bus to the pane its dispatcher runs in,
// crew id → pane id. A crew joins pane P when <bus>/crews/<crew>/pane names P,
// P is listed with an engine status in a window stamped @crew_name dispatcher
// and is not a role-grid pane, the pane file is no older than the tmux server
// (an older file names a pane id an earlier server minted), and the crew's pid
// file, when there is one, names a live process under P that is not a
// recycled pid. One crew per pane: the newest pane file wins, then the
// greater crew id, so a dispatcher restarted in the same pane takes over from
// the crew it left behind.
func joinDispatchers(bus string, wins []tmux.WindowOptions, panes []tmux.PaneOptions, probe procProbe) map[string]string {
	crewsDir := filepath.Join(bus, "crews")
	entries, err := os.ReadDir(crewsDir)
	if err != nil {
		return nil
	}
	var serverStart int64
	if len(panes) > 0 {
		serverStart = panes[0].ServerStart
	}
	byID := make(map[string]tmux.PaneOptions, len(panes))
	for _, p := range panes {
		byID[p.PaneID] = p
	}
	byTarget := windowsByTarget(wins)

	type claim struct {
		crew string
		mod  time.Time
	}
	best := map[string]claim{}
	// ReadDir names are single path elements, so a crew id can never climb
	// out of crews/.
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		crewID := e.Name()
		dir := filepath.Join(crewsDir, crewID)
		raw, mod, err := readWithMtime(filepath.Join(dir, "pane"))
		if err != nil {
			continue
		}
		paneID := strings.TrimSpace(string(raw))
		p, listed := byID[paneID]
		if !listed || (p.ClaudeStatus == "" && p.AgentScreen == "") || p.IsRolePane() ||
			byTarget[p.Target].CrewName != dispatcherCrewName {
			continue
		}
		if serverStart != 0 && mod.Unix() < serverStart {
			slog.Debug("crew source: dispatcher pane file predates the tmux server", "bus", bus, "crew", crewID, "pane", paneID)
			continue
		}
		if !pidOwnsPane(filepath.Join(dir, "pid"), p.PanePID, probe) {
			slog.Debug("crew source: dispatcher pid does not own its pane", "bus", bus, "crew", crewID, "pane", paneID)
			continue
		}
		if c, held := best[paneID]; held && (c.mod.After(mod) || c.mod.Equal(mod) && c.crew > crewID) {
			continue
		}
		best[paneID] = claim{crew: crewID, mod: mod}
	}

	out := make(map[string]string, len(best))
	for paneID, c := range best {
		out[c.crew] = paneID
	}
	return out
}

// pidOwnsPane is the join's process check, after the crew CLI's
// _recorded_pid_live. A missing or unparseable pid file has no opinion; an
// unknown start time, an unknown pane pid or a failed parent walk fails
// closed.
func pidOwnsPane(path string, panePID int, probe procProbe) bool {
	raw, mod, err := readWithMtime(path)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || pid <= 0 {
		return true
	}
	if !probe.Alive(pid) {
		return false
	}
	start := probe.Start(pid)
	if start == 0 || time.Unix(start, 0).After(mod.Add(pidRecycleSlack)) {
		return false
	}
	if panePID <= 0 {
		return false
	}
	ok, err := probe.Descends(pid, panePID)
	return err == nil && ok
}

// readWithMtime reads a small file and its mtime from one open, so the two
// always describe the same file.
func readWithMtime(path string) ([]byte, time.Time, error) {
	f, err := os.Open(path) //nolint:gosec // path is a crew dir under houston's own bus scan, never a request value
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, time.Time{}, err
	}
	return raw, fi.ModTime(), nil
}
