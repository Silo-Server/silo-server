package access

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// backfillMigrationPath is the migration whose frozen CASE resolved
// content_rating_age for rows that already existed. It deliberately inlines its
// own copy of the ladder so applied history can never be rewritten by later Go
// code — which means nothing else checks that the copy ever agreed with
// Normalize. This test is that check, run once against the real statement.
const backfillMigrationPath = "../../migrations/sql/20260923234321_content_rating_age.sql"

// TestBackfillMigrationAgreesWithNormalize executes the migration's own backfill
// statement over one fixture row per rating string Normalize claims to know, and
// asserts each resolved age equals StoredRating for the same string.
//
// A transcription slip in the frozen CASE — a token in the Go ladder but not in
// the SQL, a country prefix the anchored regexes miss, whitespace trimmed
// differently — writes a wrong or NULL age permanently for existing rows. Since
// the statement cannot be corrected after it is applied, this is the only place
// the two copies are compared.
func TestBackfillMigrationAgreesWithNormalize(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	statement := readBackfillStatement(t)
	fixtures := backfillFixtureRatings()

	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Everything runs inside a rolled-back transaction: the statement updates
	// every media_items row with a rating, so it must not touch the shared test
	// database beyond its own fixtures.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	ids := make([]string, len(fixtures))
	for i, raw := range fixtures {
		ids[i] = fmt.Sprintf("backfill-agreement-%03d", i)
		if _, err := tx.Exec(ctx,
			`INSERT INTO media_items (content_id, type, title, content_rating, content_rating_age)
			 VALUES ($1, 'movie', 'Backfill agreement', $2, NULL)`,
			ids[i], raw,
		); err != nil {
			t.Fatalf("inserting fixture %q: %v", raw, err)
		}
	}

	if _, err := tx.Exec(ctx, statement); err != nil {
		t.Fatalf("executing the migration backfill statement: %v", err)
	}

	// The migration runs without a wrapping transaction, so a concurrent index
	// build failing after this statement leaves the whole file to be applied
	// again. Its IS DISTINCT FROM guard is what keeps the retry from rewriting
	// every already-correct row, and every index entry that row owns, a second
	// time: re-running it over settled data must touch nothing.
	tag, err := tx.Exec(ctx, statement)
	if err != nil {
		t.Fatalf("re-executing the migration backfill statement: %v", err)
	}
	if tag.RowsAffected() != 0 {
		t.Errorf("re-running the backfill rewrote %d rows, want 0", tag.RowsAffected())
	}

	rows, err := tx.Query(ctx,
		`SELECT content_id, content_rating_age FROM media_items WHERE content_id = ANY($1)`, ids)
	if err != nil {
		t.Fatalf("reading back resolved ages: %v", err)
	}
	stored := make(map[string]*int, len(ids))
	for rows.Next() {
		var id string
		var age *int
		if err := rows.Scan(&id, &age); err != nil {
			t.Fatalf("scanning resolved age: %v", err)
		}
		stored[id] = age
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating resolved ages: %v", err)
	}
	rows.Close()

	if len(stored) != len(fixtures) {
		t.Fatalf("read back %d rows, inserted %d", len(stored), len(fixtures))
	}
	for i, raw := range fixtures {
		want := StoredRating(raw)
		got, ok := stored[ids[i]]
		if !ok {
			t.Fatalf("fixture %q (%s) missing from the result set", raw, ids[i])
		}
		switch {
		case want == nil && got != nil:
			t.Errorf("content_rating %q: migration stored age %d, Go stores NULL", raw, *got)
		case want != nil && got == nil:
			t.Errorf("content_rating %q: migration stored NULL, Go stores age %d", raw, *want)
		case want != nil && *want != *got:
			t.Errorf("content_rating %q: migration stored age %d, Go stores age %d", raw, *got, *want)
		}
	}
}

// readBackfillStatement cuts the single UPDATE that resolves existing rows out
// of the migration, so the test executes the shipped text rather than a copy of
// it.
func readBackfillStatement(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(backfillMigrationPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "WITH raw AS (")
	if start < 0 {
		t.Fatalf("%s: no backfill CTE chain found", backfillMigrationPath)
	}
	update := strings.Index(text[start:], "UPDATE public.media_items mi")
	if update < 0 {
		t.Fatalf("%s: backfill CTE chain has no UPDATE", backfillMigrationPath)
	}
	end := strings.Index(text[start+update:], ";")
	if end < 0 {
		t.Fatalf("%s: backfill UPDATE is not terminated", backfillMigrationPath)
	}
	return text[start : start+update+end+1]
}

// backfillFixtureRatings lists every string the migration has to resolve the
// same way Normalize does: one per token in every ladder, the unrated markers,
// each token again under every country prefix that names its system, and the
// spellings and whitespace shapes providers actually write.
func backfillFixtureRatings() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(values ...string) {
		for _, value := range values {
			if _, dup := seen[value]; dup {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}

	for _, table := range ratingLadder {
		for token := range table.ages {
			add(token)
		}
	}
	for token := range unratedTokens {
		add(token)
	}
	for code, systems := range countrySystems {
		for _, system := range systems {
			for token := range systemIndex[system] {
				add(code+":"+token, strings.ToLower(code)+":"+token)
			}
		}
	}

	add(
		// Separator and case variants that canonicalize to a known token.
		"PG-13", "pg 13", "PG.13", "tv-ma", "TV-Y7-FV", "FSK 16", "MA 15+",
		"Tous publics", "Not Rated", "N/A", "Uc",
		// The "Rated …" forms, with and without a trailing reason.
		"Rated R", "Rated PG-13", "Rated R for strong language",
		"Rated NC-17 for explicit sexual content", "Unrated",
		// Bare ages, including the boundary and what lies past it.
		"15", "16+", "+16", "21", "22", "99", "1999",
		// Unrecognized, and the bare "MA" that must stay unrecognized because
		// it is as likely to mean US TV-MA (17) as Australian MA 15+ (15).
		"banana", "Contains mild peril", "MA", "US:MA", "XX:PG", "", "   ",
		// Leading whitespace other than a space: Go trims it, one-argument
		// BTRIM does not, so the migration has to name the character set.
		"\tDE:16", "\nGB:15", " DE:16 ", "\tAU:M", "\tRated R for strong language",
		"\u00a0FSK 16", "PG-13\t",
	)
	return out
}
