package introdb

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
)

// Provider implements both markers.Provider and markers.Submitter.
var _ markers.Submitter = (*Provider)(nil)

// Provider implements markers.Provider against the TheIntroDB API.
// Movies are looked up by TMDB or IMDB ID; episodes additionally need a
// season and episode number. The provider returns at most one Marker per
// segment kind — when TheIntroDB has multiple candidate ranges (multiple
// release versions), we pick the first usable one.
type Provider struct {
	client *Client
}

// NewProvider constructs a Provider backed by the supplied client. Pass an
// already-configured *Client (from NewClient) so callers control the API
// key, base URL, and lifecycle.
func NewProvider(client *Client) *Provider {
	return &Provider{client: client}
}

// ID satisfies markers.Provider; it returns the canonical provider tag
// stored in *_markers_provider columns and emitted in telemetry.
func (p *Provider) ID() string { return ProviderID }

// FetchMarkers issues a single GET /v3/media call and converts the
// response into a markers.Result. A nil Provider (or one with no usable
// IDs) returns an empty result rather than an error so callers can
// chain providers via the Registry.
func (p *Provider) FetchMarkers(ctx context.Context, req markers.Request) (markers.Result, error) {
	if p == nil || p.client == nil {
		return markers.Result{}, nil
	}

	tmdbID := strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyTMDB])
	tvdbID := strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyTVDB])
	imdbID := strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyIMDB])
	if tmdbID == "" && tvdbID == "" && imdbID == "" {
		return markers.Result{}, nil
	}

	durationMS := int64(req.Duration / time.Millisecond)

	var resp *mediaResponse
	var err error
	switch req.Kind {
	case markers.ItemKindEpisode:
		if req.SeasonNumber <= 0 || req.EpisodeNumber <= 0 {
			return markers.Result{}, nil
		}
		resp, err = p.client.FetchEpisode(ctx, tmdbID, tvdbID, imdbID, req.SeasonNumber, req.EpisodeNumber, durationMS)
	case markers.ItemKindMovie:
		resp, err = p.client.FetchMovie(ctx, tmdbID, tvdbID, imdbID, durationMS)
	default:
		return markers.Result{}, nil
	}
	if err != nil {
		return markers.Result{}, err
	}
	if resp == nil {
		return markers.Result{}, nil
	}

	result := markers.Result{
		ProviderID:  ProviderID,
		SourceClass: models.MarkerSourceOnline,
		Algorithm:   Algorithm,
	}
	if m, ok := pickMarker(resp.Intro, markers.MarkerKindIntro, req.Duration, true); ok {
		result.Markers = append(result.Markers, m)
	}
	if m, ok := pickMarker(resp.Credits, markers.MarkerKindCredits, req.Duration, false); ok {
		result.Markers = append(result.Markers, m)
	}
	if m, ok := pickMarker(resp.Recap, markers.MarkerKindRecap, req.Duration, true); ok {
		result.Markers = append(result.Markers, m)
	}
	if m, ok := pickMarker(resp.Preview, markers.MarkerKindPreview, req.Duration, false); ok {
		result.Markers = append(result.Markers, m)
	}
	return result, nil
}

// pickMarker selects the best usable segment from a TheIntroDB response array.
// `requireEnd` is true for segments where the end timestamp is the load-bearing
// field (intro, recap) — they're allowed to start at 0 if `start_ms` is omitted.
// For trailing segments (credits, preview) the start is required but the end
// defaults to the file duration. When several candidates are usable (e.g. no
// duration match narrowed the set), the most-submitted one wins, with higher
// confidence breaking ties; with a single candidate this is the previous
// first-usable behavior. Real per-segment confidence is used when present,
// falling back to defaultConfidence only when the API omits it.
func pickMarker(stamps []segmentTimestamps, kind markers.MarkerKind, totalDuration time.Duration, requireEnd bool) (markers.Marker, bool) {
	best := markers.Marker{}
	bestSubs := -1
	found := false
	for _, s := range stamps {
		start := time.Duration(0)
		end := totalDuration
		if s.StartMs != nil {
			start = time.Duration(*s.StartMs) * time.Millisecond
		}
		if s.EndMs != nil {
			end = time.Duration(*s.EndMs) * time.Millisecond
		}
		if requireEnd && s.EndMs == nil {
			continue
		}
		if !requireEnd && s.StartMs == nil {
			continue
		}
		if end <= start {
			continue
		}
		confidence := defaultConfidence
		if s.Confidence != nil {
			confidence = *s.Confidence
		}
		subs := 0
		if s.SubmissionCount != nil {
			subs = *s.SubmissionCount
		}
		if !found || subs > bestSubs || (subs == bestSubs && confidence > best.Confidence) {
			best = markers.Marker{Kind: kind, Start: start, End: end, Confidence: confidence, SubmissionCount: subs}
			bestSubs = subs
			found = true
		}
	}
	return best, found
}

