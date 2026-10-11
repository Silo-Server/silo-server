package migrations

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestSearchNumberTokenInlinesPostgres pins normalize_search_number_token's
// output, which title_normalized and the search indexes store, and checks that
// PostgreSQL inlines it: a body PostgreSQL cannot inline plans a query for
// every token of every title write.
func TestSearchNumberTokenInlinesPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	connect := func(conforming string) *pgx.Conn {
		t.Helper()
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		config.RuntimeParams["standard_conforming_strings"] = conforming
		conn, err := pgx.ConnectConfig(t.Context(), config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close(context.Background()) })
		return conn
	}

	// Each session parses the body once and keeps it, so its string literals
	// must mean the same whichever way that session set
	// standard_conforming_strings first.
	for _, conforming := range []string{"on", "off"} {
		conn := connect(conforming)
		for input, want := range map[string]string{
			"Twentieth": "20",
			"ONE":       "1",
			"second":    "2",
			"twentyone": "twentyone",
			"101st":     "101",
			"21ST":      "21",
			"0th":       "0",
			"1stt":      "1stt",
			"a1st":      "a1st",
			"st":        "st",
			"":          "",
		} {
			var got string
			if err := conn.QueryRow(t.Context(), `SELECT public.normalize_search_number_token($1)`, input).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("standard_conforming_strings=%s: normalize_search_number_token(%q) = %q, want %q", conforming, input, got, want)
			}
		}
	}
	conn := connect("on")
	var null string
	if err := conn.QueryRow(t.Context(), `SELECT public.normalize_search_number_token(NULL)`).Scan(&null); err != nil || null != "" {
		t.Errorf("normalize_search_number_token(NULL) = %q, %v; want empty", null, err)
	}

	var plan strings.Builder
	// A column input, so the planner cannot fold the call into a constant.
	rows, err := conn.Query(t.Context(), `EXPLAIN (VERBOSE, COSTS OFF) SELECT public.normalize_search_number_token(n::text) FROM generate_series(1, 2) AS n`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.String(), "normalize_search_number_token") {
		t.Errorf("normalize_search_number_token is not inlined:\n%s", plan.String())
	}
}
