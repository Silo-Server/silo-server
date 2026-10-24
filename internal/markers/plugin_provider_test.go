package markers

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakePluginMarkerClient struct {
	fetchResp *pluginv1.FetchMarkersResponse
	fetchReq  *pluginv1.FetchMarkersRequest
	submitReq *pluginv1.SubmitMarkerRequest
	submitErr error
}

func (f *fakePluginMarkerClient) FetchMarkers(_ context.Context, req *pluginv1.FetchMarkersRequest) (*pluginv1.FetchMarkersResponse, error) {
	f.fetchReq = req
	return f.fetchResp, nil
}

func (f *fakePluginMarkerClient) SubmitMarker(_ context.Context, req *pluginv1.SubmitMarkerRequest) (*pluginv1.SubmitMarkerResponse, error) {
	f.submitReq = req
	if f.submitErr != nil {
		return nil, f.submitErr
	}
	return &pluginv1.SubmitMarkerResponse{SubmissionId: "sub1", Status: SubmissionStatusPending, Weight: 2}, nil
}

func (f *fakePluginMarkerClient) GetMarkerProviderStats(context.Context, *pluginv1.GetMarkerProviderStatsRequest) (*pluginv1.MarkerProviderStatsResponse, error) {
	return &pluginv1.MarkerProviderStatsResponse{Total: 3, Accepted: 2, Pending: 1, AcceptanceRate: 0.66}, nil
}

