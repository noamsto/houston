package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultPorts are the ports to check for OpenCode servers.
// OpenCode TUI uses random ports by default, so we also check common alternatives.
// For reliable discovery, start OpenCode with: opencode --port 4096
var DefaultPorts = []int{
	4096, 4097, 4098, 4099, 4100, // Default range
}

// DiscoveryDir is where the houston OpenCode plugin writes server info.
// Each running OpenCode instance writes a {pid}.json file here.
func DiscoveryDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "houston", "opencode-servers")
}

// DiscoveredServer represents server info from a discovery file.
type DiscoveredServer struct {
	PID       int    `json:"pid"`
	URL       string `json:"url"`
	Project   string `json:"project"`
	Directory string `json:"directory"`
	StartedAt string `json:"startedAt"`
}

// ReadDiscoveryFiles reads all discovery files from the houston plugin.
func ReadDiscoveryFiles() []DiscoveredServer {
	dir := DiscoveryDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var servers []DiscoveredServer
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}

		var srv DiscoveredServer
		if err := json.Unmarshal(data, &srv); err != nil {
			continue
		}

		// Verify process is still running
		if !isProcessRunning(srv.PID) {
			// Clean up stale file
			os.Remove(filepath.Join(dir, entry.Name()))
			continue
		}

		servers = append(servers, srv)
	}

	return servers
}

// isProcessRunning checks if a process with the given PID exists.
func isProcessRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Send signal 0 to check if process exists.
	err = process.Signal(os.Signal(nil))
	return err == nil
}

// Server represents a discovered OpenCode server.
type Server struct {
	URL     string
	Version string
	Project *Project
}

// Discovery manages finding and tracking OpenCode servers.
type Discovery struct {
	servers   map[string]*Server // URL -> Server
	serversMu sync.RWMutex

	// Configuration
	ports     []int
	hostname  string
	staticURL string // If set, only check this URL
}

// DiscoveryOption configures discovery behavior.
type DiscoveryOption func(*Discovery)

// WithPorts sets the ports to scan.
func WithPorts(ports []int) DiscoveryOption {
	return func(d *Discovery) {
		d.ports = ports
	}
}

// WithHostname sets the hostname to scan.
func WithHostname(hostname string) DiscoveryOption {
	return func(d *Discovery) {
		d.hostname = hostname
	}
}

// WithStaticURL sets a single URL to check instead of scanning.
func WithStaticURL(url string) DiscoveryOption {
	return func(d *Discovery) {
		d.staticURL = url
	}
}

// NewDiscovery creates a new OpenCode server discovery.
func NewDiscovery(opts ...DiscoveryOption) *Discovery {
	d := &Discovery{
		servers:  make(map[string]*Server),
		ports:    DefaultPorts,
		hostname: "127.0.0.1",
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Scan checks for running OpenCode servers.
// Returns the servers found during this scan.
// Discovery sources:
// 1. Static URL (if configured)
// 2. Discovery files from houston plugin (~/.local/state/houston/opencode-servers/)
// 3. Port scanning (default ports 4096-4100)
func (d *Discovery) Scan(ctx context.Context) []*Server {
	var urls []string

	if d.staticURL != "" {
		urls = []string{d.staticURL}
	} else {
		// First, check discovery files from houston plugin
		discovered := ReadDiscoveryFiles()
		for _, srv := range discovered {
			if srv.URL != "" {
				urls = append(urls, srv.URL)
				slog.Info("OpenCode discovered via plugin", "url", srv.URL, "project", srv.Project)
			}
		}

		// Also scan default ports as fallback
		for _, port := range d.ports {
			url := fmt.Sprintf("http://%s:%d", d.hostname, port)
			// Avoid duplicates
			found := false
			for _, u := range urls {
				if u == url {
					found = true
					break
				}
			}
			if !found {
				urls = append(urls, url)
			}
		}
	}

	if len(urls) == 0 {
		slog.Debug("OpenCode no URLs to scan")
		return nil
	}

	slog.Debug("OpenCode scanning", "urls", urls)

	var found []*Server
	var foundMu sync.Mutex
	var wg sync.WaitGroup

	for _, url := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()

			client := NewClient(url)
			ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()

			health, err := client.Health(ctx)
			if err != nil {
				// Server not available at this URL
				d.removeServer(url)
				return
			}

			if !health.Healthy {
				d.removeServer(url)
				return
			}

			// Get project info
			project, _ := client.GetCurrentProject(ctx)

			server := &Server{
				URL:     url,
				Version: health.Version,
				Project: project,
			}

			d.addServer(url, server)

			foundMu.Lock()
			found = append(found, server)
			foundMu.Unlock()

			slog.Info("OpenCode server found",
				"url", url,
				"version", health.Version,
				"project", projectName(project))

		}(url)
	}

	wg.Wait()
	return found
}

// GetServers returns all currently known servers.
func (d *Discovery) GetServers() []*Server {
	d.serversMu.RLock()
	defer d.serversMu.RUnlock()

	servers := make([]*Server, 0, len(d.servers))
	for _, s := range d.servers {
		servers = append(servers, s)
	}
	return servers
}

// GetServer returns a specific server by URL.
func (d *Discovery) GetServer(url string) *Server {
	d.serversMu.RLock()
	defer d.serversMu.RUnlock()
	return d.servers[url]
}

// StartBackgroundScan starts periodic scanning for servers.
// Returns a cancel function to stop scanning.
func (d *Discovery) StartBackgroundScan(ctx context.Context, interval time.Duration) context.CancelFunc {
	ctx, cancel := context.WithCancel(ctx)

	go func() {
		// Initial scan
		d.Scan(ctx)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.Scan(ctx)
			}
		}
	}()

	return cancel
}

func (d *Discovery) addServer(url string, server *Server) {
	d.serversMu.Lock()
	d.servers[url] = server
	d.serversMu.Unlock()
}

func (d *Discovery) removeServer(url string) {
	d.serversMu.Lock()
	delete(d.servers, url)
	d.serversMu.Unlock()
}

func projectName(p *Project) string {
	if p == nil {
		return ""
	}
	return p.Name
}
