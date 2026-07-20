package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
// slice, mirroring fakeReaderFontStore's style. The read-side rollup methods
// (PaceWindow, BookSeconds, DailyRollup, BookTotals, RecentSessions,
// TotalsSince) are backed by settable func fields so tests can supply canned
// responses without reimplementing the SQL rollup logic in memory; tests
// that don't touch them (e.g. the heartbeat tests) get harmless zero values.
type fakeReadingSessionStore struct {
	sessions []ReadingSession
	nextID   int64

	paceWindowFn     func(contentID string, since time.Time) (float64, int, error)
	bookSecondsFn    func(contentID string) (int, error)
	dailyRollupFn    func(from, to time.Time, loc *time.Location) ([]DayTotal, error)
	bookTotalsFn     func() ([]BookTotal, error)
	recentSessionsFn func(limit int) ([]SessionRow, error)
	totalsSinceFn    func(since time.Time, loc *time.Location) (int, error)
}

func (f *fakeReadingSessionStore) PaceWindow(_ context.Context, _ int, _, contentID string, since time.Time) (float64, int, error) {
	if f.paceWindowFn == nil {
		return 0, 0, nil
	}
	return f.paceWindowFn(contentID, since)
}

func (f *fakeReadingSessionStore) BookSeconds(_ context.Context, _ int, _, contentID string) (int, error) {
	if f.bookSecondsFn == nil {
		return 0, nil
	}
	return f.bookSecondsFn(contentID)
}

func (f *fakeReadingSessionStore) DailyRollup(_ context.Context, _ int, _ string, from, to time.Time, loc *time.Location) ([]DayTotal, error) {
	if f.dailyRollupFn == nil {
		return nil, nil
	}
	return f.dailyRollupFn(from, to, loc)
}

func (f *fakeReadingSessionStore) BookTotals(_ context.Context, _ int, _ string) ([]BookTotal, error) {
	if f.bookTotalsFn == nil {
		return nil, nil
	}
	return f.bookTotalsFn()
}

func (f *fakeReadingSessionStore) RecentSessions(_ context.Context, _ int, _ string, limit int) ([]SessionRow, error) {
	if f.recentSessionsFn == nil {
		return nil, nil
	}
	return f.recentSessionsFn(limit)
}

func (f *fakeReadingSessionStore) TotalsSince(_ context.Context, _ int, _ string, since time.Time, loc *time.Location) (int, error) {
	if f.totalsSinceFn == nil {
		return 0, nil
	}
	return f.totalsSinceFn(since, loc)
}

// fakeReadingProgressGetter is a settable fake for readingProgressGetter.
type fakeReadingProgressGetter struct {
	progress float64
	found    bool
	err      error
}