func TestPluginProviderFetchMapsAllSegments(t *testing.T) {
	start10, end60 := 10.0, 60.0
	negativeStart, endPastDuration := -1.0, 1801.0
	creditsStart := 1700.0
	client := &fakePluginMarkerClient{fetchResp: &pluginv1.FetchMarkersResponse{Markers: []*pluginv1.MarkerSegment{
		{Segment: "intro", StartSeconds: &start10, EndSeconds: &end60, Confidence: 0.8, SubmissionCount: 2, Algorithm: "intro:v1"},
		{Segment: "credits", StartSeconds: &creditsStart, Confidence: -0.2, SubmissionCount: 3},
		{Segment: "recap", EndSeconds: &start10, Confidence: math.NaN()},
		{Segment: "preview", Absent: true, Confidence: 0.95, Algorithm: "consensus:v1"},
		{Segment: "intro", StartSeconds: &negativeStart},
		{Segment: "intro", StartSeconds: &start10, EndSeconds: &endPastDuration},
	}}}
	provider, err := NewPluginProviderWithClientFactory(PluginProviderOptions{
		InstallationID: 12,
		CapabilityID:   "markers",
		DisplayName:    "Markers",
		PluginID:       "silo.markers",
		CacheRevision:  "config-revision",
	}, func(context.Context, int, string) (pluginMarkerClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory: %v", err)
	}
	if provider.CacheRevision() != "config-revision" {
		t.Fatalf("cache revision = %q, want configured revision", provider.CacheRevision())
	}

	res, err := provider.FetchMarkers(context.Background(), Request{
		Kind:               ItemKindEpisode,
		ExternalIDs:        map[string]string{ExternalIDKeyTVDB: "777"},
		SeasonNumber:       1,
		EpisodeNumber:      2,
		EpisodeExternalIDs: map[string]string{ExternalIDKeyTVDB: "9001", ExternalIDKeyTMDB: "63"},
		EpisodeNumbering:   "tvdb",
		EpisodeTitle:       "Second Episode",
		Duration:           1800 * time.Second,
		FileHash:           "8e245d9679d31e12",
		FileSize:           734003200,
	})
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if client.fetchReq.GetItemType() != "episode" || client.fetchReq.GetExternalIds().GetTvdbId() != "777" {
		t.Fatalf("fetch request = %+v", client.fetchReq)
	}
	if episode := client.fetchReq.GetEpisodeExternalIds(); episode.GetTvdbId() != "9001" || episode.GetTmdbId() != "63" ||
		client.fetchReq.GetEpisodeTitle() != "Second Episode" || client.fetchReq.GetEpisodeNumbering() != "tvdb" {
		t.Fatalf("fetch episode identity = %+v %q", episode, client.fetchReq.GetEpisodeTitle())
	}
	if client.fetchReq.GetFileHash() != "8e245d9679d31e12" || client.fetchReq.GetFileSize() != 734003200 || !client.fetchReq.GetAcceptsAbsent() {
		t.Fatalf("fetch file facts = %q %d accepts_absent=%v", client.fetchReq.GetFileHash(), client.fetchReq.GetFileSize(), client.fetchReq.GetAcceptsAbsent())
	}
	if res.SourceClass != models.MarkerSourcePlugin || res.ProviderID != "plugin:12:markers" {
		t.Fatalf("result provenance = source %q provider %q", res.SourceClass, res.ProviderID)
	}
	byKind := map[MarkerKind]Marker{}
	for _, marker := range res.Markers {
		byKind[marker.Kind] = marker
		if marker.SourceClass != models.MarkerSourcePlugin || marker.ProviderID != "plugin:12:markers" {
			t.Fatalf("marker provenance = %+v", marker)
		}
	}
	if len(res.Markers) != 4 || len(byKind) != 4 {
		t.Fatalf("mapped %d markers, want 4: %+v", len(byKind), res.Markers)
	}
	if got := byKind[MarkerKindIntro]; got.Confidence != 0.8 {
		t.Fatalf("intro confidence = %v, want 0.8 kept", got.Confidence)
	}
	if got := byKind[MarkerKindCredits]; got.End != 1800*time.Second || got.Confidence != 0 {
		t.Fatalf("credits end = %s confidence = %v, want duration default and 0 for a negative confidence", got.End, got.Confidence)
	}
	if got := byKind[MarkerKindRecap]; got.Start != 0 || got.Confidence != 0 {
		t.Fatalf("recap start = %s confidence = %v, want zero defaults for a NaN confidence", got.Start, got.Confidence)
	}
	// An absent answer has no bounds, which must not default to the whole
	// file: it is stored as a tombstone with the provider's provenance.
	if got := byKind[MarkerKindPreview]; !got.Absent || got.Start != 0 || got.End != 0 {
		t.Fatalf("absent preview = %+v, want an absent answer without bounds", got)
	}
	preview := BuildUpdatePayload(res).Preview
	if preview.Present() || !preview.Absent() || preview.Source != models.MarkerSourcePlugin ||
		preview.Provider == nil || *preview.Provider != "plugin:12:markers" || preview.Algorithm != "consensus:v1" ||
		preview.Confidence == nil || *preview.Confidence != 0.95 {
		t.Fatalf("absent preview payload = %+v, want a plugin tombstone", preview)
	}
	// A NaN confidence would win every comparison it is the incumbent of.
	if got, ok := markerFromPluginSegment(&pluginv1.MarkerSegment{Segment: "intro", Absent: true, Confidence: math.NaN()}, time.Hour); !ok || got.Confidence != 0 {
		t.Fatalf("absent intro with a NaN confidence = %+v ok=%v, want confidence 0", got, ok)
	}
	if got, ok := markerFromPluginSegment(&pluginv1.MarkerSegment{Segment: "intro", StartSeconds: &start10, EndSeconds: &end60, Confidence: 1.5}, time.Hour); !ok || got.Confidence != 0 {
		t.Fatalf("intro with a confidence above 1 = %+v ok=%v, want confidence 0", got, ok)
	}
}

func TestPluginProviderSubmitMapsRequest(t *testing.T) {
	client := &fakePluginMarkerClient{}
	provider, err := NewPluginProviderWithClientFactory(PluginProviderOptions{
		InstallationID: 12,
		CapabilityID:   "markers",
	}, func(context.Context, int, string) (pluginMarkerClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory: %v", err)
	}
	start, end := 5*time.Second, 30*time.Second
	result, err := provider.SubmitMarker(context.Background(), SubmissionRequest{
		Kind:        ItemKindMovie,
		ExternalIDs: map[string]string{ExternalIDKeyIMDB: "tt1"},
		Segment:     MarkerKindIntro,
		Start:       &start,
		End:         &end,
		Duration:    90 * time.Minute,
		FileHash:    "8e245d9679d31e12",
		FileSize:    734003200,
		Source:      models.MarkerSourceScanner,
		Algorithm:   "chromaprint:v3",
		Confidence:  0.97,
	})
	if err != nil {
		t.Fatalf("SubmitMarker: %v", err)
	}
	if result.ID != "sub1" || result.Status != SubmissionStatusPending || result.Weight != 2 {
		t.Fatalf("submit result = %+v", result)
	}
	if client.submitReq.GetItemType() != "movie" || client.submitReq.GetExternalIds().GetImdbId() != "tt1" {
		t.Fatalf("submit request identity = %+v", client.submitReq)
	}
	// A movie has no episode identity; the field stays unset as from an
	// older host.
	if client.submitReq.EpisodeExternalIds != nil || client.submitReq.GetEpisodeTitle() != "" {
		t.Fatalf("movie submit carried episode identity: %+v", client.submitReq)
	}
	if client.submitReq.GetSegment() != "intro" || client.submitReq.GetStartSeconds() != 5 || client.submitReq.GetEndSeconds() != 30 {
		t.Fatalf("submit request segment = %+v", client.submitReq)
	}
	if client.submitReq.GetFileHash() != "8e245d9679d31e12" || client.submitReq.GetFileSize() != 734003200 ||
		client.submitReq.GetSource() != models.MarkerSourceScanner || client.submitReq.GetAlgorithm() != "chromaprint:v3" ||
		client.submitReq.GetConfidence() != 0.97 || client.submitReq.GetAbsent() {
		t.Fatalf("submit request provenance = %+v", client.submitReq)
	}
}

func TestPluginProviderSubmitMapsConflicts(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "structured already exists",
			err:  status.Error(codes.AlreadyExists, "submission already exists"),
		},
		{
			name: "legacy HTTP 409",
			err:  status.Error(codes.Unknown, `introdb: submit HTTP 409: {"error":"already submitted"}`),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := submitWithPluginError(t, tt.err)
			var conflict *SubmissionConflictError
			if !errors.As(err, &conflict) {
				t.Fatalf("error = %T %v, want SubmissionConflictError", err, err)
			}
			if conflict.Provider != "plugin:12:markers" || conflict.HTTPStatus != 409 {
				t.Fatalf("conflict = %+v", conflict)
			}
		})
	}
}

