package tmux

import (
	"strconv"
	"strings"
)

// ControlEventType represents a parsed control mode line type.
type ControlEventType int

const (
	EventOutput          ControlEventType = iota
	EventBegin
	EventEnd
	EventError
	EventWindowAdd
	EventWindowClose
	EventWindowRenamed
	EventSessionChanged
	EventSessionsChanged
	EventPaneModeChanged
	EventData // non-notification line (command response body, etc.)
)

// ControlEvent is a parsed control mode line.
type ControlEvent struct {
	Type      ControlEventType
	PaneID    string // e.g. "%42"
	WindowID  string // e.g. "@1"
	CmdNumber int    // from %begin/%end/%error
	Data      string // unescaped output, window name, etc.
}

// ParseControlLine parses a single line from tmux control mode stdout.
func ParseControlLine(line string) ControlEvent {
	switch {
	case strings.HasPrefix(line, "%output "):
		return parseOutput(line)
	case strings.HasPrefix(line, "%begin "):
		return parseBlock(line, EventBegin)
	case strings.HasPrefix(line, "%end "):
		return parseBlock(line, EventEnd)
	case strings.HasPrefix(line, "%error "):
		return parseBlock(line, EventError)
	case strings.HasPrefix(line, "%window-renamed "):
		return parseWindowRenamed(line)
	case strings.HasPrefix(line, "%window-add "):
		return parseWindowEvent(line, EventWindowAdd)
	case strings.HasPrefix(line, "%window-close "):
		return parseWindowEvent(line, EventWindowClose)
	case strings.HasPrefix(line, "%sessions-changed"):
		return ControlEvent{Type: EventSessionsChanged}
	case strings.HasPrefix(line, "%session-changed "):
		return parseSessionChanged(line)
	case strings.HasPrefix(line, "%pane-mode-changed "):
		return parsePaneModeChanged(line)
	default:
		return ControlEvent{Type: EventData, Data: line}
	}
}

func parseOutput(line string) ControlEvent {
	// "%output %42 hello\015\012"
	rest := line[len("%output "):]
	spaceIdx := strings.IndexByte(rest, ' ')
	if spaceIdx < 0 {
		return ControlEvent{Type: EventOutput, PaneID: rest}
	}
	paneID := rest[:spaceIdx]
	data := UnescapeOctal(rest[spaceIdx+1:])
	return ControlEvent{Type: EventOutput, PaneID: paneID, Data: data}
}

func parseBlock(line string, eventType ControlEventType) ControlEvent {
	// "%begin 1578920019 258 0"
	fields := strings.Fields(line)
	var cmdNum int
	if len(fields) >= 3 {
		cmdNum, _ = strconv.Atoi(fields[2])
	}
	return ControlEvent{Type: eventType, CmdNumber: cmdNum}
}

func parseWindowRenamed(line string) ControlEvent {
	// "%window-renamed @1 vim"
	rest := line[len("%window-renamed "):]
	spaceIdx := strings.IndexByte(rest, ' ')
	if spaceIdx < 0 {
		return ControlEvent{Type: EventWindowRenamed, WindowID: rest}
	}
	return ControlEvent{Type: EventWindowRenamed, WindowID: rest[:spaceIdx], Data: rest[spaceIdx+1:]}
}

func parseWindowEvent(line string, eventType ControlEventType) ControlEvent {
	// "%window-add @5" or "%window-close @5"
	fields := strings.Fields(line)
	var windowID string
	if len(fields) >= 2 {
		windowID = fields[1]
	}
	return ControlEvent{Type: eventType, WindowID: windowID}
}

func parseSessionChanged(line string) ControlEvent {
	// "%session-changed $1 mysession"
	fields := strings.Fields(line)
	var name string
	if len(fields) >= 3 {
		name = strings.Join(fields[2:], " ")
	}
	return ControlEvent{Type: EventSessionChanged, Data: name}
}

func parsePaneModeChanged(line string) ControlEvent {
	// "%pane-mode-changed %3"
	fields := strings.Fields(line)
	var paneID string
	if len(fields) >= 2 {
		paneID = fields[1]
	}
	return ControlEvent{Type: EventPaneModeChanged, PaneID: paneID}
}
