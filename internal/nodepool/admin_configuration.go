package nodepool

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AdminConfigurationStore coordinates configuration writes with stored validators
// and durable pool invalidation. It does not contact workers or acknowledge that
// any replica has applied a committed configuration.
type AdminConfigurationStore struct{ pool *pgxpool.Pool }

func NewAdminConfigurationStore(pool *pgxpool.Pool) *AdminConfigurationStore {
	return &AdminConfigurationStore{pool: pool}
}

var ErrNodeConfigurationConflict = errors.New("node URL is already configured differently")

// ErrTranscodeNodeURLTaken refuses a transcode node at an address another
// transcode node already has, spelled differently ("http://gpu" and
// "http://GPU:80/"). The credential registry finds nodes by that canonical
// address, so two of them there would leave one unreachable by its key.
var ErrTranscodeNodeURLTaken = errors.New("another transcode node already has this URL")

// transcodeURLLock serializes the writes that give a transcode node a URL, so
// two of them cannot both pass the canonical-URL check before either commits.
// Taken before any row lock, so it never waits while holding one.
const transcodeURLLock = "nodepool.stream_nodes.transcode_url"

func lockTranscodeURLs(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, transcodeURLLock)
	return err
}

// checkTranscodeURLFree returns ErrTranscodeNodeURLTaken when a transcode row
// other than exceptID has nodeURL's canonical form under another spelling. A
// row with the identical URL is left to the unique constraint, whose
// violation the create path resolves as a retry.
func checkTranscodeURLFree(ctx context.Context, tx pgx.Tx, exceptID int, nodeURL string) error {
	rows, err := tx.Query(ctx, `SELECT url FROM stream_nodes WHERE type = $1 AND id <> $2`, NodeTypeTranscode, exceptID)
	if err != nil {
		return err
	}
	urls, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	canonical := CanonicalNodeURL(nodeURL)
	for _, other := range urls {
		if other != nodeURL && CanonicalNodeURL(other) == canonical {
			return ErrTranscodeNodeURLTaken
		}
	}
	return nil
}

// Snapshot pairs node configurations and revisions in one database snapshot.
// The generation includes deletions, even when the resulting list is empty.
func (s *AdminConfigurationStore) Snapshot(ctx context.Context) ([]*Node, int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT generation FROM stream_node_pool_generation WHERE singleton`).Scan(&generation); err != nil {
		return nil, 0, err
	}
	nodes, err := NewTransactionalRepository(tx).List(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := tx.Query(ctx, `SELECT id, admin_revision FROM stream_nodes`)
	if err != nil {
		return nil, 0, err
	}
	revisions, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
		ID       int
		Revision int64
	}])
	if err != nil {
		return nil, 0, err
	}
	byID := make(map[int]int64, len(revisions))
	for _, row := range revisions {
		byID[row.ID] = row.Revision
	}
	for _, node := range nodes {
		node.AdminRevision = byID[node.ID]
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return nodes, generation, nil
}

func readConfigurationRevision(ctx context.Context, tx pgx.Tx, id int) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, `SELECT admin_revision FROM stream_nodes WHERE id=$1 FOR UPDATE`, id).Scan(&revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNodeNotFound
	}
	return revision, err
}

// Create resolves an exact natural-key retry without overwriting a competing
// configuration. Its original insert already persisted the reconciliation marker.
func (s *AdminConfigurationStore) Create(ctx context.Context, input CreateNodeInput) (*Node, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if input.Type == NodeTypeTranscode {
		if err = lockTranscodeURLs(ctx, tx); err != nil {
			return nil, err
		}
		if err = checkTranscodeURLFree(ctx, tx, 0, input.URL); err != nil {
			return nil, err
		}
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	node, err := NewTransactionalRepository(savepoint).Create(ctx, input)
	if err == nil {
		if err = savepoint.Commit(ctx); err != nil {
			return nil, err
		}
	} else {
		original := err
		if err = savepoint.Rollback(ctx); err != nil {
			return nil, err
		}
		pgerr, ok := errors.AsType[*pgconn.PgError](original)
		if !ok || pgerr.Code != "23505" || pgerr.ConstraintName != "stream_nodes_url_key" {
			return nil, original
		}
		var id int
		if err = tx.QueryRow(ctx, `SELECT id FROM stream_nodes WHERE url=$1 FOR UPDATE`, input.URL).Scan(&id); err != nil {
			return nil, ErrNodeConfigurationConflict
		}
		node, err = NewTransactionalRepository(tx).GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if !matchesCreation(node, input) {
			return nil, ErrNodeConfigurationConflict
		}
	}
	node.AdminRevision, err = readConfigurationRevision(ctx, tx, node.ID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return node, nil
}

func matchesCreation(node *Node, input CreateNodeInput) bool {
	return node.Name == input.Name && node.Type == input.Type && node.URL == input.URL && node.Enabled &&
		reflect.DeepEqual(node.PublicURL, normalizeOverride(input.PublicURL)) &&
		reflect.DeepEqual(node.Group, normalizeGroup(input.Group)) &&
		reflect.DeepEqual(node.MaxJobs, normalizeCap(input.MaxJobs)) &&
		reflect.DeepEqual(node.MaxBandwidthKbps, normalizeCap(input.MaxBandwidthKbps)) &&
		node.HWAccelOverride == nil && node.HWDeviceOverride == nil &&
		node.DownloadArtifactDirOverride == nil && node.DownloadArtifactMaxBytesOverride == nil &&
		node.SourceAccess == sourceAccessOrDefault(input.SourceAccess)
}

// Update evaluates the original-version guard while holding the row lock. The
// trigger records configuration changes from bridge and v2 writers alike. The
// returned previous row is the locked pre-image of the same transaction, so a
// caller can tell a policy change (URL or acceleration override) from a resubmit.
func (s *AdminConfigurationStore) Update(ctx context.Context, id int, input UpdateNodeInput, guard func(int64) error) (node, previous *Node, err error) {
	if err := input.Validate(); err != nil {
		return nil, nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if input.URL != nil {
		// Before the row lock: the type is not known yet, but the lock must
		// come first wherever it is taken.
		if err = lockTranscodeURLs(ctx, tx); err != nil {
			return nil, nil, err
		}
	}
	revision, err := readConfigurationRevision(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if err = guard(revision); err != nil {
		return nil, nil, err
	}
	repo := NewTransactionalRepository(tx)
	previous, err = repo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	previous.AdminRevision = revision
	if err = input.ValidateFor(previous); err != nil {
		return nil, nil, err
	}
	// Only a change of address is checked, so a row that already shared its
	// address before this check existed can still be edited otherwise.
	if input.URL != nil && previous.Type == NodeTypeTranscode && CanonicalNodeURL(*input.URL) != CanonicalNodeURL(previous.URL) {
		if err = checkTranscodeURLFree(ctx, tx, id, *input.URL); err != nil {
			return nil, nil, err
		}
	}
	node, err = repo.Update(ctx, id, input)
	if err != nil {
		return nil, nil, err
	}
	node.AdminRevision, err = readConfigurationRevision(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return node, previous, nil
}

// Delete atomically removes the row and records durable reconciliation work.
// A missing node is not a receipt for a previous caller's deletion.
func (s *AdminConfigurationStore) Delete(ctx context.Context, id int, guard func(int64) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	revision, err := readConfigurationRevision(ctx, tx, id)
	if err != nil {
		return err
	}
	if err = guard(revision); err != nil {
		return err
	}
	if err = NewTransactionalRepository(tx).Delete(ctx, id); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit node deletion: %w", err)
	}
	return nil
}
