package catalogseed

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// recordingTx records the statements importTMDBRatingSources runs.
type recordingTx struct {
	pgx.Tx
	statements []string
}

func (tx *recordingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	tx.statements = append(tx.statements, strings.TrimSpace(sql))
	return pgconn.CommandTag{}, nil
}

func (tx *recordingTx) ran(prefix string) bool {
	for _, statement := range tx.statements {
		if strings.HasPrefix(statement, prefix) {
			return true
		}
	}
	return false
}

func TestImportTMDBRatingSourcesClearsOnlyWhenTheBundleCarriesThem(t *testing.T) {
	votes := int64(1200)
	items := []ItemRecord{
		{ContentID: "with-source", TMDBRating: &RatingSourceRecord{Score: 81, Votes: &votes, Provider: "silo.tmdb"}},
		{ContentID: "without-source"},
	}
	changed := map[string]bool{"with-source": true, "without-source": true}

	current := &recordingTx{}
	if err := importTMDBRatingSources(context.Background(), current, items, changed, true); err != nil {
		t.Fatalf("current bundle: %v", err)
	}
	if !current.ran("DELETE") || !current.ran("INSERT") {
		t.Fatalf("current bundle ran %q, want the stale-source delete and the upsert", current.statements)
	}

	// A bundle from an exporter that predates rating sources carries none,
	// which must not read as every overwritten item having lost its source.
	legacy := &recordingTx{}
	if err := importTMDBRatingSources(context.Background(), legacy, []ItemRecord{{ContentID: "without-source"}}, changed, false); err != nil {
		t.Fatalf("legacy bundle: %v", err)
	}
	if len(legacy.statements) != 0 {
		t.Fatalf("legacy bundle ran %q, want no statements", legacy.statements)
	}
}
