package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"
)

// fakeReadingMotivationStore is an in-memory ReadingMotivationStore backed
// by maps, mirroring fakeReadingSessionStore's style.
type fakeReadingMotivationStore struct {
	goals map[string]ReadingGoals

	putGoalsErr error

	achievements  map[string]time.Time
	achievedAtErr error

	sessions    []ReadingSession
	sessionsErr error

	finishedBooks    int
	finishedBooksErr error

	genres    []GenreSeconds
	genresErr error

	authors    []AuthorSeconds
	authorsErr error

	// persistCalls records the achievement ids PersistAchievement was
	// invoked with, in call order, so tests can assert only newly
	// satisfied badges trigger a persist.
	persistCalls []string
	persistErr   error
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
	if f.achievedAtErr != nil {
		return nil, f.achievedAtErr
	}
	return f.achievements, nil
}

func (f *fakeReadingMotivationStore) PersistAchievement(_ context.Context, _ int, _, achievementID string, at time.Time) error {
	if f.persistErr != nil {
		return f.persistErr
	}
	f.persistCalls = append(f.persistCalls, achievementID)
	if f.achievements == nil {
		f.achievements = make(map[string]time.Time)
	}
	f.achievements[achievementID] = at
	return nil
}

func (f *fakeReadingMotivationStore) SessionsSince(_ context.Context, _ int, _ string, _ time.Time) ([]ReadingSession, error) {
	if f.sessionsErr != nil {
		return nil, f.sessionsErr
	}
	return f.sessions, nil
}

func (f *fakeReadingMotivationStore) FinishedBooksInRange(_ context.Context, _ int, _ string, _, _ time.Time) (int, error) {
	if f.finishedBooksErr != nil {
		return 0, f.finishedBooksErr
	}
	return f.finishedBooks, nil
}

func (f *fakeReadingMotivationStore) GenreSeconds(_ context.Context, _ int, _ string) ([]GenreSeconds, error) {
	if f.genresErr != nil {
		return nil, f.genresErr
	}
	return f.genres, nil
}

func (f *fakeReadingMotivationStore) AuthorSeconds(_ context.Context, _ int, _ string) ([]AuthorSeconds, error) {
	if f.authorsErr != nil {
		return nil, f.authorsErr
	}
	return f.authors, nil
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

func TestSessionDaySecondsUsesLocation(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	// 2026-07-19T23:30:00Z is CEST (UTC+2) in Amsterdam, so local time is
	// 2026-07-20T01:30 -- the next day.
	sessions := []ReadingSession{
		{StartedAt: time.Date(2026, 7, 19, 23, 30, 0, 0, time.UTC), DurationSeconds: 900},
	}

	utcDays := sessionDaySeconds(sessions, time.UTC)
	if utcDays["2026-07-19"] != 900 {
		t.Fatalf("UTC bucketing = %v, want 2026-07-19: 900", utcDays)
	}

	amsDays := sessionDaySeconds(sessions, loc)
	if amsDays["2026-07-20"] != 900 {
		t.Fatalf("Europe/Amsterdam bucketing = %v, want 2026-07-20: 900", amsDays)
	}
	if _, ok := amsDays["2026-07-19"]; ok {
		t.Fatalf("Europe/Amsterdam bucketing = %v, did not expect a 2026-07-19 entry", amsDays)
	}
}

func TestStreakMath(t *testing.T) {
	const today = "2026-07-20"
	const dMinus1 = "2026-07-19"
	const dMinus2 = "2026-07-18"
	const dMinus3 = "2026-07-17"

	cases := []struct {
		name        string
		days        DaySeconds
		today       string
		wantCurrent int
		wantLongest int
	}{
		{
			name:        "today unqualified but alive off yesterday",
			days:        DaySeconds{dMinus2: 400, dMinus1: 400, today: 100},
			today:       today,
			wantCurrent: 2,
			wantLongest: 2,
		},
		{
			name:        "today qualified extends the streak",
			days:        DaySeconds{dMinus1: 400, today: 400},
			today:       today,
			wantCurrent: 2,
			wantLongest: 2,
		},
		{
			name:        "gap breaks the streak but an earlier run still counts toward longest",
			days:        DaySeconds{dMinus3: 400, dMinus1: 400, today: 400},
			today:       today,
			wantCurrent: 2,
			wantLongest: 2,
		},
		{
			name:        "sub-300s days never qualify",
			days:        DaySeconds{today: 299},
			today:       today,
			wantCurrent: 0,
			wantLongest: 0,
		},
		{
			name:        "empty days",
			days:        DaySeconds{},
			today:       today,
			wantCurrent: 0,
			wantLongest: 0,
		},
		{
			name:        "both today and yesterday missed kills the streak even with older history",
			days:        DaySeconds{dMinus3: 400, dMinus2: 0},
			today:       today,
			wantCurrent: 0,
			wantLongest: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current, longest := streakFrom(tc.days, tc.today)
			if current != tc.wantCurrent {
				t.Errorf("current = %d, want %d", current, tc.wantCurrent)
			}
			if longest != tc.wantLongest {
				t.Errorf("longest = %d, want %d", longest, tc.wantLongest)
			}
		})
	}
}

