package historyimport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPlexAdminProviderEnrichesLocalizedMovieForStableMatching(t *testing.T) {
	t.Parallel()

	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"42","type":"movie","title":"Skazani na Shawshank","year":1994,"viewedAt":1700000000}
			]}}`)
		case "/library/metadata/42":
			metadataCalls++
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"42","type":"movie","title":"The Shawshank Redemption","year":1994,
				 "Guid":[{"id":"imdb://tt0111161"},{"id":"tmdb://278"}]}
			]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	provider := NewPlexAdminProvider(newUnthrottledPlexClient(), server.URL, "admin-token", "7")
	records, warnings, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if metadataCalls != 1 {
		t.Fatalf("metadata calls = %d, want 1", metadataCalls)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.Title != "Skazani na Shawshank" {
		t.Fatalf("title = %q, want original localized history title", record.Title)
	}
	if record.TMDBID != "278" || record.IMDbID != "tt0111161" {
		t.Fatalf("ids = tmdb %q imdb %q, want enriched provider ids", record.TMDBID, record.IMDbID)
	}

	repo := &matcherRepoStub{mediaByExternal: map[string][]mediaLookupRow{
		"movie:tmdb_id:278": {{ContentID: "movie-278", Title: "The Shawshank Redemption", Year: 1994}},
	}}
	match, reason, err := NewMatcher(repo).Match(context.Background(), record)
	if err != nil || reason != "" || match == nil || match.MediaItemID != "movie-278" {
		t.Fatalf("match = %+v, reason = %q, err = %v", match, reason, err)
	}
}

func TestPlexAdminProviderTreatsPlexOnlyGuidAsUnresolved(t *testing.T) {
	t.Parallel()

	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"9","type":"movie","title":"Diuna","year":2021,"Guid":"plex://movie/abc"}
			]}}`)
		case "/library/metadata/9":
			metadataCalls++
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"9","type":"movie","Guid":[{"id":"tmdb://438631"}]}
			]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 || metadataCalls != 1 || len(records) != 1 || records[0].TMDBID != "438631" {
		t.Fatalf("records = %+v, warnings = %v, metadata calls = %d", records, warnings, metadataCalls)
	}
}

func TestPlexAdminProviderTreatsMovieTVDBGuidAsUnresolved(t *testing.T) {
	t.Parallel()

	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"9","type":"movie","title":"Diuna","year":2021,"Guid":[{"id":"tvdb://12345"}]}
			]}}`)
		case "/library/metadata/9":
			metadataCalls++
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"9","type":"movie","Guid":[{"id":"tmdb://438631"}]}
			]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 || metadataCalls != 1 || len(records) != 1 ||
		records[0].TMDBID != "438631" || records[0].TVDBID != "12345" {
		t.Fatalf("records = %+v, warnings = %v, metadata calls = %d", records, warnings, metadataCalls)
	}
}

func TestPlexAdminProviderFetchesMetadataOncePerRatingKey(t *testing.T) {
	t.Parallel()

	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":2,"Metadata":[
				{"ratingKey":"42","type":"movie","title":"Film","viewedAt":100},
				{"ratingKey":"42","type":"movie","title":"Film","viewedAt":200}
			]}}`)
		case "/library/metadata/42":
			metadataCalls++
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"42","type":"movie","year":2020,"Guid":[{"id":"tmdb://42"}]}
			]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if metadataCalls != 1 {
		t.Fatalf("metadata calls = %d, want 1", metadataCalls)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want one deduplicated record", len(records))
	}
	if records[0].Year != 2020 {
		t.Fatalf("year = %d, want metadata fallback year", records[0].Year)
	}
	wantLastPlayed := time.Unix(200, 0).UTC()
	if records[0].LastPlayedAt == nil || !records[0].LastPlayedAt.Equal(wantLastPlayed) {
		t.Fatalf("last played = %v, want %v", records[0].LastPlayedAt, wantLastPlayed)
	}
}