// SubmitMarker contributes a single segment to TheIntroDB via POST /v3/submit.
// A TMDB id is required (the submission endpoint is keyed on TMDB). Per the
// TheIntroDB convention, intro/recap submissions drop a zero start (= from the
// beginning) and credits/preview drop an end at file duration (= to the end).
func (p *Provider) SubmitMarker(ctx context.Context, req markers.SubmissionRequest) (markers.SubmissionResult, error) {
	if p == nil || p.client == nil {
		return markers.SubmissionResult{}, fmt.Errorf("introdb: provider not configured")
	}
	tmdbID, err := strconv.Atoi(strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyTMDB]))
	if err != nil || tmdbID <= 0 {
		return markers.SubmissionResult{}, fmt.Errorf("introdb: submit requires a TMDB id")
	}
	segment, err := segmentName(req.Segment)
	if err != nil {
		return markers.SubmissionResult{}, err
	}

	body := submitRequest{
		TmdbID:  tmdbID,
		ImdbID:  strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyIMDB]),
		Segment: segment,
	}
	switch req.Kind {
	case markers.ItemKindEpisode:
		if req.SeasonNumber <= 0 || req.EpisodeNumber <= 0 {
			return markers.SubmissionResult{}, fmt.Errorf("introdb: episode submit requires season and episode")
		}
		body.Type = "tv"
		season, episode := req.SeasonNumber, req.EpisodeNumber
		body.Season = &season
		body.Episode = &episode
	case markers.ItemKindMovie:
		body.Type = "movie"
	default:
		return markers.SubmissionResult{}, fmt.Errorf("introdb: unsupported item kind")
	}
	if req.Duration > 0 {
		durMs := int64(req.Duration / time.Millisecond)
		body.VideoDurationMs = &durMs
	}

	start, end := req.Start, req.End
	switch req.Segment {
	case markers.MarkerKindIntro, markers.MarkerKindRecap:
		if start != nil && *start <= 0 {
			start = nil // beginning of file
		}
	case markers.MarkerKindCredits, markers.MarkerKindPreview:
		if end != nil && req.Duration > 0 && *end >= req.Duration {
			end = nil // runs to end of file
		}
	}
	if start != nil {
		ms := int64(*start / time.Millisecond)
		body.StartMs = &ms
	}
	if end != nil {
		ms := int64(*end / time.Millisecond)
		body.EndMs = &ms
	}

	resp, err := p.client.submitSegment(ctx, body)
	if err != nil {
		return markers.SubmissionResult{}, err
	}
	if resp == nil || len(resp.Submissions) == 0 {
		return markers.SubmissionResult{Status: markers.SubmissionStatusPending}, nil
	}
	s := resp.Submissions[0]
	return markers.SubmissionResult{ID: s.ID, Status: s.Status, Weight: s.Weight}, nil
}

// FetchUserStats validates the configured key and returns contribution stats.
func (p *Provider) FetchUserStats(ctx context.Context) (markers.UserStats, error) {
	if p == nil || p.client == nil {
		return markers.UserStats{}, fmt.Errorf("introdb: provider not configured")
	}
	resp, err := p.client.fetchUserStats(ctx)
	if err != nil {
		return markers.UserStats{}, err
	}
	return markers.UserStats{
		Total:          resp.Total,
		Accepted:       resp.Accepted,
		Pending:        resp.Pending,
		Rejected:       resp.Rejected,
		AcceptanceRate: resp.AcceptanceRate,
		CurrentStreak:  resp.CurrentStreak,
		BestStreak:     resp.BestStreak,
	}, nil
}

func segmentName(kind markers.MarkerKind) (string, error) {
	switch kind {
	case markers.MarkerKindIntro:
		return "intro", nil
	case markers.MarkerKindCredits:
		return "credits", nil
	case markers.MarkerKindRecap:
		return "recap", nil
	case markers.MarkerKindPreview:
		return "preview", nil
	default:
		return "", fmt.Errorf("introdb: unknown segment kind %d", kind)
	}
}
