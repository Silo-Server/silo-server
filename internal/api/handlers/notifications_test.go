package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ---------------------------------------------------------------------------
// Fake profile resolver for child-safe tests.
// ---------------------------------------------------------------------------

// fakeProfileStore satisfies userstore.UserStore minimally.
// Only GetProfile is exercised by childSafe.
type fakeProfileStore struct {
	userstore.UserStore // embed for unimplemented methods
	isChild             bool
}

func (s *fakeProfileStore) GetProfile(_ context.Context, id string) (*userstore.Profile, error) {
	return &userstore.Profile{ID: id, IsChild: s.isChild}, nil
}

// fakeResolverAsProfileResolver implements the profileResolver interface used
// by NotificationsHandler.
type fakeResolverAsProfileResolver struct {
	isChild bool
}

func (f *fakeResolverAsProfileResolver) ForUser(_ context.Context, _ int) (userstore.UserStore, error) {
	return &fakeProfileStore{isChild: f.isChild}, nil
}

// fakeResolverWithError returns an error from ForUser.
type fakeResolverWithError struct{}

func (f *fakeResolverWithError) ForUser(_ context.Context, _ int) (userstore.UserStore, error) {
	return nil, errors.New("resolver failed")
}

// ---------------------------------------------------------------------------
// Fake store implementing notifications.Store for handler tests.
// ---------------------------------------------------------------------------

type fakeNotificationsStore struct {
	notifications.Store // embed for unimplemented panics

	list                  []*notifications.Notification
	announcements         []*notifications.Announcement
	unreadCount           int
	prefs                 map[notifications.Category]bool
	markReadCalled        bool
	markAllReadCalled     bool
	dismissErr            error
	setPrefErr            error
	deleteAnnouncementErr error
	insertCalled          bool

	// capturedFilter records the most recent ListFilter passed to List.
	capturedFilter *notifications.ListFilter
}

func (f *fakeNotificationsStore) Insert(_ context.Context, n *notifications.Notification) (bool, error) {
	f.insertCalled = true
	f.list = append(f.list, n)
	return true, nil
}

func (f *fakeNotificationsStore) List(_ context.Context, filter notifications.ListFilter) ([]*notifications.Notification, error) {
	f.capturedFilter = &filter
	return f.list, nil
}

func (f *fakeNotificationsStore) UnreadCount(_ context.Context, _ int, _ string, _ bool) (int, error) {
	return f.unreadCount, nil
}

func (f *fakeNotificationsStore) MarkRead(_ context.Context, _ int, _ string, _ []int64) error {
	f.markReadCalled = true
	return nil
}

func (f *fakeNotificationsStore) MarkAllRead(_ context.Context, _ int, _ string) error {
	f.markAllReadCalled = true
	return nil
}

func (f *fakeNotificationsStore) Dismiss(_ context.Context, _ int, _ string, _ int64) error {
	return f.dismissErr
}

func (f *fakeNotificationsStore) Preferences(_ context.Context, _ int) (map[notifications.Category]bool, error) {
	if f.prefs != nil {
		return f.prefs, nil
	}
	return map[notifications.Category]bool{}, nil
}

func (f *fakeNotificationsStore) SetPreference(_ context.Context, _ int, _ notifications.Category, _ bool) error {
	return f.setPrefErr
}

func (f *fakeNotificationsStore) InsertAnnouncement(_ context.Context, a *notifications.Announcement) error {
	a.ID = int64(len(f.announcements) + 1)
	a.CreatedAt = time.Now().UTC()
	f.announcements = append(f.announcements, a)
	return nil
}

func (f *fakeNotificationsStore) ListAnnouncements(_ context.Context) ([]*notifications.Announcement, error) {
	return f.announcements, nil
}

func (f *fakeNotificationsStore) DeleteAnnouncement(_ context.Context, id int64) error {
	if f.deleteAnnouncementErr != nil {
		return f.deleteAnnouncementErr
	}
	for i, a := range f.announcements {
		if a.ID == id {
			f.announcements = append(f.announcements[:i], f.announcements[i+1:]...)
			return nil
		}
	}
	return notifications.ErrNotFound
}

