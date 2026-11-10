package policy

import (
	"context"
	"testing"
	"time"
)

// BenchmarkResolveViewerScope measures a full evaluation: each iteration
// presents an access policy revision the memo has not seen.
func BenchmarkResolveViewerScope(b *testing.B) {
	ctx := context.Background()
	engine, err := NewEngine(ctx)
	if err != nil {
		b.Fatal(err)
	}
	pdp := NewPDP(engine)
	inputs := make([]ScopeInput, 2*decisionMemoEntries)
	for i := range inputs {
		inputs[i] = benchmarkScopeInput()
		inputs[i].AccessPolicyRevision = int64(i)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := pdp.ResolveViewerScope(ctx, inputs[i%len(inputs)]); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkResolveViewerScopeMemoHit measures one viewer's requests in
// successive seconds, which the vendor bundle decides alike because it never
// reads request_time.
func BenchmarkResolveViewerScopeMemoHit(b *testing.B) {
	ctx := context.Background()
	engine, err := NewEngine(ctx)
	if err != nil {
		b.Fatal(err)
	}
	pdp := NewPDP(engine)
	inputs := make([]ScopeInput, 2*decisionMemoEntries)
	start := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	for i := range inputs {
		inputs[i] = benchmarkScopeInput()
		inputs[i].RequestTime = start.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := pdp.ResolveViewerScope(ctx, inputs[i%len(inputs)]); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkScopeInput() ScopeInput {
	return ScopeInput{
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
}
