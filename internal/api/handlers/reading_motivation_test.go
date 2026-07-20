package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeReadingMotivationStore is an in-memory ReadingMotivationStore backed
// by maps, mirroring fakeReadingSessionStore's style. Only the goals
// methods are exercised by this task's tests; the remaining methods are
// present to satisfy the interface for later tasks.
type fakeReadingMotivationStore struct {
	goals map[string]ReadingGoals

	putGoalsErr error

	achievements map[string]time.Time
	sessions     []ReadingSession
}

func goalsKey(userID int, profileID string) string {
	return fmt.Sprintf("%d|%s", userID, profileID)
}

func (f *fakeReadingMotivationStore) GetGoals(_ context.Context, userID int, profileID string) (*ReadingGoals, error) {
	if f.goals == nil {
		return nil, nil
	}
	g, ok := f.goals[goalsKey(userID, profileID)]
	if !ok {
		return nil, nil
	}
	cp := g
	return &cp, nil
}

func (f *fakeReadingMotivationStore) PutGoals(_ context.Context, userID int, profileID string, g ReadingGoals) error {
	if f.putGoalsErr != nil {
		return f.putGoalsErr
	}
	if f.goals == nil {
		f.goals = make(map[string]ReadingGoals)
	}
	f.goals[goalsKey(userID, profileID)] = g
	return nil
}

func (f *fakeReadingMotivationStore) AchievedAt(_ context.Context, _ int, _ string) (map[string]time.Time, error) {
	return f.achievements, nil
}

func (f *fakeReadingMotivationStore) PersistAchievement(_ context.Context, _ int, _, achievementID string, at time.Time) error {
	if f.achievements == nil {
		f.achievements = make(map[string]time.Time)
	}
	f.achievements[achievementID] = at
	return nil
}

func (f *fakeReadingMotivationStore) SessionsSince(_ context.Context, _ int, _ string, _ time.Time) ([]ReadingSession, error) {
	return f.sessions, nil
}

func (f *fakeReadingMotivationStore) FinishedBooksInRange(_ context.Context, _ int, _ string, _, _ time.Time) (int, error) {
	return 0, nil
}

func (f *fakeReadingMotivationStore) GenreSeconds(_ context.Context, _ int, _ string) ([]GenreSeconds, error) {
	return nil, nil
}

func (f *fakeReadingMotivationStore) AuthorSeconds(_ context.Context, _ int, _ string) ([]AuthorSeconds, error) {
	return nil, nil
}

func TestRequestLocation(t *testing.T) {
	cases := []struct {
		name   string
		tz     string
		wantID string
	}{
		{"named IANA zone", "Europe/Amsterdam", "Europe/Amsterdam"},
		{"absent", "", "UTC"},
		{"nonsense zone", "Nonsense", "UTC"},
		{"Local is rejected", "Local", "UTC"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "/ebooks/reading-stats"
			if tc.tz != "" {
				url += "?tz=" + tc.tz
			}
			req := httptest.NewRequest(http.MethodGet, url, nil)
			loc := requestLocation(req)
			if loc == nil {
				t.Fatal("expected a non-nil location")
			}
			if loc.String() != tc.wantID {
				t.Fatalf("location = %q, want %q", loc.String(), tc.wantID)
			}
		})
	}
}

func putGoals(t *testing.T, h *ReadingMotivationHandler, userID int, profileID string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(http.MethodPut, "/ebooks/reading-goals", nil)
	} else {
		req = httptest.NewRequest(http.MethodPut, "/ebooks/reading-goals", bytes.NewReader(body))
	}
	req = req.WithContext(readingSessionAuthContext(userID, profileID))
	rr := httptest.NewRecorder()
	h.HandlePutGoals(rr, req)
	return rr
}

