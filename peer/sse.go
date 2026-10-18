package peer

import (
	"bufio"
	"bytes"
	"errors"
)

// maxLine bounds one SSE line, and the data of one event: a snapshot is one
// line, so this is what a peer can make us buffer.
const maxLine = 32 << 20

var errTooLong = errors.New("sse line too long")

// sseEvent is one dispatched server-sent event.
type sseEvent struct {
	name string
	data []byte
}

// sseReader parses text/event-stream. bufio.Scanner is not used: its 64 KiB
// default token is smaller than a modest snapshot line.
type sseReader struct {
	r     *bufio.Reader
	touch func() // called on every chunk read, for liveness
}

// next returns the next event. id: and comment lines are ignored.
func (s *sseReader) next() (sseEvent, error) {
	var ev sseEvent
	seen := false
	hasData := false
	for {
		line, err := s.line()
		if err != nil {
			return sseEvent{}, err
		}
		if len(line) == 0 {
			if seen {
				return ev, nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			ev.name = string(value)
			seen = true
		case "data":
			if len(ev.data)+len(value)+1 > maxLine {
				return sseEvent{}, errTooLong
			}
			if hasData {
				ev.data = append(ev.data, '\n')
			}
			ev.data = append(ev.data, value...)
			hasData = true
			seen = true
		}
	}
}

// line returns one line without its terminator. The result is only valid
// until the next call.
func (s *sseReader) line() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := s.r.ReadSlice('\n')
		if len(chunk) > 0 {
			s.touch()
		}
		if len(buf)+len(chunk) > maxLine+2 {
			return nil, errTooLong
		}
		switch {
		case err == nil:
			if buf != nil {
				chunk = append(buf, chunk...)
			}
			chunk = bytes.TrimSuffix(chunk, []byte("\n"))
			return bytes.TrimSuffix(chunk, []byte("\r")), nil
		case errors.Is(err, bufio.ErrBufferFull):
			buf = append(buf, chunk...)
		default:
			return nil, err
		}
	}
}