func TestMonthChallenge(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, loc)

	t.Run("previous month above the floor sets the target", func(t *testing.T) {
		days := DaySeconds{"2026-06-15": 20000}
		target, month := monthChallenge(days, now, loc)
		if target != 20000 {
			t.Errorf("target = %d, want 20000", target)
		}
		if month != 0 {
			t.Errorf("month = %d, want 0", month)
		}
	})

	t.Run("previous month below the floor is clamped to it", func(t *testing.T) {
		days := DaySeconds{"2026-06-15": 3600}
		target, _ := monthChallenge(days, now, loc)
		if target != monthChallengeFloorSeconds {
			t.Errorf("target = %d, want %d", target, monthChallengeFloorSeconds)
		}
	})

	t.Run("month boundaries are drawn in loc, not UTC", func(t *testing.T) {
		amsterdam, err := time.LoadLocation("Europe/Amsterdam")
		if err != nil {
			t.Fatalf("load location: %v", err)
		}
		// 2026-06-30T23:30:00Z is CEST (UTC+2), so locally it's
		// 2026-07-01T01:30 -- the first day of July, not the last day of
		// June. It must count toward the current month, not the previous
		// one used to derive the target.
		sessions := []ReadingSession{
			{StartedAt: time.Date(2026, 6, 30, 23, 30, 0, 0, time.UTC), DurationSeconds: 1800},
		}
		days := sessionDaySeconds(sessions, amsterdam)
		nowAmsterdam := now.In(amsterdam)

		target, month := monthChallenge(days, nowAmsterdam, amsterdam)
		if month != 1800 {
			t.Errorf("month = %d, want 1800 (the boundary session counted as July)", month)
		}
		if target != monthChallengeFloorSeconds {
			t.Errorf("target = %d, want the floor %d (June had no seconds)", target, monthChallengeFloorSeconds)
		}
	})
}

