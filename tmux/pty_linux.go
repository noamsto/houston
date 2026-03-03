package tmux

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair (master, slave).
// Echo is disabled so command input doesn't interleave with tmux output.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	defer func() {
		if err != nil {
			_ = master.Close()
		}
	}()

	// Get pts number
	var n uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); errno != 0 {
		return nil, nil, fmt.Errorf("TIOCGPTN: %w", errno)
	}

	// Unlock pts
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		return nil, nil, fmt.Errorf("TIOCSPTLCK: %w", errno)
	}

	slavePath := "/dev/pts/" + strconv.FormatUint(uint64(n), 10)
	slave, err = os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", slavePath, err)
	}

	// Put PTY in near-raw mode so bytes pass through unmodified:
	// - ECHO off:  prevent command echo interleaving with control mode output
	// - ICRNL off: prevent CR→NL conversion on input (breaks send-keys with \r)
	// - OPOST off: prevent NL→CR+NL conversion on output (simplifies read loop)
	var attr syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TCGETS, uintptr(unsafe.Pointer(&attr))); errno != 0 {
		_ = slave.Close()
		return nil, nil, fmt.Errorf("TCGETS: %w", errno)
	}
	attr.Lflag &^= syscall.ECHO | syscall.ECHOE | syscall.ECHOK | syscall.ECHONL
	attr.Iflag &^= syscall.ICRNL
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TCSETS, uintptr(unsafe.Pointer(&attr))); errno != 0 {
		_ = slave.Close()
		return nil, nil, fmt.Errorf("TCSETS: %w", errno)
	}

	return master, slave, nil
}
