package markers

import (
	"context"
	"testing"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakePluginMarkerClient struct {
	fetchResp *pluginv1.FetchMarkersResponse
	fetchReq  *pluginv1.FetchMarkersRequest
	submitReq *pluginv1.SubmitMarkerRequest
}

func (f *fakePluginMarkerClient) FetchMarkers(_ context.Context, req *pluginv1.FetchMarkersRequest) (*pluginv1.FetchMarkersResponse, error) {
	f.fetchReq = req
	return f.fetchResp, nil
}

func (f *fakePluginMarkerClient) SubmitMarker(_ context.Context, req *pluginv1.SubmitMarkerRequest) (*pluginv1.SubmitMarkerResponse, error) {
	f.submitReq = req
	return &pluginv1.SubmitMarkerResponse{SubmissionId: "sub1", Status: SubmissionStatusPending, Weight: 2}, nil
}

func (f *fakePluginMarkerClient) GetMarkerProviderStats(context.Context, *pluginv1.GetMarkerProviderStatsRequest) (*pluginv1.MarkerProviderStatsResponse, error) {
	return &pluginv1.MarkerProviderStatsResponse{Total: 3, Accepted: 2, Pending: 1, AcceptanceRate: 0.66}, nil
}

func TestPluginProviderFetchMapsAllSegments(t *testing.T) {
	start10, end60 := 10.0, 60.0
	creditsStart := 1700.0
	previewStart := 1750.0
	client := &fakePluginMarkerClient{fetchResp: &pluginv1.FetchMarkersResponse{Markers: []*pluginv1.MarkerSegment{
		{Segment: "intro", StartSeconds: &start10, EndSeconds: &end60, Confidence: 0.8, SubmissionCount: 2, Algorithm: "intro:v1"},
		{Segment: "credits", StartSeconds: &creditsStart, Confidence: 0.9, SubmissionCount: 3},
		{Segment: "recap", EndSeconds: &start10, Confidence: 0.7},
		{Segment: "preview", StartSeconds: &previewStart, Confidence: 0.6},
	}}}
	provider, err := NewPluginProviderWithClientFactory(PluginProviderOptions{
		InstallationID: 12,
		CapabilityID:   "markers",
		DisplayName:    "Markers",
		PluginID:       "silo.markers",
	}, func(context.Context, int, string) (pluginMarkerClient, error) {
		return client, nil
	})
	if err != nil {
		t.Fatalf("NewPluginProviderWithClientFactory: %v", err)
	}

	res, err := provider.FetchMarkers(context.Background(), Request{
		Kind:          ItemKindEpisode,
		ExternalIDs:   map[string]string{ExternalIDKeyTVDB: "777"},
		SeasonNumber:  1,
		EpisodeNumber: 2,
		Duration:      1800 * time.Second,
	})
	if err != nil {
		t.Fatalf("FetchMarkers: %v", err)
	}
	if client.fetchReq.GetItemType() != "episode" || client.fetchReq.GetExternalIds().GetTvdbId() != "777" {
		t.Fatalf("fetch request = %+v", client.fetchReq)
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
	if len(byKind) != 4 {
		t.Fatalf("mapped %d markers, want 4: %+v", len(byKind), res.Markers)
	}
	if got := byKind[MarkerKindCredits]; got.End != 1800*time.Second {
		t.Fatalf("credits end = %s, want duration default", got.End)
	}
	if got := byKind[MarkerKindRecap]; got.Start != 0 {
		t.Fatalf("recap start = %s, want zero default", got.Start)
	}
}

func TestPluginProviderRejectsOutOfBoundsSegments(t *testing.T) {
	negativeStart := -1.0
	start10, end61 := 10.0, 61.0
	validStart := 50.0
	duration := time.Minute

	tests := []struct {
		name    string
		segment *pluginv1.MarkerSegment
		wantOK  bool
		wantEnd time.Duration
	}{
		{
			name:    "negative start",
			segment: &pluginv1.MarkerSegment{Segment: "intro", StartSeconds: &negativeStart},
		},
		{
			name:    "end past duration",
			segment: &pluginv1.MarkerSegment{Segment: "intro", StartSeconds: &start10, EndSeconds: &end61},
		},
		{
			name:    "default end uses duration",
			segment: &pluginv1.MarkerSegment{Segment: "credits", StartSeconds: &validStart},
			wantOK:  true,
			wantEnd: duration,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker, ok := markerFromPluginSegment(tt.segment, duration)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && marker.End != tt.wantEnd {
				t.Fatalf("end = %s, want %s", marker.End, tt.wantEnd)
			}
		})
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
	if client.submitReq.GetSegment() != "intro" || client.submitReq.GetStartSeconds() != 5 || client.submitReq.GetEndSeconds() != 30 {
		t.Fatalf("submit request segment = %+v", client.submitReq)
	}
}