func TestComputeDNA(t *testing.T) {
	t.Run("diversity: single genre scores 0", func(t *testing.T) {
		dna := computeDNA(nil, []GenreSeconds{{Genre: "Sci-Fi", Seconds: 100}}, nil, time.Now(), time.UTC)
		if dna.DiversityScore != 0 {
			t.Errorf("diversity = %d, want 0", dna.DiversityScore)
		}
	})

	t.Run("diversity: two equal genres scores 50", func(t *testing.T) {
		genres := []GenreSeconds{{Genre: "Sci-Fi", Seconds: 100}, {Genre: "Fantasy", Seconds: 100}}
		dna := computeDNA(nil, genres, nil, time.Now(), time.UTC)
		if dna.DiversityScore != 50 {
			t.Errorf("diversity = %d, want 50", dna.DiversityScore)
		}
	})

	t.Run("hour buckets, average session length, and year-end projection", func(t *testing.T) {
		loc := time.UTC
		now := time.Date(2026, 7, 2, 0, 0, 0, 0, loc)

		mk := func(hour int) ReadingSession {
			return ReadingSession{
				StartedAt:       time.Date(2026, 6, 1, hour, 0, 0, 0, loc),
				DurationSeconds: 3600,
			}
		}
		sessions := []ReadingSession{
			mk(5),  // morning, lower boundary
			mk(11), // morning, upper boundary
			mk(12), // afternoon, lower boundary
			mk(16), // afternoon, upper boundary
			mk(17), // evening, lower boundary
			mk(21), // evening, upper boundary
			mk(22), // night, lower boundary
			mk(4),  // night, upper boundary (wraps past midnight)
		}

		dna := computeDNA(sessions, nil, nil, now, loc)

		wantBuckets := map[string]int{"morning": 2, "afternoon": 2, "evening": 2, "night": 2}
		for bucket, want := range wantBuckets {
			if dna.HoursByBucket[bucket] != want {
				t.Errorf("HoursByBucket[%q] = %d, want %d (buckets: %v)", bucket, dna.HoursByBucket[bucket], want, dna.HoursByBucket)
			}
		}

		if dna.AvgSessionSeconds != 3600 {
			t.Errorf("AvgSessionSeconds = %d, want 3600", dna.AvgSessionSeconds)
		}

		yearStart := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
		nextYearStart := time.Date(2027, 1, 1, 0, 0, 0, 0, loc)
		elapsedFraction := float64(now.Sub(yearStart)) / float64(nextYearStart.Sub(yearStart))
		ytdHours := 8.0 // 8 one-hour sessions, all in 2026
		wantProjected := goalProjection(ytdHours, elapsedFraction)

		if diff := dna.ProjectedYearHours - wantProjected; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("ProjectedYearHours = %v, want %v", dna.ProjectedYearHours, wantProjected)
		}
	})
}

