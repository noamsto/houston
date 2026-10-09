package hub

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
)

// initialTail is the first tail size readInitial tries.
const initialTail = 256 << 10

// bgMarkers are substrings every line that starts or stops a background task
// must contain. A line naming none of them, nor a pending tool_use id, changes
// no bgTracker state.
var bgMarkers = [][]byte{
	[]byte("run_in_background"),
	[]byte("Monitor"),
	[]byte("TaskStop"),
	[]byte("KillShell"),
	[]byte("task-notification"),
}

// readInitial is a session's first transcript read. Only the background fold
// needs history from byte 0; trail, asks and token counts are decided by the
// tail once it holds every kind of event they depend on. So it parses a tail
// that grows until it does, and folds only the background-relevant lines
// before it. The caller applies tail through applyTranscriptEvent onto bg.
func readInitial(path string) (bg bgTracker, tail []TranscriptEvent, end int64, err error) {
	f, err := os.Open(path) //nolint:gosec // transcript path is agent-supplied via hook state (not a request); reading it is the feature
	if err != nil {
		return bg, nil, 0, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return bg, nil, 0, err
	}
	size := fi.Size()

	// Past half the file, a tail plus a pre-pass costs more than one full read.
	for t := int64(initialTail); 2*t < size; t *= 2 {
		var start int64
		if start, err = lineStartFrom(f, size-t, size); err != nil {
			return bg, nil, 0, err
		}
		if tail, end, err = ReadTranscriptFrom(path, start); err != nil {
			return bg, nil, 0, err
		}
		if tailSuffices(tail) {
			bg, err = foldBackground(f, start)
			return bg, tail, end, err
		}
	}
	tail, end, err = ReadTranscriptFrom(path, 0)
	return bg, tail, end, err
}

// tailSuffices reports whether replaying only evs leaves trail, asks and
// token counts as a full replay would: the trail keeps the last maxTrail
// chips and every chip but the newest is already done, while asks and each
// token count are set by the last event that touches them.
func tailSuffices(evs []TranscriptEvent) bool {
	var toolUses int
	var asks, in, out bool
	for _, ev := range evs {
		switch ev.Type {
		case EventTypeToolUse:
			toolUses++
			asks = true
		case EventTypeToolResult, EventTypeText, eventTypePiToolCall:
			asks = true
		}
		in = in || ev.InputTokens > 0
		out = out || ev.OutputTokens > 0
	}
	return toolUses >= maxTrail && asks && in && out
}

// lineStartFrom returns the first line start at or after pos (0 < pos), or
// size when no line starts there.
func lineStartFrom(f *os.File, pos, size int64) (int64, error) {
	p := pos - 1
	r := bufio.NewReaderSize(io.NewSectionReader(f, p, size-p), 64<<10)
	for {
		chunk, err := r.ReadSlice('\n')
		p += int64(len(chunk))
		switch {
		case err == nil:
			return p, nil
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return size, nil
		default:
			return 0, err
		}
	}
}

// foldBackground folds the complete lines in [0, end) into a bgTracker,
// decoding only the lines that can change it.
func foldBackground(f *os.File, end int64) (bgTracker, error) {
	var (
		bg      bgTracker
		pending [][]byte
		long    []byte
		pos     int64
	)
	r := bufio.NewReaderSize(io.NewSectionReader(f, 0, end), 64<<10)
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long[:0], line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = r.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if bgRelevant(line, pending) {
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				for _, ev := range parseLine(string(trimmed), pos) {
					bg.apply(ev)
				}
				pending = pending[:0]
				for _, id := range bg.pending() {
					pending = append(pending, []byte(id))
				}
			}
		}
		pos += int64(len(line))
		if errors.Is(err, io.EOF) {
			return bg, nil
		}
		if err != nil {
			return bg, err
		}
	}
}

func bgRelevant(line []byte, pending [][]byte) bool {
	for _, m := range bgMarkers {
		if bytes.Contains(line, m) {
			return true
		}
	}
	for _, id := range pending {
		if bytes.Contains(line, id) {
			return true
		}
	}
	return false
}
