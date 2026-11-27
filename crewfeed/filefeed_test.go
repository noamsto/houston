package crewfeed

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

// This file is the test double for the crew CLI's events view: it serves a
// fixture <bus>/events.jsonl the way `crew dash --json --view events` serves a
// log (rows with their start offsets, a torn last line withheld, a reset when
// the log is replaced or shortened), so the store tests keep writing plain
// fixtures. Production code never reads a bus file.

// maxLine is the longest bus line the double serves, like the CLI's own cap.
const maxLine = 1 << 20

type feedPos struct {
	head string
	off  int64
}

var (
	feedPosMu sync.Mutex
	feedPoses = map[*Store]map[string]*feedPos{}
)

func feedPosOf(s *Store, bus string) *feedPos {
	feedPosMu.Lock()
	defer feedPosMu.Unlock()
	m := feedPoses[s]
	if m == nil {
		m = map[string]*feedPos{}
		feedPoses[s] = m
	}
	p := m[bus]
	if p == nil {
		p = &feedPos{}
		m[bus] = p
	}
	return p
}

// Advance applies the rows appended to <bus>/events.jsonl since the last call.
func (s *Store) Advance(bus string) {
	s.SetRescan(rescanFile())
	p := feedPosOf(s, bus)
	b, err := readBatch(filepath.Join(bus, "events.jsonl"), p)
	if err != nil {
		return
	}
	s.Apply(bus, b)
}

// readBatch reads the rows past p.off, or all of them after a reset.
func readBatch(path string, p *feedPos) (Batch, error) {
	f, size, head, err := openBus(path)
	if errors.Is(err, os.ErrNotExist) {
		return Batch{}, nil
	}
	if err != nil {
		return Batch{}, err
	}
	defer func() { _ = f.Close() }()
	var b Batch
	if head != p.head || size < p.off {
		b.Reset = p.head != "" || p.off > 0
		p.head, p.off = head, 0
	}
	if size > p.off {
		p.off, err = scanLines(io.NewSectionReader(f, p.off, size-p.off), p.off, 1<<62, func(line []byte, off int64) {
			b.Rows = append(b.Rows, Row{Offset: off, Line: bytes.Clone(line)})
		})
	}
	return b, err
}

// rescanFile is the RescanFunc over the fixture log.
func rescanFile() RescanFunc {
	return func(_ context.Context, bus string) (Batch, error) {
		return readBatch(filepath.Join(bus, "events.jsonl"), &feedPos{})
	}
}

func openBus(path string) (*os.File, int64, string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, "", err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, "", err
	}
	head, err := headHash(io.NewSectionReader(f, 0, fi.Size()))
	if err != nil {
		_ = f.Close()
		return nil, 0, "", err
	}
	return f, fi.Size(), head, nil
}

func scanLines(r io.Reader, start, end int64, fn func(line []byte, off int64)) (int64, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	off := start
	var line bytes.Buffer
	n := 0
	for {
		if n == 0 && off >= end {
			return off, nil
		}
		chunk, err := br.ReadSlice('\n')
		n += len(chunk)
		if n <= maxLine+1 {
			line.Write(chunk)
		}
		switch {
		case err == nil:
			if n <= maxLine+1 {
				fn(line.Bytes()[:line.Len()-1], off)
			}
			off += int64(n)
			line.Reset()
			n = 0
		case errors.Is(err, bufio.ErrBufferFull):
		case errors.Is(err, io.EOF):
			return off, nil
		default:
			return off, err
		}
	}
}

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
