package main

import (
	"flag"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/noamsto/houston/hook"
	"github.com/noamsto/houston/server"
	"github.com/noamsto/houston/terminal"
)

func main() {
	// Sub-command dispatch. Anything that isn't a known verb falls through
	// to the server (the default behavior houston has always had).
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "hook":
			os.Exit(cmdHook(os.Args[2:]))
		case "hooks":
			os.Exit(cmdHooks(os.Args[2:]))
		case "doctor":
			os.Exit(cmdDoctor(os.Args[2:]))
		case "-h", "--help", "help":
			printUsage(os.Stderr)
			return
		}
	}
	runServer()
}

func printUsage(w *os.File) {
	fmt.Fprintf(w, `houston — mission control for AI coding agents

Usage:
  houston [flags]              start the web server (default)
  houston hook [<event>]       handle a Claude Code hook (stdin = event JSON)
  houston hooks install        install houston's hooks into ~/.claude/settings.json
  houston hooks check          report which hooks are installed
  houston doctor               full environment check

Run "houston -h" for server flags.
`)
}

func resolveStateDir() string {
	if v := os.Getenv("HOUSTON_STATE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("HOUSTON_STATUS_DIR"); v != "" {
		return v // legacy env var — keep working
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "houston")
}

// ---------- subcommand: hook ----------

func cmdHook(args []string) int {
	event := ""
	if len(args) > 0 {
		event = args[0]
	}
	stateDir := resolveStateDir()
	if err := hook.Dispatch(event, stateDir, os.Stdin); err != nil {
		// Never fail loudly from a hook — Claude Code would surface the error
		// to the user. Log to stderr (captured by Claude Code's debug mode).
		fmt.Fprintf(os.Stderr, "houston hook %s: %v\n", event, err)
		return 0
	}
	return 0
}

// ---------- subcommand: hooks ----------

func cmdHooks(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: houston hooks {install|check} [--dry-run]")
		return 2
	}
	fs := flag.NewFlagSet("hooks", flag.ExitOnError)
	dryRun := fs.Bool("dry-run", false, "print the resulting settings.json to stdout, don't write")
	_ = fs.Parse(args[1:])

	settingsPath, err := hook.SettingsPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	binary, _ := os.Executable()

	switch args[0] {
	case "install":
		out, err := hook.Install(settingsPath, binary, *dryRun)
		if err != nil {
			fmt.Fprintf(os.Stderr, "install: %v\n", err)
			return 1
		}
		if *dryRun {
			_, _ = os.Stdout.Write(out)
			return 0
		}
		fmt.Printf("wrote %s\n", settingsPath)
		return 0

	case "check":
		findings, err := hook.Check(settingsPath, binary)
		if err != nil {
			fmt.Fprintf(os.Stderr, "check: %v\n", err)
			return 1
		}
		missing := 0
		for _, f := range findings {
			mark := "ok"
			if !f.Installed {
				mark = "missing"
				missing++
			}
			fmt.Printf("  %-20s %-7s  %s\n", f.Event, mark, f.Target)
		}
		if missing > 0 {
			fmt.Fprintf(os.Stderr, "\n%d hook(s) not installed. Run: houston hooks install\n", missing)
			return 1
		}
		return 0

	default:
		fmt.Fprintf(os.Stderr, "unknown hooks subcommand: %q\n", args[0])
		return 2
	}
}

// ---------- subcommand: doctor ----------

func cmdDoctor(_ []string) int {
	rep, err := hook.Doctor(resolveStateDir())
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		return 1
	}
	hook.Print(os.Stdout, rep)
	for _, f := range rep.Findings {
		if !f.Installed {
			return 1
		}
	}
	if !rep.StateDirOK || !rep.LoopbackOK {
		return 1
	}
	return 0
}

// ---------- default: web server ----------

func runServer() {
	addr := flag.String("addr", "127.0.0.1:9090", "HTTP listen address")
	statusDir := flag.String("status-dir", "", "Directory for hook status files")
	debug := flag.Bool("debug", false, "Enable debug logging")

	// OpenCode integration flags
	openCodeURL := flag.String("opencode-url", "", "OpenCode server URL (skip discovery)")
	noOpenCode := flag.Bool("no-opencode", false, "Disable OpenCode integration")

	noAuth := flag.Bool("no-auth", false, "disable API authentication (NOT recommended)")

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
		*statusDir = resolveStateDir()
	}

	// Auto-detect terminal for font size control
	fontCtrl := terminal.NewFontController()
	if fontCtrl.Name() != "" {
		slog.Info("terminal font control", "terminal", fontCtrl.Name())
	}

	// Embed React SPA: strip the ui/dist prefix so the FS root is the dist dir.
	uiSubFS, err := fs.Sub(uiFS, "ui/dist")
	if err != nil {
		log.Fatalf("failed to create UI sub-filesystem: %v", err)
	}

	allowedOrigins := []string{}
	if *debug {
		// The Vite dev server proxies /api here; its Origin is preserved
		// through the proxy, so it has to be allowlisted explicitly.
		allowedOrigins = append(allowedOrigins, "http://localhost:5173")
	}
	if *noAuth {
		slog.Warn("API authentication is DISABLED; any page that can reach this port can drive your tmux panes")
	}

	srv, err := server.New(server.Config{
		StatusDir:       *statusDir,
		FontController:  fontCtrl,
		OpenCodeEnabled: !*noOpenCode,
		OpenCodeURL:     *openCodeURL,
		UIFS:            uiSubFS,
		AuthEnabled:     !*noAuth,
		AllowedOrigins:  allowedOrigins,
	})
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}

	fmt.Fprintf(os.Stderr, "houston starting on http://%s\n", *addr)
	fmt.Fprintf(os.Stderr, "status directory: %s\n", *statusDir)
	if !*noAuth {
		fmt.Fprintf(os.Stderr, "api token: %s (open the UI once to authorize this browser)\n",
			filepath.Join(*statusDir, "token"))
	}

	if err := http.ListenAndServe(*addr, srv.Handler()); err != nil {
		log.Fatal(err)
	}
}
