// Package terminal provides control over the host terminal emulator.
package terminal

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FontController controls terminal font size.
type FontController interface {
	Increase() error
	Decrease() error
	Reset() error
	Name() string // Returns terminal name for display
}

// NewFontController auto-detects the terminal and returns appropriate controller.
func NewFontController() FontController {
	// Check for custom command first
	if cmd := os.Getenv("HOUSTON_FONT_CMD"); cmd != "" {
		return &CustomController{cmd: cmd}
	}

	// Try kitty
	if socket := findKittySocket(); socket != "" {
		return &KittyController{socket: socket}
	}

	// Try alacritty (v0.13+)
	if hasAlacrittyMsg() {
		return &AlacrittyController{}
	}

	// Try wezterm
	if hasWeztermCLI() {
		return &WeztermController{}
	}

	return &NoopController{}
}

// KittyController controls kitty terminal font size.
type KittyController struct {
	socket string
}

func (k *KittyController) Name() string { return "kitty" }

func (k *KittyController) Increase() error {
	return exec.Command("kitty", "@", "--to", "unix:"+k.socket, "set-font-size", "--", "+1").Run()
}

func (k *KittyController) Decrease() error {
	return exec.Command("kitty", "@", "--to", "unix:"+k.socket, "set-font-size", "--", "-1").Run()
}

func (k *KittyController) Reset() error {
	return exec.Command("kitty", "@", "--to", "unix:"+k.socket, "set-font-size", "--", "0").Run()
}

func findKittySocket() string {
	// Check /tmp/kitty-*
	matches, _ := filepath.Glob("/tmp/kitty-*")
	for _, m := range matches {
		info, err := os.Stat(m)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return m
		}
	}
	// Check KITTY_LISTEN_ON env
	if socket := os.Getenv("KITTY_LISTEN_ON"); socket != "" {
		return strings.TrimPrefix(socket, "unix:")
	}
	return ""
}

// AlacrittyController controls alacritty font size (v0.13+).
type AlacrittyController struct{}

func (a *AlacrittyController) Name() string { return "alacritty" }

func (a *AlacrittyController) Increase() error {
	return exec.Command("alacritty", "msg", "config", "font.size=+1").Run()
}

func (a *AlacrittyController) Decrease() error {
	return exec.Command("alacritty", "msg", "config", "font.size=-1").Run()
}

func (a *AlacrittyController) Reset() error {
	// Alacritty doesn't have a reset, would need to know original size
	return nil
}

func hasAlacrittyMsg() bool {
	// Check if alacritty msg works (requires IPC socket)
	err := exec.Command("alacritty", "msg", "config", "--help").Run()
	return err == nil
}

// WeztermController controls wezterm font size.
type WeztermController struct{}

func (w *WeztermController) Name() string { return "wezterm" }

func (w *WeztermController) Increase() error {
	return exec.Command("wezterm", "cli", "adjust-pane-size", "--amount", "1").Run()
}

func (w *WeztermController) Decrease() error {
	return exec.Command("wezterm", "cli", "adjust-pane-size", "--amount", "-1").Run()
}

func (w *WeztermController) Reset() error {
	return nil
}

func hasWeztermCLI() bool {
	_, err := exec.LookPath("wezterm")
	if err != nil {
		return false
	}
	// Check if we can connect
	err = exec.Command("wezterm", "cli", "list").Run()
	return err == nil
}

// CustomController uses a user-provided command.
// The command is called with "+1", "-1", or "0" as argument.
type CustomController struct {
	cmd string
}

func (c *CustomController) Name() string { return "custom" }

func (c *CustomController) Increase() error {
	return exec.Command("sh", "-c", c.cmd+" +1").Run()
}

func (c *CustomController) Decrease() error {
	return exec.Command("sh", "-c", c.cmd+" -1").Run()
}

func (c *CustomController) Reset() error {
	return exec.Command("sh", "-c", c.cmd+" 0").Run()
}

// NoopController does nothing (terminal not detected).
type NoopController struct{}

func (n *NoopController) Name() string    { return "" }
func (n *NoopController) Increase() error { return nil }
func (n *NoopController) Decrease() error { return nil }
func (n *NoopController) Reset() error    { return nil }
