//go:build !linux

package runs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const procProbeTimeout = 2 * time.Second

// psPath is absolute on purpose: houston's service PATH carries no ps.
const psPath = "/bin/ps"

// foregroundStart shells out to ps twice: once for the tty's foreground
// process group id, once for that leader's start time.
func foregroundStart(panePID int) int64 {
	if panePID <= 0 {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), procProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, psPath, "-o", "tpgid=", "-p", strconv.Itoa(panePID)).Output()
	if err != nil {
		// ps exiting non-zero with no output means the process is gone, a
		// legitimate outcome; anything else (missing binary, a killed
		// context surfacing as an *exec.ExitError) means the probe itself
		// is broken.
		if _, isExitErr := err.(*exec.ExitError); !isExitErr || ctx.Err() != nil {
			warnProbeBroken(err)
		}
		return 0
	}
	trimmed := strings.TrimSpace(string(out))
	tpgid, err := strconv.Atoi(trimmed)
	if err != nil {
		if trimmed != "" {
			warnProbeBroken(err)
		}
		return 0
	}
	if tpgid <= 0 {
		return 0
	}
	return psStart(tpgid)
}

func (hostProcs) Start(pid int) int64 { return psStart(pid) }

func (hostProcs) Descends(pid, root int) (bool, error) {
	return descendsVia(psParent, pid, root)
}

// psStart returns pid's unix start time from ps, or 0 when unknown.
func psStart(pid int) int64 {
	ctx, cancel := context.WithTimeout(context.Background(), procProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, psPath, "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		if _, isExitErr := err.(*exec.ExitError); !isExitErr || ctx.Err() != nil {
			warnProbeBroken(err)
		}
		return 0
	}
	start, ok := parseLstart(string(out))
	if !ok {
		if strings.TrimSpace(string(out)) != "" {
			warnProbeBroken(errors.New("unparseable ps lstart output"))
		}
		return 0
	}
	return start
}

// psParent returns pid's ppid from ps.
func psParent(pid int) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), procProbeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, psPath, "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		if _, isExitErr := err.(*exec.ExitError); !isExitErr || ctx.Err() != nil {
			warnProbeBroken(err)
		}
		return 0, err
	}
	trimmed := strings.TrimSpace(string(out))
	ppid, err := strconv.Atoi(trimmed)
	if err != nil {
		if trimmed != "" {
			warnProbeBroken(err)
		}
		return 0, err
	}
	return ppid, nil
}
