package bridgeimport

import (
	"database/sql"
	"net/url"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func assertImportedStoreReads(t *testing.T, pool *pgxpool.Pool, fixture importFixture) {
	t.Helper()
	uri := url.URL{Scheme: sourceFileScheme, Path: fixture.path, RawQuery: sourceReadOnlyQuery}
	source, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close() //nolint:errcheck
	sqlite := userdb.NewSQLiteUserStore(source)
	pg, err := pgstore.NewPostgresProvider(pool).ForUser(t.Context(), fixture.identity.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"parent", "child"} {
		a, err := sqlite.GetProfile(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		b, err := pg.GetProfile(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatal("profile semantics changed")
		}
	}
	a, err := sqlite.GetAudioPreference(t.Context(), "parent", fixture.item)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pg.GetAudioPreference(t.Context(), "parent", fixture.item)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) || b.AudioLanguage != "en" {
		t.Fatal("legacy track preference changed")
	}
	identity := userstore.SettingIdentity{Key: "playback.audio_language", Scope: settingscontract.ScopeProfile, ProfileID: "parent"}
	canonical, err := pg.GetSettingValue(t.Context(), identity)
	if err != nil || canonical == nil || string(canonical.Value) != `"fr"` || canonical.Revision != 8 {
		t.Fatal("canonical preference lost to legacy value")
	}
	history, err := pg.ListHistory(t.Context(), "parent", 20, 0)
	if err != nil || len(history) != 1 || history[0].ID != "history" {
		t.Fatal("history identity or visibility changed")
	}
	overrides, err := pg.ListSectionOverrides(t.Context(), "child", "home", "")
	if err != nil || len(overrides) != 1 || overrides[0].ID != "override" || !overrides[0].Removed {
		t.Fatal("section override semantics changed")
	}
	opaque, err := pg.GetSetting(t.Context(), "opaque")
	if err != nil || opaque != "synthetic opaque value" {
		t.Fatal("opaque setting changed")
	}
}
