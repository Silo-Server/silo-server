package nodepool

import (
	"errors"
	"fmt"
	"strings"
)

// fakeRow exercises scanNode without a database. pgx.Row is a single Scan
// method, so a stored capability payload can be handed to the real row scanner
// by writing into the destination pointers pgx would have filled.
type fakeRow struct {
	capabilities []byte
}

func (r fakeRow) Scan(dest ...any) error {
	if want := strings.Count(nodeColumns, ",") + 1; len(dest) != want {
		return fmt.Errorf("scanNode passed %d destinations for %d columns in nodeColumns", len(dest), want)
	}
	// capabilities is the first of the two jsonb columns scanned as raw bytes;
	// last_stats is the second. Everything else keeps its zero value.
	for _, d := range dest {
		if raw, ok := d.(*[]byte); ok {
			*raw = r.capabilities
			return nil
		}
	}
	return errors.New("scanNode has no raw-bytes destination for capabilities")
}
