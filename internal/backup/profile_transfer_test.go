package backup

import (
	"errors"
	"strings"
	"testing"
)

// TestParseProfileExportRefusesHostileDocuments: a profile import reads an
// untrusted upload. It carries only the profile's own data, so a document
// holding account or access fields, a newer or foreign format, unknown
// fields or data, or one too large or too deep is refused before anything
// is written.
func TestParseProfileExportRefusesHostileDocuments(t *testing.T) {
	head := `"format":"silo-profile-export","version":1,"server_id":"s1","exported_at":"2026-01-01T00:00:00Z","profile_name":"Main"`
	valid := `{` + head + `,"data":{"ratings":{"ratings":[{"item":"m1","rating":4,"rated_at":"2026-01-01T00:00:00Z"}]}},"items":[]}`
	if _, err := ParseProfileExport([]byte(valid)); err != nil {
		t.Fatalf("a valid document: %v", err)
	}
	cases := []struct {
		name string
		doc  string
		want error
	}{
		{"a PIN hash", `{` + head + `,"pin_hash":"x","data":{},"items":[]}`, ErrProfileImportAccessFields},
		{"access fields in preferences", `{` + head + `,"data":{"preferences":{"profile":{"max_playback_quality":"","library_restrictions_enabled":false}}},"items":[]}`, ErrProfileImportAccessFields},
		{"a newer version", `{"format":"silo-profile-export","version":2,"data":{}}`, ErrProfileExportTooNew},
		{"another format", `{"format":"silo-restore-set","version":1}`, nil},
		{"unknown fields", `{` + head + `,"extra":true,"data":{},"items":[]}`, nil},
		{"unknown data", `{` + head + `,"data":{"passwords":{}},"items":[]}`, nil},
		{"unknown fields inside data", `{` + head + `,"data":{"ratings":{"ratings":[],"owner":1}},"items":[]}`, nil},
		{"too deep", `{` + head + `,"data":{"ratings":` + strings.Repeat("[", 64) + strings.Repeat("]", 64) + `},"items":[]}`, nil},
		{"too large", `{` + head + `,"profile_name":"` + strings.Repeat("x", MaxProfileImportBytes) + `"}`, ErrProfileImportTooLarge},
		{"repeated items", `{` + head + `,"data":{},"items":[{"content_id":"m1"},{"content_id":"m1"}]}`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseProfileExport([]byte(c.doc))
			if err == nil {
				t.Fatal("accepted")
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if c.want == nil {
				if _, ok := errors.AsType[*InvalidProfileExportError](err); !ok {
					t.Fatalf("err = %v, want an invalid document", err)
				}
			}
		})
	}
}

// Imports are admitted before they take a pool connection: one per
// account, and maxConcurrentImports on the server.
func TestProfileImportAdmission(t *testing.T) {
	transfer := NewProfileTransfer(nil, nil)
	first, err := transfer.admit(1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.admit(1); !errors.Is(err, ErrProfileImportBusy) {
		t.Fatalf("a second import for one account = %v, want busy", err)
	}
	second, err := transfer.admit(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transfer.admit(3); !errors.Is(err, ErrProfileImportBusy) {
		t.Fatalf("an import past the server's limit = %v, want busy", err)
	}
	first()
	third, err := transfer.admit(1)
	if err != nil {
		t.Fatalf("an import after the account's last ended = %v", err)
	}
	second()
	third()
}

// A restored password or PIN hash this server would not check at its own
// cost is replaced with one nobody knows, locking the account or profile
// instead of making every sign-in pay the document's cost.
func TestRestoredCredentialsAreLockedOutsideTheServersCost(t *testing.T) {
	hash := func(cost string) string { return "$2a$" + cost + "$" + strings.Repeat("a", 53) }
	doc := UserDocument{Account: AccountEntry{PasswordHash: hash("31")}, Profiles: []ProfileEntry{{PINHash: hash("04")}, {PINHash: "plain"}, {}}}
	if err := doc.lockCredentials(); err != nil {
		t.Fatal(err)
	}
	if doc.Account.PasswordHash == hash("31") || doc.Profiles[0].PINHash != hash("04") || doc.Profiles[1].PINHash == "plain" ||
		doc.Profiles[2].PINHash != "" || doc.lockedCredentials != 2 {
		t.Fatalf("locked %d: password %q, pins %q %q %q", doc.lockedCredentials, doc.Account.PasswordHash,
			doc.Profiles[0].PINHash, doc.Profiles[1].PINHash, doc.Profiles[2].PINHash)
	}
}
