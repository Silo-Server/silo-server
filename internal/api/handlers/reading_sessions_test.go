package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
)

var errSessionNotFound = errors.New("reading session not found")

// fakeReadingSessionStore is an in-memory ReadingSessionStore backed by a
// slice, mirroring fakeReaderFontStore's style.
type fakeReadingSessionStore struct {
	sessions []ReadingSession
	nextID   int64
}

func (f *fakeReadingSessionStore) LatestOpen(_ context.Context, userID int, profileID, contentID string, since time.Time) (*ReadingSession, error) {
	var best *ReadingSession
	for i := range f.sessions {
		s := &f.sessions[i]
		if s.UserID != userID || s.ProfileID != profileID || s.ContentID != contentID {
			continue
		}
		if s.LastHeartbeatAt.Before(since) {
			continue
		}
		if best == nil || s.LastHeartbeatAt.After(best.LastHeartbeatAt) {
			best = s
		}
	}
	if best == nil {
		return nil, nil
	}
	cp := *best
	return &cp, nil
}

func (f *fakeReadingSessionStore) Insert(_ context.Context, s ReadingSession) error {
	f.nextID++
	s.ID = f.nextID
	f.sessions = append(f.sessions, s)
	return nil
}

func (f *fakeReadingSessionStore) Extend(_ context.Context, id int64, lastHeartbeatAt time.Time, addSeconds int, endFraction float64) error {
	for i := range f.sessions {
		if f.sessions[i].ID == id {
			f.sessions[i].LastHeartbeatAt = lastHeartbeatAt
			f.sessions[i].DurationSeconds += addSeconds
			f.sessions[i].EndFraction = endFraction
			return nil
		}
	}
	return errSessionNotFound
}

// latestOpen is a test-only helper (not part of the store interface) that
// returns the most recently touched session for (user, profile, content),
// regardless of gap, so tests can assert on session state directly.
func (f *fakeReadingSessionStore) latestOpen(userID int, profileID, contentID string) *ReadingSession {
	var best *ReadingSession
	for i := range f.sessions {
		s := &f.sessions[i]
		if s.UserID != userID || s.ProfileID != profileID || s.ContentID != contentID {
			continue
		}
		if best == nil || s.LastHeartbeatAt.After(best.LastHeartbeatAt) {
			best = s
		}
	}
	return best
}

func readingSessionAuthContext(userID int, profileID string) context.Context {
	ctx := apimw.SetClaims(context.Background(), &auth.Claims{
		UserID:    userID,
		Role:      "user",
		TokenType: auth.TokenTypeAccess,
	})
	return apimw.SetProfileID(ctx, profileID)
}

func withReadingSessionContentIDParam(req *http.Request, contentID string) *http.Request {
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("content_id", contentID)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
}

// newReadingHeartbeatRequest builds an authenticated POST request against
// the reading-heartbeat endpoint. A nil body produces a request with no
// body at all (simulating a client that sends nothing), matching the
// "missing" case in TestReadingHeartbeatValidatesFraction.
func newReadingHeartbeatRequest(t *testing.T, userID int, profileID, contentID string, body []byte) *http.Request {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(http.MethodPost, "/ebooks/"+contentID+"/reading-heartbeat", nil)
	} else {
		req = httptest.NewRequest(http.MethodPost, "/ebooks/"+contentID+"/reading-heartbeat", bytes.NewReader(body))
	}
	req = req.WithContext(readingSessionAuthContext(userID, profileID))
	return withReadingSessionContentIDParam(req, contentID)
}

func postHeartbeat(t *testing.T, h *ReadingSessionsHandler, userID int, profileID, contentID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := newReadingHeartbeatRequest(t, userID, profileID, contentID, body)
	rr := httptest.NewRecorder()
	h.HandleHeartbeat(rr, req)
	return rr
}