func TestEvaluateAchievements(t *testing.T) {
	base := func() achievementInput { return achievementInput{} }

	cases := []struct {
		id    string
		atMin func() achievementInput
		below func() achievementInput
	}{
		{"first-hour", func() achievementInput { in := base(); in.TotalSeconds = 3600; return in }, func() achievementInput { in := base(); in.TotalSeconds = 3599; return in }},
		{"ten-hours", func() achievementInput { in := base(); in.TotalSeconds = 36000; return in }, func() achievementInput { in := base(); in.TotalSeconds = 35999; return in }},
		{"fifty-hours", func() achievementInput { in := base(); in.TotalSeconds = 180000; return in }, func() achievementInput { in := base(); in.TotalSeconds = 179999; return in }},
		{"hundred-hours", func() achievementInput { in := base(); in.TotalSeconds = 360000; return in }, func() achievementInput { in := base(); in.TotalSeconds = 359999; return in }},
		{"marathon-session", func() achievementInput { in := base(); in.LongestSessionSeconds = 7200; return in }, func() achievementInput { in := base(); in.LongestSessionSeconds = 7199; return in }},
		{"streak-3", func() achievementInput { in := base(); in.LongestStreak = 3; return in }, func() achievementInput { in := base(); in.LongestStreak = 2; return in }},
		{"streak-7", func() achievementInput { in := base(); in.LongestStreak = 7; return in }, func() achievementInput { in := base(); in.LongestStreak = 6; return in }},
		{"streak-30", func() achievementInput { in := base(); in.LongestStreak = 30; return in }, func() achievementInput { in := base(); in.LongestStreak = 29; return in }},
		{"streak-100", func() achievementInput { in := base(); in.LongestStreak = 100; return in }, func() achievementInput { in := base(); in.LongestStreak = 99; return in }},
		{"first-book", func() achievementInput { in := base(); in.BooksFinished = 1; return in }, func() achievementInput { in := base(); in.BooksFinished = 0; return in }},
		{"ten-books", func() achievementInput { in := base(); in.BooksFinished = 10; return in }, func() achievementInput { in := base(); in.BooksFinished = 9; return in }},
		{"fifty-books", func() achievementInput { in := base(); in.BooksFinished = 50; return in }, func() achievementInput { in := base(); in.BooksFinished = 49; return in }},
		{"night-owl", func() achievementInput { in := base(); in.NightSeconds = 36000; return in }, func() achievementInput { in := base(); in.NightSeconds = 35999; return in }},
		{"early-bird", func() achievementInput { in := base(); in.EarlyBirdSeconds = 36000; return in }, func() achievementInput { in := base(); in.EarlyBirdSeconds = 35999; return in }},
		{"weekender", func() achievementInput { in := base(); in.WeekendSeconds = 72000; return in }, func() achievementInput { in := base(); in.WeekendSeconds = 71999; return in }},
		{"genre-hopper", func() achievementInput { in := base(); in.DistinctGenres = 5; return in }, func() achievementInput { in := base(); in.DistinctGenres = 4; return in }},
		{"deep-diver", func() achievementInput { in := base(); in.MaxBookSeconds = 36000; return in }, func() achievementInput { in := base(); in.MaxBookSeconds = 35999; return in }},
		{"finisher", func() achievementInput { in := base(); in.FinishedWithHighRead = true; return in }, func() achievementInput { in := base(); in.FinishedWithHighRead = false; return in }},
	}

	if len(cases) != 18 {
		t.Fatalf("test table has %d cases, want 18 (one per badge)", len(cases))
	}

	contains := func(ids []string, id string) bool {
		for _, got := range ids {
			if got == id {
				return true
			}
		}
		return false
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			atMin := evaluateAchievements(tc.atMin())
			if !contains(atMin, tc.id) {
				t.Errorf("at threshold: ids = %v, want %q present", atMin, tc.id)
			}

			below := evaluateAchievements(tc.below())
			if contains(below, tc.id) {
				t.Errorf("below threshold: ids = %v, want %q absent", below, tc.id)
			}
		})
	}

	t.Run("18 definitions with the exact spec ids", func(t *testing.T) {
		wantIDs := make([]string, len(cases))
		for i, tc := range cases {
			wantIDs[i] = tc.id
		}
		sort.Strings(wantIDs)

		if len(achievementDefinitions) != 18 {
			t.Fatalf("len(achievementDefinitions) = %d, want 18", len(achievementDefinitions))
		}
		gotIDs := make([]string, len(achievementDefinitions))
		for i, def := range achievementDefinitions {
			if def.Category == "" || def.Name == "" || def.Description == "" {
				t.Errorf("achievementDefinitions[%d] (%s) has an empty Category/Name/Description", i, def.ID)
			}
			gotIDs[i] = def.ID
		}
		sort.Strings(gotIDs)

		if len(gotIDs) != len(wantIDs) {
			t.Fatalf("id count mismatch: got %v want %v", gotIDs, wantIDs)
		}
		for i := range gotIDs {
			if gotIDs[i] != wantIDs[i] {
				t.Errorf("achievementDefinitions ids = %v, want (sorted) %v", gotIDs, wantIDs)
			}
		}
	})
}

// motivationRequest builds an authenticated GET request against the
// reading-motivation endpoint, optionally with a raw query string (e.g.
// "tz=Europe/Amsterdam").
func motivationRequest(userID int, profileID, rawQuery string) *http.Request {
	url := "/ebooks/reading-motivation"
	if rawQuery != "" {
		url += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	return req.WithContext(readingSessionAuthContext(userID, profileID))
}

// assertKeys unmarshals raw as a JSON object and fails the test if its key
// set isn't exactly want (order-independent).
func assertKeys(t *testing.T, raw json.RawMessage, want []string) {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal object: %v; raw = %s", err, raw)
	}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Errorf("missing key %q; raw = %s", k, raw)
		}
	}
	if len(m) != len(want) {
		got := make([]string, 0, len(m))
		for k := range m {
			got = append(got, k)
		}
		sort.Strings(got)
		t.Errorf("keys = %v, want exactly %v", got, want)
	}
}

