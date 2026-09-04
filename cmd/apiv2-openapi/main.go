// Command apiv2-openapi writes the native API v2 OpenAPI artifact from the Go
// registries alone. It opens no database, network or credential and reads
// nothing from the environment, so two runs on any machine produce the same
// bytes; make verify-apiv2-openapi relies on that.
//
// Usage:
//
//	apiv2-openapi -out contracts/api/v2/openapi.json
//	apiv2-openapi -check contracts/api/v2/openapi.json
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/Silo-Server/silo-server/internal/apiv2"
)

func main() {
	out := flag.String("out", "", "write the generated document to this path")
	check := flag.String("check", "", "exit 1 unless this file equals the generated document byte for byte")
	flag.Parse()
	if (*out == "") == (*check == "") {
		fmt.Fprintln(os.Stderr, "usage: apiv2-openapi (-out PATH | -check PATH)")
		os.Exit(2)
	}
	doc, err := apiv2.GenerateOpenAPI()
	if err != nil {
		fmt.Fprintln(os.Stderr, "apiv2-openapi:", err)
		os.Exit(1)
	}
	if *out != "" {
		if err := os.WriteFile(*out, doc, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "apiv2-openapi:", err)
			os.Exit(1)
		}
		return
	}
	committed, err := os.ReadFile(*check)
	if err != nil {
		fmt.Fprintln(os.Stderr, "apiv2-openapi:", err)
		os.Exit(1)
	}
	if !bytes.Equal(committed, doc) {
		fmt.Fprintf(os.Stderr, "apiv2-openapi: %s is stale; run make apiv2-openapi\n", *check)
		os.Exit(1)
	}
}
