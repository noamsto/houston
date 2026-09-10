package tmux

import (
	"strconv"
	"strings"
)

// ControlEventType represents a parsed control mode line type.
type ControlEventType int

const (
	EventOutput ControlEventType = iota
	EventBegin
	EventEnd
	EventError
	EventWindowAdd
	EventWindowClose
	EventWindowRenamed
	EventSessionChanged
	EventSessionsChanged
	EventPaneModeChanged
	EventPause          // %pause %N — pane output paused (flow control)
	EventContinue       // %continue %N — pane output resumed
	EventExtendedOutput // %extended-output %N delay : data (flow control variant of %output)
	EventData           // non-notification line (command response body, etc.)
)

// ControlEvent is a parsed control mode line.
type ControlEvent struct {
	Type      ControlEventType
	PaneID    string // e.g. "%42"
	WindowID  string // e.g. "@1"
	CmdNumber int    // from %begin/%end/%error
	Data      string // unescaped output, window name, etc.
}

// controlModeIntroducer is the DCS sequence tmux emits on entering control
// mode. It is glued to the front of the stream's very first line, which is the
// %begin of the block answering the attach command itself.
const controlModeIntroducer = "\x1bP1000p"

// ParseControlLine parses a single line from tmux control mode stdout.
func ParseControlLine(line string) ControlEvent {
	line = strings.TrimPrefix(line, controlModeIntroducer)
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
	case strings.HasPrefix(line, "%pause "):
		return parsePaneNotification(line, EventPause)
	case strings.HasPrefix(line, "%continue "):
		return parsePaneNotification(line, EventContinue)
	case strings.HasPrefix(line, "%extended-output "):
		return parseExtendedOutput(line)
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

func parsePaneNotification(line string, eventType ControlEventType) ControlEvent {
	// "%pause %0" or "%continue %0"
	fields := strings.Fields(line)
	var paneID string
	if len(fields) >= 2 {
		paneID = fields[1]
	}
	// This pane ID ends up in a tmux command string (ControlClient.expireGap's
	// refresh-client -A), where ';' separates commands — quoting alone can't
	// neutralize a field that itself contains a quote. A field that isn't
	// shaped like a pane ID isn't a pause/continue notification at all.
	if !isPaneID(paneID) {
		return ControlEvent{Type: EventData, Data: line}
	}
	return ControlEvent{Type: eventType, PaneID: paneID}
}

// isPaneID reports whether s has tmux's pane-ID shape: '%' followed by one or
// more digits.
func isPaneID(s string) bool {
	if len(s) < 2 || s[0] != '%' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseExtendedOutput(line string) ControlEvent {
	// "%extended-output %0 1234 : abcdef"
	rest := line[len("%extended-output "):]
	spaceIdx := strings.IndexByte(rest, ' ')
	if spaceIdx < 0 {
		return ControlEvent{Type: EventExtendedOutput, PaneID: rest}
	}
	paneID := rest[:spaceIdx]
	// Skip delay field, find " : " separator
	colonIdx := strings.Index(rest[spaceIdx:], " : ")
	if colonIdx < 0 {
		return ControlEvent{Type: EventExtendedOutput, PaneID: paneID}
	}
	data := UnescapeOctal(rest[spaceIdx+colonIdx+3:])
	return ControlEvent{Type: EventExtendedOutput, PaneID: paneID, Data: data}
}
