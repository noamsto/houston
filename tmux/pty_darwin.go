package tmux

import (
	"fmt"
	"os"
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

	// Grant and unlock the slave side, then read its device path.
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCPTYGRANT, 0); errno != 0 {
		return nil, nil, fmt.Errorf("TIOCPTYGRANT: %w", errno)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCPTYUNLK, 0); errno != 0 {
		return nil, nil, fmt.Errorf("TIOCPTYUNLK: %w", errno)
	}

	var name [128]byte
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		return nil, nil, fmt.Errorf("TIOCPTYGNAME: %w", errno)
	}
	n := 0
	for n < len(name) && name[n] != 0 {
		n++
	}
	slavePath := string(name[:n])

	slave, err = os.OpenFile(slavePath, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", slavePath, err)
	}

	// Put PTY in near-raw mode so bytes pass through unmodified:
	// - ECHO off:  prevent command echo interleaving with control mode output
	// - ICRNL off: prevent CR→NL conversion on input (breaks send-keys with \r)
	var attr syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&attr))); errno != 0 {
		_ = slave.Close()
		return nil, nil, fmt.Errorf("TIOCGETA: %w", errno)
	}
	attr.Lflag &^= syscall.ECHO | syscall.ECHOE | syscall.ECHOK | syscall.ECHONL
	attr.Iflag &^= syscall.ICRNL
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), syscall.TIOCSETA, uintptr(unsafe.Pointer(&attr))); errno != 0 {
		_ = slave.Close()
		return nil, nil, fmt.Errorf("TIOCSETA: %w", errno)
	}

	return master, slave, nil
}
