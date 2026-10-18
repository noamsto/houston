package claudemod

import (
	"cmp"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const assignOp = `(?:[-+*/%&|^]|\*\*|<<|>>>?|&&|\|\||\?\?)?=[^=>]`

var (
	// The mod chains calls across lines (`$.http\n    .fetch(`), so whitespace
	// is allowed between every part of a call.
	forbiddenAPI   = regexp.MustCompile(`\$\s*\.\s*(prompt|command|tool|agent|ui|process|fs|state|store|turn|audio|model)\s*\.|\$\s*\.\s*session\s*\.\s*(append|send)|\$\s*\.\s*env\s*\.\s*set|\bdeny\b|\bdrop\b`)
	apiCall        = regexp.MustCompile(`\$\s*\.\s*([a-zA-Z]+)(?:\s*\.\s*([a-zA-Z]+))?`)
	computedAccess = regexp.MustCompile(`\$\s*\[|=\s*\$\s*[;,)}\n]|\}\s*(?::[^=;]*)?=\s*\$(?:[^\w$.]|$)`)
	allowedCalls   = []string{"$.http.fetch", "$.env.get", "$.session.id", "$.clock.every"}

	engineParam = regexp.MustCompile(`([\w$]+)\s*:\s*EngineInterface`)
	nextCall    = regexp.MustCompile(`\bnext\s*\(([^)]*)\)`)

	optChain = regexp.MustCompile(`\?\.[\[(]?`)
	nonNull  = regexp.MustCompile(`[\w$)\]]!\.`)

	onCall   = regexp.MustCompile(`\bon\s*\(`)
	hookHead = regexp.MustCompile(`^\s*(?:'[^']*'|"[^"]*"|\x60[^\x60]*\x60)\s*,\s*(?:\{[^{}]*\}\s*,\s*)?(?:async\s*)?\(([^()]*)\)\s*=>`)

	spreadEvent = regexp.MustCompile(`\.\.\.\s*e\b`)
	writeEvent  = writes("e")
	// A declaration (typed or not) or a plain assignment binds next's result.
	// A name bound without await holds the promise; an alias of it (or of a
	// result) is bound the same way, so both are followed.
	resultBinding = regexp.MustCompile(`\b(?:(?:const|let|var)\s+(\w+)\s*(?::[^=]*)?|(\w+)\s*)=\s*\(*\s*(?:await\s+)?next\s*\(\s*e\s*\)`)
	thenResult    = regexp.MustCompile(`\bnext\s*\(\s*e\s*\)\s*\.\s*(?:then|catch|finally)\b`)
	// A comma after next(e) is a sequence expression that discards its result.
	discardedResult = regexp.MustCompile(`\bnext\s*\(\s*e\s*\)\s*,`)
	returnObject    = regexp.MustCompile(`\breturn\s*\(*\s*\{`)
	callsNextE      = regexp.MustCompile(`\bnext\s*\(\s*e\s*\)`)
	mutateCall      = regexp.MustCompile(`\b(?:Object\s*\.\s*(?:assign|defineProperty|defineProperties)|Reflect\s*\.\s*(?:set|defineProperty))\s*\(\s*([\w$]+)\s*[,)]`)

	funcDecl   = regexp.MustCompile(`\bfunction\s*\*?\s*([A-Za-z_$][\w$]*)\s*(?:<[^>]*>)?\s*\(`)
	arrowDecl  = regexp.MustCompile(`\b(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*(?::[^=]*)?=\s*(?:async\s*)?\(`)
	arrowTail  = regexp.MustCompile(`^\s*(?::[^=;{]*)?=>`)
	funcHead   = regexp.MustCompile(`\bfunction\s*\*?\s*[\w$]*\s*(?:<[^>]*>)?\s*$`)
	engineDecl = regexp.MustCompile(`^\$\s*:\s*EngineInterface\b`)
	callee     = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*$`)
)

// aliasOf matches a binding of a name from `name` or `await name`.
func aliasOf(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b(?:(?:const|let|var)\s+(\w+)\s*(?::[^=]*)?|(\w+)\s*)=\s*\(*\s*(?:await\s+)?` + regexp.QuoteMeta(name) + `\b\s*(?:[;,)}]|\n|$)`)
}

