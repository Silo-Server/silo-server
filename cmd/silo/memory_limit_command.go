package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/Silo-Server/silo-server/internal/database"
)

const memoryLimitUsage = "usage: silo memory-limit"

// runMemoryLimitCommand prints, in bytes, the memory this container may use,
// detected as POSTGRES_TUNE does. The omnibus image sizes Meilisearch's
// indexing memory from it, so both services read memory one way.
func runMemoryLimitCommand(args []string, out io.Writer) error {
	if len(args) > 0 {
		return errors.New(memoryLimitUsage)
	}
	mem, err := database.DetectMemoryLimitBytes()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, mem)
	return err
}
