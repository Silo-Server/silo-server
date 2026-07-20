package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/models"
)

// requestLocation resolves the requester's timezone from the "tz" query
// param (an IANA zone name, typically Intl.DateTimeFormat().resolvedOptions().timeZone
// from the client), falling back to UTC when the param is absent, invalid,
// or "Local". "Local" is rejected explicitly even though time.LoadLocation
// would resolve it successfully, since it means "the server's zone", not a
// meaningful request-scoped default.
func requestLocation(r *http.Request) *time.Location {
	name := strings.TrimSpace(r.URL.Query().Get("tz"))
	if name == "" || name == "Local" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// ReadingGoals is a profile's yearly reading goals. A nil field is unset.
type ReadingGoals struct {
	BooksPerYear *int
	HoursPerYear *int
}

// GenreSeconds is a per-genre reading-time rollup. A book's duration is
// split evenly across its genres.
type GenreSeconds struct {
	Genre   string
	Seconds int
}

// AuthorSeconds is a per-author reading-time rollup.
type AuthorSeconds struct {
	Author  string
	Seconds int
}

// ReadingMotivationStore persists reading goals and achievement unlocks, and
// supplies the raw aggregates the motivation endpoint's pure math (streaks,
// challenge, DNA, badge criteria) is built from.
type ReadingMotivationStore interface {
	GetGoals(ctx context.Context, userID int, profileID string) (*ReadingGoals, error)
	PutGoals(ctx context.Context, userID int, profileID string, g ReadingGoals) error

	// AchievedAt returns the profile's unlocked achievement ids mapped to
	// when they were unlocked.
	AchievedAt(ctx context.Context, userID int, profileID string) (map[string]time.Time, error)
	// PersistAchievement records a newly satisfied badge. Implementations
	// must be idempotent (a badge, once unlocked, is never revoked or
	// re-timestamped).
	PersistAchievement(ctx context.Context, userID int, profileID, achievementID string, at time.Time) error

	// SessionsSince returns raw session rows since the given time for the
	// pure aggregation math to consume.
	SessionsSince(ctx context.Context, userID int, profileID string, since time.Time) ([]ReadingSession, error)
	// FinishedBooksInRange counts ebooks whose progress crossed
	// models.EbookFinishedProgressThreshold with the progress row's
	// updated_at in [from, to).
	FinishedBooksInRange(ctx context.Context, userID int, profileID string, from, to time.Time) (int, error)
	// HasBookReadAbove reports whether any of the profile's ebooks has ever
	// (all-time, not year-scoped) reached at least threshold progress. This
	// is the "finisher" badge's real criterion (>=95% per spec), distinct
	// from and stricter than models.EbookFinishedProgressThreshold, which
	// only gates whether a book counts as "finished" at all.
	HasBookReadAbove(ctx context.Context, userID int, profileID string, threshold float64) (bool, error)
	// GenreSeconds returns all-time per-genre reading-time totals,
	// highest first.
	GenreSeconds(ctx context.Context, userID int, profileID string) ([]GenreSeconds, error)
	// AuthorSeconds returns all-time per-author reading-time totals,
	// highest first.
	AuthorSeconds(ctx context.Context, userID int, profileID string) ([]AuthorSeconds, error)
}

// streakMinSeconds is the minimum local-day reading time for that day to
// count toward a streak, per spec.
const streakMinSeconds = 300

// monthChallengeFloorSeconds is the minimum monthly challenge target, so an
// empty previous month doesn't trivialize the current one.
const monthChallengeFloorSeconds = 10800

// farFutureYears bounds the upper end of the "all-time" FinishedBooksInRange
// call used to feed the lifetime books-count achievement badges: any
// progress row's updated_at is guaranteed to fall before now plus this many
// years.
const farFutureYears = 100

// finisherProgressThreshold is the "finisher" badge's minimum all-time
// per-book progress (>=95%, per spec). It's intentionally stricter than
// models.EbookFinishedProgressThreshold, which only gates whether a book
// counts as "finished" at all for the books-count badges and goals card.
const finisherProgressThreshold = 0.95

// dayKeyLayout is the "YYYY-MM-DD" layout DaySeconds keys and streakFrom's
// "today" argument use.
const dayKeyLayout = "2006-01-02"

// DaySeconds maps a "YYYY-MM-DD" local calendar day (already resolved
// against the caller's *time.Location) to the total reading seconds
// attributed to it.
type DaySeconds map[string]int

// sessionDaySeconds buckets sessions by the local calendar day their
// started_at falls on in loc, summing duration_seconds per day. Sessions
// store started_at in UTC; only the bucketing is timezone-aware.
func sessionDaySeconds(sessions []ReadingSession, loc *time.Location) DaySeconds {
	days := make(DaySeconds)
	for _, s := range sessions {
		key := s.StartedAt.In(loc).Format(dayKeyLayout)
		days[key] += s.DurationSeconds
	}
	return days
}

// streakFrom computes the current and longest streaks over days, a map of
// local calendar day to reading seconds. A day qualifies with >=
// streakMinSeconds. The current streak is alive until a full local day is
// missed: if today doesn't qualify, the streak isn't broken yet (it just
// hasn't been extended) so current counts back from yesterday; if yesterday
// also fails to qualify, the streak is dead and current is 0.
func streakFrom(days DaySeconds, today string) (current, longest int) {
	qualifies := func(day string) bool {
		return days[day] >= streakMinSeconds
	}

	runEndingAt := func(start time.Time) int {
		n := 0
		d := start
		for qualifies(d.Format(dayKeyLayout)) {
			n++
			d = d.AddDate(0, 0, -1)
		}
		return n
	}

	todayTime, err := time.Parse(dayKeyLayout, today)
	if err != nil {
		return 0, 0
	}

	if qualifies(today) {
		current = runEndingAt(todayTime)
	} else {
		yesterday := todayTime.AddDate(0, 0, -1)
		if qualifies(yesterday.Format(dayKeyLayout)) {
			current = runEndingAt(yesterday)
		}
	}

	var qualifyingDays []time.Time
	for day, secs := range days {
		if secs < streakMinSeconds {
			continue
		}
		t, err := time.Parse(dayKeyLayout, day)
		if err != nil {
			continue
		}
		qualifyingDays = append(qualifyingDays, t)
	}
	sort.Slice(qualifyingDays, func(i, j int) bool { return qualifyingDays[i].Before(qualifyingDays[j]) })

	run := 0
	var prev time.Time
	for i, t := range qualifyingDays {
		if i > 0 && t.Sub(prev) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		if run > longest {
			longest = run
		}
		prev = t
	}

	return current, longest
}

// monthChallenge computes the current month's challenge target (the greater
// of last calendar month's total seconds and monthChallengeFloorSeconds) and
// the current month's seconds so far, both with month boundaries drawn in
// loc against now.
func monthChallenge(days DaySeconds, now time.Time, loc *time.Location) (targetSec, monthSec int) {
	nowLocal := now.In(loc)
	y, m, _ := nowLocal.Date()

	prevY, prevM := y, m-1
	if prevM < time.January {
		prevM = time.December
		prevY--
	}

	var prevMonthSec int
	for day, secs := range days {
		t, err := time.Parse(dayKeyLayout, day)
		if err != nil {
			continue
		}
		dy, dm, _ := t.Date()
		switch {
		case dy == y && dm == m:
			monthSec += secs
		case dy == prevY && dm == prevM:
			prevMonthSec += secs
		}
	}

	targetSec = prevMonthSec
	if targetSec < monthChallengeFloorSeconds {
		targetSec = monthChallengeFloorSeconds
	}
	return targetSec, monthSec
}

// goalProjection linearly extrapolates a year-end total from year-to-date
// progress and the fraction of the year elapsed. When elapsedFraction is too
// small to extrapolate meaningfully (< 0.01, i.e. very early in the year)
// ytd itself is returned rather than dividing by a near-zero denominator.
func goalProjection(ytd float64, yearElapsedFraction float64) float64 {
	if yearElapsedFraction < 0.01 {
		return ytd
	}
	return ytd / yearElapsedFraction
}

// hourBucket classifies an hour-of-day (0-23, local) into one of the four
// spec buckets: morning 05-11, afternoon 12-16, evening 17-21, night 22-04.
func hourBucket(hour int) string {
	switch {
	case hour >= 5 && hour <= 11:
		return "morning"
	case hour >= 12 && hour <= 16:
		return "afternoon"
	case hour >= 17 && hour <= 21:
		return "evening"
	default:
		return "night"
	}
}

// diversityScore is the complement of the Herfindahl index over genre-second
// shares, scaled to 0-100 and rounded: (1 - Sum(share^2)) * 100.
func diversityScore(genres []GenreSeconds) int {
	var total int
	for _, g := range genres {
		total += g.Seconds
	}
	if total <= 0 {
		return 0
	}
	var sumSquares float64
	for _, g := range genres {
		share := float64(g.Seconds) / float64(total)
		sumSquares += share * share
	}
	return int(math.Round((1 - sumSquares) * 100))
}

// dnaAggregates is the computed "Reading DNA" profile: taste breakdown,
// habits, and a year-end pace projection.
type dnaAggregates struct {
	Genres             []GenreSeconds
	Authors            []AuthorSeconds
	AvgSessionSeconds  int
	HoursByBucket      map[string]int
	ProjectedYearHours float64
	DiversityScore     int
}

// computeDNA derives dnaAggregates from raw sessions plus the store's
// all-time genre/author rollups. Hour-of-day buckets and average session
// length are computed over every session passed in; the year-end projection
// uses only sessions whose local start falls in now's local year.
func computeDNA(sessions []ReadingSession, genres []GenreSeconds, authors []AuthorSeconds, now time.Time, loc *time.Location) dnaAggregates {
	buckets := map[string]int{"morning": 0, "afternoon": 0, "evening": 0, "night": 0}
	year := now.In(loc).Year()

	var totalSeconds, ytdSeconds int
	for _, s := range sessions {
		totalSeconds += s.DurationSeconds
		local := s.StartedAt.In(loc)
		buckets[hourBucket(local.Hour())] += s.DurationSeconds
		if local.Year() == year {
			ytdSeconds += s.DurationSeconds
		}
	}

	avgSessionSeconds := 0
	if len(sessions) > 0 {
		avgSessionSeconds = totalSeconds / len(sessions)
	}

	hoursByBucket := make(map[string]int, len(buckets))
	for bucket, secs := range buckets {
		hoursByBucket[bucket] = int(math.Round(float64(secs) / 3600.0))
	}

	yearStart := time.Date(year, 1, 1, 0, 0, 0, 0, loc)
	nextYearStart := time.Date(year+1, 1, 1, 0, 0, 0, 0, loc)
	elapsedFraction := float64(now.In(loc).Sub(yearStart)) / float64(nextYearStart.Sub(yearStart))
	projected := goalProjection(float64(ytdSeconds)/3600.0, elapsedFraction)

	return dnaAggregates{
		Genres:             genres,
		Authors:            authors,
		AvgSessionSeconds:  avgSessionSeconds,
		HoursByBucket:      hoursByBucket,
		ProjectedYearHours: projected,
		DiversityScore:     diversityScore(genres),
	}
}

// achievementInput is the set of computed aggregates evaluateAchievements
// checks badge criteria against. All fields are already local-timezone-
// resolved and lifetime (not reset) totals where applicable.
type achievementInput struct {
	TotalSeconds          int
	LongestSessionSeconds int
	CurrentStreak         int
	LongestStreak         int
	BooksFinished         int
	NightSeconds          int
	EarlyBirdSeconds      int
	WeekendSeconds        int
	DistinctGenres        int
	MaxBookSeconds        int
	FinishedWithHighRead  bool
}

// AchievementDefinition is a fixed badge definition: id, display grouping,
// name, and description. Criteria live in evaluateAchievements, not here.
type AchievementDefinition struct {
	ID          string
	Category    string
	Name        string
	Description string
}

// achievementDefinitions is the fixed 18-badge starter set. Ids are load-
// bearing (persisted in reading_achievements) and must never change once
// shipped.
var achievementDefinitions = []AchievementDefinition{
	{ID: "first-hour", Category: "time", Name: "First Hour", Description: "Read for 1 hour total"},
	{ID: "ten-hours", Category: "time", Name: "Ten Hours", Description: "Read for 10 hours total"},
	{ID: "fifty-hours", Category: "time", Name: "Fifty Hours", Description: "Read for 50 hours total"},
	{ID: "hundred-hours", Category: "time", Name: "Hundred Hours", Description: "Read for 100 hours total"},
	{ID: "marathon-session", Category: "time", Name: "Marathon Session", Description: "Read for 2 hours in a single session"},
	{ID: "streak-3", Category: "streak", Name: "3-Day Streak", Description: "Read 3 days in a row"},
	{ID: "streak-7", Category: "streak", Name: "7-Day Streak", Description: "Read 7 days in a row"},
	{ID: "streak-30", Category: "streak", Name: "30-Day Streak", Description: "Read 30 days in a row"},
	{ID: "streak-100", Category: "streak", Name: "100-Day Streak", Description: "Read 100 days in a row"},
	{ID: "first-book", Category: "books", Name: "First Book", Description: "Finish 1 book"},
	{ID: "ten-books", Category: "books", Name: "Ten Books", Description: "Finish 10 books"},
	{ID: "fifty-books", Category: "books", Name: "Fifty Books", Description: "Finish 50 books"},
	{ID: "night-owl", Category: "habits", Name: "Night Owl", Description: "Read 10 hours between midnight and 5am"},
	{ID: "early-bird", Category: "habits", Name: "Early Bird", Description: "Read 10 hours between 5am and 8am"},
	{ID: "weekender", Category: "habits", Name: "Weekender", Description: "Read 20 hours on weekends"},
	{ID: "genre-hopper", Category: "exploration", Name: "Genre Hopper", Description: "Read across 5 distinct genres"},
	{ID: "deep-diver", Category: "exploration", Name: "Deep Diver", Description: "Read 10 hours on a single book"},
	{ID: "finisher", Category: "exploration", Name: "Finisher", Description: "Finish a book with at least 95% read"},
}

// evaluateAchievements returns the ids of every badge in achievementDefinitions
// whose criteria in is satisfies. It is pure and idempotent: callers merge
// the result against previously persisted unlocks (never revoked) and
// persist only the newly satisfied ones.
func evaluateAchievements(in achievementInput) []string {
	satisfied := map[string]bool{
		"first-hour":       in.TotalSeconds >= 3600,
		"ten-hours":        in.TotalSeconds >= 36000,
		"fifty-hours":      in.TotalSeconds >= 180000,
		"hundred-hours":    in.TotalSeconds >= 360000,
		"marathon-session": in.LongestSessionSeconds >= 7200,
		"streak-3":         in.LongestStreak >= 3,
		"streak-7":         in.LongestStreak >= 7,
		"streak-30":        in.LongestStreak >= 30,
		"streak-100":       in.LongestStreak >= 100,
		"first-book":       in.BooksFinished >= 1,
		"ten-books":        in.BooksFinished >= 10,
		"fifty-books":      in.BooksFinished >= 50,
		"night-owl":        in.NightSeconds >= 36000,
		"early-bird":       in.EarlyBirdSeconds >= 36000,
		"weekender":        in.WeekendSeconds >= 72000,
		"genre-hopper":     in.DistinctGenres >= 5,
		"deep-diver":       in.MaxBookSeconds >= 36000,
		"finisher":         in.FinishedWithHighRead,
	}

	var ids []string
	for _, def := range achievementDefinitions {
		if satisfied[def.ID] {
			ids = append(ids, def.ID)
		}
	}
	return ids
}

// ReadingMotivationHandler serves the reading-goals PUT endpoint (and, from
// a later task, the reading-motivation GET endpoint). Now is injected so
// achievement-unlock and goal-year math is deterministic under test.
type ReadingMotivationHandler struct {
	Store ReadingMotivationStore
	Now   func() time.Time
}

const (
	// minYearlyGoal and maxYearlyGoal bound both books_per_year and
	// hours_per_year, per spec.
	minYearlyGoal = 1
	maxYearlyGoal = 100000
)

type putGoalsRequest struct {
	BooksPerYear *int `json:"books_per_year"`
	HoursPerYear *int `json:"hours_per_year"`
}

// HandlePutGoals replaces the profile's reading goals wholesale: this is PUT
// semantics, not PATCH, so an absent field decodes to nil and clears that
// goal rather than leaving the stored value untouched.
func (h *ReadingMotivationHandler) HandlePutGoals(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reading goals are not configured")
		return
	}

	var req putGoalsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "Invalid goals body")
		return
	}
	if req.BooksPerYear != nil && (*req.BooksPerYear < minYearlyGoal || *req.BooksPerYear > maxYearlyGoal) {
		writeError(w, http.StatusBadRequest, "invalid_goal", "books_per_year must be between 1 and 100000")
		return
	}
	if req.HoursPerYear != nil && (*req.HoursPerYear < minYearlyGoal || *req.HoursPerYear > maxYearlyGoal) {
		writeError(w, http.StatusBadRequest, "invalid_goal", "hours_per_year must be between 1 and 100000")
		return
	}

	goals := ReadingGoals{BooksPerYear: req.BooksPerYear, HoursPerYear: req.HoursPerYear}
	if err := h.Store.PutGoals(r.Context(), userID, profileID, goals); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "Failed to save reading goals")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// earlyBirdStartHour/earlyBirdEndHour bound the "early-bird" badge's habit
