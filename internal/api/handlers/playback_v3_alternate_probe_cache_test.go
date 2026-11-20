package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestPrepareVirtualAlternateFileSkipsProbeForCachedCandidate pins the alternate
// loop's probe-cache-first behavior: a version switch whose first candidate
// misses and whose next candidate already has a completed probe must not pay a
// second synchronous ffprobe for that candidate. The version-switch path runs
// resolve+probe per candidate, so without the cache a provider renumbering turns
// one switched version into N probes. The candidate is still resolved (the
// provider URL is needed), but the probe is served from the cache.
func TestPrepareVirtualAlternateFileSkipsProbeForCachedCandidate(t *testing.T) {
	const alternateURI = "virtual://movie/tt-alt-probe-cache?result=cand-alt"

	alternate := &models.MediaFile{
		ID:                         601,
		ContentID:                  "movie-alt-probe-cache",
		FilePath:                   alternateURI,
		VirtualOwnerInstallationID: 5,
		Container:                  "virtual",
	}

	detailedCalls := 0
	proberCalls := 0
	cacheLookups := 0
	h := &PlaybackHandler{
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, _ string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			detailedCalls++
			return ResolvedVirtualMedia{URL: "https://93.184.216.34/stream/token=alt", URI: alternateURI, CandidateID: "cand-alt"}, nil
		}),
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(_ context.Context, _ string, _ int, _ string, _ int) (string, error) {
			return "https://93.184.216.34/stream/token=legacy", nil
		}),
		VirtualPlaybackSourceProber: func(_ context.Context, _ string, f *models.MediaFile) (*models.MediaFile, error) {
			proberCalls++
			f.VideoTracks = []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}}
			f.AudioTracks = []models.AudioTrack{{Codec: "aac", Channels: 2}}
			f.Container = "mkv"
			f.Resolution = "1080p"
			f.CodecVideo = "h264"
			f.CodecAudio = "aac"
			return f, nil
		},
		VirtualProbeCacheLookup: func(_ string, probeFile *models.MediaFile) *models.MediaFile {
			cacheLookups++
			return &models.MediaFile{
				FilePath:                   probeFile.FilePath,
				VirtualOwnerInstallationID: probeFile.VirtualOwnerInstallationID,
				VideoTracks:                []models.VideoTrack{{Codec: "h264", Width: 1920, Height: 1080}},
				AudioTracks:                []models.AudioTrack{{Codec: "aac", Channels: 2}},
				Resolution:                 "1080p",
				CodecVideo:                 "h264",
				CodecAudio:                 "aac",
				Container:                  "mkv",
				Duration:                   5400,
			}
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/replan", nil)
	prepared, provenance, err := h.prepareVirtualAlternateFileV3(req, alternate, "profile-1", false)
	if err != nil {
		t.Fatalf("prepareVirtualAlternateFileV3 error: %v", err)
	}
	if prepared == nil || prepared.FilePath != alternateURI {
		t.Fatalf("prepared alternate = %#v, want the resolved candidate %q", prepared, alternateURI)
	}
	if provenance != ProbeProvenanceVerified {
		t.Fatalf("prepared provenance = %q, want %q (the resolver's cached-probe verdict, not discarded)", provenance, ProbeProvenanceVerified)
	}
	if proberCalls != 0 {
		t.Fatalf("synchronous prober calls = %d, want 0: the cached candidate must not be re-probed", proberCalls)
	}
	if cacheLookups == 0 {
		t.Fatal("probe cache was never consulted for the alternate candidate")
	}
	if detailedCalls == 0 {
		t.Fatal("the alternate candidate was never resolved: the cache short-circuits the probe, not the resolve")
	}
}