func TestMotivationEndpointShape(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	store := &fakeReadingMotivationStore{
		sessions: []ReadingSession{
			{ContentID: "book-1", StartedAt: now.Add(-2 * time.Hour), DurationSeconds: 3600},
		},
		goals:         map[string]ReadingGoals{},
		achievements:  map[string]time.Time{"first-hour": now.Add(-24 * time.Hour)},
		finishedBooks: 3,
		genres:        []GenreSeconds{{Genre: "Sci-Fi", Seconds: 3600}},
		authors:       []AuthorSeconds{{Author: "Jane Doe", Seconds: 3600}},
	}
	books, hours := 20, 100
	store.goals[goalsKey(1, "profile-a")] = ReadingGoals{BooksPerYear: &books, HoursPerYear: &hours}

	h := &ReadingMotivationHandler{Store: store, Now: func() time.Time { return now }}
	rr := httptest.NewRecorder()
	h.HandleGetMotivation(rr, motivationRequest(1, "profile-a", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &top); err != nil {
		t.Fatalf("unmarshal top level: %v; body = %s", err, rr.Body.String())
	}
	wantTop := []string{"streak", "goals", "challenge", "achievements", "dna"}
	for _, k := range wantTop {
		if _, ok := top[k]; !ok {
			t.Errorf("missing top-level key %q; body = %s", k, rr.Body.String())
		}
	}
	if len(top) != len(wantTop) {
		t.Errorf("top-level keys = %v, want exactly %v", top, wantTop)
	}

	assertKeys(t, top["streak"], []string{"current_days", "longest_days", "today_seconds", "today_qualified"})
	assertKeys(t, top["goals"], []string{
		"books_per_year", "hours_per_year", "books_finished_ytd",
		"hours_ytd", "books_on_track_for", "hours_on_track_for",
	})
	assertKeys(t, top["challenge"], []string{"target_seconds", "month_seconds", "percent"})
	assertKeys(t, top["dna"], []string{
		"genres", "authors", "diversity_score", "avg_session_seconds",
		"hours_by_bucket", "projected_year_hours",
	})

	var achievements []map[string]json.RawMessage
	if err := json.Unmarshal(top["achievements"], &achievements); err != nil {
		t.Fatalf("unmarshal achievements: %v", err)
	}
	if len(achievements) != 18 {
		t.Fatalf("len(achievements) = %d, want 18", len(achievements))
	}
	for i, a := range achievements {
		for _, k := range []string{"id", "category", "name", "description", "achieved_at"} {
			if _, ok := a[k]; !ok {
				t.Errorf("achievements[%d] missing key %q: %v", i, k, a)
			}
		}
	}
}

// TestMotivationPersistsNewUnlocks fixtures three consecutive qualifying
// days (>=300s each) totaling 3900s, which satisfies both "first-hour"
// (>=3600s total) and "streak-3" (LongestStreak>=3). The store already has
// "first-hour" persisted, so only "streak-3" should trigger a new
// PersistAchievement call; the response must show both as achieved.
func TestMotivationPersistsNewUnlocks(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	earlierUnlock := now.Add(-48 * time.Hour)

	store := &fakeReadingMotivationStore{
		sessions: []ReadingSession{
			{ContentID: "book-1", StartedAt: time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC), DurationSeconds: 1300},
			{ContentID: "book-1", StartedAt: time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC), DurationSeconds: 1300},
			{ContentID: "book-1", StartedAt: time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC), DurationSeconds: 1300},
		},
		achievements: map[string]time.Time{"first-hour": earlierUnlock},
	}

	h := &ReadingMotivationHandler{Store: store, Now: func() time.Time { return now }}
	rr := httptest.NewRecorder()
	h.HandleGetMotivation(rr, motivationRequest(1, "profile-a", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
	}

	if len(store.persistCalls) != 1 || store.persistCalls[0] != "streak-3" {
		t.Fatalf("persistCalls = %v, want exactly [\"streak-3\"]", store.persistCalls)
	}

	var resp readingMotivationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body = %s", err, rr.Body.String())
	}
	var gotFirstHour, gotStreak3 bool
	for _, a := range resp.Achievements {
		switch a.ID {
		case "first-hour":
			gotFirstHour = a.AchievedAt != nil
		case "streak-3":
			gotStreak3 = a.AchievedAt != nil
		}
	}
	if !gotFirstHour {
		t.Error("first-hour should show as achieved in the response")
	}
	if !gotStreak3 {
		t.Error("streak-3 should show as newly achieved in the response")
	}
}