// window (5am-8am local), which is narrower than hourBucket's "morning"
// bucket (5-11) used for the DNA hour-of-day breakdown.
const (
	earlyBirdStartHour = 5
	earlyBirdEndHour   = 8
)

// nightOwlEndHour bounds the "night-owl" badge's habit window: local hours
// [0, nightOwlEndHour), i.e. midnight-5am, per spec. This is distinct from
// hourBucket's "night" DNA bucket (22-04), which wraps across midnight.
const nightOwlEndHour = 5

type readingMotivationStreakResponse struct {
	CurrentDays    int  `json:"current_days"`
	LongestDays    int  `json:"longest_days"`
	TodaySeconds   int  `json:"today_seconds"`
	TodayQualified bool `json:"today_qualified"`
}

type readingMotivationGoalsResponse struct {
	BooksPerYear     *int    `json:"books_per_year"`
	HoursPerYear     *int    `json:"hours_per_year"`
	BooksFinishedYTD int     `json:"books_finished_ytd"`
	HoursYTD         float64 `json:"hours_ytd"`
	BooksOnTrackFor  int     `json:"books_on_track_for"`
	HoursOnTrackFor  int     `json:"hours_on_track_for"`
}

type readingMotivationChallengeResponse struct {
	TargetSeconds int `json:"target_seconds"`
	MonthSeconds  int `json:"month_seconds"`
	Percent       int `json:"percent"`
}

