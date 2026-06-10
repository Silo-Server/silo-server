package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// ---------------------------------------------------------------------------
// Minimal fake UserRepository for AdminHandler tests.
// ---------------------------------------------------------------------------

type fakeUserRepo struct {
	user   *models.User
	getErr error
}

func (f *fakeUserRepo) List(_ context.Context) ([]*models.User, error) { return nil, nil }

func (f *fakeUserRepo) Create(_ context.Context, _ models.CreateUserInput) (*models.User, error) {
	return f.user, nil
}

func (f *fakeUserRepo) Update(_ context.Context, _ int, _ models.UpdateUserInput) error {
	return nil
}

func (f *fakeUserRepo) Delete(_ context.Context, _ int) error { return nil }

func (f *fakeUserRepo) GetByID(_ context.Context, _ int) (*models.User, error) {
	return f.user, f.getErr
}

// Compile-time check.
var _ UserRepository = (*fakeUserRepo)(nil)

// ---------------------------------------------------------------------------
// Helper: build AdminHandler with a notifications service backed by the
// provided fakeNotificationsStore.
// ---------------------------------------------------------------------------

func newAdminHandlerWithNotifications(userRepo UserRepository, notifStore *fakeNotificationsStore) *AdminHandler {
	hub := evt.NewHub("test", nil)
	svc := notifications.NewService(notifStore, hub)
	h := NewAdminHandler(userRepo, nil, nil)
	h.NotificationsSvc = svc
	return h
}

// adminUpdateRequest builds a PUT /admin/users/{id} HTTP request.
func adminUpdateRequest(body []byte, userID string) *http.Request {
	req := httptest.NewRequest(http.MethodPut, "/admin/users/"+userID, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", userID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

// ---------------------------------------------------------------------------
// Tests: HandleUpdateUser password change notification
// ---------------------------------------------------------------------------

// TestHandleUpdateUser_WithPassword_SystemNotificationSent asserts that when
// Password is included in the update payload, exactly one system notification
// of type "system.password_changed" is emitted for the target user.
func TestHandleUpdateUser_WithPassword_SystemNotificationSent(t *testing.T) {
	targetUser := &models.User{
		ID:       99,
		Username: "alice",
		Email:    "alice@example.com",
		Role:     "user",
	}
	repo := &fakeUserRepo{user: targetUser}
	notifStore := &fakeNotificationsStore{}
	h := newAdminHandlerWithNotifications(repo, notifStore)

	body := []byte(`{"password":"newSecret123"}`)
	req := adminUpdateRequest(body, "99")
	rec := httptest.NewRecorder()

	h.HandleUpdateUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	if len(notifStore.list) != 1 {
		t.Fatalf("expected 1 system notification, got %d", len(notifStore.list))
	}
	n := notifStore.list[0]
	if n.Type != "system.password_changed" {
		t.Errorf("notification type = %q, want %q", n.Type, "system.password_changed")
	}
	if n.UserID != 99 {
		t.Errorf("notification user_id = %d, want 99", n.UserID)
	}
	if n.Category != notifications.CategorySystem {
		t.Errorf("notification category = %q, want %q", n.Category, notifications.CategorySystem)
	}
}

// TestHandleUpdateUser_WithoutPassword_NoNotification asserts that when the
// update payload does not include a password, no system notification is sent.
func TestHandleUpdateUser_WithoutPassword_NoNotification(t *testing.T) {
	targetUser := &models.User{
		ID:       99,
		Username: "alice",
		Email:    "alice@example.com",
		Role:     "user",
	}
	repo := &fakeUserRepo{user: targetUser}
	notifStore := &fakeNotificationsStore{}
	h := newAdminHandlerWithNotifications(repo, notifStore)

	body := []byte(`{"username":"alice-renamed"}`)
	req := adminUpdateRequest(body, "99")
	rec := httptest.NewRecorder()

	h.HandleUpdateUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	if len(notifStore.list) != 0 {
		t.Errorf("expected 0 notifications on non-password update, got %d", len(notifStore.list))
	}
}
