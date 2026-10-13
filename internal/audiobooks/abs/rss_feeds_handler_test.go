package abs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type memRSSFeedStore struct {
	mu   sync.Mutex
	rows map[string]RSSFeed
}

func newMemRSSFeedStore() *memRSSFeedStore { return &memRSSFeedStore{rows: map[string]RSSFeed{}} }

func (m *memRSSFeedStore) ListUserFeeds(_ context.Context, userID, profileID string) ([]RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]RSSFeed, 0)
	for _, f := range m.rows {
		if f.UserID == userID && f.ProfileID == profileID && f.ClosedAt == nil {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *memRSSFeedStore) GetFeed(_ context.Context, id string) (RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.rows[id]
	if !ok {
		return RSSFeed{}, ErrNotFound
	}
	return f, nil
}

func (m *memRSSFeedStore) GetFeedBySlug(_ context.Context, slug string) (RSSFeed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.rows {
		if f.Slug == slug && f.ClosedAt == nil {
			return f, nil
		}
	}
	return RSSFeed{}, ErrNotFound
}

func (m *memRSSFeedStore) CreateFeed(_ context.Context, f RSSFeed) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.rows {
		if existing.Slug == f.Slug && existing.ClosedAt == nil {
			return errors.New("unique violation duplicate key")
		}
	}
	f.CreatedAt = time.Now()
	m.rows[f.ID] = f
	return nil
}

func (m *memRSSFeedStore) CloseFeed(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.rows[id]
	if !ok {
		return nil
	}
	now := time.Now()
	f.ClosedAt = &now
	m.rows[id] = f
	return nil
}

func newFeedsHarness(t *testing.T, knownItems ...string) (*Handler, *memRSSFeedStore) {
	t.Helper()
	known := map[string]*models.MediaItem{}
	for _, id := range knownItems {
		known[id] = nil
	}
	store := newMemRSSFeedStore()
	resolver := newFakeOwnerResolver().
		set("1", "", catalog.AccessFilter{UserID: 1}).
		set("2", "", catalog.AccessFilter{UserID: 2})
	h := New(Dependencies{MediaStore: &stubMediaStore{known: known}, RSSFeedStore: store, AccessResolver: resolver})
	return h, store
}

// fakeOwnerResolver hands out a fixed access filter per (user, profile). A
// principal it does not know is unavailable, as a disabled or deleted owner
// is.
type fakeOwnerResolver struct {
	mu     sync.Mutex
	access map[string]catalog.AccessFilter
}

func newFakeOwnerResolver() *fakeOwnerResolver {
	return &fakeOwnerResolver{access: map[string]catalog.AccessFilter{}}
}

func (f *fakeOwnerResolver) set(userID, profileID string, filter catalog.AccessFilter) *fakeOwnerResolver {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.access[userID+"/"+profileID] = filter
	return f
}

func (f *fakeOwnerResolver) remove(userID, profileID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.access, userID+"/"+profileID)
}

func (f *fakeOwnerResolver) ResolveABSAccess(ctx context.Context, userID, profileID string) (catalog.AccessFilter, error) {
	return f.ResolveABSOwnerAccess(ctx, userID, profileID)
}

func (f *fakeOwnerResolver) ResolveABSOwnerAccess(_ context.Context, userID, profileID string) (catalog.AccessFilter, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	filter, ok := f.access[userID+"/"+profileID]
	if !ok {
		return catalog.AccessFilter{}, ErrOwnerUnavailable
	}
	return filter, nil
}

// accessMediaStore applies an access filter the way ABSMediaStore does: the
// item is visible when one of its files' libraries passes the filter and its
// rating passes the maturity ceiling, and only files in permitted libraries
// come back.
type accessMediaStore struct {
	noopMediaStore
	item  *models.MediaItem
	files []*models.MediaFile
}

func (s *accessMediaStore) visible(id string, filter catalog.AccessFilter) bool {
	if s.item == nil || s.item.ContentID != id || !access.RatingAllowed(s.item.ContentRating, filter.MaxContentRating) {
		return false
	}
	return len(catalog.FilterMediaFilesByAccess(s.files, catalog.AccessFilter{
		AllowedLibraryIDs:  filter.AllowedLibraryIDs,
		DisabledLibraryIDs: filter.DisabledLibraryIDs,
	})) > 0
}

func (s *accessMediaStore) GetAudiobookByID(_ context.Context, id string, filter catalog.AccessFilter) (*models.MediaItem, error) {
	if !s.visible(id, filter) {
		return nil, nil
	}
	return s.item, nil
}

func (s *accessMediaStore) GetMediaFiles(_ context.Context, id string, filter catalog.AccessFilter) ([]*models.MediaFile, error) {
	if !s.visible(id, filter) {
		return []*models.MediaFile{}, nil
	}
	return catalog.FilterMediaFilesByAccess(s.files, filter), nil
}