type readingMotivationAchievementResponse struct {
	ID          string  `json:"id"`
	Category    string  `json:"category"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	AchievedAt  *string `json:"achieved_at"`
}

type readingMotivationGenreResponse struct {
	Name    string `json:"name"`
	Seconds int    `json:"seconds"`
}

type readingMotivationAuthorResponse struct {
	Name    string `json:"name"`
	Seconds int    `json:"seconds"`
}

type readingMotivationDNAResponse struct {
	Genres             []readingMotivationGenreResponse  `json:"genres"`
	Authors            []readingMotivationAuthorResponse `json:"authors"`
	DiversityScore     int                               `json:"diversity_score"`
	AvgSessionSeconds  int                               `json:"avg_session_seconds"`
	HoursByBucket      map[string]int                    `json:"hours_by_bucket"`
	ProjectedYearHours float64                           `json:"projected_year_hours"`
}

type readingMotivationResponse struct {
	Streak       readingMotivationStreakResponse        `json:"streak"`
	Goals        readingMotivationGoalsResponse         `json:"goals"`
	Challenge    readingMotivationChallengeResponse     `json:"challenge"`
	Achievements []readingMotivationAchievementResponse `json:"achievements"`
	DNA          readingMotivationDNAResponse           `json:"dna"`
}

// HandleGetMotivation assembles the full reading-motivation payload: streak,
// yearly goals progress, the auto monthly challenge, the 18-badge
// achievement grid (evaluating and persisting any newly satisfied ones),
// and the Reading DNA taste/habit breakdown.
//
// Each section is computed from its own store call(s): a failing call is
// logged and that section falls back to its zero value (empty slice/zeroed
// struct) rather than aborting the whole response. Only the identity check
// and a nil store/Now are request-level preconditions that can fail the
// endpoint outright.
func (h *ReadingMotivationHandler) HandleGetMotivation(w http.ResponseWriter, r *http.Request) {
	userID := apimw.GetUserID(r.Context())
	profileID := apimw.GetProfileID(r.Context())
	if userID == 0 {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Authentication required")
		return
	}
	if h == nil || h.Store == nil || h.Now == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "Reading motivation is not configured")
		return
	}

	ctx := r.Context()
	loc := requestLocation(r)
	now := h.Now()
	nowLocal := now.In(loc)

	logSectionErr := func(section string, err error) {
		slog.ErrorContext(ctx, "reading motivation: section failed, degrading to zero value",
			"component", "api", "section", section, "error", err)
	}

	// Sessions feed streak/challenge/DNA/achievement math; a failure here
	// degrades every section that reads from it to its own zero value
	// rather than a hard 500, same as any other section.
	sessions, err := h.Store.SessionsSince(ctx, userID, profileID, time.Time{})
	if err != nil {
		logSectionErr("sessions", err)
		sessions = nil
	}

	days := sessionDaySeconds(sessions, loc)
	todayKey := nowLocal.Format(dayKeyLayout)
	currentStreak, longestStreak := streakFrom(days, todayKey)
	todaySeconds := days[todayKey]

	targetSeconds, monthSeconds := monthChallenge(days, now, loc)
	percent := 0
	if targetSeconds > 0 {
		percent = int(math.Round(float64(monthSeconds) / float64(targetSeconds) * 100))
	}
	if percent > 100 {
		percent = 100
	}

	// "Finished book" is scoped to the current local year for the goals
	// card's year-to-date count, but the books-count achievement badges
	// ("first-book"/"ten-books"/"fifty-books") are lifetime totals per
	// spec, so they're fed by a separate all-time call rather than
	// finishedYTD. Each call degrades independently.
	yearStart := time.Date(nowLocal.Year(), 1, 1, 0, 0, 0, 0, loc)
	yearEnd := yearStart.AddDate(1, 0, 0)
	finishedYTD, err := h.Store.FinishedBooksInRange(ctx, userID, profileID, yearStart, yearEnd)
	if err != nil {
		logSectionErr("finished_books", err)
		finishedYTD = 0
	}

	finishedAllTime, err := h.Store.FinishedBooksInRange(ctx, userID, profileID, time.Time{}, now.AddDate(farFutureYears, 0, 0))
	if err != nil {
		logSectionErr("finished_books_all_time", err)
		finishedAllTime = 0
	}

	// The "finisher" badge is a real, independent criterion: has any book
	// ever (all-time) reached finisherProgressThreshold, not just "at least
	// one book was finished this year" (which duplicates "first-book" and
	// uses the looser models.EbookFinishedProgressThreshold).
	highRead, err := h.Store.HasBookReadAbove(ctx, userID, profileID, finisherProgressThreshold)
	if err != nil {
		logSectionErr("finisher_high_read", err)
		highRead = false
	}

	goalsRow, err := h.Store.GetGoals(ctx, userID, profileID)
	if err != nil {
		logSectionErr("goals", err)
		goalsRow = nil
	}

	genres, err := h.Store.GenreSeconds(ctx, userID, profileID)
	if err != nil {
		logSectionErr("genre_seconds", err)
		genres = nil
	}

	authors, err := h.Store.AuthorSeconds(ctx, userID, profileID)
	if err != nil {
		logSectionErr("author_seconds", err)
		authors = nil
	}

	dna := computeDNA(sessions, genres, authors, now, loc)

	elapsedFraction := float64(nowLocal.Sub(yearStart)) / float64(yearEnd.Sub(yearStart))
	var ytdSeconds int
	var totalSeconds, longestSessionSeconds, maxBookSeconds int
	var nightSeconds, earlyBirdSeconds, weekendSeconds int
	bookSeconds := make(map[string]int)
	for _, s := range sessions {
		totalSeconds += s.DurationSeconds
		if s.DurationSeconds > longestSessionSeconds {
			longestSessionSeconds = s.DurationSeconds
		}
		bookSeconds[s.ContentID] += s.DurationSeconds

		local := s.StartedAt.In(loc)
		if local.Year() == nowLocal.Year() {
			ytdSeconds += s.DurationSeconds
		}
		hour := local.Hour()
		// The "night-owl" badge's window is midnight-5am (per spec),
		// narrower than and offset from hourBucket's "night" DNA bucket
		// (22-04), so it's accumulated separately rather than reusing
		// hourBucket here.
		if hour < nightOwlEndHour {
			nightSeconds += s.DurationSeconds
		}
		if hour >= earlyBirdStartHour && hour < earlyBirdEndHour {
			earlyBirdSeconds += s.DurationSeconds
		}
		if wd := local.Weekday(); wd == time.Saturday || wd == time.Sunday {
			weekendSeconds += s.DurationSeconds
		}
	}
	for _, secs := range bookSeconds {
		if secs > maxBookSeconds {
			maxBookSeconds = secs
		}
	}

	goalsResp := readingMotivationGoalsResponse{
		BooksFinishedYTD: finishedYTD,
		HoursYTD:         float64(ytdSeconds) / 3600.0,
		BooksOnTrackFor:  int(math.Round(goalProjection(float64(finishedYTD), elapsedFraction))),
		HoursOnTrackFor:  int(math.Round(goalProjection(float64(ytdSeconds)/3600.0, elapsedFraction))),
	}
	if goalsRow != nil {
		goalsResp.BooksPerYear = goalsRow.BooksPerYear
		goalsResp.HoursPerYear = goalsRow.HoursPerYear
	}

	achievedAt, err := h.Store.AchievedAt(ctx, userID, profileID)
	if err != nil {
		logSectionErr("achievements", err)
		achievedAt = nil
	}
	if achievedAt == nil {
		achievedAt = make(map[string]time.Time)
	}

	satisfied := evaluateAchievements(achievementInput{
		TotalSeconds:          totalSeconds,
		LongestSessionSeconds: longestSessionSeconds,
		CurrentStreak:         currentStreak,
		LongestStreak:         longestStreak,
		BooksFinished:         finishedAllTime,
		NightSeconds:          nightSeconds,
		EarlyBirdSeconds:      earlyBirdSeconds,
		WeekendSeconds:        weekendSeconds,
		DistinctGenres:        len(genres),
		MaxBookSeconds:        maxBookSeconds,
		FinishedWithHighRead:  highRead,
	})
	for _, id := range satisfied {
		if _, already := achievedAt[id]; already {
			continue
		}
		if err := h.Store.PersistAchievement(ctx, userID, profileID, id, now); err != nil {
			// Persistence failure is a silent skip: the badge shows locked
			// in this response and the next call retries the unlock.
			slog.ErrorContext(ctx, "reading motivation: persist achievement failed",
				"component", "api", "achievement_id", id, "error", err)
			continue
		}
		achievedAt[id] = now
	}

	achievementsResp := make([]readingMotivationAchievementResponse, 0, len(achievementDefinitions))
	for _, def := range achievementDefinitions {
		item := readingMotivationAchievementResponse{
			ID:          def.ID,
			Category:    def.Category,
			Name:        def.Name,
			Description: def.Description,
		}
		if at, ok := achievedAt[def.ID]; ok {
			formatted := at.UTC().Format(time.RFC3339)
			item.AchievedAt = &formatted
		}
		achievementsResp = append(achievementsResp, item)
	}

	genresResp := make([]readingMotivationGenreResponse, 0, len(dna.Genres))
	for _, g := range dna.Genres {
		genresResp = append(genresResp, readingMotivationGenreResponse{Name: g.Genre, Seconds: g.Seconds})
	}
	authorsResp := make([]readingMotivationAuthorResponse, 0, len(dna.Authors))
	for _, a := range dna.Authors {
		authorsResp = append(authorsResp, readingMotivationAuthorResponse{Name: a.Author, Seconds: a.Seconds})
	}

	writeJSON(w, http.StatusOK, readingMotivationResponse{
		Streak: readingMotivationStreakResponse{
			CurrentDays:    currentStreak,
			LongestDays:    longestStreak,
			TodaySeconds:   todaySeconds,
			TodayQualified: todaySeconds >= streakMinSeconds,
		},
		Goals: goalsResp,
		Challenge: readingMotivationChallengeResponse{
			TargetSeconds: targetSeconds,
			MonthSeconds:  monthSeconds,
			Percent:       percent,
		},
		Achievements: achievementsResp,
		DNA: readingMotivationDNAResponse{
			Genres:             genresResp,
			Authors:            authorsResp,
			DiversityScore:     dna.DiversityScore,
			AvgSessionSeconds:  dna.AvgSessionSeconds,
			HoursByBucket:      dna.HoursByBucket,
			ProjectedYearHours: dna.ProjectedYearHours,
		},
	})
}

// PGReadingMotivationStore is the Postgres-backed ReadingMotivationStore.
type PGReadingMotivationStore struct {
	pool *pgxpool.Pool
}

// NewPGReadingMotivationStore constructs a PGReadingMotivationStore.
func NewPGReadingMotivationStore(pool *pgxpool.Pool) *PGReadingMotivationStore {
	return &PGReadingMotivationStore{pool: pool}
}

func (s *PGReadingMotivationStore) GetGoals(ctx context.Context, userID int, profileID string) (*ReadingGoals, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	var g ReadingGoals
	err := s.pool.QueryRow(ctx, `
		SELECT books_per_year, hours_per_year
		FROM reading_goals
		WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
	).Scan(&g.BooksPerYear, &g.HoursPerYear)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get goals: %w", err)
	}
	return &g, nil
}

