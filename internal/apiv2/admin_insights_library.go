package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
)

// The Insights Library read: how the catalog is used against the window's
// plays. The shared window input, problem mapping and labeled counts live in
// admin_insights.go.

type AdminInsightsLibrarySummary struct {
	Titles                int64    `json:"titles" doc:"Distinct movies and series in scope; a title in two libraries counts once"`
	PlayedTitles          int64    `json:"played_titles" doc:"Titles with a play in the window; a series counts when any episode has one"`
	PlayedShare           float64  `json:"played_share"`
	NeverStarted          int64    `json:"never_started" doc:"Titles with no playback session at all, up to now"`
	AddedTitles           int64    `json:"added_titles" doc:"Titles first seen in the window"`
	AddedPlayed           int64    `json:"added_played" doc:"Titles added in the window that have a play since, up to now"`
	MedianDaysToFirstPlay *float64 `json:"median_days_to_first_play" nullable:"true" doc:"Median days from being added to the first play, over titles added in the window that have one"`
}

type AdminInsightsLibraryCoverage struct {
	LibraryID    ID      `json:"library_id"`
	Name         string  `json:"name"`
	Titles       int64   `json:"titles"`
	PlayedTitles int64   `json:"played_titles"`
	PlayedShare  float64 `json:"played_share"`
	NeverStarted int64   `json:"never_started"`
	AddedTitles  int64   `json:"added_titles"`
}

type AdminInsightsLabeledPlays struct {
	Key    string `json:"key"`
	Plays  int64  `json:"plays"`
	Titles int64  `json:"titles" doc:"Distinct played titles behind the plays"`
}

type AdminInsightsUnplayedTitle struct {
	ContentID   string  `json:"content_id"`
	Kind        string  `json:"kind" enum:"movie,series"`
	Title       string  `json:"title"`
	Year        *int    `json:"year" nullable:"true"`
	AddedAt     Instant `json:"added_at"`
	LibraryID   ID      `json:"library_id"`
	LibraryName string  `json:"library_name"`
}

type AdminInsightsLibrary struct {
	From                  Instant                        `json:"from"`
	To                    Instant                        `json:"to"`
	TZ                    string                         `json:"tz"`
	OldestHistoryAt       NullableInstant                `json:"oldest_history_at"`
	Summary               AdminInsightsLibrarySummary    `json:"summary"`
	Libraries             []AdminInsightsLibraryCoverage `json:"libraries" doc:"Libraries with movies or series, most titles first"`
	Genres                []AdminInsightsLabeledPlays    `json:"genres" doc:"Up to 20 genres by plays; a title counts in each of its genres"`
	Decades               []AdminInsightsLabeledPlays    `json:"decades" doc:"Plays by release decade (key is the decade's first year), oldest first, unknown last"`
	ContentRatings        []AdminInsightsLabeledPlays    `json:"content_ratings" doc:"Up to 15 content ratings by plays"`
	TimeToFirstPlay       []AdminInsightsLabeledCount    `json:"time_to_first_play" doc:"Titles added in the window by how long they waited for a first play: day, week, month, later or not_yet; sessions holds the title count"`
	RecentlyAddedUnplayed []AdminInsightsUnplayedTitle   `json:"recently_added_unplayed" doc:"Up to 20 titles added in the window with no play yet, newest first"`
}

type AdminInsightsLibraryOutput struct {
	Body AdminInsightsLibrary
}

func registerAdminInsightsLibrary(reg *Registry) {
	Register(reg, insightsOp("library", "getAdminInsightsLibrary", "Read how much of each movie and TV library was played, titles never started, plays by genre, decade and content rating, and how soon new titles get their first play."), func(ctx context.Context, in *AdminInsightsWindowInput) (*AdminInsightsLibraryOutput, error) {
		if reg.deps.AdminInsights == nil {
			return nil, unavailable("insights")
		}
		q, p := insightsWindowQueryOf(in.Range, in.FromDate, in.ToDate, in.TZ, in.LibraryID, in.UserID, in.ProfileID)
		if p != nil {
			return nil, p
		}
		res, err := reg.deps.AdminInsights.ReadAdminInsightsLibrary(ctx, q, cacheRefresh(ctx, in.Refresh))
		if err != nil || res == nil {
			return nil, adminInsightsProblem(err)
		}
		return &AdminInsightsLibraryOutput{Body: adminInsightsLibraryOf(res)}, nil
	})
}

func adminInsightsLibraryOf(res *handlers.AdminInsightsLibrary) AdminInsightsLibrary {
	body := AdminInsightsLibrary{
		From: NewInstant(res.From), To: NewInstant(res.To), TZ: res.TZ,
		OldestHistoryAt: destinationInstant(res.OldestHistoryAt),
		Summary: AdminInsightsLibrarySummary{
			Titles: res.Summary.Titles, PlayedTitles: res.Summary.PlayedTitles, PlayedShare: res.Summary.PlayedShare,
			NeverStarted: res.Summary.NeverStarted, AddedTitles: res.Summary.AddedTitles, AddedPlayed: res.Summary.AddedPlayed,
			MedianDaysToFirstPlay: res.Summary.MedianDaysToFirstPlay,
		},
		Libraries:             make([]AdminInsightsLibraryCoverage, 0, len(res.Libraries)),
		Genres:                adminInsightsLabeledPlaysOf(res.Genres),
		Decades:               adminInsightsLabeledPlaysOf(res.Decades),
		ContentRatings:        adminInsightsLabeledPlaysOf(res.ContentRatings),
		TimeToFirstPlay:       adminInsightsLabeledCountsOf(res.TimeToFirstPlay),
		RecentlyAddedUnplayed: make([]AdminInsightsUnplayedTitle, 0, len(res.RecentlyAddedUnplayed)),
	}
	for _, lib := range res.Libraries {
		body.Libraries = append(body.Libraries, AdminInsightsLibraryCoverage{
			LibraryID: IDFromInt(int64(lib.LibraryID)), Name: lib.Name, Titles: lib.Titles, PlayedTitles: lib.PlayedTitles,
			PlayedShare: lib.PlayedShare, NeverStarted: lib.NeverStarted, AddedTitles: lib.AddedTitles,
		})
	}
	for _, t := range res.RecentlyAddedUnplayed {
		body.RecentlyAddedUnplayed = append(body.RecentlyAddedUnplayed, AdminInsightsUnplayedTitle{
			ContentID: t.ContentID, Kind: t.Kind, Title: t.Title, Year: t.Year, AddedAt: NewInstant(t.AddedAt),
			LibraryID: IDFromInt(int64(t.LibraryID)), LibraryName: t.LibraryName,
		})
	}
	return body
}

func adminInsightsLabeledPlaysOf(in []handlers.AdminInsightsLabeledPlays) []AdminInsightsLabeledPlays {
	out := make([]AdminInsightsLabeledPlays, 0, len(in))
	for _, item := range in {
		out = append(out, AdminInsightsLabeledPlays(item))
	}
	return out
}
