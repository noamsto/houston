package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/noams/tmux-dashboard/server"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "HTTP listen address")
	statusDir := flag.String("status-dir", "", "Directory for hook status files")
	flag.Parse()

	if *statusDir == "" {
		home, _ := os.UserHomeDir()
		*statusDir = filepath.Join(home, ".local", "state", "tmux-dashboard")
	}

	srv, err := server.New(server.Config{
		StatusDir: *statusDir,
	})
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	fmt.Fprintf(os.Stderr, "tmux-dashboard starting on http://%s\n", *addr)
	fmt.Fprintf(os.Stderr, "status directory: %s\n", *statusDir)

	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
