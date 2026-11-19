package abs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

// fakeOwnerResolver hands out a fixed access filter per (user, profile). A
// principal it does not know is unavailable, as a disabled or deleted owner
// is. Every account is a server administrator, and every principal its
// account's primary profile, until the test says otherwise.
type fakeOwnerResolver struct {
	mu         sync.Mutex
	access     map[string]catalog.AccessFilter
	nonAdmin   map[string]bool
	nonPrimary map[string]bool
}

func newFakeOwnerResolver() *fakeOwnerResolver {
	return &fakeOwnerResolver{access: map[string]catalog.AccessFilter{}, nonAdmin: map[string]bool{}, nonPrimary: map[string]bool{}}
}

// demote takes the server admin role from the account.
func (f *fakeOwnerResolver) demote(userID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonAdmin[userID] = true
}

// notPrimary makes the principal a profile other than its account's primary.
func (f *fakeOwnerResolver) notPrimary(userID, profileID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonPrimary[userID+"/"+profileID] = true
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
	filter, _, err := f.ResolveABSOwnerAccess(ctx, userID, profileID)
	return filter, err
}

func (f *fakeOwnerResolver) ResolveABSOwnerAccess(_ context.Context, userID, profileID string) (catalog.AccessFilter, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	filter, ok := f.access[userID+"/"+profileID]
	if !ok {
		return catalog.AccessFilter{}, false, ErrOwnerUnavailable
	}
	return filter, !f.nonAdmin[userID] && !f.nonPrimary[userID+"/"+profileID], nil
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
	accounts *downloadAccounts
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
	accounts := &downloadAccounts{denied: map[int]bool{}}
	h.SetDownloads(newDownloadService(true, accounts))
	return ownerFeedFixture{h: h, resolver: resolver, accounts: accounts, owner: owner, body: body}
}

func (fx ownerFeedFixture) getFeed() *httptest.ResponseRecorder {
	return dispatchABSWithParams(http.MethodGet, "/feed/owner-feed.xml",
		map[string]string{"slug": "owner-feed.xml"}, nil, "", "", fx.h.handlePublicFeed)
}

func (fx ownerFeedFixture) getFile(ino string) *httptest.ResponseRecorder {
	return dispatchABSWithParams(http.MethodGet, "/feed/owner-feed/file/"+ino,
		map[string]string{"slug": "owner-feed", "ino": ino}, nil, "", "", fx.h.handlePublicFeedFile)
}

type failingOwnerResolver struct{}

func (failingOwnerResolver) ResolveABSAccess(context.Context, string, string) (catalog.AccessFilter, error) {
	return catalog.AccessFilter{}, errors.New("database unavailable")
}

func (failingOwnerResolver) ResolveABSOwnerAccess(context.Context, string, string) (catalog.AccessFilter, bool, error) {
	return catalog.AccessFilter{}, false, errors.New("database unavailable")
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
