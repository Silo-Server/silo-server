package pgstore

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestAdminSettingsPaginationAccountIsolation(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	if _, err = p.Exec(t.Context(), `CREATE TEMP TABLE user_setting_values (LIKE public.user_setting_values INCLUDING ALL)`); err != nil {
		t.Fatal(err)
	}
	store := newStore(p, 100)
	other := newStore(p, 101)
	for _, profile := range []string{"a", "b", "c"} {
		id := userstore.SettingIdentity{Key: "playback.audio_language", Scope: settingscontract.ScopeProfile, ProfileID: profile}
		if _, err = store.UpsertSettingValue(t.Context(), id, json.RawMessage(`"en"`)); err != nil {
			t.Fatal(err)
		}
		if _, err = other.UpsertSettingValue(t.Context(), id, json.RawMessage(`"fr"`)); err != nil {
			t.Fatal(err)
		}
	}
	var after userstore.SettingIdentity
	for n, profile := range []string{"a", "b", "c"} {
		rows, more, err := store.ListAdminSettingValuesPage(t.Context(), after, 1)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%+v %v", rows, err)
		}
		if rows[0].ProfileID != profile || string(rows[0].Value) != `"en"` || more != (n < 2) {
			t.Fatalf("wrong page %+v %v", rows, more)
		}
		after = rows[0].SettingIdentity
	}
}
