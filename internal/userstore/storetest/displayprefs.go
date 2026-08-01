package storetest

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

// RunJellycompatDisplayPrefs runs the Jellyfin DisplayPreferences storage
// conformance tests. It is exposed separately from RunSuite so each backend
// pins this behavior alongside its own migration tests: the blobs are opaque
// Jellyfin client JSON and must round-trip byte-for-byte through the dedicated
// jellycompat_displayprefs table.
func RunJellycompatDisplayPrefs(t *testing.T, newStore func(t *testing.T) userstore.UserStore) {
	ctx := context.Background()

	// The prefs id Jellyfin clients use for their main settings document.
	const userSettingsID = "usersettings"

	t.Run("RoundTrip", func(t *testing.T) {
		store := newStore(t)

		// Unset reads as empty, not an error.
		if got, err := store.GetJellycompatDisplayPrefs(ctx, userSettingsID, "emby"); err != nil || got != "" {
			t.Fatalf("unset prefs = (%q, %v), want (\"\", nil)", got, err)
		}

		// The value round-trips verbatim, unusual spacing and key order intact.
		blob := `{"SortBy":"SortName",  "CustomPrefs":{"b":"2","a":"1"}}`
		if err := store.SetJellycompatDisplayPrefs(ctx, userSettingsID, "emby", blob); err != nil {
			t.Fatalf("SetJellycompatDisplayPrefs: %v", err)
		}
		if got, err := store.GetJellycompatDisplayPrefs(ctx, userSettingsID, "emby"); err != nil || got != blob {
			t.Fatalf("prefs = (%q, %v), want the stored blob byte-for-byte", got, err)
		}

		// A second write replaces the first.
		if err := store.SetJellycompatDisplayPrefs(ctx, userSettingsID, "emby", `{"SortBy":"DateCreated"}`); err != nil {
			t.Fatalf("overwrite: %v", err)
		}
		if got, _ := store.GetJellycompatDisplayPrefs(ctx, userSettingsID, "emby"); got != `{"SortBy":"DateCreated"}` {
			t.Fatalf("after overwrite got %q", got)
		}
	})

	t.Run("KeyedByIDAndClient", func(t *testing.T) {
		store := newStore(t)

		writes := map[[2]string]string{
			{userSettingsID, "emby"}:         `{"n":1}`,
			{userSettingsID, "jellyfin-web"}: `{"n":2}`,
			{"f137a2dd", "emby"}:             `{"n":3}`,
			// The empty client is a real identity: Jellyfin clients may omit
			// the query parameter entirely.
			{userSettingsID, ""}: `{"n":4}`,
		}
		for key, value := range writes {
			if err := store.SetJellycompatDisplayPrefs(ctx, key[0], key[1], value); err != nil {
				t.Fatalf("set %v: %v", key, err)
			}
		}
		for key, want := range writes {
			got, err := store.GetJellycompatDisplayPrefs(ctx, key[0], key[1])
			if err != nil || got != want {
				t.Errorf("prefs %v = (%q, %v), want %q", key, got, err, want)
			}
		}
	})
}