// ownerFeedFixture is one open feed owned by user 1, profile "kid", for a
// PG-13 book with a version in library 1 and another in library 2. The owner
// starts with access to library 1 only.
type ownerFeedFixture struct {
	h        *Handler
	resolver *fakeOwnerResolver
	owner    catalog.AccessFilter
	body     []byte
}

func newOwnerFeedFixture(t *testing.T) ownerFeedFixture {
	t.Helper()
	dir := t.TempDir()
	body := []byte("library-one-audio")
	for name, data := range map[string][]byte{"one.mp3": body, "two.mp3": []byte("library-two-audio")} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	media := &accessMediaStore{
		item: &models.MediaItem{ContentID: "book-1", Title: "Book", ContentRating: "PG-13"},
		files: []*models.MediaFile{
			{ID: 11, ContentID: "book-1", MediaFolderID: 1, FilePath: filepath.Join(dir, "one.mp3")},
			{ID: 22, ContentID: "book-1", MediaFolderID: 2, FilePath: filepath.Join(dir, "two.mp3")},
		},
	}
	owner := catalog.AccessFilter{UserID: 1, ProfileID: "kid", AllowedLibraryIDs: []int{1}}
	resolver := newFakeOwnerResolver().set("1", "kid", owner)
	feeds := newMemRSSFeedStore()
	if err := feeds.CreateFeed(context.Background(), RSSFeed{
		ID: "feed-1", UserID: "1", ProfileID: "kid", LibraryItemID: "book-1", Slug: "owner-feed",
	}); err != nil {
		t.Fatal(err)
	}
	h := New(Dependencies{MediaStore: media, RSSFeedStore: feeds, AccessResolver: resolver})
	return ownerFeedFixture{h: h, resolver: resolver, owner: owner, body: body}
}

func (fx ownerFeedFixture) getFeed() *httptest.ResponseRecorder {
	return dispatchABSWithParams(http.MethodGet, "/feed/owner-feed.xml",
		map[string]string{"slug": "owner-feed.xml"}, nil, "", "", fx.h.handlePublicFeed)
}

func (fx ownerFeedFixture) getFile(ino string) *httptest.ResponseRecorder {
	return dispatchABSWithParams(http.MethodGet, "/feed/owner-feed/file/"+ino,
		map[string]string{"slug": "owner-feed", "ino": ino}, nil, "", "", fx.h.handlePublicFeedFile)
}

func TestFeed_Open_GeneratesSlug(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	slug, _ := got["slug"].(string)
	if len(slug) != 16 {
		t.Errorf("slug = %q (len %d), want 16-char auto-generated", slug, len(slug))
	}
}

func TestFeed_Open_CustomSlug(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{"slug":"my-cool-feed"}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["slug"] != "my-cool-feed" {
		t.Errorf("slug = %v, want my-cool-feed", got["slug"])
	}
}

func TestFeed_Open_InvalidSlug_400(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{"slug":"BAD!"}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestFeed_Open_Collision_409(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	body := []byte(`{"slug":"taken-slug"}`)
	_ = dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, body, "1", "", h.handleOpenItemFeed)
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, body, "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
}

func TestFeed_Open_UnknownItem_404(t *testing.T) {
	h, _ := newFeedsHarness(t)
	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/ghost/open",
		map[string]string{"itemId": "ghost"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestFeed_List_OwnerOnly(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	_ = dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	rec := dispatchABSWithParams(http.MethodGet, "/api/feeds", nil, nil, "2", "", h.handleListRSSFeeds)
	var env map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	feeds, _ := env["feeds"].([]any)
	if len(feeds) != 0 {
		t.Errorf("user 2 sees %d feeds, want 0", len(feeds))
	}
}

func TestFeed_Close_Owner(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	openRec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	var open map[string]any
	_ = json.Unmarshal(openRec.Body.Bytes(), &open)
	id, _ := open["id"].(string)

	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/"+id+"/close", map[string]string{"id": id}, nil, "1", "", h.handleCloseFeed)
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rec.Code)
	}
}