func (f *fakeNotificationsStore) DismissAnnouncementNotifications(_ context.Context, _ int64) error {
	return nil
}
func (f *fakeNotificationsStore) ProfileIDsForUsers(_ context.Context, _ []int) (map[int][]string, error) {
	return nil, nil
}
func (f *fakeNotificationsStore) PurgeOld(_ context.Context, _, _ time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeNotificationsStore) AdminUserIDs(_ context.Context) ([]int, error) { return nil, nil }
func (f *fakeNotificationsStore) UserIDsWithLibraryAccess(_ context.Context, _ int) ([]int, error) {
	return nil, nil
}
func (f *fakeNotificationsStore) AllEnabledUserIDs(_ context.Context) ([]int, error) { return nil, nil }

// Compile-time check.
var _ notifications.Store = (*fakeNotificationsStore)(nil)

// ---------------------------------------------------------------------------
// Helper: build a handler over the fake store.
// ---------------------------------------------------------------------------

func newTestNotificationsHandler(store *fakeNotificationsStore) *NotificationsHandler {
	hub := evt.NewHub("test", nil)
	svc := notifications.NewService(store, hub)
	return NewNotificationsHandler(svc)
}

func newTestNotificationsHandlerWithResolver(store *fakeNotificationsStore, resolver profileResolver) *NotificationsHandler {
	hub := evt.NewHub("test", nil)
	svc := notifications.NewService(store, hub)
	return NewNotificationsHandler(svc, resolver)
}

// notifUserRequest creates an authenticated request with a profile ID.
func notifUserRequest(method, target string, body []byte, userID int, profileID string) *http.Request {
	var req *http.Request
	if len(body) > 0 {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: userID, Role: "user"})
	ctx = apimw.SetProfileID(ctx, profileID)
	return req.WithContext(ctx)
}

