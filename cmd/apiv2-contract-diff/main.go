// Command apiv2-contract-diff runs the native API v2 semantic diff policy:
// it compares the committed contracts/api/v2/openapi.json (revision) with the
// same file at the merge base (base) using the pinned oasdiff library, and
// fails on a breaking change that contracts/api/v2/breaking-approvals.json
// does not approve exactly. When contracts/api/v2/LOCKED exists the contract
// is post-lock and no approval applies.
//
// Usage:
//
//	apiv2-contract-diff -base BASE.json -revision contracts/api/v2/openapi.json -contracts contracts/api/v2
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Silo-Server/silo-server/internal/contractspec"
)

func main() {
	base := flag.String("base", "", "the OpenAPI document at the merge base (empty file or missing: nothing to compare)")
	revision := flag.String("revision", "contracts/api/v2/openapi.json", "the committed OpenAPI document")
	contracts := flag.String("contracts", "contracts/api/v2", "directory holding breaking-approvals.json and, post-lock, LOCKED")
	flag.Parse()
	if err := run(*base, *revision, *contracts); err != nil {
		fmt.Fprintln(os.Stderr, "apiv2-contract-diff:", err)
		if errors.Is(err, contractspec.ErrBreaking) {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

func run(basePath, revisionPath, contractsDir string) error {
	baseDoc, err := os.ReadFile(basePath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(baseDoc) == 0 {
		fmt.Println("apiv2-contract-diff: no base document; nothing to compare")
		return nil
	}
	revisionDoc, err := os.ReadFile(revisionPath)
	if err != nil {
		return err
	}
	approvals, err := contractspec.LoadApprovals(os.DirFS(contractsDir))
	if err != nil {
		return err
	}
	_, statErr := os.Stat(filepath.Join(contractsDir, contractspec.LockMarkerPath))
	locked := statErr == nil
	changes, err := contractspec.Diff(baseDoc, revisionDoc)
	if err != nil {
		return err
	}
	for _, c := range changes {
		fmt.Printf("%-4s %-6s %s [%s %s] fingerprint=%s: %s\n", c.Level, c.Method, c.Path, c.OperationID, c.ID, c.Fingerprint, c.Text)
	}
	if err := contractspec.Policy(changes, approvals, locked); err != nil {
		return err
	}
	fmt.Printf("apiv2-contract-diff: %d change(s), none breaking without approval (locked=%v)\n", len(changes), locked)
	return nil
}
