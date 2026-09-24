package sections

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func newNextUpModeTestStore(t *testing.T) userstore.UserStore {
	t.Helper()

	dsn := "file:" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if err := userdb.InitSchema(db); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	store := userdb.NewSQLiteUserStore(db)
	if err := store.CreateProfile(context.Background(), userstore.Profile{ID: "profile-1", Name: "Main"}); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	return store
}

func upsertNextUpMode(t *testing.T, store userstore.UserStore, profileID, mode string) {
	t.Helper()
	if _, err := store.UpsertSettingValue(context.Background(), userstore.SettingIdentity{
		Key:       settingskeys.UiNextUpMode,
		Scope:     settingscontract.ScopeProfile,
		ProfileID: profileID,
	}, json.RawMessage(`"`+mode+`"`)); err != nil {
		t.Fatalf("upsert canonical next-up mode: %v", err)
	}
}

// TestNextUpModeCanonicalRowWins replays the cutover bug: the web writes the
// profile-scoped canonical ui.next_up_mode row, and the sections read must see
// it even while a stale legacy account value disagrees.
func TestNextUpModeCanonicalRowWins(t *testing.T) {
	ctx := context.Background()
	store := newNextUpModeTestStore(t)

	if err := store.SetSetting(ctx, "next_up_mode", "combined"); err != nil {
		t.Fatalf("seed legacy setting: %v", err)
	}
	upsertNextUpMode(t, store, "profile-1", NextUpModeSeparate)

	if got := NextUpMode(ctx, store, "profile-1"); got != NextUpModeSeparate {
		t.Errorf("NextUpMode = %q, want canonical %q", got, NextUpModeSeparate)
	}
}

// TestNextUpModeIgnoresLegacySetting: the retired-fallback migration copied
// the legacy account value onto every profile that existed, so the read no
// longer consults it. A profile without a canonical row reads the default.
func TestNextUpModeIgnoresLegacySetting(t *testing.T) {
	ctx := context.Background()
	store := newNextUpModeTestStore(t)

	if err := store.SetSetting(ctx, "next_up_mode", "separate"); err != nil {
		t.Fatalf("seed legacy setting: %v", err)
	}

	if got := NextUpMode(ctx, store, "profile-1"); got != NextUpModeCombined {
		t.Errorf("NextUpMode = %q, want default %q; the legacy key is retired", got, NextUpModeCombined)
	}
}

// TestNextUpModeDefaultsToCombined pins the historical meaning of absence.
func TestNextUpModeDefaultsToCombined(t *testing.T) {
	store := newNextUpModeTestStore(t)

	if got := NextUpMode(context.Background(), store, "profile-1"); got != NextUpModeCombined {
		t.Errorf("NextUpMode = %q, want default %q", got, NextUpModeCombined)
	}
}

// TestNextUpModeProfileIsolation: one profile's canonical mode must not leak
// into another profile on the same account.
func TestNextUpModeProfileIsolation(t *testing.T) {
	ctx := context.Background()
	store := newNextUpModeTestStore(t)
	if err := store.CreateProfile(ctx, userstore.Profile{ID: "profile-2", Name: "Kids"}); err != nil {
		t.Fatalf("create second profile: %v", err)
	}

	upsertNextUpMode(t, store, "profile-1", NextUpModeSeparate)

	if got := NextUpMode(ctx, store, "profile-2"); got != NextUpModeCombined {
		t.Errorf("NextUpMode for the other profile = %q, want %q", got, NextUpModeCombined)
	}
}

// failingStore fails every settings read. A read would degrade NextUpMode to
// combined, so getting "separate" back proves the store was never asked.
type failingStore struct{ userstore.UserStore }

func (failingStore) ListSettingValuesForResolution(context.Context, userstore.SettingResolutionQuery) ([]userstore.SettingValue, error) {
	return nil, errors.New("store read")
}

// TestNextUpModeReadsTheRequestScope: a request that passed viewer access
// carries the resolved mode, so section fetchers asking again cost no read.
func TestNextUpModeReadsTheRequestScope(t *testing.T) {
	ctx := access.SetScope(context.Background(), access.Scope{
		UserID: 1, ProfileID: "profile-1", NextUpMode: NextUpModeSeparate,
	})
	if got := NextUpMode(ctx, failingStore{}, "profile-1"); got != NextUpModeSeparate {
		t.Errorf("NextUpMode = %q, want the scope's %q", got, NextUpModeSeparate)
	}
}

// TestNextUpModeScopeForAnotherProfileIsNotUsed: the scope answers only for
// the profile it was resolved for.
func TestNextUpModeScopeForAnotherProfileIsNotUsed(t *testing.T) {
	store := newNextUpModeTestStore(t)
	ctx := access.SetScope(context.Background(), access.Scope{
		UserID: 1, ProfileID: "profile-2", NextUpMode: NextUpModeSeparate,
	})
	if got := NextUpMode(ctx, store, "profile-1"); got != NextUpModeCombined {
		t.Errorf("NextUpMode = %q, want profile-1's stored default %q", got, NextUpModeCombined)
	}
}
