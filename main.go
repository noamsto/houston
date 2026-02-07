package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/noamsto/houston/server"
	"github.com/noamsto/houston/terminal"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9090", "HTTP listen address")
	statusDir := flag.String("status-dir", "", "Directory for hook status files")
	debug := flag.Bool("debug", false, "Enable debug logging")

	// OpenCode integration flags
	openCodeURL := flag.String("opencode-url", "", "OpenCode server URL (skip discovery)")
	noOpenCode := flag.Bool("no-opencode", false, "Disable OpenCode integration")

	flag.Parse()

	// Configure slog
	logLevel := slog.LevelInfo
	if *debug {
		logLevel = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	})))

	if *statusDir == "" {
		home, _ := os.UserHomeDir()
		*statusDir = filepath.Join(home, ".local", "state", "houston")
	}

	// Auto-detect terminal for font size control
	fontCtrl := terminal.NewFontController()
	if fontCtrl.Name() != "" {
		slog.Info("terminal font control", "terminal", fontCtrl.Name())
	}

	srv, err := server.New(server.Config{
		StatusDir:       *statusDir,
		FontController:  fontCtrl,
		OpenCodeEnabled: !*noOpenCode,
		OpenCodeURL:     *openCodeURL,
	})
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	fmt.Fprintf(os.Stderr, "houston starting on http://%s\n", *addr)
	fmt.Fprintf(os.Stderr, "status directory: %s\n", *statusDir)

	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
