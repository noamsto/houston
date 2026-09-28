//go:build tools

// Command sessionlog prints a transcript file as ACP JSONL, one chat.Update
// per line, for eyeballing a Reader's output during development.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/noamsto/houston/chat"
)

func main() {
	engine := flag.String("engine", "claude", "engine name (claude, claude-code)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sessionlog [-engine claude] <file>")
		os.Exit(2)
	}
	path := flag.Arg(0)

	r := chat.For(*engine)
	if r == nil {
		fmt.Fprintf(os.Stderr, "sessionlog: no reader for engine %q\n", *engine)
		os.Exit(1)
	}

	updates, _, _, err := r.Read(path, chat.Cursor{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sessionlog: %v\n", err)
		os.Exit(1)
	}

	w := bufio.NewWriter(os.Stdout)
	defer func() { _ = w.Flush() }()

	for i, u := range updates {
		u.Seq = uint64(i + 1)
		b, err := json.Marshal(u)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sessionlog: %v\n", err)
			os.Exit(1)
		}
		if _, err := w.Write(b); err != nil {
			fmt.Fprintf(os.Stderr, "sessionlog: %v\n", err)
			os.Exit(1)
		}
		if err := w.WriteByte('\n'); err != nil {
			fmt.Fprintf(os.Stderr, "sessionlog: %v\n", err)
			os.Exit(1)
		}
	}
}