func (f *fakeReadingProgressGetter) GetProgress(_ context.Context, _ int, _, _ string) (float64, bool, error) {
	return f.progress, f.found, f.err
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

func TestPaceAndTimeLeftThresholds(t *testing.T) {
	t.Run("book pace wins when this-book data clears the 600s minimum", func(t *testing.T) {
		pace, timeLeft := paceAndTimeLeft(0.2, 1200, 0, 0, 0.5)
		if pace == nil {
			t.Fatal("expected non-nil pace")
		}
		if math.Abs(*pace-0.6) > 1e-9 {
			t.Fatalf("pace = %v, want 0.6", *pace)
		}
		if timeLeft == nil {
			t.Fatal("expected non-nil timeLeft")
		}
		if diff := *timeLeft - 3000; diff < -1 || diff > 1 {
			t.Fatalf("timeLeft = %d, want ~3000 (+/-1)", *timeLeft)
		}
	})

	t.Run("falls back to all-books pace when this-book data is under 600s", func(t *testing.T) {
		// Book has only 300s of data (below the 600s minimum); all-books
		// has 0.3 fractions over 3600s (clears the 1800s minimum), so the
		// all-books rate is used instead.
		pace, timeLeft := paceAndTimeLeft(0.05, 300, 0.3, 3600, 0.5)
		if pace == nil {
			t.Fatal("expected non-nil pace from all-books fallback")
		}
		if math.Abs(*pace-0.3) > 1e-9 {
			t.Fatalf("pace = %v, want 0.3 (all-books rate, not this-book rate)", *pace)
		}
		if timeLeft == nil {
			t.Fatal("expected non-nil timeLeft")
		}
	})

	t.Run("both under their minimums yields no estimate", func(t *testing.T) {
		pace, timeLeft := paceAndTimeLeft(0.05, 300, 0.1, 900, 0.5)
		if pace != nil || timeLeft != nil {
			t.Fatalf("pace/timeLeft = %v/%v, want nil/nil", pace, timeLeft)
		}
	})

	t.Run("zero or negative fraction deltas are guarded to nil", func(t *testing.T) {
		// This-book clears the 600s minimum but has a zero fraction delta;
		// all-books clears the 1800s minimum but has a negative delta.
		// Both are guarded against (no divide-by-zero, no nonsense negative
		// pace), so the overall result is nil.
		pace, timeLeft := paceAndTimeLeft(0, 1200, -0.1, 3600, 0.5)
		if pace != nil || timeLeft != nil {
			t.Fatalf("pace/timeLeft = %v/%v, want nil/nil", pace, timeLeft)
		}
	})
}

func TestReadingStatsBookHandler(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

	t.Run("computes pace, time-left, and book seconds from the store and progress", func(t *testing.T) {
		store := &fakeReadingSessionStore{
			paceWindowFn: func(contentID string, since time.Time) (float64, int, error) {
				if !since.Equal(now.Add(-14 * 24 * time.Hour)) {
					t.Fatalf("pace window since = %v, want now-14d", since)
				}
				if contentID == "book-1" {
					return 0.2, 1200, nil
				}
				return 0.9, 9000, nil // all-books window; unused since book-1 wins
			},
			bookSecondsFn: func(contentID string) (int, error) {
				if contentID != "book-1" {
					t.Fatalf("book seconds contentID = %q, want book-1", contentID)
				}
				return 4000, nil
			},
		}
		progress := &fakeReadingProgressGetter{progress: 0.5, found: true}
		h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }, Progress: progress}

		req := httptest.NewRequest(http.MethodGet, "/ebooks/book-1/reading-stats", nil)
		req = req.WithContext(readingSessionAuthContext(1, "profile-a"))
		req = withReadingSessionContentIDParam(req, "book-1")
		rr := httptest.NewRecorder()
		h.HandleBookStats(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
		}

		var resp struct {
			PaceFractionPerHour *float64 `json:"pace_fraction_per_hour"`
			TimeLeftSeconds     *int64   `json:"time_left_seconds"`
			BookSeconds         int      `json:"book_seconds"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v; body = %s", err, rr.Body.String())
		}
		if resp.PaceFractionPerHour == nil || math.Abs(*resp.PaceFractionPerHour-0.6) > 1e-9 {
			t.Fatalf("pace_fraction_per_hour = %v, want 0.6", resp.PaceFractionPerHour)
		}
		if resp.TimeLeftSeconds == nil {
			t.Fatal("expected non-nil time_left_seconds")
		}
		if resp.BookSeconds != 4000 {
			t.Fatalf("book_seconds = %d, want 4000", resp.BookSeconds)
		}
	})

	t.Run("no progress row and insufficient pace data yields nulls", func(t *testing.T) {
		store := &fakeReadingSessionStore{
			paceWindowFn: func(string, time.Time) (float64, int, error) { return 0, 100, nil },
			bookSecondsFn: func(string) (int, error) {
				return 0, nil
			},
		}
		progress := &fakeReadingProgressGetter{found: false}
		h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }, Progress: progress}

		req := httptest.NewRequest(http.MethodGet, "/ebooks/book-2/reading-stats", nil)
		req = req.WithContext(readingSessionAuthContext(1, "profile-a"))
		req = withReadingSessionContentIDParam(req, "book-2")
		rr := httptest.NewRecorder()
		h.HandleBookStats(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
		}

		var resp struct {
			PaceFractionPerHour *float64 `json:"pace_fraction_per_hour"`
			TimeLeftSeconds     *int64   `json:"time_left_seconds"`
			BookSeconds         int      `json:"book_seconds"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v; body = %s", err, rr.Body.String())
		}
		if resp.PaceFractionPerHour != nil {
			t.Fatalf("pace_fraction_per_hour = %v, want null", *resp.PaceFractionPerHour)
		}
		if resp.TimeLeftSeconds != nil {
			t.Fatalf("time_left_seconds = %v, want null", *resp.TimeLeftSeconds)
		}
		if resp.BookSeconds != 0 {
			t.Fatalf("book_seconds = %d, want 0", resp.BookSeconds)
		}
	})
}

