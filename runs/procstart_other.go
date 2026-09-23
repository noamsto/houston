//go:build !linux

package runs

import (
	"context"
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
		return 0
	}
	tpgid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || tpgid <= 0 {
		return 0
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), procProbeTimeout)
	defer cancel2()
	cmd := exec.CommandContext(ctx2, psPath, "-o", "lstart=", "-p", strconv.Itoa(tpgid))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err = cmd.Output()
	if err != nil {
		return 0
	}
	start, ok := parseLstart(string(out))
	if !ok {
		return 0
	}
	return start
}
