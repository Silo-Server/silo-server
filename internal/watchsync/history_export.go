package watchsync

import (
	"time"

	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"
)

// maxHistoryExportPage bounds one read of a connection's pending history
// exports: the largest batch a plugin may declare.
const maxHistoryExportPage = publicmanifest.MaxWatchSyncBatchSize

// exportDrainReserve is how much of a run's deadline the history export
// leaves unused: room for one plugin call that has already started and for
// the flows that run after the export. A manual sync has a deadline; a
// scheduled one does not, so it drains the whole backlog.
const exportDrainReserve = 3 * time.Minute

// exportSnapshotSkew allows for clock difference between the server, which
// stamps when a run read local history, and the database, which stamps when
// an export row was created.
const exportSnapshotSkew = time.Minute

// HistoryExportCursor is the last export one pass over the backlog listed, in
// the backlog's (watched_at, id) order. Listing after it continues the pass, so
// an export the pass already tried is not tried again before the next sync.
// The zero value starts at the oldest export.
type HistoryExportCursor struct {
	WatchedAt time.Time
	ID        string
}
