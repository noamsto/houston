//go:build linux

package runs

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// writeStat writes a fake /proc/<pid>/stat file under root using the same
// real-shaped comm as procstart_test.go's realShapedStat.
func writeStat(t *testing.T, root string, pid int, tpgid, startTicks int64) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stat"), []byte(realShapedStat(tpgid, startTicks)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestForegroundStartIn(t *testing.T) {
	const shellPID, enginePID = 100, 200

	t.Run("resolves through shell to engine", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "stat"), []byte("btime 1000\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeStat(t, root, shellPID, enginePID, 0)
		writeStat(t, root, enginePID, 0, 250)

		got := foregroundStartIn(root, shellPID)
		if want := int64(1002); got != want {
			t.Errorf("foregroundStartIn = %d, want %d", got, want)
		}
	})

	t.Run("negative tpgid", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "stat"), []byte("btime 1000\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeStat(t, root, shellPID, -1, 0)

		if got := foregroundStartIn(root, shellPID); got != 0 {
			t.Errorf("foregroundStartIn = %d, want 0", got)
		}
	})

	t.Run("missing engine dir", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "stat"), []byte("btime 1000\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		writeStat(t, root, shellPID, enginePID, 0) // enginePID dir never created

		if got := foregroundStartIn(root, shellPID); got != 0 {
			t.Errorf("foregroundStartIn = %d, want 0", got)
		}
	})

	t.Run("panePID zero", func(t *testing.T) {
		root := t.TempDir()
		if got := foregroundStartIn(root, 0); got != 0 {
			t.Errorf("foregroundStartIn = %d, want 0", got)
		}
	})

	t.Run("missing root stat (no btime)", func(t *testing.T) {
		root := t.TempDir()
		// No root/stat file written.
		writeStat(t, root, shellPID, enginePID, 0)
		writeStat(t, root, enginePID, 0, 250)

		if got := foregroundStartIn(root, shellPID); got != 0 {
			t.Errorf("foregroundStartIn = %d, want 0", got)
		}
	})
}

// TestProcStartInRealKernel sanity-checks procStartIn against the real
// kernel's own /proc for this test process.
func TestProcStartInRealKernel(t *testing.T) {
	now := time.Now().Unix()
	got := procStartIn("/proc", os.Getpid())
	if got < now-3600 || got > now+1 {
		t.Errorf("procStartIn(/proc, self) = %d, want within [%d, %d]", got, now-3600, now+1)
	}
}

// TestForegroundStartInRealTTY exercises the real /proc against this test
// process's actual controlling tty, when it has one (CI/sandboxes often
// don't).
func TestForegroundStartInRealTTY(t *testing.T) {
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	tpgid, _, ok := parseProcStat(string(stat))
	if !ok {
		t.Fatal("could not parse /proc/self/stat")
	}
	if tpgid <= 0 {
		t.Skip("no controlling tty")
	}

	now := time.Now().Unix()
	got := foregroundStartIn("/proc", os.Getpid())
	if got <= 0 {
		t.Fatalf("foregroundStartIn(/proc, self) = %d, want > 0", got)
	}
	if got > now+1 {
		t.Errorf("foregroundStartIn(/proc, self) = %d, want <= %d", got, now+1)
	}
}
