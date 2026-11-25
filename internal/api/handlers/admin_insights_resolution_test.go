package handlers

import "testing"

func TestInsightsResolutionClasses(t *testing.T) {
	cases := []struct {
		name                         string
		label                        string
		width, height                int
		method, videoDecision, codec string
		target                       string
		routeFacts                   bool
		wantSource, wantDelivered    string
	}{
		// A full-size encode of a scope 4K file reports its exact height; it is
		// still 4K, not a downscale to 1080p.
		{name: "scope 4K original encode", label: "2160p", width: 3840, height: 1600, method: "transcode", videoDecision: "transcode", target: "1600p", routeFacts: true, wantSource: "2160p", wantDelivered: "2160p"},
		{name: "scope 4K on the 1080p rung", label: "2160p", width: 3840, height: 1600, method: "transcode", videoDecision: "transcode", target: "800p", routeFacts: true, wantSource: "2160p", wantDelivered: "1080p"},
		{name: "scope 1080p on the 720p rung", label: "1080p", width: 1920, height: 800, method: "transcode", videoDecision: "transcode", target: "534p", routeFacts: true, wantSource: "1080p", wantDelivered: "720p"},
		{name: "same-size encode of a 2562x1440 source", label: "2160p", width: 2562, height: 1440, method: "transcode", videoDecision: "transcode", target: "1440p", routeFacts: true, wantSource: "2160p", wantDelivered: "2160p"},
		{name: "transcode that kept the source size", label: "1080p", width: 1920, height: 1080, method: "transcode", videoDecision: "transcode", routeFacts: true, wantSource: "1080p", wantDelivered: "1080p"},
		{name: "audio-only transcode copies the video", label: "2160p", width: 3840, height: 2160, method: "transcode", videoDecision: "remux", target: "1080p", routeFacts: true, wantSource: "2160p", wantDelivered: "2160p"},
		{name: "transcode recorded before route facts", label: "1080p", width: 1920, height: 1080, method: "transcode", wantSource: "1080p", wantDelivered: "unknown"},
		{name: "direct play", label: "720p", width: 1280, height: 720, method: "direct", wantSource: "720p", wantDelivered: "720p"},
		{name: "legacy 4k label without dimensions", label: "4k", method: "remux", wantSource: "2160p", wantDelivered: "2160p"},
		{name: "PAL SD label lands where the scanner puts 720x576", label: "576p", method: "direct", wantSource: "720p", wantDelivered: "720p"},
		{name: "unknown source", method: "transcode", videoDecision: "transcode", target: "720p", routeFacts: true, wantSource: "unknown", wantDelivered: "720p"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := insightsSourceResolutionClass(tc.label, tc.width, tc.height)
			dst := insightsDeliveredResolutionClass(tc.method, tc.videoDecision, tc.codec, tc.target, tc.routeFacts, src, tc.width, tc.height)
			if src != tc.wantSource || dst != tc.wantDelivered {
				t.Fatalf("source=%q delivered=%q, want %q %q", src, dst, tc.wantSource, tc.wantDelivered)
			}
		})
	}
}