func TestPutGoalsValidatesAndPersists(t *testing.T) {
	t.Run("valid goals are stored and 204", func(t *testing.T) {
		store := &fakeReadingMotivationStore{}
		h := &ReadingMotivationHandler{Store: store, Now: func() time.Time { return time.Now() }}

		rr := putGoals(t, h, 1, "profile-a", []byte(`{"books_per_year":30,"hours_per_year":200}`))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204; body = %s", rr.Code, rr.Body.String())
		}
		got, err := store.GetGoals(context.Background(), 1, "profile-a")
		if err != nil {
			t.Fatalf("GetGoals: %v", err)
		}
		if got == nil || got.BooksPerYear == nil || *got.BooksPerYear != 30 {
			t.Fatalf("books_per_year = %+v, want 30", got)
		}
		if got.HoursPerYear == nil || *got.HoursPerYear != 200 {
			t.Fatalf("hours_per_year = %+v, want 200", got)
		}
	})

	t.Run("PUT replaces both fields; absent field clears it", func(t *testing.T) {
		store := &fakeReadingMotivationStore{}
		h := &ReadingMotivationHandler{Store: store, Now: func() time.Time { return time.Now() }}

		rr := putGoals(t, h, 1, "profile-a", []byte(`{"books_per_year":30,"hours_per_year":200}`))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("first PUT status = %d, want 204; body = %s", rr.Code, rr.Body.String())
		}

		// books_per_year explicitly null, hours_per_year present: books
		// cleared, hours set to 200.
		rr = putGoals(t, h, 1, "profile-a", []byte(`{"books_per_year":null,"hours_per_year":200}`))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("second PUT status = %d, want 204; body = %s", rr.Code, rr.Body.String())
		}
		got, err := store.GetGoals(context.Background(), 1, "profile-a")
		if err != nil {
			t.Fatalf("GetGoals: %v", err)
		}
		if got.BooksPerYear != nil {
			t.Fatalf("books_per_year = %v, want cleared (nil)", *got.BooksPerYear)
		}
		if got.HoursPerYear == nil || *got.HoursPerYear != 200 {
			t.Fatalf("hours_per_year = %+v, want 200", got.HoursPerYear)
		}

		// A third PUT omitting hours_per_year entirely (absent = null =
		// cleared) must clear it too, since PUT replaces both fields
		// wholesale rather than patching.
		rr = putGoals(t, h, 1, "profile-a", []byte(`{"books_per_year":50}`))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("third PUT status = %d, want 204; body = %s", rr.Code, rr.Body.String())
		}
		got, err = store.GetGoals(context.Background(), 1, "profile-a")
		if err != nil {
			t.Fatalf("GetGoals: %v", err)
		}
		if got.BooksPerYear == nil || *got.BooksPerYear != 50 {
			t.Fatalf("books_per_year = %+v, want 50", got.BooksPerYear)
		}
		if got.HoursPerYear != nil {
			t.Fatalf("hours_per_year = %v, want cleared (nil) since the PUT omitted it", *got.HoursPerYear)
		}
	})

	t.Run("out-of-range values are rejected with field-named messages", func(t *testing.T) {
		cases := []struct {
			name       string
			body       []byte
			wantInBody string
		}{
			{"books_per_year zero", []byte(`{"books_per_year":0}`), "books_per_year"},
			{"hours_per_year above max", []byte(`{"hours_per_year":100001}`), "hours_per_year"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				store := &fakeReadingMotivationStore{}
				h := &ReadingMotivationHandler{Store: store, Now: func() time.Time { return time.Now() }}

				rr := putGoals(t, h, 1, "profile-a", tc.body)
				if rr.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
				}
				if !bytes.Contains(rr.Body.Bytes(), []byte(tc.wantInBody)) {
					t.Fatalf("body = %s, want it to mention %q", rr.Body.String(), tc.wantInBody)
				}
				if len(store.goals) != 0 {
					t.Fatalf("expected store untouched on validation failure, got %v", store.goals)
				}
			})
		}
	})

	t.Run("non-JSON body is a 400", func(t *testing.T) {
		store := &fakeReadingMotivationStore{}
		h := &ReadingMotivationHandler{Store: store, Now: func() time.Time { return time.Now() }}

		rr := putGoals(t, h, 1, "profile-a", []byte(`not json`))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
		}
	})
}

// TestHistoryUsesRequestTimezone pins the additive tz behavior on the
// existing history endpoint: a session at 2026-07-19T23:30:00Z lands on
// local day 2026-07-20 in Europe/Amsterdam (UTC+2 in July) but 2026-07-19 in
// UTC. The fake's dailyRollupFn mimics the real DailyRollup's
// date_trunc('day', started_at AT TIME ZONE $n) bucketing by converting the
// fixed session timestamp into the requested location itself.
func TestHistoryUsesRequestTimezone(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	sessionStart := time.Date(2026, 7, 19, 23, 30, 0, 0, time.UTC)

	newStore := func() *fakeReadingSessionStore {
		return &fakeReadingSessionStore{
			totalsSinceFn: func(_ time.Time, _ *time.Location) (int, error) { return 0, nil },
			dailyRollupFn: func(_, _ time.Time, loc *time.Location) ([]DayTotal, error) {
				local := sessionStart.In(loc)
				day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
				return []DayTotal{{Date: day, Seconds: 1800}}, nil
			},
		}
	}

	runHistory := func(url string) []struct {
		Date    string `json:"date"`
		Seconds int    `json:"seconds"`
	} {
		h := &ReadingSessionsHandler{Store: newStore(), Now: func() time.Time { return now }}
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req = req.WithContext(readingSessionAuthContext(1, "profile-a"))
		rr := httptest.NewRecorder()
		h.HandleHistory(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
		}
		var resp struct {
			Days []struct {
				Date    string `json:"date"`
				Seconds int    `json:"seconds"`
			} `json:"days"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode response: %v; body = %s", err, rr.Body.String())
		}
		return resp.Days
	}

	daysUTC := runHistory("/ebooks/reading-stats")
	daysAmsterdam := runHistory("/ebooks/reading-stats?tz=Europe/Amsterdam")

	if len(daysUTC) != 1 || len(daysAmsterdam) != 1 {
		t.Fatalf("expected exactly one day in each response; utc=%v amsterdam=%v", daysUTC, daysAmsterdam)
	}
	if daysUTC[0].Date != "2026-07-19" {
		t.Fatalf("UTC day = %q, want 2026-07-19", daysUTC[0].Date)
	}
	if daysAmsterdam[0].Date != "2026-07-20" {
		t.Fatalf("Europe/Amsterdam day = %q, want 2026-07-20", daysAmsterdam[0].Date)
	}
	if daysUTC[0].Date == daysAmsterdam[0].Date {
		t.Fatal("expected the days rollup date strings to differ between UTC and Europe/Amsterdam")
	}
}
