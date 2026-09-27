// sqlite-bridge-preflight inventories an offline SQLite backup without importing it.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Silo-Server/silo-server/internal/userdb/bridgeimport"
)

func main() {
	source := flag.String("source", "", "explicit existing standalone offline account database")
	account := flag.Int64("account-id", 0, "existing central account ID matching the source filename")
	manifest := flag.Bool("manifest", false, "print the mapping manifest without opening a database")
	flag.Parse()
	if *manifest {
		if err := json.NewEncoder(os.Stdout).Encode(bridgeimport.Manifest()); err != nil {
			os.Exit(1)
		}
		return
	}
	report, err := bridgeimport.Inspect(context.Background(), *source, *account)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		os.Exit(1)
	}
	// Inventory success is not migration readiness.
	os.Exit(2)
}
