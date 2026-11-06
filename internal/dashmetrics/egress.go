package dashmetrics

import (
	"fmt"

	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// egressDelta is the viewer bytes one process served between two telemetry
// snapshots. Total covers every viewer-egress byte; Download is the subset
// served by file-transfer routes (telemetry transfers: offline/direct
// downloads, ebook and ABS file fetches) rather than streaming playback.
// Download is always <= Total, so a reader can derive playback as the
// difference without ever going negative. Sessions holds each playback
// session's share of the growth, keyed by session id, for sessions that grew.
type egressDelta struct {
	Total    int64
	Download int64
	Sessions map[string]int64
}

// computeEgressDelta returns the viewer bytes this process served between two
// telemetry snapshots, together with the cumulative counters the next call must
// compare against.
//
// Only RoleViewerEgress routes and transfers count. A proxy node's viewer
// traffic also traverses the API node as RoleInternalRelay, and counting both
// would report every relayed byte twice.
//
// The split leans on the registry's own taxonomy: playback traffic aggregates
// into logical sessions (ClassPlayback/ClassManifest routes), while
// file-transfer traffic aggregates into transfers (ClassTransfer routes), so
// session growth is playback egress and transfer growth is download egress. A
// route added to either class is classified automatically.
//
// Sessions are keyed by id and generation: the registry prunes a session five
// minutes after its last request, and one re-created under the same id (a play
// resumed after a long pause) counts from zero again under a new generation,
// so all of its bytes are new growth. A counter that shrinks anyway
// contributes nothing rather than a negative rate. Entries that vanished from the snapshot are dropped, which
// keeps the map bounded by the live session count.
func computeEgressDelta(prev map[string]int64, snapshot streamtelemetry.Snapshot) (egressDelta, map[string]int64) {
	next := make(map[string]int64, len(snapshot.Sessions)+len(snapshot.Transfers))
	var delta egressDelta

	record := func(key string, cumulative int64) int64 {
		next[key] = cumulative
		if grown := cumulative - prev[key]; grown > 0 {
			return grown
		}
		return 0
	}

	for _, session := range snapshot.Sessions {
		var bytes int64
		for _, route := range session.Routes {
			if route.Role == streamtelemetry.RoleViewerEgress {
				bytes += route.BytesAccepted
			}
		}
		key := fmt.Sprintf("session:%s\x00%d", session.SessionID, session.Generation)
		if grown := record(key, bytes); grown > 0 {
			delta.Total += grown
			if delta.Sessions == nil {
				delta.Sessions = make(map[string]int64)
			}
			delta.Sessions[session.SessionID] += grown
		}
	}

	for _, transfer := range snapshot.Transfers {
		if transfer.Role != streamtelemetry.RoleViewerEgress {
			continue
		}
		grown := record("transfer:"+transfer.ID, transfer.BytesAccepted)
		delta.Total += grown
		delta.Download += grown
	}

	return delta, next
}