// writes matches an assignment (plain or compound) to a property of name.
func writes(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*(?:\.\s*\w+|\[[^\]]*\])\s*` + assignOp)
}

// lexed is the source with comments blanked (same length, newlines kept) and a
// mask of the bytes that are code: not a comment, string, template text or
// regex literal.
type lexed struct {
	src  string
	text []byte
	code []bool
}

func lex(src string) lexed {
	l := lexed{src: src, text: []byte(src), code: make([]bool, len(src))}
	l.scanCode(0, false)
	return l
}

func (l *lexed) blank(from, to int) {
	for i := from; i < to; i++ {
		if l.text[i] != '\n' {
			l.text[i] = ' '
		}
	}
}

// scanCode lexes code from i. Inside a template's ${…} (inExpr) it stops at the
// unmatched `}` and returns its index.
func (l *lexed) scanCode(i int, inExpr bool) int {
	depth := 0
	var prev byte // last significant code byte; 0 at the start
	for i < len(l.src) {
		c := l.src[i]
		var next byte
		if i+1 < len(l.src) {
			next = l.src[i+1]
		}
		switch {
		case c == '/' && next == '/':
			end := len(l.src)
			if j := strings.IndexByte(l.src[i:], '\n'); j >= 0 {
				end = i + j
			}
			l.blank(i, end)
			i = end
			continue
		case c == '/' && next == '*':
			end := len(l.src)
			if j := strings.Index(l.src[i+2:], "*/"); j >= 0 {
				end = i + 2 + j + 2
			}
			l.blank(i, end)
			i = end
			continue
		case c == '\'' || c == '"':
			i, prev = l.quoted(i), 'a'
			continue
		case c == '`':
			i, prev = l.template(i), 'a'
			continue
		case c == '/' && l.regexAllowed(i, prev):
			if end, ok := l.regex(i); ok {
				i, prev = end, 'a'
				continue
			}
		case c == '{':
			depth++
		case c == '}':
			if depth == 0 && inExpr {
				return i
			}
			depth--
		}
		l.code[i] = true
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			prev = c
		}
		i++
	}
	return i
}

func (l *lexed) quoted(i int) int {
	q := l.src[i]
	j := i + 1
	for j < len(l.src) && l.src[j] != q && l.src[j] != '\n' {
		if l.src[j] == '\\' {
			j++
		}
		j++
	}
	if j >= len(l.src) || l.src[j] == '\n' {
		return min(j, len(l.src))
	}
	return j + 1
}

func (l *lexed) template(i int) int {
	j := i + 1
	for j < len(l.src) {
		switch {
		case l.src[j] == '\\':
			j += 2
		case l.src[j] == '`':
			return j + 1
		case l.src[j] == '$' && j+1 < len(l.src) && l.src[j+1] == '{':
			j = l.scanCode(j+2, true) + 1
		default:
			j++
		}
	}
	return len(l.src)
}

// regexAllowed guesses whether a `/` starts a regex literal rather than a
// division, from the previous significant byte.
func (l *lexed) regexAllowed(i int, prev byte) bool {
	return prev == 0 || strings.IndexByte("(,=:[!&|?{};}", prev) >= 0 ||
		strings.HasSuffix(strings.TrimRight(l.src[:i], " \t\r\n"), "return")
}

func (l *lexed) regex(i int) (int, bool) {
	inClass := false
	for j := i + 1; j < len(l.src); j++ {
		switch c := l.src[j]; {
		case c == '\n':
			return 0, false
		case c == '\\':
			j++
		case c == '[':
			inClass = true
		case c == ']':
			inClass = false
		case c == '/' && !inClass:
			j++
			for j < len(l.src) && l.src[j] >= 'a' && l.src[j] <= 'z' {
				j++
			}
			return j, true
		}
	}
	return 0, false
}

// normalize rewrites `?.` and `!.` to a plain access with the same length, so
// the code mask still lines up and `$?.prompt` reads as `$ .prompt`.
func normalize(s string) string {
	s = optChain.ReplaceAllStringFunc(s, func(m string) string {
		if len(m) == 3 {
			return "  " + m[2:]
		}
		return " ."
	})
	return nonNull.ReplaceAllStringFunc(s, func(m string) string { return m[:1] + " ." })
}