func TestReadingHeartbeatStartsAndExtendsSessions(t *testing.T) {
	store := &fakeReadingSessionStore{}
	t0 := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	now := t0
	h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }}

	rr := postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0.10}`))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}
	if len(store.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(store.sessions))
	}
	s := store.latestOpen(1, "profile-a", "book-1")
	if s == nil {
		t.Fatal("expected an open session")
	}
	if s.DurationSeconds != 0 {
		t.Fatalf("duration = %d, want 0", s.DurationSeconds)
	}
	if s.StartFraction != 0.10 || s.EndFraction != 0.10 {
		t.Fatalf("start/end fraction = %v/%v, want 0.10/0.10", s.StartFraction, s.EndFraction)
	}
	firstID := s.ID

	// t0+30s: same session extends.
	now = t0.Add(30 * time.Second)
	rr = postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0.12}`))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}
	if len(store.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 (extended, not new)", len(store.sessions))
	}
	s = store.latestOpen(1, "profile-a", "book-1")
	if s.ID != firstID {
		t.Fatalf("session id = %d, want extended session %d", s.ID, firstID)
	}
	if s.DurationSeconds != 30 {
		t.Fatalf("duration = %d, want 30", s.DurationSeconds)
	}
	if s.EndFraction != 0.12 {
		t.Fatalf("end fraction = %v, want 0.12", s.EndFraction)
	}

	// t0+30s+200s: gap > 120s, so a new session starts; the first session
	// is left untouched.
	now = t0.Add(30 * time.Second).Add(200 * time.Second)
	rr = postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0.13}`))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}
	if len(store.sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 (new session after gap)", len(store.sessions))
	}
	var firstSession *ReadingSession
	for i := range store.sessions {
		if store.sessions[i].ID == firstID {
			firstSession = &store.sessions[i]
		}
	}
	if firstSession == nil {
		t.Fatal("expected first session to still exist")
	}
	if firstSession.DurationSeconds != 30 || firstSession.EndFraction != 0.12 {
		t.Fatalf("first session mutated: duration=%d end=%v, want 30/0.12", firstSession.DurationSeconds, firstSession.EndFraction)
	}
	newSession := store.latestOpen(1, "profile-a", "book-1")
	if newSession.ID == firstID {
		t.Fatal("expected a distinct new session row")
	}
	if newSession.DurationSeconds != 0 {
		t.Fatalf("new session duration = %d, want 0", newSession.DurationSeconds)
	}
	if newSession.StartFraction != 0.13 || newSession.EndFraction != 0.13 {
		t.Fatalf("new session start/end = %v/%v, want 0.13/0.13", newSession.StartFraction, newSession.EndFraction)
	}
}

func TestReadingHeartbeatCapsCredit(t *testing.T) {
	store := &fakeReadingSessionStore{}
	t0 := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	now := t0
	h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }}

	rr := postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0.10}`))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}

	// Gap of 100s (< 120s session gap) extends the session, but the credited
	// duration is capped at heartbeatMaxCredit (90s), not the full 100s gap.
	now = t0.Add(100 * time.Second)
	rr = postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0.20}`))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}
	if len(store.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(store.sessions))
	}
	s := store.latestOpen(1, "profile-a", "book-1")
	if s.DurationSeconds != 90 {
		t.Fatalf("duration = %d, want 90 (capped)", s.DurationSeconds)
	}
}

func TestReadingHeartbeatValidatesFraction(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"negative", []byte(`{"fraction":-0.1}`)},
		{"above one", []byte(`{"fraction":1.5}`)},
		{"NaN literal is invalid JSON", []byte(`{"fraction":NaN}`)},
		{"missing body", nil},
		{"empty object (missing fraction key)", []byte(`{}`)},
		{"other field only", []byte(`{"other":1}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeReadingSessionStore{}
			now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
			h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }}

			rr := postHeartbeat(t, h, 1, "profile-a", "book-1", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
			}
			if len(store.sessions) != 0 {
				t.Fatalf("expected store untouched, got %d sessions", len(store.sessions))
			}
		})
	}
}

func TestReadingHeartbeatAcceptsExplicitZeroFraction(t *testing.T) {
	store := &fakeReadingSessionStore{}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }}

	rr := postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0}`))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}
	if len(store.sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(store.sessions))
	}
	s := store.latestOpen(1, "profile-a", "book-1")
	if s == nil {
		t.Fatal("expected a session")
	}
	if s.StartFraction != 0.0 || s.EndFraction != 0.0 {
		t.Fatalf("fractions = %v/%v, want 0/0", s.StartFraction, s.EndFraction)
	}
}

func TestReadingHeartbeatExtendsAtExactSessionGap(t *testing.T) {
	store := &fakeReadingSessionStore{}
	t0 := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	now := t0
	h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }}

	rr := postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0.10}`))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}
	firstID := store.latestOpen(1, "profile-a", "book-1").ID

	// Exactly 120s later: should still extend the existing session (>= check).
	now = t0.Add(120 * time.Second)
	rr = postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0.20}`))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
	}
	if len(store.sessions) != 1 {
		t.Fatalf("sessions = %d, want 1 (should extend, not create new)", len(store.sessions))
	}
	s := store.latestOpen(1, "profile-a", "book-1")
	if s.ID != firstID {
		t.Fatalf("session id = %d, want extended session %d", s.ID, firstID)
	}
}

func TestReadingHeartbeatScopedToProfile(t *testing.T) {
	store := &fakeReadingSessionStore{}
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }}

	rrA := postHeartbeat(t, h, 1, "profile-a", "book-1", []byte(`{"fraction":0.10}`))
	if rrA.Code != http.StatusNoContent {
		t.Fatalf("profile A status = %d, want 204; body = %s", rrA.Code, rrA.Body.String())
	}
	rrB := postHeartbeat(t, h, 1, "profile-b", "book-1", []byte(`{"fraction":0.50}`))
	if rrB.Code != http.StatusNoContent {
		t.Fatalf("profile B status = %d, want 204; body = %s", rrB.Code, rrB.Body.String())
	}

	if len(store.sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 (one per profile)", len(store.sessions))
	}
	sessionA := store.latestOpen(1, "profile-a", "book-1")
	sessionB := store.latestOpen(1, "profile-b", "book-1")
	if sessionA == nil || sessionB == nil {
		t.Fatal("expected a session for each profile")
	}
	if sessionA.ID == sessionB.ID {
		t.Fatal("expected distinct sessions per profile")
	}
	if sessionA.StartFraction != 0.10 {
		t.Fatalf("profile A start fraction = %v, want 0.10", sessionA.StartFraction)
	}
	if sessionB.StartFraction != 0.50 {
		t.Fatalf("profile B start fraction = %v, want 0.50", sessionB.StartFraction)
	}
}
