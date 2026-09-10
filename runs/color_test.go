package runs

import "testing"

func TestTmuxColorToHex(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"cube low corner", "colour16", "#000000"},
		{"cube high corner", "colour231", "#ffffff"},
		{"cube mid", "colour168", "#d75f87"},
		{"greyscale low", "colour232", "#080808"},
		{"greyscale high", "colour255", "#eeeeee"},
		{"hex passthrough", "#abcdef", "#abcdef"},
		{"hex too short", "#abcde", ""},
		{"hex too long", "#abcdef1", ""},
		{"hex non-hex characters", "#abcdez", ""},
		{"theme-dependent low index", "colour1", ""},
		{"theme-dependent high boundary", "colour15", ""},
		{"colour name", "red", ""},
		{"default", "default", ""},
		{"empty", "", ""},
		{"unparseable", "colourfoo", ""},
		{"out of range", "colour256", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := tmuxColorToHex(c.in); got != c.want {
				t.Errorf("tmuxColorToHex(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
