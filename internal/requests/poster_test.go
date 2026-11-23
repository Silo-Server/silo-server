package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// An admin reads a request's poster from the stored row alone, also while
// requests are turned off; anything but a TMDB image path names no poster.
func TestAdminRequestPosterURL(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = false
	store.requests["r1"] = &Request{ID: "r1", PosterPath: "/heat.JPG"}
	store.requests["r2"] = &Request{ID: "r2", PosterPath: "https://example.test/x.jpg"}
	svc := newTestService(store)
	admin := Viewer{UserID: 1, IsAdmin: true}

	if _, err := svc.AdminRequestPosterURL(context.Background(), testViewer(1), "r1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member: err = %v, want ErrForbidden", err)
	}
	if got, err := svc.AdminRequestPosterURL(context.Background(), admin, "r1"); err != nil || got != "https://image.tmdb.org/t/p/w500/heat.JPG" {
		t.Fatalf("poster = %q, %v", got, err)
	}
	if got, err := svc.AdminRequestPosterURL(context.Background(), admin, "r2"); err != nil || got != "" {
		t.Fatalf("other shape = %q, %v", got, err)
	}
	if _, err := svc.AdminRequestPosterURL(context.Background(), admin, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: err = %v, want ErrNotFound", err)
	}
}

// Art a client sends in another shape is replaced by TMDB's, never refused.
func TestCreateRequestReplacesPosterThatIsNotATMDBPath(t *testing.T) {
	store := newFakeStore()
	store.settings.RequestsEnabled = true
	svc := newTestServiceWithTMDB(store, &fakeTMDBClient{detail: &tmdb.MediaDetail{PosterPath: "/fight.jpg"}})
	input := fightClubInput
	input.PosterPath, input.BackdropPath = "//evil.test/x.jpg", "/ok.png"

	if _, err := svc.CreateRequest(context.Background(), testViewer(1), input); err != nil {
		t.Fatal(err)
	}
	if got := store.created[0].Input; got.PosterPath != "/fight.jpg" || got.BackdropPath != "/ok.png" {
		t.Fatalf("stored poster %q, backdrop %q", got.PosterPath, got.BackdropPath)
	}
}
