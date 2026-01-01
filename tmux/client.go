// tmux/client.go
package tmux

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Session struct {
	Name         string
	Created      time.Time
	Windows      int
	Attached     bool
	LastActivity time.Time
}

type Client struct {
	tmuxPath string
}

func NewClient() *Client {
	return &Client{tmuxPath: "tmux"}
}

func parseSessionLine(line string) (Session, error) {
	parts := strings.Split(line, "|")
	if len(parts) != 5 {
		return Session{}, fmt.Errorf("invalid session line: %s", line)
	}

	created, _ := strconv.ParseInt(parts[1], 10, 64)
	windows, _ := strconv.Atoi(parts[2])
	attached := parts[3] == "1"
	activity, _ := strconv.ParseInt(parts[4], 10, 64)

	return Session{
		Name:         parts[0],
		Created:      time.Unix(created, 0),
		Windows:      windows,
		Attached:     attached,
		LastActivity: time.Unix(activity, 0),
	}, nil
}

func (c *Client) ListSessions() ([]Session, error) {
	cmd := exec.Command(c.tmuxPath, "list-sessions", "-F",
		"#{session_name}|#{session_created}|#{session_windows}|#{session_attached}|#{session_activity}")

	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(err.Error(), "no server running") {
			return nil, nil
		}
		return nil, err
	}

	var sessions []Session
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		s, err := parseSessionLine(line)
		if err != nil {
			continue
		}
		sessions = append(sessions, s)
	}

	return sessions, nil
}
