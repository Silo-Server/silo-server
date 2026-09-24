package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/themesongs"
)

type themeFileFixture struct {
	file   themesongs.File
	err    error
	filter catalog.AccessFilter
}

func (s *themeFileFixture) Resolve(_ context.Context, id string, _ bool, filter catalog.AccessFilter) (string, []themesongs.File, error) {
	s.filter = filter
	return id, []themesongs.File{s.file}, s.err
}

func TestThemeGrantRechecksCurrentAuthorityAndFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.mp3")
	if err := os.WriteFile(path, []byte("theme bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store := &themeFileFixture{file: themesongs.File{Song: themesongs.Song{ID: "7", Container: "mp3"}, OwnerPath: dir, Path: path, Size: info.Size(), Modified: info.ModTime().Truncate(time.Microsecond)}}
	svc := themesongs.NewService(store, "test secret")
	sessions := &socketSessionFixture{valid: true}
	users := &socketUserFixture{user: models.User{ID: 7, Enabled: true, AccessPolicyRevision: 2}}
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "profile", ProfileVerified: true, AllowedLibraryIDs: []int{3}, MaxContentRating: "PG-13", AllowUnratedContent: true}}
	h := &ThemeSongsHandler{Service: svc, Sessions: sessions, Users: users, Resolver: viewer}
	token, _, err := h.Mint(t.Context(), themesongs.Identity{UserID: 7, ProfileID: "profile", SessionID: "session", PolicyRevision: 2}, "movie", "7", catalog.AccessFilter{}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, f, err := h.OpenGrant(t.Context(), "movie", "7", token)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	cfg := streamtelemetry.DefaultConfig("theme-test")
	cfg.Enabled = true
	registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
	route := streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyNative, Method: http.MethodGet, Pattern: "/theme", Class: streamtelemetry.ClassTransfer, Role: streamtelemetry.RoleViewerEgress, Enrolled: true}
	observed := registry.Observe(route)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, opened, err := h.OpenGrant(r.Context(), "movie", "7", token)
		if err != nil {
			t.Fatal(err)
		}
		_ = opened.Close()
		w.WriteHeader(http.StatusOK)
	}))
	observed.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/theme", nil))
	snapshot := registry.Sweep()
	if len(snapshot.Transfers) != 1 || len(snapshot.Sessions) != 0 || snapshot.Transfers[0].Subject != streamtelemetry.UserSubject(7) || snapshot.Transfers[0].ProfileID != "profile" {
		t.Fatalf("theme transfer identity: %+v", snapshot)
	}
	if len(store.filter.AllowedLibraryIDs) != 1 || store.filter.AllowedLibraryIDs[0] != 3 {
		t.Fatal("current scope was not applied")
	}
	// The recheck must carry every scope field, including the server-wide
	// unrated-content decision, or an allowed unrated title mints a grant the
	// audio request then refuses.
	if store.filter.MaxContentRating != "PG-13" || !store.filter.AllowUnratedContent {
		t.Fatalf("content-rating scope was not applied: %+v", store.filter)
	}
	for _, tc := range []struct {
		name          string
		change, reset func()
	}{
		{"logout", func() { sessions.valid = false }, func() { sessions.valid = true }},
		{"disabled account", func() { users.user.Enabled = false }, func() { users.user.Enabled = true }},
		{"policy revision", func() { users.user.AccessPolicyRevision++ }, func() { users.user.AccessPolicyRevision-- }},
		{"deleted profile", func() { viewer.err = access.ErrProfileNotFound }, func() { viewer.err = nil }},
		{"revoked library", func() { store.err = catalog.ErrItemNotFound }, func() { store.err = nil }},
		{"replaced file", func() { store.file.Size++ }, func() { store.file.Size-- }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.change()
			defer tc.reset()
			_, f, err := h.OpenGrant(t.Context(), "movie", "7", token)
			if err == nil {
				if f != nil {
					_ = f.Close()
				}
				t.Fatal("revoked authority was accepted")
			}
		})
	}
	if _, _, err := h.OpenGrant(t.Context(), "other", "7", token); !errors.Is(err, themesongs.ErrGrant) {
		t.Fatal("wrong owner accepted", err)
	}
}
