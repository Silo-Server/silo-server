package backup

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
)

// A live API key bound to a profile is carried only where its binding
// survives: into a schema that has the column and a restored profile for it.
// Carried without the binding it would act as the whole account.
func TestCarryDropsAProfileKeyItCannotBindDB(t *testing.T) {
	i := slices.IndexFunc(secretTables, func(s secretTable) bool { return s.name == tableAPIKeys })
	live := `[{"id": 1, "user_id": 1, "label": "account", "key_hash": "a", "key_prefix": "", "scopes": []},
		{"id": 2, "user_id": 1, "label": "kid", "key_hash": "b", "key_prefix": "", "scopes": ["watch:read"], "profile_id": "kid"},
		{"id": 3, "user_id": 1, "label": "gone", "key_hash": "c", "key_prefix": "", "scopes": ["watch:read"], "profile_id": "gone"}]`
	for name, tc := range map[string]struct {
		column string
		want   []string
	}{
		"backup from before keys could be bound": {want: []string{"account"}},
		"backup with the column":                 {column: ", profile_id text", want: []string{"account", "kid"}},
	} {
		t.Run(name, func(t *testing.T) {
			pool, dsn := testDB(t)
			ctx := t.Context()
			conn, err := pgx.Connect(ctx, createDatabase(t, pool, dsn))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			if _, err = tx.Exec(ctx, `
				CREATE TABLE api_keys (id bigint PRIMARY KEY, user_id integer NOT NULL, label text NOT NULL,
					key_hash text NOT NULL, key_prefix text NOT NULL DEFAULT '', scopes text[] NOT NULL DEFAULT '{}'`+tc.column+`);
				CREATE TABLE user_profiles (id text, user_id integer);
				CREATE TEMP TABLE carried_owners (id bigint);
				INSERT INTO user_profiles VALUES ('kid', 1);
				INSERT INTO carried_owners VALUES (1)`); err != nil {
				t.Fatal(err)
			}
			result := CarriedCredentials{Kept: map[string]int{}}
			if err = secretTables[i].carryInto(ctx, tx, live, &result); err != nil {
				t.Fatal(err)
			}
			rows, err := tx.Query(ctx, `SELECT label FROM api_keys ORDER BY id`)
			if err != nil {
				t.Fatal(err)
			}
			got, err := pgx.CollectRows(rows, pgx.RowTo[string])
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) || len(result.Dropped) != 3-len(tc.want) {
				t.Fatalf("carried %v, dropped %v", got, result.Dropped)
			}
		})
	}
}
