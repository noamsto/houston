package tmux

// UnescapeOctal decodes tmux control mode octal escaping.
// Characters with ASCII value <32 and backslash are encoded as \NNN (3-digit octal).
func UnescapeOctal(s string) string {
	hasBackslash := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' {
			hasBackslash = true
			break
		}
	}
	if !hasBackslash {
		return s
	}

	buf := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			d1, d2, d3 := s[i+1], s[i+2], s[i+3]
			if d1 >= '0' && d1 <= '3' && d2 >= '0' && d2 <= '7' && d3 >= '0' && d3 <= '7' {
				val := (d1-'0')*64 + (d2-'0')*8 + (d3 - '0')
				buf = append(buf, val)
				i += 3
				continue
			}
		}
		buf = append(buf, s[i])
	}
	return string(buf)
}
