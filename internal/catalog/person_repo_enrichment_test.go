package catalog

import (
	"fmt"
	"strings"
	"testing"
)

// The generated SQL is asserted clause by clause because the enrichment rule
// only exists as SQL: there is no Go decision to unit test. Matching whole
// generated clauses (not loose fragments) is what makes a mis-wired column —
// the photo_source_path assignment gated on photo_thumbhash, say — fail here.
// Behavior is covered by TestPersonCreditEnrichmentPhotoRules, which needs a
// Postgres instance and therefore does not run in the default gate.
func TestPersonPhotoEnrichmentSQLGuardsCachedArtwork(t *testing.T) {
	single := personPhotoReplacePredicate("photo_path", "$1")
	for _, fragment := range []string{
		"COALESCE(photo_path, '') = '' AND $1 <> ''",
		"photo_path = '-' OR photo_path LIKE '%://%'",
		"$1 NOT IN ('', '-')",
		"$1 IS DISTINCT FROM photo_path",
	} {
		if !strings.Contains(single, fragment) {
			t.Fatalf("photo replace predicate %q is missing %q", single, fragment)
		}
	}

	batch := buildBatchPersonEnrichmentQuery()
	gate := personPhotoReplacePredicate("people.photo_path", "t.photo_path")
	if !strings.Contains(batch, gate) {
		t.Fatalf("batch enrichment SQL does not gate on the photo replace predicate %q", gate)
	}
	// Every photo column moves under the same photo_path decision, so the
	// served path can never be bound to the previous image's source or hash.
	for _, column := range []string{"photo_path", "photo_source_path", "photo_thumbhash"} {
		clause := fmt.Sprintf("%s = CASE WHEN %s THEN t.%s ELSE people.%s END", column, gate, column, column)
		if !strings.Contains(batch, clause) {
			t.Fatalf("batch enrichment SQL is missing the guarded assignment %q", clause)
		}
		destructive := fmt.Sprintf("WHEN t.%s NOT IN ('', '-') THEN t.%s", column, column)
		if strings.Contains(batch, destructive) {
			t.Fatalf("batch enrichment SQL still unconditionally overwrites %s", column)
		}
	}
	if !strings.Contains(batch, "WHERE people.id = t.id") || !strings.Contains(batch, "updated_at = NOW()") {
		t.Fatal("batch enrichment SQL lost its guarded update or timestamp assignment")
	}
}
