package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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
// Helper: build AdminHandler with a realtime hub whose events the test can
// observe. Password changes publish a domain event; the notifications
// materializer (tested separately) turns it into the user's notification.
// ---------------------------------------------------------------------------

func newAdminHandlerWithHub(userRepo UserRepository) (*AdminHandler, *evt.Hub) {
	rh := notifications.NewHub("test", nil)
	h := NewAdminHandler(userRepo, nil, nil)
	h.RealtimeHub = rh
	return h, rh.EventsHub()
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

// TestHandleUpdateUser_WithPassword_PublishesEvent asserts that including a
// Password in the update payload publishes one user.password_changed event on
// ChannelSessions targeted at the updated user.
func TestHandleUpdateUser_WithPassword_PublishesEvent(t *testing.T) {
	targetUser := &models.User{
		ID:       99,
		Username: "alice",
		Email:    "alice@example.com",
		Role:     "user",
	}
	repo := &fakeUserRepo{user: targetUser}
	h, eventsHub := newAdminHandlerWithHub(repo)
	ch, unsub := eventsHub.Subscribe()
	defer unsub()

	body := []byte(`{"password":"newSecret123"}`)
	req := adminUpdateRequest(body, "99")
	rec := httptest.NewRecorder()

	h.HandleUpdateUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	select {
	case env := <-ch:
		if env.Channel != evt.ChannelSessions {
			t.Errorf("channel = %q, want %q", env.Channel, evt.ChannelSessions)
		}
		if env.Event != notifications.EventUserPasswordChanged {
			t.Errorf("event = %q, want %q", env.Event, notifications.EventUserPasswordChanged)
		}
		if env.UserID != 99 {
			t.Errorf("event user_id = %d, want 99", env.UserID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a user.password_changed event, got none")
	}
}

// TestHandleUpdateUser_WithoutPassword_NoEvent asserts that a non-password
// update publishes no user.password_changed event.
func TestHandleUpdateUser_WithoutPassword_NoEvent(t *testing.T) {
	targetUser := &models.User{
		ID:       99,
		Username: "alice",
		Email:    "alice@example.com",
		Role:     "user",
	}
	repo := &fakeUserRepo{user: targetUser}
	h, eventsHub := newAdminHandlerWithHub(repo)
	ch, unsub := eventsHub.Subscribe()
	defer unsub()

	body := []byte(`{"username":"alice-renamed"}`)
	req := adminUpdateRequest(body, "99")
	rec := httptest.NewRecorder()

	h.HandleUpdateUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	select {
	case env := <-ch:
		t.Errorf("expected no event, got %q on %q", env.Event, env.Channel)
	case <-time.After(100 * time.Millisecond):
		// no event, as expected
	}
}
