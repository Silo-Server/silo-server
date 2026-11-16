package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

// TestPrepareVirtualAlternateReusesAFreshResolvedSource pins the alternate-walk
// reuse: an alternate candidate row resolves against the fresh provider answer a
// replan already holds instead of forcing another listing. Every alternate of one
// title shares a provider-neutral key, so a forced list per sibling paid the same
// multi-second round-trip repeatedly for one candidate set.
func TestPrepareVirtualAlternateReusesAFreshResolvedSource(t *testing.T) {
	const alternateURI = "virtual://movie/tt-alt-reuse?result=cand-alt"
	alternate := &models.MediaFile{
		ID: 601, ContentID: "movie-alt-reuse", FilePath: alternateURI, VirtualOwnerInstallationID: 5,
	}
	var forced []bool
	h := &PlaybackHandler{
		VirtualPlaybackResolver: VirtualPlaybackResolverFunc(func(_ context.Context, path string, _ int, _ string, _ int) (string, error) {
			return "http://127.0.0.1:9/stream?path=" + path, nil
		}),
		VirtualMediaDetailedResolver: VirtualMediaDetailedResolverFunc(func(_ context.Context, uri string, _ int, _ int, _ string, forceRefresh bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			forced = append(forced, forceRefresh)
			return ResolvedVirtualMedia{URL: "http://127.0.0.1:9/stream", URI: uri, CandidateID: "cand-alt"}, nil
		}),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/playback/replan", nil)
	prepared, _, err := h.prepareVirtualAlternateFileV3(req, alternate, "profile-1", false)
	if err != nil {
		t.Fatalf("prepare alternate: %v", err)
	}
	if prepared == nil || prepared.FilePath != alternateURI {
		t.Fatalf("prepared alternate = %#v, want %q", prepared, alternateURI)
	}
	if len(forced) != 1 || forced[0] {
		t.Fatalf("forced re-list flags = %v, want the alternate resolved from the fresh provider answer", forced)
	}
}