func TestPlexAdminProviderSkipsMetadataForStableProviderGuid(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/status/sessions/history/all" {
			t.Errorf("unexpected metadata request to %q", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":2,"Metadata":[
			{"ratingKey":"42","type":"movie","title":"Film","year":2020},
			{"ratingKey":"42","type":"movie","title":"Film","year":2020,"Guid":[{"id":"tmdb://42"}]}
		]}}`)
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 || len(records) != 1 || records[0].TMDBID != "42" {
		t.Fatalf("records = %+v, warnings = %v", records, warnings)
	}
}

func TestPlexAdminProviderMetadataFailureFallsBackToTitleYear(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response string
		status   int
	}{
		{name: "request failure", status: http.StatusInternalServerError},
		{name: "empty metadata", status: http.StatusOK, response: `{"MediaContainer":{"Metadata":[]}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/status/sessions/history/all":
					_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":1,"Metadata":[
						{"ratingKey":"42","type":"movie","title":"Arrival","year":2016}
					]}}`)
				case "/library/metadata/42":
					w.WriteHeader(tt.status)
					_, _ = fmt.Fprint(w, tt.response)
				default:
					t.Errorf("unexpected path %q", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			records, warnings, err := NewPlexAdminProvider(
				newUnthrottledPlexClient(), server.URL, "admin-token", "7",
			).Fetch(context.Background())
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(records) != 1 || records[0].Title != "Arrival" || records[0].Year != 2016 {
				t.Fatalf("records = %+v, want title/year fallback record", records)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "1 of 1 unique items") {
				t.Fatalf("warnings = %v, want one aggregated unresolved warning", warnings)
			}
			if tt.status != http.StatusOK && !strings.Contains(warnings[0], "plex http 500") {
				t.Fatalf("warnings = %v, want the first upstream error named", warnings)
			}

			repo := &matcherRepoStub{mediaByTitleYear: map[string][]mediaLookupRow{
				"movie:Arrival:2016": {{ContentID: "movie-arrival", Title: "Arrival", Year: 2016}},
			}}
			match, reason, err := NewMatcher(repo).Match(context.Background(), records[0])
			if err != nil || reason != "" || match == nil || match.MediaItemID != "movie-arrival" {
				t.Fatalf("fallback match = %+v, reason = %q, err = %v", match, reason, err)
			}
		})
	}
}

func TestPlexAdminProviderPreservesEpisodeSeriesEnrichment(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"episode-1","type":"episode","title":"Pilot","grandparentTitle":"Rozdzielenie",
				 "grandparentRatingKey":"series-1","parentIndex":1,"index":1,"Guid":[{"id":"tvdb://episode-1"}]}
			]}}`)
		case "/library/metadata/series-1":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"series-1","type":"show","title":"Severance","year":2022,
				 "Guid":[{"id":"tvdb://371980"}]}
			]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 || len(records) != 1 {
		t.Fatalf("records = %+v, warnings = %v", records, warnings)
	}
	if records[0].SeriesTVDBID != "371980" || records[0].SeriesYear != 2022 {
		t.Fatalf("series identity = tvdb %q year %d", records[0].SeriesTVDBID, records[0].SeriesYear)
	}
}

