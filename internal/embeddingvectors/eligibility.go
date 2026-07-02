package embeddingvectors

import (
	"fmt"
	"strings"
)

// ItemEligibilityWhereClause returns the SQL predicate that selects the
// media items eligible for semantic embedding: matched items, plus audiobooks
// and ebooks (which are embeddable regardless of metadata match status).
//
// This is the single source of truth for embed-eligibility. Both the
// recommendations queries and the catalog coverage counts delegate here so the
// vector "coverage" denominator (embed-eligible items) and the populations
// recommendations operate over stay in lockstep. The empty/blank alias falls
// back to the bare media_items table name.
//
// The emitted string must stay byte-identical to the historical
// recommendations predicate; see eligibility_test.go.
func ItemEligibilityWhereClause(alias string) string {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		alias = "media_items"
	}
	return fmt.Sprintf("(%s.status = 'matched' OR %s.type = 'audiobook' OR %s.type = 'ebook')", alias, alias, alias)
}
