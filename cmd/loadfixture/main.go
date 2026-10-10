//go:build tools

// Command loadfixture writes a synthetic houston state dir and Claude
// projects dir for load measurements. Run houston against it with
// HOME=<dir>/home houston -status-dir <dir>/state.
package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"

	"github.com/noamsto/houston/internal/loadfixture"
)

func main() {
	dir := flag.String("dir", "", "output directory (required)")
	sessions := flag.Int("sessions", 250, "number of sessions")
	scale := flag.Float64("scale", 1, "transcript size multiplier")
	seed := flag.Int64("seed", 1, "random seed")
	flag.Parse()
	if *dir == "" {
		log.Fatal("-dir is required")
	}
	ss, err := loadfixture.Write(filepath.Join(*dir, "state"), filepath.Join(*dir, "home", ".claude", "projects"),
		loadfixture.Options{Sessions: *sessions, Scale: *scale, Seed: *seed})
	if err != nil {
		log.Fatal(err)
	}
	var total int64
	for _, s := range ss {
		total += s.Size
	}
	fmt.Printf("%d sessions, %.1f MB of transcripts\n", len(ss), float64(total)/1e6)
}