func TestFeed_Close_NonOwner_404(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	openRec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open", map[string]string{"itemId": "book-1"}, []byte(`{}`), "1", "", h.handleOpenItemFeed)
	var open map[string]any
	_ = json.Unmarshal(openRec.Body.Bytes(), &open)
	id, _ := open["id"].(string)

	rec := dispatchABSWithParams(http.MethodPost, "/api/feeds/"+id+"/close", map[string]string{"id": id}, nil, "2", "", h.handleCloseFeed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPublicFeed_UnknownSlug_404(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	rec := dispatchABSWithParams(http.MethodGet, "/feed/missing.xml",
		map[string]string{"slug": "missing.xml"}, nil, "", "", h.handlePublicFeed)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestPublicFeed_HappyPath_XML(t *testing.T) {
	h, _ := newFeedsHarness(t, "book-1")
	openRec := dispatchABSWithParams(http.MethodPost, "/api/feeds/item/book-1/open",
		map[string]string{"itemId": "book-1"}, []byte(`{"slug":"happy-feed"}`), "1", "", h.handleOpenItemFeed)
	if openRec.Code != http.StatusOK {
		t.Fatalf("seed open failed: %s", openRec.Body.String())
	}

	rec := dispatchABSWithParams(http.MethodGet, "/feed/happy-feed.xml",
		map[string]string{"slug": "happy-feed.xml"}, nil, "", "", h.handlePublicFeed)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/rss+xml") {
		t.Errorf("Content-Type = %q, want application/rss+xml", ct)
	}
	body := rec.Body.String()
	for _, needle := range []string{"<rss", "<channel>", "<title>"} {
		if !strings.Contains(body, needle) {
			t.Errorf("body missing %q; got %s", needle, body)
		}
	}
}

// The feed lists and serves only the versions its owner can access, not every
// version of the item.
func TestPublicFeed_OnlyOwnerAccessibleVersions(t *testing.T) {
	fx := newOwnerFeedFixture(t)

	rec := fx.getFeed()
	if rec.Code != http.StatusOK {
		t.Fatalf("feed status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if xml := rec.Body.String(); !strings.Contains(xml, "/feed/owner-feed/file/11") || strings.Contains(xml, "/file/22") {
		t.Fatalf("feed should list only file 11; got %s", xml)
	}

	if rec := fx.getFile("11"); rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), fx.body) {
		t.Fatalf("accessible file: status = %d, body = %q", rec.Code, rec.Body.String())
	}
	if rec := fx.getFile("22"); rec.Code != http.StatusNotFound {
		t.Fatalf("file in a library the owner cannot access: status = %d, want 404", rec.Code)
	}
}

// A feed applies its owner's access at request time, so it stops working once
// the owner can no longer see the item, and answers exactly like a closed feed.
func TestPublicFeed_FollowsOwnerCurrentAccess(t *testing.T) {
	cases := map[string]func(fx ownerFeedFixture){
		"library access revoked": func(fx ownerFeedFixture) {
			revoked := fx.owner
			revoked.AllowedLibraryIDs = []int{}
			fx.resolver.set("1", "kid", revoked)
		},
		"maturity limit tightened": func(fx ownerFeedFixture) {
			tightened := fx.owner
			tightened.MaxContentRating = "PG"
			fx.resolver.set("1", "kid", tightened)
		},
		"owner disabled": func(fx ownerFeedFixture) {
			fx.resolver.remove("1", "kid")
		},
	}
	closed := newOwnerFeedFixture(t)
	if err := closed.h.deps.RSSFeedStore.CloseFeed(context.Background(), "feed-1"); err != nil {
		t.Fatal(err)
	}
	closedFeed, closedFile := closed.getFeed(), closed.getFile("11")

	for name, revoke := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newOwnerFeedFixture(t)
			if rec := fx.getFile("11"); rec.Code != http.StatusOK {
				t.Fatalf("before the change: file status = %d, want 200", rec.Code)
			}
			revoke(fx)

			for _, got := range []struct {
				route    string
				rec, ref *httptest.ResponseRecorder
			}{
				{"feed", fx.getFeed(), closedFeed},
				{"file", fx.getFile("11"), closedFile},
			} {
				if got.rec.Code != http.StatusNotFound || got.rec.Body.String() != got.ref.Body.String() {
					t.Errorf("%s: status = %d, body = %q; want the closed-feed answer %d, %q",
						got.route, got.rec.Code, got.rec.Body.String(), got.ref.Code, got.ref.Body.String())
				}
			}
		})
	}
}

type failingOwnerResolver struct{}

func (failingOwnerResolver) ResolveABSAccess(context.Context, string, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{}, errors.New("database unavailable")
}

func (failingOwnerResolver) ResolveABSOwnerAccess(context.Context, string, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{}, errors.New("database unavailable")
}

// Without an owner to resolve, the feed fails closed. A failed owner lookup is
// a server error rather than the closed-feed 404, so podcast apps do not drop
// a working feed over a transient fault.
func TestPublicFeed_OwnerResolutionFailures(t *testing.T) {
	cases := map[string]struct {
		resolver AccessResolver
		want     int
	}{
		"no resolver":         {nil, http.StatusNotFound},
		"owner lookup failed": {failingOwnerResolver{}, http.StatusInternalServerError},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fx := newOwnerFeedFixture(t)
			fx.h.deps.AccessResolver = tc.resolver
			if rec := fx.getFeed(); rec.Code != tc.want {
				t.Errorf("feed status = %d, want %d", rec.Code, tc.want)
			}
			if rec := fx.getFile("11"); rec.Code != tc.want {
				t.Errorf("file status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}
