package userdb

import (
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// TestSectionOverrideUserAddedFieldsRoundTrip is the SQLite-side regression for
// the macroscope finding: the four user-added columns (is_user_added,
// user_section_type, user_config, user_title) must survive a Save/List cycle.
// Before the persistence-layer fix the INSERT and SELECT both ignored these
// columns, so the resolver later saw IsUserAdded=false on every load and
// dropped the section.
func TestSectionOverrideUserAddedFieldsRoundTrip(t *testing.T) {
	db := newSchemaDB(t)

	in := []userstore.SectionOverride{
		{
			SectionID:       "", // user-added: no admin section to override
			IsUserAdded:     true,
			UserSectionType: "hidden_gems",
			UserConfig:      `{"min_rating":7.5}`,
			UserTitle:       "Hidden Gems",
		},
		{
			// A normal admin-section customization should round-trip the legacy
			// fields and leave the user-added fields zero-valued.
			SectionID: "admin-1",
			Title:     "Renamed",
		},
	}

	if err := SaveSectionOverrides(db, "p1", "home", "", in); err != nil {
		t.Fatalf("SaveSectionOverrides: %v", err)
	}

	got, err := ListSectionOverrides(db, "p1", "home", "")
	if err != nil {
		t.Fatalf("ListSectionOverrides: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d overrides, want 2", len(got))
	}

	// The list is ordered by COALESCE(position, 999999); both are nil so the
	// order is implementation-defined. Find each by SectionID instead.
	var userAdded, customization *userstore.SectionOverride
	for i := range got {
		switch got[i].SectionID {
		case "":
			userAdded = &got[i]
		case "admin-1":
			customization = &got[i]
		}
	}

	if userAdded == nil {
		t.Fatal("did not find user-added override (SectionID='') in list")
	}
	if !userAdded.IsUserAdded {
		t.Error("user-added IsUserAdded = false, want true")
	}
	if userAdded.UserSectionType != "hidden_gems" {
		t.Errorf("user-added UserSectionType = %q, want hidden_gems", userAdded.UserSectionType)
	}
	if userAdded.UserConfig != `{"min_rating":7.5}` {
		t.Errorf("user-added UserConfig = %q, want %q", userAdded.UserConfig, `{"min_rating":7.5}`)
	}
	if userAdded.UserTitle != "Hidden Gems" {
		t.Errorf("user-added UserTitle = %q, want Hidden Gems", userAdded.UserTitle)
	}

	if customization == nil {
		t.Fatal("did not find admin-section customization in list")
	}
	if customization.IsUserAdded {
		t.Error("legacy customization IsUserAdded = true, want false")
	}
	if customization.UserSectionType != "" || customization.UserConfig != "" || customization.UserTitle != "" {
		t.Errorf("legacy customization leaked user-added fields: type=%q config=%q title=%q",
			customization.UserSectionType, customization.UserConfig, customization.UserTitle)
	}
}
