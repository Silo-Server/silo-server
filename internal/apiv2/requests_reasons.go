package apiv2

import "github.com/Silo-Server/silo-server/internal/requests/discoverrows"

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