func TestPlexAdminProviderUsesOneSeriesRequestForEpisodeHistory(t *testing.T) {
	t.Parallel()

	const episodeCount = 100
	var history strings.Builder
	history.WriteString(`{"MediaContainer":{"totalSize":100,"Metadata":[`)
	for i := 1; i <= episodeCount; i++ {
		if i > 1 {
			history.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&history,
			`{"ratingKey":"episode-%d","type":"episode","title":"Odcinek %d","grandparentTitle":"Rozdzielenie","grandparentRatingKey":"series-1","parentIndex":1,"index":%d}`,
			i, i, i,
		)
	}
	history.WriteString(`]}}`)

	seriesMetadataCalls := 0
	episodeMetadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, history.String())
		case "/library/metadata/series-1":
			seriesMetadataCalls++
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"series-1","type":"show","title":"Severance","year":2022,
				 "Guid":[{"id":"tvdb://371980"}]}
			]}}`)
		default:
			if strings.HasPrefix(r.URL.Path, "/library/metadata/episode-") {
				episodeMetadataCalls++
			}
			t.Errorf("unexpected metadata request to %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(records) != episodeCount {
		t.Fatalf("records = %d, want %d", len(records), episodeCount)
	}
	if seriesMetadataCalls != 1 || episodeMetadataCalls != 0 {
		t.Fatalf("series metadata calls = %d, episode metadata calls = %d; want 1 and 0", seriesMetadataCalls, episodeMetadataCalls)
	}
	for _, record := range records {
		if record.SeriesTVDBID != "371980" || record.EpisodeNumber <= 0 {
			t.Fatalf("record = %+v, want series identity and episode coordinates", record)
		}
	}
}

func TestPlexAdminProviderBatchesItemMetadataRequests(t *testing.T) {
	t.Parallel()

	const movieCount = plexMetadataBatchSize + 5
	var history, batchOne, batchTwo strings.Builder
	history.WriteString(`{"MediaContainer":{"totalSize":` + fmt.Sprint(movieCount) + `,"Metadata":[`)
	for i := 1; i <= movieCount; i++ {
		if i > 1 {
			history.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&history, `{"ratingKey":"%d","type":"movie","title":"Film %d","viewedAt":%d}`, i, i, i)
		target := &batchOne
		if i > plexMetadataBatchSize {
			target = &batchTwo
		}
		if target.Len() > 0 {
			target.WriteByte(',')
		}
		_, _ = fmt.Fprintf(target, `{"ratingKey":"%d","type":"movie","year":2000,"Guid":[{"id":"tmdb://%d"}]}`, i, i)
	}
	history.WriteString(`]}}`)

	var metadataPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, history.String())
		case strings.HasPrefix(r.URL.Path, "/library/metadata/"):
			metadataPaths = append(metadataPaths, r.URL.Path)
			keys := strings.Split(strings.TrimPrefix(r.URL.Path, "/library/metadata/"), ",")
			body := &batchOne
			if keys[0] != "1" {
				body = &batchTwo
			}
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[`+body.String()+`]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(metadataPaths) != 2 {
		t.Fatalf("metadata requests = %v, want two batches", metadataPaths)
	}
	if got := strings.Count(metadataPaths[0], ",") + 1; got != plexMetadataBatchSize {
		t.Fatalf("first batch carried %d keys, want %d (%s)", got, plexMetadataBatchSize, metadataPaths[0])
	}
	if len(records) != movieCount {
		t.Fatalf("records = %d, want %d", len(records), movieCount)
	}
	for _, record := range records {
		if record.TMDBID != record.ExternalID || record.Year != 2000 {
			t.Fatalf("record = %+v, want enriched from its batch", record)
		}
	}
}

func TestPlexAdminProviderRetriesFailedBatchPerKey(t *testing.T) {
	t.Parallel()

	var metadataPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/status/sessions/history/all":
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":2,"Metadata":[
				{"ratingKey":"1","type":"movie","title":"Kept","year":2001},
				{"ratingKey":"2","type":"movie","title":"Deleted","year":2002}
			]}}`)
		case "/library/metadata/1,2", "/library/metadata/2":
			metadataPaths = append(metadataPaths, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		case "/library/metadata/1":
			metadataPaths = append(metadataPaths, r.URL.Path)
			_, _ = fmt.Fprint(w, `{"MediaContainer":{"Metadata":[
				{"ratingKey":"1","type":"movie","Guid":[{"id":"tmdb://1"}]}
			]}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(metadataPaths) != 3 {
		t.Fatalf("metadata requests = %v, want batch then one per key", metadataPaths)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "1 of 2 unique items") || !strings.Contains(warnings[0], "plex http 404") {
		t.Fatalf("warnings = %v, want one unresolved item with the first error named", warnings)
	}
	byKey := map[string]Record{}
	for _, record := range records {
		byKey[record.ExternalID] = record
	}
	if byKey["1"].TMDBID != "1" || byKey["2"].TMDBID != "" || byKey["2"].Title != "Deleted" {
		t.Fatalf("records = %+v, want key 1 enriched and key 2 on title/year fallback", records)
	}
}

func TestPlexAdminProviderDoesNotRetrySystematicBatchFailurePerKey(t *testing.T) {
	t.Parallel()

	const movieCount = plexMetadataBatchSize*2 + 1
	var history strings.Builder
	history.WriteString(`{"MediaContainer":{"totalSize":` + fmt.Sprint(movieCount) + `,"Metadata":[`)
	for i := 1; i <= movieCount; i++ {
		if i > 1 {
			history.WriteByte(',')
		}
		_, _ = fmt.Fprintf(&history, `{"ratingKey":"%d","type":"movie","title":"Film %d"}`, i, i)
	}
	history.WriteString(`]}}`)

	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/status/sessions/history/all" {
			_, _ = fmt.Fprint(w, history.String())
			return
		}
		if strings.HasPrefix(r.URL.Path, "/library/metadata/") {
			metadataCalls++
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		t.Errorf("unexpected path %q", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	records, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	wantBatchCalls := (movieCount + plexMetadataBatchSize - 1) / plexMetadataBatchSize
	if metadataCalls != wantBatchCalls {
		t.Fatalf("metadata calls = %d, want %d batch requests and no per-key retries", metadataCalls, wantBatchCalls)
	}
	if len(records) != movieCount {
		t.Fatalf("records = %d, want all %d title/year fallback records", len(records), movieCount)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], fmt.Sprintf("%d of %d unique items", movieCount, movieCount)) {
		t.Fatalf("warnings = %v, want one aggregated warning for all unresolved items", warnings)
	}
}

func TestFetchMetadataBatchReadsVideoResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/library/metadata/42" {
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, `{"MediaContainer":{"Video":[
			{"ratingKey":"42","type":"movie","Guid":[{"id":"tmdb://278"}]}
		]}}`)
	}))
	defer server.Close()

	items, err := newUnthrottledPlexClient().FetchMetadataBatch(
		context.Background(), server.URL, "admin-token", []string{"42"},
	)
	if err != nil {
		t.Fatalf("FetchMetadataBatch: %v", err)
	}
	if len(items) != 1 || items[0].RatingKey != "42" {
		t.Fatalf("items = %+v, want metadata returned through Video", items)
	}
}

func TestPlexAdminProviderSkipsMetadataForNonVideoItems(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/status/sessions/history/all" {
			t.Errorf("unexpected metadata request to %q", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprint(w, `{"MediaContainer":{"totalSize":2,"Metadata":[
			{"ratingKey":"song-1","type":"track","title":"Song","viewedAt":100},
			{"ratingKey":"clip-1","type":"clip","title":"Trailer","viewedAt":200}
		]}}`)
	}))
	defer server.Close()

	_, warnings, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none for kinds the matcher does not support", warnings)
	}
}

func TestPlexAdminProviderAbortsMetadataSweepOnCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	metadataCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/status/sessions/history/all":
			var history strings.Builder
			history.WriteString(`{"MediaContainer":{"totalSize":200,"Metadata":[`)
			for i := 1; i <= 200; i++ {
				if i > 1 {
					history.WriteByte(',')
				}
				_, _ = fmt.Fprintf(&history, `{"ratingKey":"%d","type":"movie","title":"Film %d"}`, i, i)
			}
			history.WriteString(`]}}`)
			_, _ = fmt.Fprint(w, history.String())
		case strings.HasPrefix(r.URL.Path, "/library/metadata/"):
			metadataCalls++
			// The run is canceled while the first batch is in flight.
			cancel()
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	records, _, err := NewPlexAdminProvider(
		newUnthrottledPlexClient(), server.URL, "admin-token", "7",
	).Fetch(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if records != nil {
		t.Fatalf("records = %d, want none after cancellation", len(records))
	}
	if metadataCalls != 1 {
		t.Fatalf("metadata calls = %d, want the sweep to stop at the first canceled request", metadataCalls)
	}
}

func newUnthrottledPlexClient() *PlexClient {
	client := NewPlexClient()
	client.limiter = nil
	return client
}
