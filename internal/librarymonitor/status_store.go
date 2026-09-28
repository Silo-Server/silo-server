package librarymonitor

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/models"
)

// StatusFreshness is how old a node report may be before readers ignore
// it. Nodes refresh every DefaultStatusRefresh, so a fresh row survives two
// missed refreshes.
const StatusFreshness = 3 * time.Minute

// NodeReport is one stored row of library_monitor_status.
type NodeReport struct {
	NodeID      string
	LibraryID   int
	State       State
	Backend     string
	Detail      string
	Directories int
	UpdatedAt   time.Time
}

// StatusStore persists node reports in library_monitor_status. It
// implements StatusReporter.
type StatusStore struct {
	db *pgxpool.Pool
}

// NewStatusStore returns a store over db.
func NewStatusStore(db *pgxpool.Pool) *StatusStore {
	return &StatusStore{db: db}
}

var _ StatusReporter = (*StatusStore)(nil)

// Report replaces the node's rows in one transaction: every given status is
// upserted with updated_at = now(), and the node's rows for libraries not
// listed are deleted. A status for a library that no longer exists is
// skipped rather than failing the batch.
func (s *StatusStore) Report(ctx context.Context, nodeID string, statuses []LibraryStatus) error {
	// One row per library; a later status for the same library wins, since
	// an upsert cannot touch the same row twice.
	index := make(map[int]int, len(statuses))
	var (
		ids         []int
		states      []string
		backends    []string
		details     []string
		directories []int
	)
	for _, st := range statuses {
		if i, ok := index[st.LibraryID]; ok {
			states[i], backends[i], details[i], directories[i] = string(st.State), st.Backend, st.Detail, st.Directories
			continue
		}
		index[st.LibraryID] = len(ids)
		ids = append(ids, st.LibraryID)
		states = append(states, string(st.State))
		backends = append(backends, st.Backend)
		details = append(details, st.Detail)
		directories = append(directories, st.Directories)
	}
	if ids == nil {
		// A NULL array would match nothing in the prune below.
		ids = []int{}
	}

	return pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if len(ids) > 0 {
			if _, err := tx.Exec(ctx, `
				INSERT INTO library_monitor_status (node_id, library_id, state, backend, detail, directories, updated_at)
				SELECT $1, u.library_id, u.state, u.backend, u.detail, u.directories, now()
				FROM unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::int[])
					AS u(library_id, state, backend, detail, directories)
				WHERE EXISTS (SELECT 1 FROM media_folders f WHERE f.id = u.library_id)
				ON CONFLICT (node_id, library_id) DO UPDATE SET
					state = EXCLUDED.state,
					backend = EXCLUDED.backend,
					detail = EXCLUDED.detail,
					directories = EXCLUDED.directories,
					updated_at = EXCLUDED.updated_at`,
				nodeID, ids, states, backends, details, directories); err != nil {
				return fmt.Errorf("upsert library monitor status: %w", err)
			}
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM library_monitor_status WHERE node_id = $1 AND NOT (library_id = ANY($2::int[]))`,
			nodeID, ids); err != nil {
			return fmt.Errorf("prune library monitor status: %w", err)
		}
		return nil
	})
}

// RemoveNode deletes every row of the node.
func (s *StatusStore) RemoveNode(ctx context.Context, nodeID string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM library_monitor_status WHERE node_id = $1`, nodeID); err != nil {
		return fmt.Errorf("remove library monitor status: %w", err)
	}
	return nil
}

// FreshReports returns every report younger than StatusFreshness, measured
// on the database clock that stamped it, ordered by library then node.
func (s *StatusStore) FreshReports(ctx context.Context) ([]NodeReport, error) {
	rows, err := s.db.Query(ctx, `
		SELECT node_id, library_id, state, backend, detail, directories, updated_at
		FROM library_monitor_status
		WHERE updated_at > now() - make_interval(secs => $1)
		ORDER BY library_id, node_id`, StatusFreshness.Seconds())
	if err != nil {
		return nil, fmt.Errorf("list library monitor status: %w", err)
	}
	reports, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (NodeReport, error) {
		var r NodeReport
		var state string
		err := row.Scan(&r.NodeID, &r.LibraryID, &state, &r.Backend, &r.Detail, &r.Directories, &r.UpdatedAt)
		r.State = State(state)
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("scan library monitor status: %w", err)
	}
	return reports, nil
}

// StatusSnapshot is everything the admin status read derives its states
// from.
type StatusSnapshot struct {
	// ServerEnabled is the live scanner.realtime_monitoring value.
	ServerEnabled bool
	Libraries     []*models.MediaFolder
	// Reports are the fresh node reports.
	Reports []NodeReport
}

// StatusReader assembles a StatusSnapshot for the admin status read.
type StatusReader struct {
	Store   *StatusStore
	Folders FolderLister
	// ServerEnabled reads the live scanner.realtime_monitoring value.
	ServerEnabled func() bool
}

// RealtimeMonitoringStatus reads the server switch, every library, and the
// fresh node reports.
func (r *StatusReader) RealtimeMonitoringStatus(ctx context.Context) (StatusSnapshot, error) {
	folders, err := r.Folders.List(ctx)
	if err != nil {
		return StatusSnapshot{}, fmt.Errorf("list libraries: %w", err)
	}
	reports, err := r.Store.FreshReports(ctx)
	if err != nil {
		return StatusSnapshot{}, fmt.Errorf("read library monitor status: %w", err)
	}
	enabled := true
	if r.ServerEnabled != nil {
		enabled = r.ServerEnabled()
	}
	return StatusSnapshot{ServerEnabled: enabled, Libraries: folders, Reports: reports}, nil
}
