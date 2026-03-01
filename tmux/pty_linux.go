package tmux

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// openPTY opens a pseudo-terminal pair (master, slave).
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}

	defer func() {
		if err != nil {
			master.Close()
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

	return master, slave, nil
}
