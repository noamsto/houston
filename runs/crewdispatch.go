package runs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
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

// maxCrewFile caps a crew's pane or pid file, each one short id; a larger
// file is not one the crew CLI wrote.
const maxCrewFile = 64

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
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Debug("crew source: read crews directory", "bus", bus, "error", err)
		}
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
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Debug("crew source: read dispatcher pane file", "bus", bus, "crew", crewID, "error", err)
			}
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
// unreadable one (not a small regular file), an unknown start time, an
// unknown pane pid or a failed parent walk fails closed.
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

// readWithMtime reads a crew file of at most maxCrewFile bytes and its mtime
// from one open, so the two always describe the same file. Anything but a
// regular file (a symlink is followed) is an error; O_NONBLOCK keeps a FIFO
// from blocking the open.
func readWithMtime(path string) ([]byte, time.Time, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0) //nolint:gosec // path is a crew dir under houston's own bus scan, never a request value
	if err != nil {
		return nil, time.Time{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, time.Time{}, err
	}
	if !fi.Mode().IsRegular() {
		return nil, time.Time{}, fmt.Errorf("%s: not a regular file (%s)", path, fi.Mode().Type())
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxCrewFile+1))
	if err != nil {
		return nil, time.Time{}, err
	}
	if len(raw) > maxCrewFile {
		return nil, time.Time{}, fmt.Errorf("%s: larger than %d bytes", path, maxCrewFile)
	}
	return raw, fi.ModTime(), nil
}