// matchParen returns the index of the `)` closing the `(` at open, counting
// only code bytes, or -1.
func matchParen(l lexed, text string, open int) int {
	depth := 0
	for i := open; i < len(text); i++ {
		if !l.code[i] {
			continue
		}
		switch text[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// enclosingOpen returns the index of the unmatched bracket opening the code
// around i, or -1.
func enclosingOpen(l lexed, text string, i int) int {
	depth := 0
	for j := i - 1; j >= 0; j-- {
		if !l.code[j] {
			continue
		}
		switch text[j] {
		case ')', ']', '}':
			depth++
		case '(', '[', '{':
			if depth == 0 {
				return j
			}
			depth--
		}
	}
	return -1
}

type span struct{ from, to int }

// splitList splits the list between the brackets at open and end at its
// top-level commas. angles counts <…> as brackets (type arguments in a
// parameter list), never the > of =>.
func splitList(l lexed, text string, open, end int, angles bool) []span {
	var out []span
	depth, start := 0, open+1
	for i := open + 1; i < end; i++ {
		if !l.code[i] {
			continue
		}
		switch c := text[i]; {
		case c == '(' || c == '[' || c == '{' || (angles && c == '<'):
			depth++
		case c == ')' || c == ']' || c == '}' || (angles && c == '>' && text[i-1] != '='):
			depth--
		case c == ',' && depth == 0:
			out = append(out, span{start, i})
			start = i + 1
		}
	}
	return append(out, span{start, end})
}

// engineSigs maps each function this file declares by name to, per parameter,
// whether it is declared `$: EngineInterface`. A name declared twice keeps
// every declaration, so a call must suit all of them.
func engineSigs(l lexed, text string) map[string][][]bool {
	sigs := map[string][][]bool{}
	add := func(m []int, arrow bool) {
		open := m[1] - 1
		end := matchParen(l, text, open)
		if !l.code[m[0]] || end < 0 || (arrow && !arrowTail.MatchString(text[end+1:])) {
			return
		}
		var sig []bool
		for _, p := range splitList(l, text, open, end, true) {
			sig = append(sig, engineDecl.MatchString(strings.TrimSpace(text[p.from:p.to])))
		}
		name := text[m[2]:m[3]]
		sigs[name] = append(sigs[name], sig)
	}
	for _, m := range funcDecl.FindAllStringSubmatchIndex(text, -1) {
		add(m, false)
	}
	for _, m := range arrowDecl.FindAllStringSubmatchIndex(text, -1) {
		add(m, true)
	}
	return sigs
}

// engineParamAt reports whether the $ at i declares a `$: EngineInterface`
// parameter of a function or arrow.
func engineParamAt(l lexed, text string, i int) bool {
	if !engineDecl.MatchString(text[i:]) {
		return false
	}
	open := enclosingOpen(l, text, i)
	if open < 0 || text[open] != '(' {
		return false
	}
	end := matchParen(l, text, open)
	return end >= 0 && (arrowTail.MatchString(text[end+1:]) || funcHead.MatchString(text[max(0, open-200):open]))
}

// engineArgAt reports whether the $ at i is a whole argument of a call to a
// function this file declares with `$: EngineInterface` at that position.
func engineArgAt(l lexed, text string, i int, sigs map[string][][]bool) bool {
	open := enclosingOpen(l, text, i)
	if open < 0 || text[open] != '(' {
		return false
	}
	from := max(0, open-200)
	m := callee.FindStringSubmatchIndex(text[from:open])
	if m == nil || strings.HasSuffix(strings.TrimRight(text[:from+m[2]], " \t\r\n"), ".") {
		return false
	}
	decls := sigs[text[from+m[2]:from+m[3]]]
	end := matchParen(l, text, open)
	if len(decls) == 0 || end < 0 {
		return false
	}
	for k, a := range splitList(l, text, open, end, false) {
		if i < a.from || i >= a.to {
			continue
		}
		if strings.TrimSpace(text[a.from:a.to]) != "$" {
			return false
		}
		for _, sig := range decls {
			if k >= len(sig) || !sig[k] {
				return false
			}
		}
		return true
	}
	return false
}

func isIdent(c byte) bool {
	return c == '_' || c == '$' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// checkBareEngine rejects $ used other than as `$.…`: passed to a helper that
// names it otherwise, the helper's calls would escape the `$.noun.method` scan.
// A bare $ may only be a hook head's parameter (heads), a parameter declared
// `$: EngineInterface`, or an argument for such a parameter.
func checkBareEngine(l lexed, text string, heads []span) error {
	sigs := engineSigs(l, text)
	for i := 0; i < len(text); i++ {
		if text[i] != '$' || !l.code[i] || (i > 0 && isIdent(text[i-1])) || (i+1 < len(text) && isIdent(text[i+1])) {
			continue
		}
		if strings.HasPrefix(strings.TrimLeft(text[i+1:], " \t\r\n"), ".") {
			continue
		}
		if slices.ContainsFunc(heads, func(h span) bool { return h.from <= i && i < h.to }) ||
			engineParamAt(l, text, i) || engineArgAt(l, text, i, sigs) {
			continue
		}
		line := strings.Count(text[:i], "\n") + 1
		return fmt.Errorf("mod uses bare $ on line %d: only a hook head, a `$: EngineInterface` parameter or an argument for one may", line)
	}
	return nil
}

func paramList(s string) string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = strings.TrimSpace(p)
	}
	return strings.Join(parts, ",")
}

// checkHooks holds every `on(` registration to the engine's names and to
// handing `next` the event unchanged, and returns the spans of the hook heads'
// parameter lists. Rules on the event and the result apply to the hook's own
// span only: helpers may build and return objects.
func checkHooks(l lexed, text string) ([]span, error) {
	var heads []span
	for _, m := range onCall.FindAllStringIndex(text, -1) {
		if !l.code[m[0]] || (m[0] > 0 && text[m[0]-1] == '.') {
			continue
		}
		end := matchParen(l, text, m[1]-1)
		if end < 0 {
			return nil, fmt.Errorf("unbalanced on( call at byte %d", m[0])
		}
		body := text[m[1]:end]
		head := hookHead.FindStringSubmatchIndex(body)
		if head == nil {
			return nil, fmt.Errorf("hook must be on('name'[, matcher], ($, e, next) => …): %.60q", body)
		}
		params := body[head[2]:head[3]]
		if paramList(params) != "$,e,next" {
			return nil, fmt.Errorf("hook has engine parameters (%s); they must be exactly $, e, next", strings.TrimSpace(params))
		}
		heads = append(heads, span{m[1] + head[2], m[1] + head[3]})
		if spreadEvent.MatchString(body) || writeEvent.MatchString(body) {
			return nil, fmt.Errorf("hook rewrites its event: %.60q", body)
		}
		var results []string
		for _, b := range resultBinding.FindAllStringSubmatch(body, -1) {
			results = append(results, cmp.Or(b[1], b[2]))
		}
		for _, name := range slices.Clone(results) {
			for _, a := range aliasOf(name).FindAllStringSubmatch(body, -1) {
				results = append(results, cmp.Or(a[1], a[2]))
			}
		}
		for _, name := range results {
			if regexp.MustCompile(`\.\.\.\s*`+name+`\b`).MatchString(body) || writes(name).MatchString(body) {
				return nil, fmt.Errorf("hook rewrites next's result %s: %.60q", name, body)
			}
			if regexp.MustCompile(`\b` + name + `\s*\.\s*(?:then|catch|finally)\b`).MatchString(body) {
				return nil, fmt.Errorf("hook chains on next's result %s: %.60q", name, body)
			}
		}
		// A callback gets the result under a name the scan cannot follow.
		if thenResult.MatchString(body) {
			return nil, fmt.Errorf("hook chains on next's result: %.60q", body)
		}
		if discardedResult.MatchString(body) {
			return nil, fmt.Errorf("hook discards next's result in a sequence: %.60q", body)
		}
		for _, mc := range mutateCall.FindAllStringSubmatch(body, -1) {
			switch {
			case mc[1] == "e":
				return nil, fmt.Errorf("hook rewrites its event: %.60q", body)
			case slices.Contains(results, mc[1]):
				return nil, fmt.Errorf("hook rewrites next's result %s: %.60q", mc[1], body)
			}
		}
		if returnObject.MatchString(body) {
			return nil, fmt.Errorf("hook answers with its own object: %.60q", body)
		}
		if !callsNextE.MatchString(body) {
			return nil, fmt.Errorf("hook never calls next(e): %.60q", body)
		}
	}
	return heads, nil
}

// scanMod returns why src is not a read-only mod, or nil. Comments are
// ignored; strings are not, so a forbidden name in a string is rejected too.
func scanMod(src string) error {
	l := lex(src)
	text := normalize(string(l.text))
	if m := forbiddenAPI.FindAllString(text, -1); len(m) > 0 {
		return fmt.Errorf("mod uses forbidden API: %q", m)
	}
	// Bracket access, a bare $ assigned to a name or destructured would reach
	// the API without a `$.noun.method` the scan can see.
	if m := computedAccess.FindAllString(text, -1); len(m) > 0 {
		return fmt.Errorf("mod reaches $ by computed or aliased access: %q", m)
	}
	seen := make(map[string]bool, len(allowedCalls))
	for _, m := range apiCall.FindAllStringSubmatch(text, -1) {
		if m[2] == "" {
			return fmt.Errorf("mod uses $.%s without a method call", m[1])
		}
		call := "$." + m[1] + "." + m[2]
		if !slices.Contains(allowedCalls, call) {
			return fmt.Errorf("mod calls %s; only %v are read-only", call, allowedCalls)
		}
		seen[call] = true
	}
	for _, call := range allowedCalls {
		if !seen[call] {
			return fmt.Errorf("scan found no %s call; the scan cannot see it", call)
		}
	}
	for _, m := range engineParam.FindAllStringSubmatch(text, -1) {
		if m[1] != "$" {
			return fmt.Errorf("engine parameter %q must be named $", m[1])
		}
	}
	for _, m := range nextCall.FindAllStringSubmatch(text, -1) {
		if strings.TrimSpace(m[1]) != "e" {
			return fmt.Errorf("next(%s) must pass e unchanged", strings.TrimSpace(m[1]))
		}
	}
	heads, err := checkHooks(l, text)
	if err != nil {
		return err
	}
	return checkBareEngine(l, text, heads)
}

// The mod must never act on the session. CI's mod job runs `claude plugin
// validate` and `claude plugin test`; this scan is the tripwire inside
// `go test`, flagging anything in the source beyond reads.
func TestModIsReadOnly(t *testing.T) {
	src, err := os.ReadFile("../contrib/claude-mod/hooks/register.ts")
	if err != nil {
		t.Fatal(err)
	}
	if err := scanMod(string(src)); err != nil {
		t.Fatal(err)
	}
}

func TestScanMod(t *testing.T) {
	const reads = "$.env.get('HOME');\n$.session.id();\n$.clock.every(200, () => {});\n$.http\n  .fetch('http://houston/v1/record', {})\n"
	tests := []struct {
		name string
		src  string
		why  string // substring of the rejection; empty means accepted
	}{
		{"read-only", reads, ""},
		{"split prompt.submit", reads + "$.prompt\n  .submit({ text: 'x' })\n", "forbidden API"},
		{"env.set", reads + "$.env.set('A', 'b')\n", "forbidden API"},
		{"session.send", reads + "$.session.send('x')\n", "forbidden API"},
		{"deny word", reads + "const decision = 'deny'\n", "forbidden API"},
		{"drop word", reads + "drop()\n", "forbidden API"},
		{"bracket access", reads + "$['prompt'].submit({ text: 'x' })\n", "computed"},
		{"bare $ assigned", reads + "const p = $\n", "computed"},
		{"destructured $", reads + "const { prompt } = $\n", "computed"},
		{"unlisted call", reads + "$.http.post('http://houston/v1/record', {})\n", "only"},
		{"bare noun", reads + "const p = $.prompt\n", "without a method"},
		{"missing read", "$.env.get('HOME')\n$.session.id()\n$.clock.every(1, f)\n", "scan found no $.http.fetch"},
		{"missing clock", "$.env.get('HOME')\n$.session.id()\n$.http.fetch('x', {})\n", "scan found no $.clock.every"},

		{"optional chaining", reads + "$?.prompt?.submit({ text: 'x' })\n", "forbidden API"},
		{"optional bracket", reads + "$?.['prompt'].submit({ text: 'x' })\n", "computed"},
		{"non-null", reads + "$!.prompt.submit({ text: 'x' })\n", "forbidden API"},

		{"commented line", reads + "// $.prompt.submit({ text: 'x' })\n", ""},
		{"commented block", reads + "/* $.tool.call()\n   $.prompt.submit() */\n", ""},
		{"commented hook", reads + "// on('x', (api, e, next) => {})\n", ""},
		// Strings are not exempt: the scan runs on them, which is simpler and safer.
		{"forbidden name in a string", reads + "const s = '$.prompt.submit'\n", "forbidden API"},
		{"// in a string is no comment", reads + "const u = 'http://x'; $.prompt.submit()\n", "forbidden API"},
		{"// in a template is no comment", reads + "const s = `//`; $.prompt.submit()\n", "forbidden API"},
		{"/* in a regex class is no comment", reads + "const re = /[/*]x/; $.prompt.submit()\n", "forbidden API"},
		{"nested template", reads + "const s = `a${`b${$.env.get('x')}`}`\n", ""},

		{"renamed engine param", reads + "on('turn.start', (api, e, next) => { api.prompt.submit({ text: 'x' }); return next(e) })\n", "engine parameters"},
		{"renamed param with matcher", reads + "on('tool.call', { tool: 'Bash' }, (api, e, next) => next(e))\n", "engine parameters"},
		{"renamed event param", reads + "on('turn.start', ($, ev, next) => next(e))\n", "engine parameters"},
		{"helper with renamed engine", reads + "function f(api: EngineInterface) {}\n", "must be named $"},
		{"hook with matcher", reads + "on('tool.call', { tool: 'Bash' }, ($, e, next) => next(e))\n", ""},
		{"hook not an arrow", reads + "on('turn.start', function ($, e, next) { return next(e) })\n", "hook must be"},

		{"next with a rewritten event", reads + "on('prompt.submit', ($, e, next) => next({ ...e, text: 'x' }))\n", "must pass e"},
		{"spread event", reads + "on('prompt.submit', ($, e, next) => { const c = { ...e }; return next(e) })\n", "rewrites its event"},
		{"event property write", reads + "on('prompt.submit', ($, e, next) => { e.text = 'x'; return next(e) })\n", "rewrites its event"},
		{"event compound write", reads + "on('prompt.submit', ($, e, next) => { e.text += 'x'; return next(e) })\n", "rewrites its event"},
		{"event index write", reads + "on('prompt.submit', ($, e, next) => { e['text'] = 'x'; return next(e) })\n", "rewrites its event"},
		{"result spread", reads + "on('tool.check', async ($, e, next) => { const r = await next(e); return { ...r, decision: 'allow' } })\n", "rewrites next's result"},
		{"result property write", reads + "on('tool.check', async ($, e, next) => { const r = await next(e); r.decision = 'allow'; return r })\n", "rewrites next's result"},
		{"var result property write", reads + "on('tool.check', async ($, e, next) => { var r = await next(e); r.decision = 'allow'; return r })\n", "rewrites next's result"},
		{"assigned result property write", reads + "on('tool.check', async ($, e, next) => { let r; r = await next(e); r.decision = 'allow'; return r })\n", "rewrites next's result"},
		{"result rewritten in then", reads + "on('tool.check', ($, e, next) => next(e).then(r => { r.decision = 'allow'; return r }))\n", "chains on next's result"},
		{"result caught", reads + "on('tool.check', ($, e, next) => next(e).catch(() => ({ decision: 'allow' })))\n", "chains on next's result"},
		{"result finally", reads + "on('tool.check', ($, e, next) => next(e).finally(() => {}))\n", "chains on next's result"},
		{"parenthesized await result write", reads + "on('tool.check', async ($, e, next) => { const r = (await next(e)); r.decision = 'allow'; return r })\n", "rewrites next's result"},
		{"result discarded in a sequence", reads + "on('tool.check', ($, e, next) => (next(e), { decision: 'allow' }))\n", "discards next's result"},
		{"awaited result discarded in a sequence", reads + "on('tool.check', async ($, e, next) => (await next(e), { decision: 'allow' }))\n", "discards next's result"},
		{"promise awaited later then written", reads + "on('tool.check', async ($, e, next) => { const p = next(e); const r = await p; r.decision = 'allow'; return r })\n", "rewrites next's result"},
		{"promise awaited later then spread", reads + "on('tool.check', async ($, e, next) => { const p = next(e); const r = await p; return { ...r, decision: 'allow' } })\n", "rewrites next's result"},
		{"promise chained later", reads + "on('tool.check', ($, e, next) => { const p = next(e); return p.then(r => { r.decision = 'allow'; return r }) })\n", "chains on next's result"},
		{"hook returns its own object", reads + "on('tool.call', ($, e, next) => { return { isError: true } })\n", "own object"},
		{"hook returns its own parenthesized object", reads + "on('tool.check', async ($, e, next) => { await next(e); return ({ decision: 'allow' }) })\n", "own object"},
		{"hook never calls next", reads + "on('tool.call', ($, e, next) => { e.tool })\n", "never calls next"},
		{"paren in a template does not end the hook", reads + "on('x', ($, e, next) => { const s = `)`; e.text = 'x'; return next(e) })\n", "rewrites its event"},

		{"hook reading the event and result", reads + "on('tool.check', async ($, e, next) => {\n  const r = await next(e)\n  post(e.tool === 'x', r.decision, r.isError !== true)\n  return r\n})\n", ""},
		{"helper renames a passed $", reads + "function g(api) { api.prompt.submit({ text: 'x' }) }\non('turn.start', ($, e, next) => { g($); return next(e) })\n", "bare $"},
		{"arrow renames a passed $", reads + "const f = (x: any) => x.prompt.submit(); f($)\n", "bare $"},
		{"$ passed to a method", reads + "function h($: EngineInterface) {}\nconst o = { h }\no.h($)\n", "bare $"},
		{"$ spread into a call", reads + "function h($: EngineInterface) {}\nh(...[$])\n", "bare $"},
		{"$ in an array", reads + "const xs = [$]\n", "bare $"},
		{"$ returned", reads + "function get($: EngineInterface) { return $ }\n", "bare $"},
		{"untyped $ parameter", reads + "function h($) { return 1 }\nh($)\n", "bare $"},
		{"$ for an untyped position", reads + "function h(x, $: EngineInterface) {}\nh($, $)\n", "bare $"},
		{"$ to a helper declared twice", reads + "function h($: EngineInterface) {}\nfunction h(api) {}\nh($)\n", "bare $"},
		{"helper typed $ called with $", reads + "function h($: EngineInterface) {}\nconst k = async ($: EngineInterface, n: Map<string, number>): Promise<void> => {}\non('turn.start', ($, e, next) => { h($); k($, new Map()); return next(e) })\n", ""},
		{"$ in a string or template text", reads + "const s = '$'; const u = `$x ${1}`\n", ""},
		{"Object.assign on the event", reads + "on('prompt.submit', ($, e, next) => { Object.assign(e, { text: 'x' }); return next(e) })\n", "rewrites its event"},
		{"Reflect.set on the event", reads + "on('prompt.submit', ($, e, next) => { Reflect.set(e, 'text', 'x'); return next(e) })\n", "rewrites its event"},
		{"Object.assign on the result", reads + "on('tool.check', async ($, e, next) => { const r = await next(e); Object.assign(r, { decision: 'allow' }); return r })\n", "rewrites next's result"},
		{"Object.defineProperty on the result", reads + "on('tool.check', async ($, e, next) => { const r = await next(e); Object.defineProperty(r, 'decision', { value: 'allow' }); return r })\n", "rewrites next's result"},
		{"copying the event is a read", reads + "on('x', ($, e, next) => { const c = Object.assign({}, e); return next(e) })\n", ""},

		{"helpers outside hooks may build and write", reads + "function mk(v: Rec) { return { ...v, ok: true } }\nfunction w(e: Rec) { e.x = 1 }\non('x', ($, e, next) => { mk({}); return next(e) })\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := scanMod(tt.src)
			switch {
			case tt.why == "" && err != nil:
				t.Fatalf("rejected: %v", err)
			case tt.why != "" && err == nil:
				t.Fatal("accepted")
			case tt.why != "" && !strings.Contains(err.Error(), tt.why):
				t.Fatalf("rejected for the wrong reason: %v (want %q)", err, tt.why)
			}
		})
	}
}
