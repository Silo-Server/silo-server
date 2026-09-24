package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsmigrate"
	"github.com/Silo-Server/silo-server/migrations"
)

// retiredFallbackAccount is one account's legacy rows and stored canonical
// rows before the migration runs.
type retiredFallbackAccount struct {
	name     string
	legacy   map[string]string            // legacy user_settings key -> raw value
	profiles []string                     // every profile on the account
	stored   map[string]map[string]string // profile -> canonical key -> stored JSON
	// converts records whether settingsmigrate.PlanRetiredFallback produces a
	// row for the account's single legacy value, so the parity check cannot
	// pass by both sides writing nothing. Nil for multi-key accounts.
	converts *bool
}

// retiredFallbackAccounts covers every shape PlanRetiredFallback handles. The
// SQLite user store converts the same values in Go, so a shape the SQL gets
// wrong would make the two backends disagree about a profile's settings.
func retiredFallbackAccounts() []retiredFallbackAccount {
	idList := func(n int, extra ...int) string {
		parts := make([]string, 0, n+len(extra))
		for id := 1; id <= n; id++ {
			parts = append(parts, strconv.Itoa(id))
		}
		for _, id := range extra {
			parts = append(parts, strconv.Itoa(id))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	yes, no := true, false
	single := func(name, key, raw string, converts bool) retiredFallbackAccount {
		canonical := settingskeys.UiDisabledLibraryIds
		stored := `[9999]`
		if key == "next_up_mode" {
			canonical = settingskeys.UiNextUpMode
			stored = `"combined"`
		}
		c := &no
		if converts {
			c = &yes
		}
		return retiredFallbackAccount{
			name:     name,
			legacy:   map[string]string{key: raw},
			profiles: []string{"p-a", "p-b", "p-stored"},
			// A stored row always won over the fallback, so it wins here too.
			stored:   map[string]map[string]string{"p-stored": {canonical: stored}},
			converts: c,
		}
	}
	ids := func(name, raw string, converts bool) retiredFallbackAccount {
		return single("hidden libraries "+name, "disabled_library_ids", raw, converts)
	}
	mode := func(name, raw string, converts bool) retiredFallbackAccount {
		return single("next-up mode "+name, "next_up_mode", raw, converts)
	}

	return []retiredFallbackAccount{
		ids("valid", `[4,7]`, true),
		ids("with non-positive ids and repeats", `[0,4,4,-2,7]`, true),
		ids("with null entries", `[3,null,5]`, true),
		ids("with JSON whitespace", " \n[ 1 ,\t2 ]\r\n", true),
		ids("with negative zero", `[-0,3]`, true),
		ids("at the int64 maximum", `[9223372036854775807]`, true),
		ids("with the int64 minimum", `[-9223372036854775808,1]`, true),
		ids("at the 512-id limit", idList(512), true),
		ids("at the limit after dropping repeats", idList(512, 5), true),
		ids("empty", `[]`, false),
		ids("empty with whitespace", `[ ]`, false),
		ids("only nulls", `[ null ]`, false),
		ids("only non-positive ids", `[0,-1]`, false),
		ids("JSON null", `null`, false),
		ids("empty string", ``, false),
		ids("blank", "   ", false),
		ids("truncated", `[1,2`, false),
		ids("trailing comma", `[1,]`, false),
		ids("leading comma", `[,1]`, false),
		ids("missing comma", `[1 2]`, false),
		ids("trailing garbage", `[1]x`, false),
		ids("object", `{"ids":[1]}`, false),
		ids("number", `5`, false),
		ids("quoted array", `"[1]"`, false),
		ids("fraction", `[1.0,2]`, false),
		ids("exponent", `[1e2]`, false),
		ids("string id", `["1",2]`, false),
		ids("nested array", `[[1]]`, false),
		ids("boolean", `[true]`, false),
		ids("uppercase null", `[NULL]`, false),
		ids("leading zero", `[01]`, false),
		ids("above int64", `[9223372036854775808,1]`, false),
		ids("below int64", `[-9223372036854775809,1]`, false),
		ids("twenty digits", `[10000000000000000000,1]`, false),
		ids("byte order mark", "\ufeff[1]", false),
		ids("non-JSON whitespace", "\u00a0[1]", false),
		ids("over the 512-id limit", idList(513), false),

		mode("separate", "separate", true),
		mode("combined", "combined", true),
		mode("with ASCII whitespace", " separate\n", false),
		mode("with vertical tab, form feed, NEL and line separator", "\v\fseparate\u0085\u2028", false),
		mode("with Unicode spaces", "\u00a0\u1680\u2000combined\u200a\u202f\u205f\u3000", false),
		mode("empty", "", false),
		mode("blank", " \t\u0085 ", false),
		mode("unknown", "sideways", false),
		mode("wrong case", "Separate", false),
		mode("JSON-quoted", `"separate"`, false),
		mode("two words", "separate combined", false),
		mode("zero-width space", "\u200bseparate", false),
		mode("Mongolian vowel separator", "\u180eseparate", false),
		mode("byte order mark", "\ufeffseparate", false),

		{
			name: "both keys on a household",
			legacy: map[string]string{
				"disabled_library_ids": `[0,4,4,7]`,
				"next_up_mode":         "separate",
			},
			profiles: []string{"p-new", "p-ids-stored", "p-both-stored"},
			stored: map[string]map[string]string{
				"p-ids-stored": {settingskeys.UiDisabledLibraryIds: `[9]`},
				"p-both-stored": {
					settingskeys.UiDisabledLibraryIds: `[9]`,
					settingskeys.UiNextUpMode:         `"combined"`,
				},
			},
		},
		{
			name: "account without profiles",
			legacy: map[string]string{
				"disabled_library_ids": `[4]`,
				"next_up_mode":         "separate",
			},
		},
	}
}

// TestRetiredSettingsFallbacksMigrationMatchesPlanner applies the SQL
// migration over seeded legacy rows and checks that it writes exactly what
// settingsmigrate.PlanRetiredFallback plans, the conversion the SQLite user
// store runs for the same values.
func TestRetiredSettingsFallbacksMigrationMatchesPlanner(t *testing.T) {
	matches, err := filepath.Glob("../../migrations/sql/*_materialize_retired_settings_fallbacks.sql")
	if err != nil {
		t.Fatalf("find migration: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("migration matches = %v; want exactly one", matches)
	}
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := RunMigrations(ctx, pool, migrations.FS, "sql"); err != nil {
		t.Fatalf("initial migration: %v", err)
	}

	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("load settings contract: %v", err)
	}
	planner := settingsmigrate.New(contract, settingscontract.ObjectSchemas())

	normalize := func(t *testing.T, value string) string {
		t.Helper()
		var out string
		if err := pool.QueryRow(ctx, `SELECT $1::jsonb::text`, value).Scan(&out); err != nil {
			t.Fatalf("normalize %q: %v", value, err)
		}
		return out
	}

	accounts := retiredFallbackAccounts()
	userIDs := make([]int, len(accounts))
	legacyRows := 0
	for i, account := range accounts {
		name := fmt.Sprintf("retired-fallback-%d-%d", i, time.Now().UnixNano())
		if err := pool.QueryRow(ctx, `
INSERT INTO users (username, email, password_hash, role)
VALUES ($1, $2, 'x', 'user') RETURNING id`, name, name+"@example.com").Scan(&userIDs[i]); err != nil {
			t.Fatalf("seed user for %s: %v", account.name, err)
		}
		userID := userIDs[i]
		t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })
		for _, profileID := range account.profiles {
			if _, err := pool.Exec(ctx,
				`INSERT INTO user_profiles (id, user_id, name) VALUES ($1, $2, $1)`, profileID, userID); err != nil {
				t.Fatalf("seed profile %s for %s: %v", profileID, account.name, err)
			}
		}
		for profileID, rows := range account.stored {
			for key, value := range rows {
				if _, err := pool.Exec(ctx, `
INSERT INTO user_setting_values (user_id, key, scope, profile_id, value)
VALUES ($1, $2, 'profile', $3, $4::jsonb)`, userID, key, profileID, value); err != nil {
					t.Fatalf("store %s for %s on %s: %v", key, profileID, account.name, err)
				}
			}
		}
		for key, value := range account.legacy {
			if _, err := pool.Exec(ctx,
				`INSERT INTO user_settings (user_id, key, value) VALUES ($1, $2, $3)`, userID, key, value); err != nil {
				t.Fatalf("seed legacy %s for %s: %v", key, account.name, err)
			}
			legacyRows++
		}
	}

	// rows reads every canonical row an account has, keyed by identity.
	rows := func(t *testing.T, userID int) map[string]string {
		t.Helper()
		result, err := pool.Query(ctx, `
SELECT scope, COALESCE(profile_id, ''), key, value::text
  FROM user_setting_values WHERE user_id = $1`, userID)
		if err != nil {
			t.Fatalf("read canonical rows: %v", err)
		}
		defer result.Close()
		out := map[string]string{}
		for result.Next() {
			var scope, profileID, key, value string
			if err := result.Scan(&scope, &profileID, &key, &value); err != nil {
				t.Fatalf("scan canonical row: %v", err)
			}
			out[scope+"/"+profileID+"/"+key] = value
		}
		if err := result.Err(); err != nil {
			t.Fatalf("read canonical rows: %v", err)
		}
		return out
	}
	// snapshot captures every seeded account's rows with their revisions and
	// timestamps, so a re-run that rewrites a row in place still shows up.
	snapshot := func(t *testing.T) string {
		t.Helper()
		var out sql.NullString
		if err := pool.QueryRow(ctx, `
SELECT string_agg(user_id || ':' || scope || ':' || COALESCE(profile_id, '') || ':' || key || '=' || value::text
                  || '@' || revision || '@' || updated_at, ',' ORDER BY user_id, scope, profile_id, key)
  FROM user_setting_values WHERE user_id = ANY($1)`, userIDs).Scan(&out); err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		return out.String
	}

	applyMigrationForTest(ctx, t, pool, matches[0])

	for i, account := range accounts {
		t.Run(account.name, func(t *testing.T) {
			want := map[string]string{}
			for profileID, stored := range account.stored {
				for key, value := range stored {
					want["profile/"+profileID+"/"+key] = normalize(t, value)
				}
			}
			for legacyKey, raw := range account.legacy {
				planned, err := planner.PlanRetiredFallback(legacyKey, raw)
				if account.converts != nil {
					if converts := err == nil && len(planned) > 0; converts != *account.converts {
						t.Fatalf("PlanRetiredFallback(%q, %q) converts = %v (err %v), want %v",
							legacyKey, raw, converts, err, *account.converts)
					}
				}
				if err != nil {
					continue
				}
				for _, profileID := range account.profiles {
					for _, value := range planned {
						identity := "profile/" + profileID + "/" + value.Key
						if _, stored := want[identity]; !stored {
							want[identity] = normalize(t, string(value.Value))
						}
					}
				}
			}

			got := rows(t, userIDs[i])
			for identity, value := range want {
				if got[identity] != value {
					t.Errorf("%s = %q, want %q (planner)", identity, got[identity], value)
				}
			}
			for identity, value := range got {
				if _, ok := want[identity]; !ok {
					t.Errorf("%s = %q written, but the planner plans no row", identity, value)
				}
			}
		})
	}

	t.Run("legacy rows are kept", func(t *testing.T) {
		var count int
		if err := pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM user_settings WHERE user_id = ANY($1)`, userIDs).Scan(&count); err != nil {
			t.Fatalf("count legacy rows: %v", err)
		}
		if count != legacyRows {
			t.Errorf("legacy rows = %d, want all %d left in place", count, legacyRows)
		}
	})

	t.Run("a second run is a no-op", func(t *testing.T) {
		before := snapshot(t)
		applyMigrationForTest(ctx, t, pool, matches[0])
		if after := snapshot(t); after != before {
			t.Errorf("second run changed rows:\nbefore %s\nafter  %s", before, after)
		}
	})
}
