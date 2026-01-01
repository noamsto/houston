package main

import (
	"flag"
	"fmt"
	"os"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()

	fmt.Fprintf(os.Stderr, "tmux-dashboard starting on %s\n", *addr)
}
