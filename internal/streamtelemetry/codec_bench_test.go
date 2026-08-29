package streamtelemetry

import (
	"fmt"
	"testing"
	"time"
)

var benchmarkTime = time.Unix(1_700_000_000, 0)

func benchmarkSessionID(index int) string { return fmt.Sprintf("session-%05d", index) }

func BenchmarkCodec(b *testing.B) {
	session := populatedSessionView()
	encoded, err := encodeSession(session)
	if err != nil {
		b.Fatal(err)
	}
	b.Run("encode_session", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			if _, err := encodeSession(session); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(len(encoded)), "encoded_bytes")
	})
	b.Run("decode_session", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(encoded)))
		for b.Loop() {
			if _, err := decodeSession(encoded); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(len(encoded)), "encoded_bytes")
	})
}

func BenchmarkBuildGlobalView(b *testing.B) {
	const sessionCount = 50_000
	cfg := DefaultConfig("node")
	snapshot := Snapshot{PublisherID: "publisher", NodeID: "node", PublisherEpoch: 1, Sequence: 1, CapturedAt: benchmarkTime}
	snapshot.Sessions = make([]SessionView, sessionCount)
	for i := range snapshot.Sessions {
		snapshot.Sessions[i] = SessionView{SessionID: benchmarkSessionID(i), Routes: []RouteActivityView{{Role: RoleViewerEgress, BytesAccepted: 1}}}
	}
	set := PublisherSet{Members: []Member{{PublisherID: "publisher", LastHeartbeat: benchmarkTime}}, Snapshots: []Snapshot{snapshot}}
	params := ViewParams{Freshness: cfg.Freshness, MembershipTTL: cfg.MembershipTTL, MaxMergedSessions: sessionCount, MaxMergedTransfers: cfg.MaxMergedTransfers,
		MaxViewerIPsPerSession: cfg.MaxViewerIPsPerSession, MaxDeviceIDsPerSession: cfg.MaxDeviceIDsPerSession, MaxClientVariantsPerSession: cfg.MaxClientVariantsPerSession,
		MaxUserAgentsPerSession: cfg.MaxClientVariantsPerSession, MaxMediaFileIDsPerSession: cfg.MaxMediaFileIDsPerSession, MaxPlayMethodsPerSession: cfg.MaxPlayMethodsPerSession,
		MaxTokenIssuedAtPerSession: cfg.MaxTokenIssuedAtPerSession, MaxRoutesPerSession: cfg.MaxRoutesPerSession, MaxIdentityConflictsPerSession: cfg.MaxIdentityConflictsPerSession}
	b.ReportAllocs()
	for b.Loop() {
		_ = BuildGlobalView(set, benchmarkTime, params)
	}
}
