package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// seerrCapabilityID is the Seerr plugin's capability sub-id.
const seerrCapabilityID = "seerr"

// Seerr's media statuses for a season that is on its way or already there:
// pending, processing, partially available and available. Unknown,
// blacklisted and deleted leave the season requestable.
var seerrSeasonOnItsWay = map[int]bool{2: true, 3: true, 4: true, 5: true}

// Seerr's request statuses that no longer bring anything: declined and
// failed.
var seerrRequestDropped = map[int]bool{3: true, 4: true}

// seerrStatusSeason is a season with Seerr's media status.
type seerrStatusSeason struct {
	SeasonNumber int `json:"seasonNumber"`
	Status       int `json:"status"`
	Status4K     int `json:"status4k"`
}

// seerrSeries is the part of Seerr's /api/v1/tv/{tmdbId} answer this server
// reads: what Seerr tracks for the series and the requests made there.
type seerrSeries struct {
	MediaInfo *struct {
		Seasons  []seerrStatusSeason `json:"seasons"`
		Requests []struct {
			Status  int `json:"status"`
			Seasons []struct {
				SeasonNumber int `json:"seasonNumber"`
				Status       int `json:"status"`
			} `json:"seasons"`
		} `json:"requests"`
	} `json:"mediaInfo"`
}

// findSeerrSeries reads what Seerr tracks for the series with tmdbID.
func findSeerrSeries(ctx context.Context, baseURL, apiKey string, tmdbID int) (*seerrSeries, error) {
	url := fmt.Sprintf("%s/api/v1/tv/%d", strings.TrimRight(strings.TrimSpace(baseURL), "/"), tmdbID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Api-Key", strings.TrimSpace(apiKey))
	resp, err := monitoredSeasonsHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, monitoredSeasonsMaxBody))
		return nil, fmt.Errorf("seerr answered %d", resp.StatusCode)
	}
	var out seerrSeries
	if err := json.NewDecoder(io.LimitReader(resp.Body, monitoredSeasonsMaxBody)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding seerr series: %w", err)
	}
	return &out, nil
}

// isSeerrConnection reports an enabled Seerr connection that has what a read
// needs.
func isSeerrConnection(in Integration) bool {
	return in.Enabled && in.CapabilityID == seerrCapabilityID &&
		strings.TrimSpace(in.BaseURL) != "" && strings.TrimSpace(in.APIKeyRef) != ""
}

// mergeSeerrSeasons adds the regular seasons Seerr has on their way, in
// either quality, or in a request that still stands.
func mergeSeerrSeasons(into map[int]bool, media *seerrSeries) {
	if media == nil || media.MediaInfo == nil {
		return
	}
	for _, season := range media.MediaInfo.Seasons {
		if season.SeasonNumber > 0 && (seerrSeasonOnItsWay[season.Status] || seerrSeasonOnItsWay[season.Status4K]) {
			into[season.SeasonNumber] = true
		}
	}
	for _, request := range media.MediaInfo.Requests {
		if seerrRequestDropped[request.Status] {
			continue
		}
		for _, season := range request.Seasons {
			if season.SeasonNumber > 0 && !seerrRequestDropped[season.Status] {
				into[season.SeasonNumber] = true
			}
		}
	}
}