// TestMotivationDegradesPerSection pins the per-section degradation
// contract: a failing store call for one section never 500s the whole
// request, and only that section falls back to its zero value.
func TestMotivationDegradesPerSection(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	store := &fakeReadingMotivationStore{
		sessions: []ReadingSession{
			{ContentID: "book-1", StartedAt: now.Add(-2 * time.Hour), DurationSeconds: 3600},
		},
		authors:       []AuthorSeconds{{Author: "Jane Doe", Seconds: 3600}},
		finishedBooks: 2,
		genresErr:     fmt.Errorf("genre join exploded"),
	}

	h := &ReadingMotivationHandler{Store: store, Now: func() time.Time { return now }}
	rr := httptest.NewRecorder()
	h.HandleGetMotivation(rr, motivationRequest(1, "profile-a", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (section failure must degrade, not abort); body = %s", rr.Code, rr.Body.String())
	}

	var resp readingMotivationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v; body = %s", err, rr.Body.String())
	}
	if len(resp.DNA.Genres) != 0 {
		t.Errorf("dna.genres = %v, want empty", resp.DNA.Genres)
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &top); err != nil {
		t.Fatalf("unmarshal top level: %v", err)
	}
	var dna map[string]json.RawMessage
	if err := json.Unmarshal(top["dna"], &dna); err != nil {
		t.Fatalf("unmarshal dna: %v", err)
	}
	if string(dna["genres"]) != "[]" {
		t.Errorf(`dna.genres raw JSON = %s, want "[]" (not null)`, dna["genres"])
	}

	// Other sections stay intact: authors survived (unaffected store call),
	// and streak/goals math over the still-good sessions/finished-books
	// data is unaffected by the genre failure.
	if len(resp.DNA.Authors) != 1 {
		t.Errorf("dna.authors = %v, want the one fixture author to survive", resp.DNA.Authors)
	}
	if resp.Goals.BooksFinishedYTD != 2 {
		t.Errorf("goals.books_finished_ytd = %d, want 2", resp.Goals.BooksFinishedYTD)
	}
	if resp.Streak.TodaySeconds != 3600 {
		t.Errorf("streak.today_seconds = %d, want 3600", resp.Streak.TodaySeconds)
	}
}

// TestMotivationUsesTimezone reuses the 23:30Z-session trick from
// TestHistoryUsesRequestTimezone: a session at 2026-07-19T23:30:00Z lands on
// local day 2026-07-20 in Europe/Amsterdam (UTC+2 in July) but 2026-07-19 in
// UTC, shifting which day "today" attribution lands on.
func TestMotivationUsesTimezone(t *testing.T) {
	now := time.Date(2026, 7, 20, 10, 0, 0, 0, time.UTC)
	store := &fakeReadingMotivationStore{
		sessions: []ReadingSession{
			{ContentID: "book-1", StartedAt: time.Date(2026, 7, 19, 23, 30, 0, 0, time.UTC), DurationSeconds: 900},
		},
	}

	run := func(rawQuery string) readingMotivationResponse {
		h := &ReadingMotivationHandler{Store: store, Now: func() time.Time { return now }}
		rr := httptest.NewRecorder()
		h.HandleGetMotivation(rr, motivationRequest(1, "profile-a", rawQuery))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
		}
		var resp readingMotivationResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v; body = %s", err, rr.Body.String())
		}
		return resp
	}

	utc := run("")
	amsterdam := run("tz=Europe/Amsterdam")

	if utc.Streak.TodaySeconds != 0 {
		t.Errorf("UTC today_seconds = %d, want 0 (session falls on yesterday in UTC)", utc.Streak.TodaySeconds)
	}
	if utc.Streak.TodayQualified {
		t.Error("UTC today_qualified = true, want false")
	}
	if amsterdam.Streak.TodaySeconds != 900 {
		t.Errorf("Europe/Amsterdam today_seconds = %d, want 900 (session falls on today in +02:00)", amsterdam.Streak.TodaySeconds)
	}
	if !amsterdam.Streak.TodayQualified {
		t.Error("Europe/Amsterdam today_qualified = false, want true (900s >= 300s minimum)")
	}
}
