package introdb

import (
	"context"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/markers"
	"github.com/Silo-Server/silo-server/internal/models"
)

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
	imdbID := strings.TrimSpace(req.ExternalIDs[markers.ExternalIDKeyIMDB])
	if tmdbID == "" && imdbID == "" {
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
		resp, err = p.client.FetchEpisode(ctx, tmdbID, imdbID, req.SeasonNumber, req.EpisodeNumber, durationMS)
	case markers.ItemKindMovie:
		resp, err = p.client.FetchMovie(ctx, tmdbID, imdbID, durationMS)
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

// pickMarker selects the first usable segment from a TheIntroDB response
// array. `requireEnd` is true for segments where the end timestamp is the
// load-bearing field (intro, recap) — they're allowed to start at 0 if
// `start_ms` is omitted. For trailing segments (credits, preview) the
// start is required but the end defaults to the file duration.
func pickMarker(stamps []segmentTimestamps, kind markers.MarkerKind, totalDuration time.Duration, requireEnd bool) (markers.Marker, bool) {
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
		return markers.Marker{Kind: kind, Start: start, End: end, Confidence: 0.9}, true
	}
	return markers.Marker{}, false
}