func (s *PGReadingMotivationStore) PutGoals(ctx context.Context, userID int, profileID string, g ReadingGoals) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("reading motivation store is not configured")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reading_goals (user_id, profile_id, books_per_year, hours_per_year, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, profile_id) DO UPDATE
		SET books_per_year = $3, hours_per_year = $4, updated_at = now()`,
		userID, profileID, g.BooksPerYear, g.HoursPerYear,
	)
	if err != nil {
		return fmt.Errorf("put goals: %w", err)
	}
	return nil
}

func (s *PGReadingMotivationStore) AchievedAt(ctx context.Context, userID int, profileID string) (map[string]time.Time, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT achievement_id, achieved_at
		FROM reading_achievements
		WHERE user_id = $1 AND profile_id = $2`,
		userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("achieved at: %w", err)
	}
	defer rows.Close()

	out := make(map[string]time.Time)
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("scan achieved at: %w", err)
		}
		out[id] = at
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate achieved at: %w", err)
	}
	return out, nil
}

func (s *PGReadingMotivationStore) PersistAchievement(ctx context.Context, userID int, profileID, achievementID string, at time.Time) error {
	if s == nil || s.pool == nil {
		return fmt.Errorf("reading motivation store is not configured")
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO reading_achievements (user_id, profile_id, achievement_id, achieved_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, profile_id, achievement_id) DO NOTHING`,
		userID, profileID, achievementID, at,
	)
	if err != nil {
		return fmt.Errorf("persist achievement: %w", err)
	}
	return nil
}

func (s *PGReadingMotivationStore) SessionsSince(ctx context.Context, userID int, profileID string, since time.Time) ([]ReadingSession, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, profile_id, content_id, started_at, last_heartbeat_at,
		       duration_seconds, start_fraction, end_fraction
		FROM reading_sessions
		WHERE user_id = $1 AND profile_id = $2 AND started_at >= $3
		ORDER BY started_at`,
		userID, profileID, since,
	)
	if err != nil {
		return nil, fmt.Errorf("sessions since: %w", err)
	}
	defer rows.Close()

	var out []ReadingSession
	for rows.Next() {
		var sess ReadingSession
		if err := rows.Scan(
			&sess.ID,
			&sess.UserID,
			&sess.ProfileID,
			&sess.ContentID,
			&sess.StartedAt,
			&sess.LastHeartbeatAt,
			&sess.DurationSeconds,
			&sess.StartFraction,
			&sess.EndFraction,
		); err != nil {
			return nil, fmt.Errorf("scan sessions since: %w", err)
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions since: %w", err)
	}
	return out, nil
}

func (s *PGReadingMotivationStore) FinishedBooksInRange(ctx context.Context, userID int, profileID string, from, to time.Time) (int, error) {
	if s == nil || s.pool == nil {
		return 0, fmt.Errorf("reading motivation store is not configured")
	}
	var count int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM ebook_reader_progress
		WHERE user_id = $1 AND profile_id = $2 AND progress >= $3 AND updated_at >= $4 AND updated_at < $5`,
		userID, profileID, models.EbookFinishedProgressThreshold, from, to,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("finished books in range: %w", err)
	}
	return count, nil
}

func (s *PGReadingMotivationStore) HasBookReadAbove(ctx context.Context, userID int, profileID string, threshold float64) (bool, error) {
	if s == nil || s.pool == nil {
		return false, fmt.Errorf("reading motivation store is not configured")
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM ebook_reader_progress
			WHERE user_id = $1 AND profile_id = $2 AND progress >= $3
		)`,
		userID, profileID, threshold,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("has book read above: %w", err)
	}
	return exists, nil
}