func TestPluginProviderSubmitMapsPermanentRefusals(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name:       "legacy HTTP 400",
			err:        status.Error(codes.Unknown, `introdb: submit HTTP 400: {"error":"Season 2 does not exist for this show on TMDB"}`),
			wantStatus: 400,
		},
		{
			name:       "legacy HTTP 422",
			err:        status.Error(codes.Unknown, `introdb: submit HTTP 422: unprocessable`),
			wantStatus: 422,
		},
		{
			name: "structured invalid argument",
			err:  status.Error(codes.InvalidArgument, "season does not exist"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := submitWithPluginError(t, tt.err)
			var invalid *SubmissionInvalidError
			if !errors.As(err, &invalid) {
				t.Fatalf("error = %T %v, want SubmissionInvalidError", err, err)
			}
			if invalid.Provider != "plugin:12:markers" || invalid.HTTPStatus != tt.wantStatus {
				t.Fatalf("invalid = %+v, want HTTP status %d", invalid, tt.wantStatus)
			}
		})
	}
}

func TestPluginProviderSubmitKeepsOtherErrorsRetryable(t *testing.T) {
	for _, pluginErr := range []error{
		status.Error(codes.Unknown, "introdb: submit HTTP 500: unavailable"),
		status.Error(codes.Unknown, "introdb: submit HTTP 401: invalid api key"),
		status.Error(codes.Unknown, "introdb: submit HTTP 408: timeout"),
		status.Error(codes.Unavailable, "plugin unavailable"),
		status.Error(codes.FailedPrecondition, "api key not configured"),
	} {
		_, err := submitWithPluginError(t, pluginErr)
		var conflict *SubmissionConflictError
		var invalid *SubmissionInvalidError
		if errors.As(err, &conflict) || errors.As(err, &invalid) {
			t.Fatalf("error for %v = %T, want retryable provider error", pluginErr, err)
		}
	}
}

