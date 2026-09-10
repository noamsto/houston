package runs

import (
	"fmt"
	"strconv"
	"strings"
)

// cubeLevels are the six intensity steps tmux's 256-colour cube (indices
// 16-231) uses per channel.
var cubeLevels = [6]int{0, 95, 135, 175, 215, 255}

// tmuxColorToHex converts a tmux colour value (as read from a user option
// like @crew_color) to a CSS hex colour, or "" for "no opinion".
//
// Indices 16-231 are the 6x6x6 colour cube, 232-255 are the greyscale ramp,
// and a "#rrggbb" value (exactly six hex digits) passes through unchanged;
// anything else starting with "#" maps to "" rather than a malformed value.
// Indices 0-15 map to "" rather than a colour, because they're
// terminal-theme-dependent — there's no fixed RGB for "colour1", it's
// whatever the user's palette says red is — and the crew palette this feeds
// never emits one anyway (its lowest entry is colour25).
func tmuxColorToHex(v string) string {
	if strings.HasPrefix(v, "#") {
		if isHex6(v[1:]) {
			return v
		}
		return ""
	}
	n, ok := strings.CutPrefix(v, "colour")
	if !ok {
		return ""
	}
	i, err := strconv.Atoi(n)
	if err != nil || i < 16 || i > 255 {
		return ""
	}
	if i >= 232 {
		g := 8 + 10*(i-232)
		return fmt.Sprintf("#%02x%02x%02x", g, g, g)
	}
	i -= 16
	r := cubeLevels[i/36]
	g := cubeLevels[(i/6)%6]
	b := cubeLevels[i%6]
	return fmt.Sprintf("#%02x%02x%02x", r, g, b)
}

// isHex6 reports whether s is exactly six hexadecimal digits.
func isHex6(s string) bool {
	if len(s) != 6 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}