func TestReadingStatsHistoryHandler(t *testing.T) {
	now := time.Date(2026, 7, 20, 15, 30, 0, 0, time.UTC)
	today := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)

	manySessions := make([]SessionRow, 60)
	for i := range manySessions {
		manySessions[i] = SessionRow{
			ContentID:       fmt.Sprintf("book-%d", i),
			Title:           fmt.Sprintf("Book %d", i),
			StartedAt:       today.Add(-time.Duration(i) * time.Hour),
			DurationSeconds: 60,
			StartFraction:   0.1,
			EndFraction:     0.2,
		}
	}

	var capturedFrom, capturedTo time.Time
	var capturedLimit int
	newStore := func() *fakeReadingSessionStore {
		return &fakeReadingSessionStore{
			totalsSinceFn: func(since time.Time, _ *time.Location) (int, error) {
				switch {
				case since.Equal(today):
					return 100, nil
				case since.Equal(today.AddDate(0, 0, -6)):
					return 500, nil
				case since.Equal(today.AddDate(0, 0, -29)):
					return 2000, nil
				case since.IsZero():
					return 9000, nil
				default:
					return 0, fmt.Errorf("unexpected TotalsSince(since=%v)", since)
				}
			},
			dailyRollupFn: func(from, to time.Time, _ *time.Location) ([]DayTotal, error) {
				capturedFrom, capturedTo = from, to
				return []DayTotal{
					{Date: today.AddDate(0, 0, -1), Seconds: 200},
					{Date: today, Seconds: 100},
				}, nil
			},
			bookTotalsFn: func() ([]BookTotal, error) {
				return []BookTotal{
					{ContentID: "book-1", Title: "The Hobbit", Seconds: 3000, LastReadAt: today},
					{ContentID: "book-2", Title: "", Seconds: 1000, LastReadAt: today.AddDate(0, 0, -2)},
				}, nil
			},
			recentSessionsFn: func(limit int) ([]SessionRow, error) {
				capturedLimit = limit
				if limit < len(manySessions) {
					return manySessions[:limit], nil
				}
				return manySessions, nil
			},
		}
	}

	t.Run("default range, totals, title fallback, and session cap", func(t *testing.T) {
		store := newStore()
		h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }}

		req := httptest.NewRequest(http.MethodGet, "/ebooks/reading-stats", nil)
		req = req.WithContext(readingSessionAuthContext(1, "profile-a"))
		rr := httptest.NewRecorder()
		h.HandleHistory(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
		}

		var resp struct {
			Totals struct {
				TodaySeconds   int `json:"today_seconds"`
				WeekSeconds    int `json:"week_seconds"`
				MonthSeconds   int `json:"month_seconds"`
				AllTimeSeconds int `json:"all_time_seconds"`
			} `json:"totals"`
			Days []struct {
				Date    string `json:"date"`
				Seconds int    `json:"seconds"`
			} `json:"days"`
			Books []struct {
				ContentID  string `json:"content_id"`
				Title      string `json:"title"`
				Seconds    int    `json:"seconds"`
				LastReadAt string `json:"last_read_at"`
			} `json:"books"`
			Sessions []struct {
				ContentID       string  `json:"content_id"`
				Title           string  `json:"title"`
				StartedAt       string  `json:"started_at"`
				DurationSeconds int     `json:"duration_seconds"`
				StartFraction   float64 `json:"start_fraction"`
				EndFraction     float64 `json:"end_fraction"`
			} `json:"sessions"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v; body = %s", err, rr.Body.String())
		}

		if resp.Totals.TodaySeconds != 100 || resp.Totals.WeekSeconds != 500 ||
			resp.Totals.MonthSeconds != 2000 || resp.Totals.AllTimeSeconds != 9000 {
			t.Fatalf("totals = %+v, want today=100 week=500 month=2000 all_time=9000", resp.Totals)
		}

		wantFrom := today.AddDate(0, 0, -365)
		if !capturedFrom.Equal(wantFrom) {
			t.Fatalf("DailyRollup from = %v, want %v (default range = last 366 days)", capturedFrom, wantFrom)
		}
		if !capturedTo.Equal(today) {
			t.Fatalf("DailyRollup to = %v, want %v", capturedTo, today)
		}

		if len(resp.Days) != 2 {
			t.Fatalf("days = %d, want 2", len(resp.Days))
		}

		if len(resp.Books) != 2 {
			t.Fatalf("books = %d, want 2", len(resp.Books))
		}
		if resp.Books[0].Title != "The Hobbit" {
			t.Fatalf("books[0].title = %q, want %q", resp.Books[0].Title, "The Hobbit")
		}
		if resp.Books[1].Title != "Removed book" {
			t.Fatalf("books[1].title = %q, want fallback %q for an empty joined title", resp.Books[1].Title, "Removed book")
		}

		if capturedLimit != 50 {
			t.Fatalf("RecentSessions limit = %d, want 50", capturedLimit)
		}
		if len(resp.Sessions) != 50 {
			t.Fatalf("sessions = %d, want capped at 50 (fixture has 60)", len(resp.Sessions))
		}
	})

	t.Run("bad from/to query params are rejected", func(t *testing.T) {
		store := newStore()
		h := &ReadingSessionsHandler{Store: store, Now: func() time.Time { return now }}

		for _, qs := range []string{"from=not-a-date", "to=also-not-a-date", "from=2026-13-40"} {
			req := httptest.NewRequest(http.MethodGet, "/ebooks/reading-stats?"+qs, nil)
			req = req.WithContext(readingSessionAuthContext(1, "profile-a"))
			rr := httptest.NewRecorder()
			h.HandleHistory(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("qs=%q: status = %d, want 400; body = %s", qs, rr.Code, rr.Body.String())
			}
		}
	})
}
