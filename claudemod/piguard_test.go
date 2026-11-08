package claudemod

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

var (
	piTypeSource = "@earendil-works/pi-coding-agent"
	piModules    = []string{"node:http", "node:crypto"}
	piEvents     = []string{
		"session_start", "session_shutdown", "agent_start", "turn_start",
		"tool_execution_start", "tool_execution_end", "agent_settled",
		"session_compact", "session_compact_failed",
	}

	importWord  = regexp.MustCompile(`\bimport\b`)
	importStmt  = regexp.MustCompile(`^import\s+(type\s+)?(?:[^'";]*?\bfrom\s*)?(['"])([^'"]+)['"]`)
	dynImport   = regexp.MustCompile(`^import\s*\(`)
	requireCall = regexp.MustCompile(`\brequire\s*\(`)

	piWord      = regexp.MustCompile(`\bpi\b`)
	piOn        = regexp.MustCompile(`^pi\s*\.\s*on\s*\(`)
	piFactory   = regexp.MustCompile(`export\s+default\s+function\s*[\w$]*\s*\(\s*(pi)\s*:\s*ExtensionAPI\s*\)`)
	eventName   = regexp.MustCompile(`^\s*(["'])([^"']+)["']\s*,`)
	handlerHead = regexp.MustCompile(`^\s*(?:async\s*)?\([^()]*\)\s*=>\s*\{`)

	ctxWord    = regexp.MustCompile(`\bctx\b`)
	ctxRead    = regexp.MustCompile(`^ctx\s*\.\s*sessionManager\s*\.\s*(?:getSessionId|getSessionFile|getCwd)\s*\(`)
	paramHead  = regexp.MustCompile(`\bfunction\s*\*?\s*[\w$]*\s*(?:<[^>]*>)?\s*$`)
	calleeName = regexp.MustCompile(`([A-Za-z_$][\w$]*)\s*$`)

	returnValue = regexp.MustCompile(`\breturn\b\s*[^\s;}]`)

	replyAPI = regexp.MustCompile(`\.\s*on\s*\(\s*["']data["']|\.\s*pipe\s*\(|\.\s*read\s*\(|\bsetEncoding\b`)
)

// codeAt returns the matches of re in text that start on a code byte.
func codeAt(l lexed, text string, re *regexp.Regexp) [][]int {
	var out [][]int
	for _, m := range re.FindAllStringSubmatchIndex(text, -1) {
		if l.code[m[0]] {
			out = append(out, m)
		}
	}
	return out
}

func lineOf(text string, i int) int { return strings.Count(text[:i], "\n") + 1 }

