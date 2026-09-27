package jellycompat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/jackc/pgx/v5"
)

type personMapRepo map[int64]*models.Person

func (r personMapRepo) Get(_ context.Context, id int64) (*models.Person, error) {
	if p, ok := r[id]; ok {
		return p, nil
	}
	return nil, pgx.ErrNoRows
}

// EnsureAccessible models a restricted-library credit: only an unrestricted
// filter sees the person.
func (r personMapRepo) EnsureAccessible(_ context.Context, id int64, filter catalog.AccessFilter) error {
	if _, ok := r[id]; !ok || filter.AllowedLibraryIDs != nil {
		return pgx.ErrNoRows
	}
	return nil
}

// TestJellyfinWebPersonPhotoUsesSignedTag reproduces Jellyfin Web's cast card
// request: an anonymous <img> GET carrying only the PrimaryImageTag from the
// item detail response.
func TestJellyfinWebPersonPhotoUsesSignedTag(t *testing.T) {
	const secret = "image-secret"
	codec := NewResourceIDCodec()
	routeID := codec.EncodeIntID(EncodedIDPerson, 287)
	otherRouteID := codec.EncodeIntID(EncodedIDPerson, 288)
	people := personMapRepo{
		287: {ID: 287, Name: "Brad Pitt", PhotoPath: "tmdb/people/287/profile/original.abc123.webp", PhotoThumbhash: "thumb-287"},
		288: {ID: 288, Name: "Edward Norton", PhotoPath: "tmdb/people/288/profile/original.def456.webp", PhotoThumbhash: "thumb-288"},
	}

	m := newMapper(codec, &config.Config{Auth: config.AuthConfig{JWTSecret: secret}})
	detail := m.itemFromDetail(upstreamItemDetail{
		ContentID: "movie-1", Type: "movie", Title: "Fight Club",
		Cast: []catalog.CastCredit{
			{Name: "Brad Pitt", PersonID: "287", PhotoURL: "https://cdn.example.test/287.webp?sig=one", PhotoThumbhash: "thumb-287", PhotoPath: "tmdb/people/287/profile/original.abc123.webp"},
			{Name: "No Photo", PersonID: "289"},
		},
		Crew: []catalog.CrewCredit{
			{Name: "Edward Norton", Job: "Producer", PersonID: "288", PhotoURL: "https://cdn.example.test/288.webp?sig=one", PhotoThumbhash: "thumb-288", PhotoPath: "tmdb/people/288/profile/original.def456.webp"},
		},
	}, false, nil)
	if len(detail.People) != 3 {
		t.Fatalf("people = %+v", detail.People)
	}
	castTag, noPhotoTag, crewTag := detail.People[0].PrimaryImageTag, detail.People[1].PrimaryImageTag, detail.People[2].PrimaryImageTag
	if castTag == "" || crewTag == "" || noPhotoTag != "" {
		t.Fatalf("tags cast=%q crew=%q noPhoto=%q", castTag, crewTag, noPhotoTag)
	}
	persons := &PersonsHandler{codec: codec, imageTags: newImageTagSigner(secret)}
	if got := persons.personToDTO(*people[287]).ImageTags["Primary"]; got != castTag {
		t.Fatalf("/Persons tag = %q, want the item detail tag %q", got, castTag)
	}

	resolver := &recordingImageResolver{}
	detailSvc := &catalog.DetailService{}
	detailSvc.SetImageResolver(resolver)
	h := &ImagesHandler{
		codec:      codec,
		images:     NewImageCache(time.Hour, time.Now),
		personRepo: people,
		detailSvc:  detailSvc,
		imageTags:  newImageTagSigner(secret),
		accessFilter: func(_ context.Context, _ int, profileID string) catalog.AccessFilter {
			if profileID == "visible" {
				return catalog.AccessFilter{}
			}
			return catalog.AccessFilter{AllowedLibraryIDs: []int{}}
		},
	}
	request := func(route, tag, profile string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/Items/"+route+"/Images/Primary?fillHeight=446&fillWidth=298&quality=96&tag="+tag, nil)
		req = withImageRouteParams(req, route, "Primary")
		if profile != "" {
			req = req.WithContext(context.WithValue(req.Context(), compatSessionKey, &Session{StreamAppUserID: 7, ProfileID: profile}))
		}
		rec := httptest.NewRecorder()
		h.HandleItemImage(rec, req)
		return rec
	}

	assertImageRedirect(t, request(routeID, castTag, ""), "https://cdn.example.test/tmdb/people/287/profile/w300.abc123.webp")
	assertImageRedirect(t, request(otherRouteID, crewTag, ""), "https://cdn.example.test/tmdb/people/288/profile/w300.def456.webp")
	// A tag authorizes by itself, matching the item and collection image routes.
	assertImageRedirect(t, request(routeID, castTag, "hidden"), "https://cdn.example.test/tmdb/people/287/profile/w300.abc123.webp")
	if _, ok := h.images.LookupSized(personImageCacheRouteID(routeID, people[287].PhotoPath), "Primary", "", compatCardImageSize); !ok {
		t.Fatal("signed request did not warm the shared cache")
	}

	// The cache is now warm. Unsigned tags, tags for another person, and tags
	// for a replaced photo must still not reach it.
	legacyTag := tagValue("https://cdn.example.test/287.webp?sig=one")
	for name, tc := range map[string]struct{ route, tag, profile string }{
		"anonymous without tag":      {routeID, "", ""},
		"anonymous legacy tag":       {routeID, legacyTag, ""},
		"restricted legacy tag":      {routeID, legacyTag, "hidden"},
		"restricted without tag":     {routeID, "", "hidden"},
		"tag from another person":    {routeID, crewTag, ""},
		"tag replayed on other item": {codec.EncodeStringID(EncodedIDItem, "movie-1"), castTag, ""},
	} {
		if rec := request(tc.route, tc.tag, tc.profile); rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
			t.Fatalf("%s: status=%d location=%q", name, rec.Code, rec.Header().Get("Location"))
		}
	}
	if rec := request(routeID, "", "visible"); rec.Code != http.StatusFound {
		t.Fatalf("visible session without tag = %d", rec.Code)
	}

	people[287] = &models.Person{ID: 287, PhotoPath: "tmdb/people/287/profile/original.new789.webp", PhotoThumbhash: "thumb-287-new"}
	if rec := request(routeID, castTag, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("tag for replaced photo = %d", rec.Code)
	}
	// The new photo's tag must not be served the old photo from the warm cache.
	newTag := persons.personToDTO(*people[287]).ImageTags["Primary"]
	assertImageRedirect(t, request(routeID, newTag, ""), "https://cdn.example.test/tmdb/people/287/profile/w300.new789.webp")
	assertImageRedirect(t, request(routeID, "", "visible"), "https://cdn.example.test/tmdb/people/287/profile/w300.new789.webp")

	// Without a thumbhash the photo path alone must still rotate the tag.
	people[288] = &models.Person{ID: 288, PhotoPath: "tmdb/people/288/profile/original.def456.webp", PhotoThumbhash: "-"}
	noThumbTag := persons.personToDTO(*people[288]).ImageTags["Primary"]
	if rec := request(otherRouteID, noThumbTag, ""); rec.Code != http.StatusFound {
		t.Fatalf("tag without thumbhash = %d", rec.Code)
	}
	people[288] = &models.Person{ID: 288, PhotoPath: "tmdb/people/288/profile/original.new000.webp", PhotoThumbhash: "-"}
	if got := persons.personToDTO(*people[288]).ImageTags["Primary"]; got == noThumbTag {
		t.Fatal("tag did not change when a photo without thumbhash was replaced")
	}
	if rec := request(otherRouteID, noThumbTag, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("tag for replaced photo without thumbhash = %d", rec.Code)
	}

	h.imageTags = nil
	if rec := request(otherRouteID, crewTag, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("tag accepted without a signing secret = %d", rec.Code)
	}
}
