package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// selectionRequalifiedFixture is the measured production shape: the viewer
// selected a persisted candidate (`?result=R`), and the provider's current
// listing names the same result id under a profile-qualified URI
// (`?profile=1080p&result=R`) whose own catalog row exists but has never been
// probed. Rows 1760194 and 1760227 on the reporting deployment are this pair.
func selectionRequalifiedFixture(t *testing.T, proberCalls *atomic.Int32) (*PlaybackHandler, *models.MediaFile, string) {
	t.Helper()
	const (
		selectedURI = "virtual://series/tmdb/259140/1/3?result=be28"
		listedURI   = "virtual://series/tmdb/259140/1/3?profile=1080p&result=be28"
	)
	stamp := time.Now().Add(-time.Hour)
	selected := &models.MediaFile{
		ID: 194, ContentID: "episode-tmdb-259140-1-3",
		FilePath: selectedURI, Container: "mkv", CodecVideo: "hevc", CodecAudio: "eac3",
		Resolution: "1080p", Bitrate: 3295, Duration: 1462, FileSize: 700_000_000,
		ProbeSource: "virtual", ProbeUpdatedAt: &stamp,
		ProviderReleaseName: "ranma122024s01e03eac3dl1080pwebdvhdrh265vector",
		ProviderReleaseSize: 700_000_000,
		VideoTracks:         []models.VideoTrack{{Codec: "hevc", Width: 1920, Height: 1080, BitDepth: 10, FrameRate: "23.976", Bitrate: 3295}},
		AudioTracks:         []models.AudioTrack{{Codec: "eac3", Channels: 2, Language: "de"}},
		SubtitleTracks:      []models.SubtitleTrack{{Codec: "ass", Language: "de"}},
		UpdatedAt:           time.Now(),
	}
	owner := &models.MediaFile{
		ID: 227, ContentID: "episode-tmdb-259140-1-3",
		FilePath: listedURI, Container: "virtual",
		ProviderReleaseName: "ranma122024s01e03eac3dl1080pwebdvhdrh265vector",
		ProviderReleaseSize: 700_000_000,
		UpdatedAt:           time.Now(),
	}
	h := &PlaybackHandler{
		VirtualCandidateTrustWindow: func() time.Duration { return 720 * time.Hour },
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(
			func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
				return "https://93.184.216.34/stream?path=" + path, nil
			}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(
			func(context.Context, string, int, int, string, bool, []string, string) (ResolvedVirtualMedia, error) {
				return ResolvedVirtualMedia{URL: "https://93.184.216.34/stream/token=fresh", URI: listedURI, CandidateID: "be28"}, nil
			}),
		// force_relink asks for the provider's current answer, so the listing
		// runs and carries the profile-qualified URI.
		VirtualPlaybackStreamLister: VirtualPlaybackStreamListerFunc(
			func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
				return []VirtualPlaybackStream{{
					ID: "be28", URI: listedURI, Resolution: "1080p",
					CodecVideo: "hevc", CodecAudio: "eac3", Container: "mkv",
				}}, nil
			}),
		VirtualFileLookup: func(_ context.Context, path string) (*models.MediaFile, error) {
			switch path {
			case listedURI:
				row := *owner
				return &row, nil
			case selectedURI:
				row := *selected
				return &row, nil
			}
			return nil, nil
		},
		VirtualPlaybackSourceProber: func(_ context.Context, _ string, file *models.MediaFile) (*models.MediaFile, error) {
			proberCalls.Add(1)
			return file, nil
		},
	}
	return h, selected, listedURI
}

// TestExplicitSelectionKeepsProbeEvidenceOnRequalifiedListedRow pins the
// measured selected-start regression: a forced relist renamed the selected
// candidate's URI, the exact-path owner row carried no evidence, and the start
// enumerated the same release's streams again. The selected row already proved
// those bytes, so the plan must reuse that inventory and arm no probe.
func TestExplicitSelectionKeepsProbeEvidenceOnRequalifiedListedRow(t *testing.T) {
	for _, test := range []struct {
		name      string
		deferPast bool
	}{{name: "post-commit deferred start", deferPast: true}, {name: "synchronous replan", deferPast: false}} {
		t.Run(test.name, func(t *testing.T) {
			var proberCalls atomic.Int32
			h, selected, listedURI := selectionRequalifiedFixture(t, &proberCalls)
			req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
			resolved, err := h.resolveVirtualPlaybackSource(req, selected, "profile-1", true, nil, "be28", "auto", 0, true,
				virtualResolveOptionsV3{allowFailedCandidate: true, explicitSelection: true, deferProbePastCommit: test.deferPast})
			if err != nil {
				t.Fatalf("resolveVirtualPlaybackSource error: %v", err)
			}
			if got := proberCalls.Load(); got != 0 {
				t.Fatalf("prober ran %d times for a release the selected row already enumerated", got)
			}
			if resolved.DeferredProbe != nil {
				t.Fatal("a deferred probe was armed for a release that already carries inventory")
			}
			if !resolved.ProbeSucceeded {
				t.Fatalf("provenance = %q, want verified evidence reused", resolved.Provenance)
			}
			if resolved.URI != listedURI {
				t.Fatalf("resolved URI = %q, want the listed candidate %q", resolved.URI, listedURI)
			}
			if resolved.File == nil || resolved.File.ID != 227 {
				t.Fatalf("resolved row = %#v, want the listed candidate's owner row 227", resolved.File)
			}
			if len(resolved.File.VideoTracks) != 1 || len(resolved.File.AudioTracks) != 1 || len(resolved.File.SubtitleTracks) != 1 {
				t.Fatalf("carried inventory = video %d, audio %d, subtitle %d; want the selected row's 1/1/1",
					len(resolved.File.VideoTracks), len(resolved.File.AudioTracks), len(resolved.File.SubtitleTracks))
			}
			if resolved.File.ProbeUpdatedAt == nil {
				t.Fatal("carried evidence has no probe stamp; the row would look unprobed again")
			}
			if resolved.File.FilePath != listedURI {
				t.Fatalf("resolved path = %q, want the served candidate %q", resolved.File.FilePath, listedURI)
			}
		})
	}
}