func submitWithPluginError(t *testing.T, pluginErr error) (SubmissionResult, error) {
	t.Helper()
	client := &fakePluginMarkerClient{submitErr: pluginErr}
	provider, err := NewPluginProviderWithClientFactory(PluginProviderOptions{
		InstallationID: 12,
		CapabilityID:   "markers",
	}, func(context.Context, int, string) (pluginMarkerClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory: %v", err)
	}
	return provider.SubmitMarker(context.Background(), SubmissionRequest{
		Kind:        ItemKindMovie,
		ExternalIDs: map[string]string{ExternalIDKeyTMDB: "123"},
		Segment:     MarkerKindIntro,
	})
}

// The cmd/silo reload test covers the Submitter side; this pins what that
// test does not see.
func TestReadOnlyPluginProviderIsFetchOnly(t *testing.T) {
	inner, err := NewPluginProviderWithClientFactory(PluginProviderOptions{
		InstallationID: 42,
		CapabilityID:   "public-markers",
		CacheRevision:  "config-revision",
	}, func(context.Context, int, string) (pluginMarkerClient, error) {
		return &fakePluginMarkerClient{}, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory: %v", err)
	}
	var provider Provider = NewReadOnlyPluginProvider(inner)
	if _, ok := provider.(SubmissionRequirementProvider); ok {
		t.Fatal("read-only plugin provider exposes submission requirements")
	}
	// Population keys its cache by CacheRevision, and plugin reload compares it
	// to decide whether to refresh the runtime.
	revisioned, ok := provider.(interface{ CacheRevision() string })
	if !ok || revisioned.CacheRevision() != "config-revision" {
		t.Fatalf("CacheRevision not forwarded: ok=%v", ok)
	}
}

func TestPluginCapabilityFlagsFromMetadata(t *testing.T) {
	tests := []struct {
		name     string
		parse    func(map[string]any) bool
		metadata map[string]any
		want     bool
	}{
		{name: "contribution: key absent keeps legacy submitters", parse: PluginSupportsContributionFromMetadata, metadata: map[string]any{"default_fetch_priority": 20}, want: true},
		{name: "contribution: explicit true", parse: PluginSupportsContributionFromMetadata, metadata: map[string]any{"supports_contribution": true}, want: true},
		{name: "contribution: explicit false", parse: PluginSupportsContributionFromMetadata, metadata: map[string]any{"supports_contribution": false}, want: false},
		{name: "contribution: string false", parse: PluginSupportsContributionFromMetadata, metadata: map[string]any{"supports_contribution": " false "}, want: false},
		{name: "contribution: unparseable string stays compatible", parse: PluginSupportsContributionFromMetadata, metadata: map[string]any{"supports_contribution": "nope"}, want: true},
		{name: "contribution: wrong type stays compatible", parse: PluginSupportsContributionFromMetadata, metadata: map[string]any{"supports_contribution": float64(0)}, want: true},
		{name: "amend: key absent", parse: PluginSupportsAmendFromMetadata, metadata: map[string]any{"supports_contribution": true}, want: false},
		{name: "amend: explicit true", parse: PluginSupportsAmendFromMetadata, metadata: map[string]any{"supports_amend": true}, want: true},
		{name: "absent: key absent", parse: PluginSupportsAbsentFromMetadata, metadata: nil, want: false},
		{name: "absent: explicit true", parse: PluginSupportsAbsentFromMetadata, metadata: map[string]any{"supports_absent": true}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.parse(tt.metadata); got != tt.want {
				t.Fatalf("parse() = %v, want %v", got, tt.want)
			}
		})
	}
}
