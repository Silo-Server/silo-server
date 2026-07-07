package policy

import (
	"context"
	"testing"
)

func BenchmarkResolveViewerScope(b *testing.B) {
	ctx := context.Background()
	engine, err := NewEngine(ctx)
	if err != nil {
		b.Fatal(err)
	}
	pdp := NewPDP(engine)
	input := ScopeInput{
		SchemaVersion:         1,
		UserID:                42,
		SessionID:             "sess-1",
		ProfileID:             "prof-1",
		AccountLibraryIDs:     []int{1, 2, 3},
		AccountRestricted:     true,
		AccountMaxQuality:     "2160p",
		AccessPolicyRevision:  9,
		DisabledLibraryIDs:    []int{2},
		ProfilePresent:        true,
		ProfileMaxRating:      "PG-13",
		ProfileMaxQuality:     "720p",
		ProfileLibraryLimited: true,
		ProfileLibraryIDs:     []int{2, 3, 4},
		ProfileHasPIN:         true,
		ProfileVerified:       true,
		ProfileMetadataLang:   "fr",
		RequestTime:           "2026-07-02T12:00:00Z",
		DeviceID:              "device-1",
		ClientIP:              "192.0.2.10",
		IsAPIKey:              false,
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := pdp.ResolveViewerScope(ctx, input); err != nil {
			b.Fatal(err)
		}
	}
}
