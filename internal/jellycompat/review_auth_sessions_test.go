package jellycompat

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/google/uuid"
)

func TestPlaybackGrantAcceptsEquivalentUUIDs(t *testing.T) {
	itemID, sourceID := uuid.NewString(), uuid.NewString()
	compact := func(id string) string { return strings.ToUpper(strings.ReplaceAll(id, "-", "")) }
	for _, path := range []string{
		"/Videos/" + compact(itemID) + "/stream?PlaySessionId=grant&MediaSourceId=" + compact(sourceID),
		"/Videos/" + compact(itemID) + "/" + compact(sourceID) + "/Subtitles/2/stream.vtt?PlaySessionId=grant",
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			sessions, store := seedDirectPlaySession(t, fixedNow)
			store.Put(PlaybackSession{ID: "grant", CompatToken: "compat-tok", RouteItemID: itemID, MediaSources: []PlaybackMediaSource{{ID: sourceID}}})
			var reached bool
			rec := httptest.NewRecorder()
			directPlayRouter(t, sessions, store, nil, &reached).ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			if rec.Code != http.StatusOK || !reached {
				t.Fatalf("%s %s: status=%d %s", method, path, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestPersonImageResolvesAndRevalidatesAdminKey(t *testing.T) {
	keyAuth, _, validator := adminKeyAuthWithPrimary(t, time.Now)
	codec := NewResourceIDCodec()
	routeID := codec.EncodeIntID(EncodedIDPerson, 287)
	detail := &catalog.DetailService{}
	detail.SetImageResolver(&recordingImageResolver{})
	h := &ImagesHandler{codec: codec, keyAuth: keyAuth, images: NewImageCache(time.Hour, time.Now),
		personRepo: fakeImagePersonRepo{person: &models.Person{ID: 287, PhotoPath: "tmdb/people/287/profile/original.abc123.webp"}}, detailSvc: detail,
		accessFilter: func(_ context.Context, userID int, profileID string) catalog.AccessFilter {
			if userID != 2 || profileID != "p1" {
				t.Fatalf("unexpected key scope: %d/%s", userID, profileID)
			}
			return catalog.AccessFilter{}
		}}
	request := func() *httptest.ResponseRecorder {
		req := withImageRouteParams(httptest.NewRequest(http.MethodGet, "/Items/"+routeID+"/Images/Primary?api_key=sa_test", nil), routeID, "Primary")
		rec := httptest.NewRecorder()
		h.HandleItemImage(rec, req)
		return rec
	}
	if rec := request(); rec.Code != http.StatusFound {
		t.Fatalf("admin key image=%d %s", rec.Code, rec.Body.String())
	}
	validator.key = nil
	if rec := request(); rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
		t.Fatalf("revoked key used warmed cache: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

type unavailableSessionContent struct{ ContentService }

func (unavailableSessionContent) GetItemDetail(context.Context, *Session, string, *int) (*upstreamItemDetail, error) {
	return nil, errors.New("catalog unavailable")
}

type pingTestStore interface {
	CompatPlaybackStore
	TouchActiveForToken(context.Context, string, string) error
}

func assertPingPreservesAbsoluteLifetime(t *testing.T, store pingTestStore, now *time.Time) {
	t.Helper()
	id := uuid.NewString()
	store.Put(PlaybackSession{ID: id, CompatToken: id, UpstreamSessionID: "native"})
	t.Cleanup(func() { store.Delete(id) })
	before, ok := store.Get(id)
	if !ok {
		t.Fatal("session not stored")
	}
	*now = now.Add(55 * time.Minute)
	if err := store.TouchActiveForToken(t.Context(), id, id); err != nil {
		t.Fatal(err)
	}
	after, ok := store.Get(id)
	if !ok || !after.UpdatedAt.Equal(*now) || !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("ping must refresh activity without extending grant: %+v", after)
	}
	*now = before.ExpiresAt
	if err := store.TouchActiveForToken(t.Context(), id, id); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("ping revived expired grant: %v", err)
	}
	if _, ok := store.Get(id); ok {
		t.Fatal("expired grant remains usable")
	}
}

func TestSessionPingPreservesAbsoluteLifetime(t *testing.T) {
	now := time.Now().UTC()
	assertPingPreservesAbsoluteLifetime(t, NewPlaybackSessionStore(time.Hour, func() time.Time { return now }), &now)
}