// notifAdminRequest creates an admin-authenticated request.
func notifAdminRequest(method, target string, body []byte, userID int) *http.Request {
	var req *http.Request
	if len(body) > 0 {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	ctx := apimw.SetClaims(req.Context(), &auth.Claims{UserID: userID, Role: "admin"})
	return req.WithContext(ctx)
}

// notifChiRequest adds a chi URL param to a request context.
func notifChiRequest(r *http.Request, key, value string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add(key, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

// ---------------------------------------------------------------------------
// Tests: HandleList
// ---------------------------------------------------------------------------

func TestNotificationsHandleList_HappyPath_TwoRowsNoCursor(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeNotificationsStore{
		list: []*notifications.Notification{
			{ID: 2, UserID: 1, Category: notifications.CategoryContent, Type: "content.added", Title: "New Movie", CreatedAt: now},
			{ID: 1, UserID: 1, Category: notifications.CategoryRequest, Type: "request.approved", Title: "Approved", CreatedAt: now.Add(-time.Minute)},
		},
	}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodGet, "/notifications", nil, 1, "prof-1")
	h.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Items      []*notifications.Notification `json:"items"`
		NextCursor *int64                        `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items count = %d, want 2", len(resp.Items))
	}
	// len < limit (default 50), so next_cursor must be null.
	if resp.NextCursor != nil {
		t.Fatalf("next_cursor = %v, want nil (items < limit)", resp.NextCursor)
	}
}

// ---------------------------------------------------------------------------
// Tests: HandleUnreadCount
// ---------------------------------------------------------------------------

func TestNotificationsHandleUnreadCount_ReturnsCount(t *testing.T) {
	store := &fakeNotificationsStore{unreadCount: 7}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodGet, "/notifications/unread-count", nil, 1, "prof-1")
	h.HandleUnreadCount(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Count != 7 {
		t.Fatalf("count = %d, want 7", resp.Count)
	}
}

// ---------------------------------------------------------------------------
// Tests: HandleMarkRead
// ---------------------------------------------------------------------------

func TestNotificationsHandleMarkRead_EmptyBody_Returns400(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodPost, "/notifications/read", []byte(`{}`), 1, "prof-1")
	h.HandleMarkRead(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationsHandleMarkRead_AllTrue_Returns204(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodPost, "/notifications/read", []byte(`{"all":true}`), 1, "prof-1")
	h.HandleMarkRead(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if !store.markAllReadCalled {
		t.Fatal("MarkAllRead was not called")
	}
}

func TestNotificationsHandleMarkRead_IDs_Returns204(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodPost, "/notifications/read", []byte(`{"ids":[1,2,3]}`), 1, "prof-1")
	h.HandleMarkRead(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
	if !store.markReadCalled {
		t.Fatal("MarkRead was not called")
	}
}

// ---------------------------------------------------------------------------
// Tests: HandleDismiss
// ---------------------------------------------------------------------------

func TestNotificationsHandleDismiss_NotFound_Returns404(t *testing.T) {
	store := &fakeNotificationsStore{dismissErr: notifications.ErrNotFound}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodPost, "/notifications/999/dismiss", nil, 1, "prof-1")
	req = notifChiRequest(req, "id", "999")
	h.HandleDismiss(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationsHandleDismiss_BadID_Returns400(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodPost, "/notifications/abc/dismiss", nil, 1, "prof-1")
	req = notifChiRequest(req, "id", "abc")
	h.HandleDismiss(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Tests: HandleGetPreferences
// ---------------------------------------------------------------------------

func TestNotificationsHandleGetPreferences_DefaultsReturned(t *testing.T) {
	// No stored prefs → service returns all MutableCategories with defaults.
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodGet, "/notifications/preferences", nil, 1, "prof-1")
	h.HandleGetPreferences(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Preferences []notifications.Preference `json:"preferences"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Preferences) != len(notifications.MutableCategories) {
		t.Fatalf("prefs count = %d, want %d", len(resp.Preferences), len(notifications.MutableCategories))
	}

	prefMap := make(map[notifications.Category]bool)
	for _, p := range resp.Preferences {
		prefMap[p.Category] = p.Enabled
	}

	// content_digest defaults to false (opt-in); all others default to true.
	for _, cat := range notifications.MutableCategories {
		enabled, ok := prefMap[cat]
		if !ok {
			t.Errorf("category %q missing from preferences", cat)
			continue
		}
		if cat == notifications.CategoryContentDigest {
			if enabled {
				t.Errorf("content_digest default should be false (opt-in), got true")
			}
		} else {
			if !enabled {
				t.Errorf("category %q default should be true, got false", cat)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: HandlePutPreferences
// ---------------------------------------------------------------------------

func TestNotificationsHandlePutPreferences_RejectsAnnouncementCategory_400(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	// "announcement" is not in MutableCategories.
	body := `{"preferences":[{"category":"announcement","enabled":false}]}`
	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodPut, "/notifications/preferences", []byte(body), 1, "prof-1")
	h.HandlePutPreferences(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationsHandlePutPreferences_ValidPrefs_204(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	body := `{"preferences":[{"category":"request","enabled":false},{"category":"content","enabled":true}]}`
	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodPut, "/notifications/preferences", []byte(body), 1, "prof-1")
	h.HandlePutPreferences(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// Tests: Admin announcements
// ---------------------------------------------------------------------------

func TestNotificationsHandleCreateAnnouncement_201(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	body := `{"title":"Test Announcement","body":"Hello","audience":{"all":true}}`
	rec := httptest.NewRecorder()
	req := notifAdminRequest(http.MethodPost, "/admin/announcements", []byte(body), 1)
	h.HandleCreateAnnouncement(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", rec.Code, rec.Body.String())
	}

	var resp notifications.Announcement
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Title != "Test Announcement" {
		t.Fatalf("title = %q, want %q", resp.Title, "Test Announcement")
	}
	if resp.CreatedBy == nil || *resp.CreatedBy != 1 {
		t.Fatalf("created_by = %v, want 1", resp.CreatedBy)
	}
}

func TestNotificationsHandleCreateAnnouncement_ValidationError_400(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	// Empty title triggers service validation error.
	body := `{"title":"","body":"Hello","audience":{"all":true}}`
	rec := httptest.NewRecorder()
	req := notifAdminRequest(http.MethodPost, "/admin/announcements", []byte(body), 1)
	h.HandleCreateAnnouncement(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationsHandleDeleteAnnouncement_204(t *testing.T) {
	store := &fakeNotificationsStore{
		announcements: []*notifications.Announcement{
			{ID: 5, Title: "To Delete", Audience: notifications.Audience{All: true}},
		},
	}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifAdminRequest(http.MethodDelete, "/admin/announcements/5", nil, 1)
	req = notifChiRequest(req, "id", "5")
	h.HandleDeleteAnnouncement(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body: %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationsHandleDeleteAnnouncement_NotFound_404(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifAdminRequest(http.MethodDelete, "/admin/announcements/999", nil, 1)
	req = notifChiRequest(req, "id", "999")
	h.HandleDeleteAnnouncement(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rec.Code, rec.Body.String())
	}
}

func TestNotificationsHandleListAnnouncements_ReturnsItems(t *testing.T) {
	store := &fakeNotificationsStore{
		announcements: []*notifications.Announcement{
			{ID: 1, Title: "Hello", Audience: notifications.Audience{All: true}},
		},
	}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifAdminRequest(http.MethodGet, "/admin/announcements", nil, 1)
	h.HandleListAnnouncements(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Items []*notifications.Announcement `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items count = %d, want 1", len(resp.Items))
	}
}

// ---------------------------------------------------------------------------
// Tests: ChildSafe profile filtering
// ---------------------------------------------------------------------------

// TestNotificationsList_ChildProfileFiltered asserts that when the profile
// resolver reports IsChild=true, the ListFilter passed to the store has
// ChildSafe=true.
func TestNotificationsList_ChildProfileFiltered(t *testing.T) {
	store := &fakeNotificationsStore{}
	resolver := &fakeResolverAsProfileResolver{isChild: true}
	h := newTestNotificationsHandlerWithResolver(store, resolver)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodGet, "/notifications", nil, 1, "prof-child")
	h.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if store.capturedFilter == nil {
		t.Fatal("List was not called; capturedFilter is nil")
	}
	if !store.capturedFilter.ChildSafe {
		t.Fatalf("capturedFilter.ChildSafe = false, want true for child profile")
	}
}

// TestNotificationsList_AdultProfileNotFiltered asserts that a non-child
// profile leaves ChildSafe=false in the ListFilter.
func TestNotificationsList_AdultProfileNotFiltered(t *testing.T) {
	store := &fakeNotificationsStore{}
	resolver := &fakeResolverAsProfileResolver{isChild: false}
	h := newTestNotificationsHandlerWithResolver(store, resolver)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodGet, "/notifications", nil, 1, "prof-adult")
	h.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if store.capturedFilter == nil {
		t.Fatal("List was not called; capturedFilter is nil")
	}
	if store.capturedFilter.ChildSafe {
		t.Fatalf("capturedFilter.ChildSafe = true, want false for non-child profile")
	}
}

// TestNotificationsList_NilResolverDefaultsToFalse asserts that when no
// resolver is wired (nil), ChildSafe remains false.
func TestNotificationsList_NilResolverDefaultsToFalse(t *testing.T) {
	store := &fakeNotificationsStore{}
	h := newTestNotificationsHandler(store) // no resolver

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodGet, "/notifications", nil, 1, "prof-1")
	h.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if store.capturedFilter == nil {
		t.Fatal("List was not called; capturedFilter is nil")
	}
	if store.capturedFilter.ChildSafe {
		t.Fatalf("capturedFilter.ChildSafe = true, want false when no resolver is wired")
	}
}

// TestNotificationsList_ResolverErrorFailsClosed asserts that when the resolver
// returns an error, ChildSafe=true (fail closed).
func TestNotificationsList_ResolverErrorFailsClosed(t *testing.T) {
	store := &fakeNotificationsStore{}
	resolver := &fakeResolverWithError{}
	h := newTestNotificationsHandlerWithResolver(store, resolver)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodGet, "/notifications", nil, 1, "prof-1")
	h.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if store.capturedFilter == nil {
		t.Fatal("List was not called; capturedFilter is nil")
	}
	if !store.capturedFilter.ChildSafe {
		t.Fatalf("capturedFilter.ChildSafe = false, want true (fail closed on resolver error)")
	}
}

// TestNotificationsList_NextCursorSet asserts that when limit=2 and the store
// returns exactly 2 notifications, next_cursor is set to the last item's ID.
func TestNotificationsList_NextCursorSet(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeNotificationsStore{
		list: []*notifications.Notification{
			{ID: 12, UserID: 1, Category: notifications.CategoryContent, Type: "content.added", Title: "New Movie", CreatedAt: now},
			{ID: 11, UserID: 1, Category: notifications.CategoryRequest, Type: "request.approved", Title: "Approved", CreatedAt: now.Add(-time.Minute)},
		},
	}
	h := newTestNotificationsHandler(store)

	rec := httptest.NewRecorder()
	req := notifUserRequest(http.MethodGet, "/notifications?limit=2", nil, 1, "prof-1")
	h.HandleList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Items      []*notifications.Notification `json:"items"`
		NextCursor *int64                        `json:"next_cursor"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Items) != 2 {
		t.Fatalf("items count = %d, want 2", len(resp.Items))
	}
	// With limit=2 and 2 items returned, next_cursor should be set to the last item's ID (11).
	if resp.NextCursor == nil {
		t.Fatal("next_cursor is nil, want 11")
	}
	if *resp.NextCursor != 11 {
		t.Fatalf("next_cursor = %d, want 11", *resp.NextCursor)
	}
}

// Suppress unused import.
var _ = strings.Contains