func (s *PGReadingMotivationStore) GenreSeconds(ctx context.Context, userID int, profileID string) ([]GenreSeconds, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT g.genre, SUM(rs.duration_seconds::float / GREATEST(cardinality(mi.genres), 1))::bigint
		FROM reading_sessions rs
		JOIN media_items mi ON mi.content_id = rs.content_id
		CROSS JOIN LATERAL unnest(mi.genres) AS g(genre)
		WHERE rs.user_id = $1 AND rs.profile_id = $2
		GROUP BY g.genre
		ORDER BY 2 DESC`,
		userID, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("genre seconds: %w", err)
	}
	defer rows.Close()

	var out []GenreSeconds
	for rows.Next() {
		var g GenreSeconds
		if err := rows.Scan(&g.Genre, &g.Seconds); err != nil {
			return nil, fmt.Errorf("scan genre seconds: %w", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate genre seconds: %w", err)
	}
	return out, nil
}

func (s *PGReadingMotivationStore) AuthorSeconds(ctx context.Context, userID int, profileID string) ([]AuthorSeconds, error) {
	if s == nil || s.pool == nil {
		return nil, fmt.Errorf("reading motivation store is not configured")
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.name, SUM(rs.duration_seconds)
		FROM reading_sessions rs
		JOIN item_people ip ON ip.content_id = rs.content_id AND ip.kind = $3
		JOIN people p ON p.id = ip.person_id
		WHERE rs.user_id = $1 AND rs.profile_id = $2
		GROUP BY p.name
		ORDER BY 2 DESC
		LIMIT 8`,
		userID, profileID, int(models.PersonKindAuthor),
	)
	if err != nil {
		return nil, fmt.Errorf("author seconds: %w", err)
	}
	defer rows.Close()

	var out []AuthorSeconds
	for rows.Next() {
		var a AuthorSeconds
		if err := rows.Scan(&a.Author, &a.Seconds); err != nil {
			return nil, fmt.Errorf("scan author seconds: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate author seconds: %w", err)
	}
	return out, nil
}
