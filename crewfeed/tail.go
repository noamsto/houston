package crewfeed

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
)

// maxLine is the longest bus line the feed folds. A longer one is consumed
// without an entry, by the same rule in the live tail and in a rescan, so the
// ids around it never depend on how the file was read.
const maxLine = 1 << 20

const readBuf = 64 << 10

// scanLines hands fn every complete line of r (positioned at file offset
// start) that starts before end, without its '\n', together with that offset.
// It returns the offset just past the last line it consumed: a torn trailing
// line is left for a later read. The offset is exact on a read error too, so a
// caller that folded the lines it saw can resume from it. fn must not keep
// line.
func scanLines(r io.Reader, start, end int64, fn func(line []byte, off int64)) (int64, error) {
	br := bufio.NewReaderSize(r, readBuf)
	off := start
	var line []byte
	n := 0
	for {
		if n == 0 && off >= end {
			return off, nil
		}
		chunk, err := br.ReadSlice('\n')
		n += len(chunk)
		if n <= maxLine+1 {
			line = append(line, chunk...)
		}
		switch {
		case err == nil:
			if n <= maxLine+1 {
				fn(line[:len(line)-1], off)
			}
			off += int64(n)
			line, n = line[:0], 0
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return off, nil
		default:
			return off, err
		}
	}
}

// headHash hashes the first complete line of r, '\n' included, or returns ""
// while it has none. It streams, so a huge first line costs no memory.
func headHash(r io.Reader) (string, error) {
	h := sha256.New()
	br := bufio.NewReaderSize(r, 4<<10)
	for {
		chunk, err := br.ReadSlice('\n')
		h.Write(chunk)
		switch {
		case err == nil:
			return hex.EncodeToString(h.Sum(nil)), nil
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return "", nil
		default:
			return "", err
		}
	}
}
