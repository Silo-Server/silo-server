package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// A cold virtual resolve owns one deadline. Once it is spent, the candidate
// loop must stop instead of starting another resolve: every further attempt is
// pre-empted before its own wait begins and reports a negative bound (the
// production series bound_ms=-34/-65/-95/-126 on four consecutive attempts),
// and the resulting "provider listing failure" is what let the version fallback
// swap the viewer's release. The first candidate here burns the deadline; no
// later candidate may be attempted on the spent budget.
func TestResolveVirtualPlaybackSourceStopsWhenTheColdBudgetIsSpent(t *testing.T) {
	const neut = "virtual://movie/zz-resolve-budget"
	file := &models.MediaFile{ID: 1, ContentID: "movie-zz-resolve-budget", FilePath: neut, VirtualOwnerInstallationID: 5}
	candidates := []VirtualPlaybackStream{
		{ID: "a", URI: neut + "?result=a", Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv"},
		{ID: "b", URI: neut + "?result=b", Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv"},
		{ID: "c", URI: neut + "?result=c", Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv"},
		{ID: "d", URI: neut + "?result=d", Resolution: "1080p", CodecVideo: "h264", CodecAudio: "aac", Container: "mkv"},
	}
	resolves := 0
	attempted := make([]string, 0, len(candidates))
	h := NewPlaybackHandler(playback.NewSessionManager(0, 0))
	h.VirtualPlaybackResolver = VirtualPlaybackResolverFunc(func(context.Context, string, int, string, int) (string, error) {
		return "", errors.New("simple resolver must not be used when the detailed resolver is set")
	})
	h.VirtualPlaybackStreamLister = VirtualPlaybackStreamListerFunc(
		func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return candidates, nil
		})
	h.VirtualMediaDetailedResolver = VirtualMediaDetailedResolverFunc(
		func(ctx context.Context, uri string, _ int, _ int, _ string, _ bool, _ []string, _ string) (ResolvedVirtualMedia, error) {
			resolves++
			attempted = append(attempted, virtualResultCandidateID(uri))
			// The first attempt spends the cold budget; later attempts would
			// inherit an already-spent deadline.
			<-ctx.Done()
			return ResolvedVirtualMedia{}, errors.New("resolve virtual playback: no streams available from provider")
		})

	req := httptest.NewRequest(http.MethodPost, "/api/v2/playback/start", nil)
	budgetCtx, cancel := context.WithTimeout(req.Context(), 200*time.Millisecond)
	defer cancel()
	req = req.WithContext(budgetCtx)

	if _, err := h.resolveVirtualPlaybackSource(req, file, "profile-1", false, nil, "", "auto", 0, false); err == nil {
		t.Fatal("a resolve whose cold budget ran out returned a source")
	}
	if len(attempted) != 1 {
		t.Fatalf("candidate attempts = %v, want only the first candidate before the cold budget was spent", attempted)
	}
	if resolves != 1 {
		t.Fatalf("provider resolves = %d, want 1", resolves)
	}
}
