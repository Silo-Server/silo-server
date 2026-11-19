package handlers

import (
	"testing"
)

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
