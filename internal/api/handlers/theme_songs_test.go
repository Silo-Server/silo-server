package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/nodepool"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
	"github.com/Silo-Server/silo-server/internal/themedelivery"
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
	viewer := &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "profile", ProfileVerified: true, AllowedLibraryIDs: []int{3}, MaturityLimits: access.MaturityLimits{MaxContentRating: "PG-13", AllowUnratedContent: true}}}
	h := &ThemeSongsHandler{Service: svc, Sessions: sessions, Users: users, Resolver: viewer}
	authorization, err := h.Authorize(t.Context(), themesongs.Identity{UserID: 7, ProfileID: "profile", SessionID: "session", PolicyRevision: 2}, "movie", "7", catalog.AccessFilter{}, nil, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	token := authorization.Grant
	if authorization.URL != "" || authorization.Delivery != themesongs.DeliveryOriginal || token == "" {
		t.Fatalf("unrouted authorization = %+v", authorization)
	}
	_, _, f, err := h.OpenGrant(t.Context(), "movie", "7", token)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	cfg := streamtelemetry.DefaultConfig("theme-test")
	cfg.Enabled = true
	registry := streamtelemetry.NewRegistry(cfg, streamtelemetry.NewLocalStore(), nil)
	route := streamtelemetry.MediaRoute{Family: streamtelemetry.FamilyNative, Method: http.MethodGet, Pattern: "/theme", Class: streamtelemetry.ClassTransfer, Role: streamtelemetry.RoleViewerEgress, Enrolled: true}
	observed := registry.Observe(route)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, opened, err := h.OpenGrant(r.Context(), "movie", "7", token)
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
			_, _, f, err := h.OpenGrant(t.Context(), "movie", "7", token)
			if err == nil {
				if f != nil {
					_ = f.Close()
				}
				t.Fatal("revoked authority was accepted")
			}
		})
	}
	if _, _, _, err := h.OpenGrant(t.Context(), "other", "7", token); !errors.Is(err, themesongs.ErrGrant) {
		t.Fatal("wrong owner accepted", err)
	}
}

type themeRoutePlanner struct{ proxy *nodepool.Node }

func (p themeRoutePlanner) PlanRoute(req nodepool.RouteRequest) nodepool.Plan {
	if req.NeedsTranscode || !req.NeedsProxy || (req.ProxyEligible != nil && !req.ProxyEligible(p.proxy)) {
		return nodepool.Plan{}
	}
	return nodepool.Plan{ProxyNode: p.proxy}
}

func TestThemeAuthorizeRoutesWithoutLocalFileAndGrantsConversionLocally(t *testing.T) {
	// The file does not exist on this API node: a proxy route must not need it.
	missing := themesongs.File{Song: themesongs.Song{ID: "7", Container: "ogg"}, AudioCodec: "vorbis", AudioChannels: 2, OwnerPath: "/nonexistent", Path: "/nonexistent/theme.ogg", Size: 10, Modified: time.Unix(1_700_000_000, 0)}
	store := &themeFileFixture{file: missing}
	raw, err := json.Marshal(playback.HWAccelInfo{TransportFeatures: []string{playback.TransportFeatureThemeAudioEgressV1}})
	if err != nil {
		t.Fatal(err)
	}
	proxy := &nodepool.Node{ID: 3, URL: "http://proxy-a", Capabilities: raw}
	h := &ThemeSongsHandler{Service: themesongs.NewService(store, "test secret"), Router: &themedelivery.Router{
		Planner: themeRoutePlanner{proxy: proxy}, Secret: func() string { return "stream secret" },
	}}
	identity := themesongs.Identity{UserID: 7, ProfileID: "profile", SessionID: "session", PolicyRevision: 2}
	authorization, err := h.Authorize(t.Context(), identity, "movie", "7", catalog.AccessFilter{}, nil, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(authorization.URL, "http://proxy-a/stream/theme/") || authorization.Grant != "" || authorization.ContentType != "audio/ogg" {
		t.Fatalf("routed authorization = %+v", authorization)
	}
	if _, err := h.Authorize(t.Context(), identity, "movie", "7", catalog.AccessFilter{}, []themesongs.Format{{Container: "mp3"}}, time.Now().Add(time.Minute)); !errors.Is(err, themesongs.ErrNotAcceptable) {
		t.Fatalf("unplayable theme: %v", err)
	}

	// A conversion nothing in this deployment can run is not offered.
	if _, err := h.Authorize(t.Context(), identity, "movie", "7", catalog.AccessFilter{}, []themesongs.Format{{Container: "m4a", AudioCodec: "aac"}}, time.Now().Add(time.Minute)); !errors.Is(err, themesongs.ErrNotAcceptable) {
		t.Fatalf("unconvertible theme: %v", err)
	}

	// With no proxy able to convert and a local AAC recipe, the conversion is
	// served here under a converted grant.
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.ogg")
	if err := os.WriteFile(path, []byte("ogg bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store.file = themesongs.File{Song: themesongs.Song{ID: "7", Container: "ogg"}, AudioCodec: "vorbis", AudioChannels: 2, OwnerPath: dir, Path: path, Size: info.Size(), Modified: info.ModTime().Truncate(time.Microsecond)}
	h.Router.LocalConversion = func(context.Context) bool { return true }
	h.Sessions = &socketSessionFixture{valid: true}
	h.Users = &socketUserFixture{user: models.User{ID: 7, Enabled: true, AccessPolicyRevision: 2}}
	h.Resolver = &socketViewerFixture{scope: access.Scope{UserID: 7, ProfileID: "profile", ProfileVerified: true}}
	authorization, err = h.Authorize(t.Context(), identity, "movie", "7", catalog.AccessFilter{}, []themesongs.Format{{Container: "m4a", AudioCodec: "aac"}}, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if authorization.URL != "" || authorization.Grant == "" || authorization.Delivery != themesongs.DeliveryConverted || authorization.ContentType != "audio/mp4" {
		t.Fatalf("local conversion authorization = %+v", authorization)
	}
	file, delivery, f, err := h.OpenGrant(t.Context(), "movie", "7", authorization.Grant)
	if err != nil || delivery != themesongs.DeliveryConverted || f != nil || file.Path != path {
		t.Fatalf("converted grant = %+v %q %v %v", file, delivery, f, err)
	}
	rec := httptest.NewRecorder()
	h.ServeConverted(rec, httptest.NewRequest(http.MethodHead, "/theme", nil), file)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != themesongs.ConvertedContentType {
		t.Fatalf("converted HEAD = %d %v", rec.Code, rec.Header())
	}
}
