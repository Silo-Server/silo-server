package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type staticSessionImageResolver struct{ url string }

func (r staticSessionImageResolver) ResolveImageURL(context.Context, string, string) string {
	return r.url
}

func (r staticSessionImageResolver) ResolveImageURLs(_ context.Context, paths []string, _ string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		out[p] = r.url
	}
	return out
}

// A loader without ItemPosters keeps same-server rooted artwork URLs and omits
// off-server object-store URLs rather than leaking them on session rows.
func TestPlaybackSessionsLoaderFallbackKeepsPostersOnServer(t *testing.T) {
	localDetail := &catalog.DetailService{}
	localDetail.SetImageResolver(staticSessionImageResolver{url: "/api/v2/artwork/tmdb/movies/1/poster/w300.r1.webp?exp=1&sig=x"})
	localLoader := NewPlaybackSessionsLoader(nil, nil, localDetail)
	if got := localLoader.posterURL(t.Context(), "movie-1", "tmdb/movies/1/poster/original.r1.webp"); got != "/api/v2/artwork/tmdb/movies/1/poster/w300.r1.webp?exp=1&sig=x" {
		t.Fatalf("local fallback = %q", got)
	}

	s3Detail := &catalog.DetailService{}
	s3Detail.SetImageResolver(staticSessionImageResolver{url: "https://s3.example/bucket/tmdb/movies/1/poster/w300.r1.webp?X-Amz-Signature=abc"})
	s3Loader := NewPlaybackSessionsLoader(nil, nil, s3Detail)
	if got := s3Loader.posterURL(t.Context(), "movie-1", "tmdb/movies/1/poster/original.r1.webp"); got != "" {
		t.Fatalf("off-server fallback = %q, want empty", got)
	}
}

// BenchmarkActivitySessionEnrichment measures reporting work only. It does not
// include transport startup, media I/O, database latency, or client rendering.
func BenchmarkActivitySessionEnrichment(b *testing.B) {
	for _, method := range []string{"direct", "remux", "direct_stream", "transcode"} {
		b.Run(method, func(b *testing.B) {
			input := playbackSessionRow{PlayMethod: method, SourceContainer: "mkv", SourceVideoCodec: "hevc", SourceVideoResolution: "2160p", SourceAudioCodec: "truehd"}
			if method == "direct_stream" {
				input.PlayMethod, input.TranscodeAudio, input.TargetAudioCodec = "remux", true, "aac"
			}
			if method == "transcode" {
				input.TargetVideoCodec = "h264"
			}
			b.ReportAllocs()
			for b.Loop() {
				row := input
				enrichPlaybackSessionRow(&row, nil)
				if row.EffectivePlayMethod == "" {
					b.Fatal("known route became unknown")
				}
			}
		})
	}
}
