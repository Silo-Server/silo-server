// Package introdb implements a markers.Provider (and markers.Submitter)
// against the public TheIntroDB API (https://theintrodb.org). It fetches
// intro/recap/credits/preview timestamps for episodes and movies via
// GET /v3/media, and contributes detected/corrected segments back via
// POST /v3/submit (key required). GET /v3/user/stats validates a key.
package introdb

// ProviderID is the canonical identifier stored in
// media_files.*_markers_provider for markers sourced from TheIntroDB.
const ProviderID = "introdb"

// Algorithm is the algorithm tag written alongside markers. The version
// suffix lets us invalidate or refresh markers if the upstream contract
// changes.
const Algorithm = "introdb:v3"

// defaultConfidence is applied when TheIntroDB omits a per-segment confidence
// in the /media response. Real per-segment confidence is preferred when present.
const defaultConfidence = 0.9

// DefaultBaseURL is the production TheIntroDB v3 endpoint. Overridable
// in tests via Client.SetBaseURL.
const DefaultBaseURL = "https://api.theintrodb.org/v3"

// mediaResponse mirrors the JSON shape returned by GET /v3/media.
// Each segment kind is an array of zero or more entries; absent fields
// are decoded as empty slices via Go's zero-value semantics.
type mediaResponse struct {
	TmdbID  int                 `json:"tmdb_id"`
	Type    string              `json:"type"`
	Season  *int                `json:"season,omitempty"`
	Episode *int                `json:"episode,omitempty"`
	Intro   []segmentTimestamps `json:"intro,omitempty"`
	Recap   []segmentTimestamps `json:"recap,omitempty"`
	Credits []segmentTimestamps `json:"credits,omitempty"`
	Preview []segmentTimestamps `json:"preview,omitempty"`
}

// segmentTimestamps is the per-occurrence shape returned by TheIntroDB.
// Either bound may be nil — for intro/recap, start may be omitted (segment
// begins at file start); for credits/preview, end may be omitted (segment
// runs to file end). Confidence and SubmissionCount are optional per-segment
// quality signals used to rank multiple candidates for the same segment kind.
type segmentTimestamps struct {
	StartMs         *int64   `json:"start_ms,omitempty"`
	EndMs           *int64   `json:"end_ms,omitempty"`
	Confidence      *float64 `json:"confidence,omitempty"`
	SubmissionCount *int     `json:"submission_count,omitempty"`
}

// submitRequest is the POST /v3/submit body. tmdb_id is required; start_ms and
// end_ms are sent as explicit null (no omitempty) when the segment begins at
// the start (intro/recap) or runs to the end (credits/preview).
type submitRequest struct {
	TmdbID          int    `json:"tmdb_id"`
	ImdbID          string `json:"imdb_id,omitempty"`
	Type            string `json:"type"`
	Segment         string `json:"segment"`
	Season          *int   `json:"season,omitempty"`
	Episode         *int   `json:"episode,omitempty"`
	VideoDurationMs *int64 `json:"video_duration_ms,omitempty"`
	StartMs         *int64 `json:"start_ms"`
	EndMs           *int64 `json:"end_ms"`
}

// submitResponse mirrors the POST /v3/submit success body.
type submitResponse struct {
	Submissions []submissionRecord `json:"submissions"`
}

type submissionRecord struct {
	ID     string  `json:"id"`
	Status string  `json:"status"` // pending | accepted | rejected
	Weight float64 `json:"weight"`
}

// userStatsResponse mirrors GET /v3/user/stats. A non-empty Error (or a non-2xx
// status) means the key is invalid.
type userStatsResponse struct {
	Total          int     `json:"total"`
	Accepted       int     `json:"accepted"`
	Pending        int     `json:"pending"`
	Rejected       int     `json:"rejected"`
	AcceptanceRate float64 `json:"acceptance_rate"`
	CurrentStreak  int     `json:"current_streak"`
	BestStreak     int     `json:"best_streak"`
	Error          string  `json:"error,omitempty"`
}
