package chat

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
)

// readLines is the line loop every JSONL Reader shares: it detects a replaced
// file (shorter, or the bytes before from.Offset changed), consumes only
// complete lines, and hands each non-blank one with its byte offset to decode.
func readLines(path string, from Cursor, decode func(line []byte, off int64) []Update) (updates []Update, next Cursor, reset bool, err error) {
	f, err := os.Open(path) //nolint:gosec // transcript path is agent-supplied via hook state (not a request); reading it is the feature
	if err != nil {
		return nil, from, false, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, from, false, err
	}

	offset := from.Offset
	if info.Size() < offset || (len(from.Pending) > 0 && !bytes.Equal(from.Pending, tailPrint(f, offset))) {
		reset = true
		offset = 0
	}

	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, from, false, err
	}

	r := bufio.NewReaderSize(f, 64*1024)
	pos := offset
	for {
		line, rerr := r.ReadBytes('\n')
		if rerr != nil {
			// A partial (unterminated) line, or clean EOF with nothing left.
			// Left for the next call — see the chunking-independence
			// contract in doc.go.
			break
		}
		lineOffset := pos
		pos += int64(len(line))

		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			updates = append(updates, decode(trimmed, lineOffset)...)
		}
	}

	return updates, Cursor{Offset: pos, Pending: tailPrint(f, pos)}, reset, nil
}

// tailPrintLen is how many bytes before a cursor's Offset its fingerprint
// covers.
const tailPrintLen = 64

// tailPrint fingerprints the bytes just before offset, so a Read can tell
// that the file under a cursor was replaced even when it isn't shorter —
// truncated and regrown past Offset, or a new file at the same path. It is
// nil at offset 0, where there is nothing to lose.
func tailPrint(f *os.File, offset int64) json.RawMessage {
	if offset <= 0 {
		return nil
	}
	n := min(offset, tailPrintLen)
	buf := make([]byte, n)
	if _, err := f.ReadAt(buf, offset-n); err != nil {
		return nil
	}
	sum := sha256.Sum256(buf)
	b, _ := json.Marshal(hex.EncodeToString(sum[:]))
	return b
}
