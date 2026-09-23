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

	ctx2, cancel2 := context.WithTimeout(context.Background(), procProbeTimeout)
	defer cancel2()
	cmd := exec.CommandContext(ctx2, psPath, "-o", "lstart=", "-p", strconv.Itoa(tpgid))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err = cmd.Output()
	if err != nil {
		if _, isExitErr := err.(*exec.ExitError); !isExitErr || ctx2.Err() != nil {
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
