// Package introdb implements a markers.Provider against the public
// TheIntroDB API (https://theintrodb.org). The provider is read-only:
// it fetches intro/recap/credits/preview timestamps for episodes and
// movies. Submissions are intentionally not supported.
package introdb

// ProviderID is the canonical identifier stored in
// media_files.*_markers_provider for markers sourced from TheIntroDB.
const ProviderID = "introdb"

// Algorithm is the algorithm tag written alongside markers. The version
// suffix lets us invalidate or refresh markers if the upstream contract
// changes.
const Algorithm = "introdb:v3"

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
// runs to file end).
type segmentTimestamps struct {
	StartMs *int64 `json:"start_ms,omitempty"`
	EndMs   *int64 `json:"end_ms,omitempty"`
}
