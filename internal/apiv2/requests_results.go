package apiv2

import (
	mediarequests "github.com/Silo-Server/silo-server/internal/requests"
	"github.com/Silo-Server/silo-server/internal/requests/discoverrows"
)

// RequestMediaReason names a library title that led to a recommendation.
type RequestMediaReason struct {
	ContentID string `json:"content_id" doc:"The catalog item" example:"movie:heat-1995"`
	Title     string `json:"title" example:"Heat"`
}

func requestMediaReasonsOf(reasons []discoverrows.Reason) []RequestMediaReason {
	if len(reasons) == 0 {
		return nil
	}
	out := make([]RequestMediaReason, 0, len(reasons))
	for _, r := range reasons {
		out = append(out, RequestMediaReason(r))
	}
	return out
}

func requestMediaResultsOf(results []mediarequests.MediaResult) []RequestMediaResult {
	out := make([]RequestMediaResult, 0, len(results))
	for _, r := range results {
		out = append(out, RequestMediaResult{
			MediaType: string(r.MediaType), TMDBID: r.TMDBID, Title: r.Title, Year: r.Year, Overview: r.Overview,
			PosterPath: r.PosterPath, BackdropPath: r.BackdropPath, ReleaseDate: calendarDate(r.ReleaseDate),
			Popularity: r.Popularity, VoteAverage: r.VoteAverage, Availability: string(r.Availability),
			LibraryContentID: r.LibraryContentID, Request: requestMediaStateOf(r.Request),
			Reasons: requestMediaReasonsOf(r.Reasons),
		})
	}
	return out
}