// matchBrace returns the index of the `}` closing the `{` at open, counting
// only code bytes, or -1.
func matchBrace(l lexed, text string, open int) int {
	depth := 0
	for i := open; i < len(text); i++ {
		if !l.code[i] {
			continue
		}
		switch text[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

func checkPiImports(l lexed, text string) error {
	for _, m := range codeAt(l, text, importWord) {
		rest := text[m[0]:]
		if dynImport.MatchString(rest) {
			return fmt.Errorf("dynamic import( on line %d", lineOf(text, m[0]))
		}
		s := importStmt.FindStringSubmatch(rest)
		switch {
		case s == nil:
			return fmt.Errorf("unrecognized import on line %d", lineOf(text, m[0]))
		case s[1] != "" && s[3] != piTypeSource:
			return fmt.Errorf("import type from %q on line %d: only %s", s[3], lineOf(text, m[0]), piTypeSource)
		case s[1] == "" && !slices.Contains(piModules, s[3]):
			return fmt.Errorf("import from %q on line %d: only %v", s[3], lineOf(text, m[0]), piModules)
		}
	}
	if m := codeAt(l, text, requireCall); len(m) > 0 {
		return fmt.Errorf("require( on line %d", lineOf(text, m[0][0]))
	}
	return nil
}

// checkPiUse holds `pi` to the factory parameter and the pi.on( calls, and
// returns the end of each pi.on( call's name and the handlers' bodies.
func checkPiUse(l lexed, text string) (map[string]bool, error) {
	paramAt := -1
	if f := piFactory.FindStringSubmatchIndex(text); f != nil && l.code[f[0]] {
		paramAt = f[2]
	}
	events := map[string]bool{}
	calls := 0
	for _, m := range codeAt(l, text, piWord) {
		i := m[0]
		if i == paramAt {
			continue
		}
		if (i > 0 && (text[i-1] == '.' || text[i-1] == '$')) || !piOn.MatchString(text[i:]) {
			return nil, fmt.Errorf("pi used other than pi.on( on line %d", lineOf(text, i))
		}
		calls++
		open := i + strings.IndexByte(text[i:], '(')
		name := eventName.FindStringSubmatchIndex(text[open+1:])
		if name == nil {
			return nil, fmt.Errorf("pi.on( on line %d needs a string-literal event name", lineOf(text, i))
		}
		events[text[open+1+name[4]:open+1+name[5]]] = true
		rest := open + 1 + name[1]
		head := handlerHead.FindStringIndex(text[rest:])
		if head == nil {
			return nil, fmt.Errorf("pi.on handler on line %d must be an inline arrow function with a block body", lineOf(text, i))
		}
		bodyOpen := rest + head[1] - 1
		end := matchBrace(l, text, bodyOpen)
		if end < 0 {
			return nil, fmt.Errorf("unbalanced handler body on line %d", lineOf(text, i))
		}
		for _, r := range returnValue.FindAllStringIndex(text[bodyOpen:end], -1) {
			if l.code[bodyOpen+r[0]] {
				return nil, fmt.Errorf("handler returns a value on line %d", lineOf(text, bodyOpen+r[0]))
			}
		}
	}
	if calls == 0 {
		return nil, fmt.Errorf("scan found no pi.on( call; the scan cannot see it")
	}
	return events, nil
}

func checkPiEvents(events map[string]bool) error {
	for _, want := range piEvents {
		if !events[want] {
			return fmt.Errorf("extension does not subscribe to %s", want)
		}
	}
	for got := range events {
		if !slices.Contains(piEvents, got) {
			return fmt.Errorf("extension subscribes to %s; only %v are allowed", got, piEvents)
		}
	}
	return nil
}

// ctxAllowed reports whether the ctx at i is a read of the session, a
// parameter, or the whole argument of a call.
func ctxAllowed(l lexed, text string, i int) bool {
	if ctxRead.MatchString(text[i:]) {
		return true
	}
	open := enclosingOpen(l, text, i)
	if open < 0 || text[open] != '(' {
		return false
	}
	before := strings.TrimRight(text[open+1:i], " \t\r\n")
	after := strings.TrimLeft(text[i+len("ctx"):], " \t\r\n")
	if before != "" && !strings.HasSuffix(before, ",") {
		return false
	}
	end := matchParen(l, text, open)
	if end < 0 {
		return false
	}
	tail := text[end+1:]
	isParams := arrowTail.MatchString(tail) || paramHead.MatchString(text[:open])
	if isParams {
		return strings.HasPrefix(after, ",") || strings.HasPrefix(after, ")") || strings.HasPrefix(after, ":")
	}
	if before != "" || !strings.HasPrefix(after, ")") {
		return false
	}
	callee := calleeName.FindStringSubmatch(text[:open])
	return callee != nil && !slices.Contains([]string{"if", "while", "for", "switch", "return", "typeof", "await"}, callee[1])
}

func checkPiCtx(l lexed, text string) error {
	for _, m := range codeAt(l, text, ctxWord) {
		i := m[0]
		if (i > 0 && (text[i-1] == '.' || text[i-1] == '$')) || !ctxAllowed(l, text, i) {
			return fmt.Errorf("ctx used beyond the session reads, a parameter or a call argument on line %d", lineOf(text, i))
		}
	}
	return nil
}

// scanPi returns why src is not a read-only pi extension, or nil. Comments are
// ignored and identifiers in strings do not count.
func scanPi(src string) error {
	l := lex(src)
	text := string(l.text)
	if err := checkPiImports(l, text); err != nil {
		return err
	}
	events, err := checkPiUse(l, text)
	if err != nil {
		return err
	}
	if err := checkPiEvents(events); err != nil {
		return err
	}
	if err := checkPiCtx(l, text); err != nil {
		return err
	}
	if m := codeAt(l, text, replyAPI); len(m) > 0 {
		return fmt.Errorf("extension reads houston's reply (%s) on line %d", strings.TrimSpace(text[m[0][0]:m[0][1]]), lineOf(text, m[0][0]))
	}
	return nil
}

// The extension must never act on the session or read houston's reply.
func TestPiExtensionIsReadOnly(t *testing.T) {
	src, err := os.ReadFile("../contrib/pi-mod/houston.ts")
	if err != nil {
		t.Fatal(err)
	}
	if err := scanPi(string(src)); err != nil {
		t.Fatal(err)
	}
}

func TestScanPi(t *testing.T) {
	const base = `import { randomUUID } from "node:crypto";
import http from "node:http";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";
/*IMPORTS*/
type Ctx = { sessionManager: { getCwd(): string } };
function coords(ctx: Ctx) { return { cwd: ctx.sessionManager.getCwd() }; }
/*HELPERS*/
export default function (pi: ExtensionAPI) {
  /*EXTRA*/
  pi.on("session_start", async (_event, ctx) => { post(coords(ctx)); });
  pi.on("agent_start", async (_event, ctx) => { post(coords(ctx)); });
  pi.on("turn_start", async () => { post("x"); });
  pi.on("tool_execution_start", async (event) => { post(event.toolName); });
  pi.on("tool_execution_end", async (event) => { post(event.toolName); });
  pi.on("agent_settled", async (event) => { post(event.aborted); });
  pi.on("session_compact", async () => { post("x"); });
  pi.on("session_compact_failed", async () => { post("x"); });
  pi.on("session_shutdown", async (event) => { await post(event.reason); });
}
`
	const compact = `  pi.on("session_compact", async () => { post("x"); });` + "\n"
	const compactFailed = `  pi.on("session_compact_failed", async () => { post("x"); });` + "\n"
	tests := []struct {
		name string
		old  string // part of base to replace
		new  string
		why  string // substring of the rejection; empty means accepted
	}{
		{"compliant base", "", "", ""},
		{"extra import", "/*IMPORTS*/", `import fs from "node:fs";`, "node:fs"},
		{"import type from another module", "/*IMPORTS*/", `import type { X } from "node:http";`, "import type"},
		{"value import from the pi package", "/*IMPORTS*/", `import { X } from "@earendil-works/pi-coding-agent";`, "import from"},
		{"side-effect import", "/*IMPORTS*/", `import "node:fs";`, "node:fs"},
		{"dynamic import", "/*HELPERS*/", `const m = import("node:fs");`, "dynamic import"},
		{"require", "/*HELPERS*/", `const fs = require("node:fs");`, "require"},
		{"aliased pi", "/*EXTRA*/", `const x = pi;`, "pi used"},
		{"pi passed to a function", "/*EXTRA*/", `f(pi);`, "pi used"},
		{"pi.sendMessage", "/*EXTRA*/", `pi.sendMessage("x");`, "pi used"},
		{"pi bracket access", "/*EXTRA*/", `pi["on"]("x", () => {});`, "pi used"},
		{"member pi", "/*EXTRA*/", `o.pi.on("x", () => {});`, "pi used"},
		{"non-literal event name", `pi.on("agent_start",`, `pi.on(name,`, "string-literal"},
		{"template event name", `pi.on("agent_start",`, "pi.on(`agent_start`,", "string-literal"},
		{"extra event", "/*EXTRA*/", `pi.on("tool_call", async () => {});`, "tool_call"},
		{"missing event", compactFailed, "", "session_compact_failed"},
		{"renamed event", `"turn_start"`, `"turn_end"`, "turn_start"},
		{"ctx.abort", "/*HELPERS*/", `function f(ctx: Ctx) { ctx.abort(); }`, "ctx used"},
		{"ctx.ui", "/*HELPERS*/", `function f(ctx: Ctx) { ctx.ui.notify("x"); }`, "ctx used"},
		{"ctx getEntries", "/*HELPERS*/", `function f(ctx: Ctx) { ctx.sessionManager.getEntries(); }`, "ctx used"},
		{"ctx bracket access", "/*HELPERS*/", `function f(ctx: Ctx) { ctx["sessionManager"]; }`, "ctx used"},
		{"ctx aliased", "/*HELPERS*/", `function f(ctx: Ctx) { const c = ctx; }`, "ctx used"},
		{"ctx in an array", "/*HELPERS*/", `function f(ctx: Ctx) { g([ctx]); }`, "ctx used"},
		{"ctx beside another argument", "/*HELPERS*/", `function f(ctx: Ctx) { g(1, ctx); }`, "ctx used"},
		{"ctx returned", "/*HELPERS*/", `function f(ctx: Ctx) { return ctx; }`, "ctx used"},
		{"ctx in a condition", "/*HELPERS*/", `function f(ctx: Ctx) { if (ctx) {} }`, "ctx used"},
		{"expression-body handler", compact, `  pi.on("session_compact", async () => post("x"));` + "\n", "block body"},
		{"named handler", compact, `  pi.on("session_compact", handler);` + "\n", "block body"},
		{"handler returning a value", compact, `  pi.on("session_compact", async () => { return 1; });` + "\n", "returns a value"},
		{"handler returning a call", compact, `  pi.on("session_compact", async () => { if (a) return post("x"); });` + "\n", "returns a value"},
		{"handler with a bare return", compact, `  pi.on("session_compact", async () => { if (a) return; post("x"); });` + "\n", ""},
		{"helper returning a value", "/*HELPERS*/", `function mk() { return { a: 1 }; }`, ""},
		{"ctx as a call argument", "/*HELPERS*/", `function f(ctx: Ctx) { g(ctx); }`, ""},
		{"ctx session reads", "/*HELPERS*/", `function f(ctx: Ctx) { ctx.sessionManager.getSessionId(); ctx.sessionManager.getSessionFile(); }`, ""},
		{"response data listener", "/*HELPERS*/", `res.on("data", () => {});`, "reply"},
		{"response data listener, single quotes", "/*HELPERS*/", `res.on('data', () => {});`, "reply"},
		{"pipe", "/*HELPERS*/", `res.pipe(out);`, "reply"},
		{"read", "/*HELPERS*/", `res.read();`, "reply"},
		{"setEncoding", "/*HELPERS*/", `res.setEncoding("utf8");`, "reply"},
		{"error listener is no reply read", "/*HELPERS*/", `req.on("error", () => {}); res.resume();`, ""},
		{"commented pi call", "/*HELPERS*/", `// pi.sendMessage("x")`, ""},
		{"commented ctx call", "/*HELPERS*/", `/* ctx.abort() */`, ""},
		{"pi and ctx in strings", "/*HELPERS*/", `const s = "pi.sendMessage(ctx.abort())"; const e = { engine: "pi" };`, ""},
	}
	t.Run("blind guard", func(t *testing.T) {
		err := scanPi("export default function (pi: ExtensionAPI) {}\n")
		if err == nil || !strings.Contains(err.Error(), "no pi.on") {
			t.Fatalf("want a no-pi.on rejection, got %v", err)
		}
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := base
			if tt.old != "" {
				if !strings.Contains(base, tt.old) {
					t.Fatalf("base lacks %q", tt.old)
				}
				src = strings.Replace(base, tt.old, tt.new, 1)
			}
			err := scanPi(src)
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
