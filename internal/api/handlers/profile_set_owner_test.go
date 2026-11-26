package handlers

import (
	"encoding/json"
	"testing"

	"github.com/Silo-Server/silo-server/internal/profileedits"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// An override's token is bound to its account, profile and field: two
// overrides that differ only there, with the same set value, the same
// answer to whether a clear applies, and the same edit revision, get
// different tokens, so an etag shown for one never clears another. It
// catches any of the three left out of the hash, which the database test
// can't see because its revisions differ anyway.
func TestOverrideTokenBindsAccountProfileAndField(t *testing.T) {
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatal(err)
	}
	applied, err := json.Marshal(profileSetApplied{Name: "Kid", PIN: profileSetPINKeep})
	if err != nil {
		t.Fatal(err)
	}
	const syncID = "0f8fad5b-d9cb-469f-a165-70867728950e"
	row := func(profileID string) *userstore.ProfileSetRow {
		r := &userstore.ProfileSetRow{SyncApplied: applied}
		r.ID, r.SyncID = profileID, syncID
		return r
	}
	// The same revision on every field, so only the inputs under test differ.
	var edits []profileedits.Edit
	for _, f := range []string{profileedits.FieldIsChild, profileedits.FieldRequireAdvisoryAge, profileedits.FieldRequestsBlocked} {
		edits = append(edits, profileedits.Edit{SyncID: syncID, Field: f, By: profileedits.ByOwner, Revision: 5})
	}
	token := func(account int, profileID, field string) string {
		t.Helper()
		tok, err := overrideToken(contract, account, row(profileID), edits, field)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	base := token(7, "p-kid", profileedits.FieldIsChild)
	if again := token(7, "p-kid", profileedits.FieldIsChild); again != base {
		t.Fatalf("the same override gave two tokens: %s, %s", base, again)
	}
	for name, other := range map[string]string{
		"another account":                       token(8, "p-kid", profileedits.FieldIsChild),
		"another profile of the account":        token(7, "p-other", profileedits.FieldIsChild),
		"another field with the same set value": token(7, "p-kid", profileedits.FieldRequireAdvisoryAge),
		"a third field with the same set value": token(7, "p-kid", profileedits.FieldRequestsBlocked),
	} {
		if other == base {
			t.Errorf("%s has the same token as the first override", name)
		}
	}
}
