// Command route-inventory builds the machine-readable inventory of every route
// the legacy native HTTP listeners register.
//
// The inventory is produced from registration source, not from one runtime
// wiring, so a route hidden behind optional dependency construction still
// appears. The generator fails rather than emitting a partial answer when it
// meets a registration it cannot account for.
//
// Usage (the repository root is the cwd):
//
//	go run ./cmd/route-inventory -out contracts/api/v2/route-inventory.json
//	go run ./cmd/route-inventory -check contracts/api/v2/route-inventory.json
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/routeinventory"
)

func main() {
	out := flag.String("out", "", "write the inventory to this path (repository-relative or absolute)")
	check := flag.String("check", "", "compare the generated inventory against this committed path and fail on any difference")
	root := flag.String("root", ".", "repository root to analyze")
	flag.Parse()

	if (*out == "") == (*check == "") {
		fmt.Fprintln(os.Stderr, "route-inventory: exactly one of -out or -check is required")
		os.Exit(2)
	}

	repoRoot, err := routeinventory.FindRepoRoot(*root)
	if err != nil {
		fail(err)
	}
	inventory, err := routeinventory.Analyze(routeinventory.DefaultConfig(repoRoot))
	if err != nil {
		fail(err)
	}
	encoded, err := inventory.MarshalIndented()
	if err != nil {
		fail(err)
	}

	if *out != "" {
		path := resolve(repoRoot, *out)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("route-inventory: wrote %d routes across %d listeners to %s\n",
			inventory.Totals.Routes, len(inventory.Listeners), *out)
		return
	}

	path := resolve(repoRoot, *check)
	committed, err := os.ReadFile(path)
	if err != nil {
		fail(err)
	}
	if !bytes.Equal(committed, encoded) {
		fmt.Fprintf(os.Stderr, "route-inventory: %s is stale; run `make route-inventory` and review the diff\n", *check)
		os.Exit(1)
	}
	fmt.Printf("route-inventory: %s is current (%d routes)\n", *check, inventory.Totals.Routes)
}

func resolve(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "route-inventory:", err)
	os.Exit(1)
}
