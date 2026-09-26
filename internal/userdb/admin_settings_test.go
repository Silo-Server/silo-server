package userdb

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestAdminSettingsPaginationCompleteIdentity(t *testing.T) {
	db := newSchemaDB(t)
	for _, profile := range []string{"a", "b", "c"} {
		id := userstore.SettingIdentity{Key: "playback.audio_language", Scope: settingscontract.ScopeProfile, ProfileID: profile}
		if _, err := UpsertSettingValue(db, id, json.RawMessage(`"en"`)); err != nil {
			t.Fatal(err)
		}
	}
	var after userstore.SettingIdentity
	for n, profile := range []string{"a", "b", "c"} {
		rows, more, err := listAdminSettingValuesPage(t.Context(), db, after, 1)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%+v %v", rows, err)
		}
		if rows[0].ProfileID != profile || more != (n < 2) {
			t.Fatalf("wrong page %+v %v", rows, more)
		}
		after = rows[0].SettingIdentity
	}
}
