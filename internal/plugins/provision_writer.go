package plugins

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	provisionLockNamespace = 0x53494c4f // SILO
	provisionLockID        = 0x50524f56 // PROV
	provisionHeartbeat     = 5 * time.Second
	provisionDBTimeout     = 10 * time.Second
)

var errProvisionAuthorityLost = errors.New("plugin provisioning writer session lost")

type ProvisionStatus struct {
	PluginID string
	Version  string
	State    string
	Detail   string
}

func ListProvisionStatus(ctx context.Context, db provisionConfigQuery) ([]ProvisionStatus, error) {
	rows, err := db.Query(ctx, `SELECT plugin_id, version, state, detail FROM plugin_provision_status ORDER BY plugin_id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ProvisionStatus])
}

type ProvisionWriter struct {
	pool          *pgxpool.Pool
	repositories  *RepositoryStore
	installations *InstallationStore
	installer     *Installer
	catalog       *CatalogService
	configs       *RuntimeConfigStore
	service       *Service
	document      *ProvisionDocument
	placeMetadata func(context.Context, pgx.Tx, int) error

	sessionName      string
	conn             *pgx.Conn
	retry            bool
	placementPending bool
	heldForRestore   bool
	started          bool

	localMu     sync.RWMutex
	localState  string
	localDetail string
}

func NewProvisionWriter(pool *pgxpool.Pool, repositories *RepositoryStore, installations *InstallationStore, installer *Installer, catalog *CatalogService, configs *RuntimeConfigStore, service *Service, document *ProvisionDocument, placeMetadata func(context.Context, pgx.Tx, int) error) *ProvisionWriter {
	return &ProvisionWriter{
		sessionName: "silo-plugin-provision-" + cryptorand.Text(),
		pool:        pool, repositories: repositories, installations: installations, installer: installer, catalog: catalog,
		configs: configs, service: service, document: document, placeMetadata: placeMetadata,
	}
}

func (w *ProvisionWriter) statuses(state, detail string) []ProvisionStatus {
	records := make([]ProvisionStatus, 0, len(w.document.Plugins))
	for _, id := range slices.Sorted(maps.Keys(w.document.Plugins)) {
		records = append(records, ProvisionStatus{PluginID: id, Version: w.document.Plugins[id].Version, State: state, Detail: detail})
	}
	return records
}

func (w *ProvisionWriter) setLocalStatus(state, detail string) {
	w.localMu.Lock()
	defer w.localMu.Unlock()
	w.localState, w.localDetail = state, detail
}

func (w *ProvisionWriter) localStatusReason() (string, string) {
	w.localMu.RLock()
	defer w.localMu.RUnlock()
	return w.localState, w.localDetail
}

func (w *ProvisionWriter) LocalStatus() []ProvisionStatus {
	state, detail := w.localStatusReason()
	if state == "" {
		return nil
	}
	return w.statuses(state, detail)
}

func (w *ProvisionWriter) tryWriter(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, provisionDBTimeout)
	defer cancel()
	// This session lives outside the service pool. It still owns the lock
	// while other readers and preparation use the pool.
	cfg := w.pool.Config().ConnConfig.Copy()
	cfg.RuntimeParams["application_name"] = w.sessionName
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return false, err
	}
	var acquired bool
	err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1::int,$2::int)`, provisionLockNamespace, provisionLockID).Scan(&acquired)
	if err == nil && !acquired && w.started {
		// A broken TCP connection can leave our old backend holding the
		// lock. Retry that case, but yield permanently to a different writer.
		var otherWriter bool
		err = conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 `+provisionLockFrom+`
			AND pid NOT IN (SELECT pid FROM pg_stat_activity WHERE application_name = $3))`,
			uint32(provisionLockNamespace), uint32(provisionLockID), w.sessionName).Scan(&otherWriter)
		if err == nil && !otherWriter {
			err = errors.New("waiting to reacquire the provisioning writer lock")
		}
	}
	if err != nil || !acquired {
		_ = conn.Close(ctx)
		return false, err
	}
	w.conn = conn
	w.started = true
	return true, nil
}

const provisionLockFrom = `FROM pg_locks
	WHERE locktype = 'advisory' AND granted AND mode = 'ExclusiveLock'
	  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
	  AND classid = $1::oid AND objid = $2::oid AND objsubid = 2`

func verifyProvisionAuthority(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) error {
	var held bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 `+provisionLockFrom+` AND pid = pg_backend_pid())`,
		uint32(provisionLockNamespace), uint32(provisionLockID)).Scan(&held)
	if err != nil || !held {
		return errProvisionAuthorityLost
	}
	return nil
}

func (w *ProvisionWriter) Close() {
	if w.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), provisionDBTimeout)
		defer cancel()
		_ = w.conn.Close(ctx)
		w.conn = nil
	}
}

// StartOffline claims writer authority and tries to apply using only stored
// verified archives. The listener can start while remote work is pending.
func (w *ProvisionWriter) StartOffline(ctx context.Context) error {
	if w.document == nil {
		return nil
	}
	acquired, err := w.tryWriter(ctx)
	if err != nil {
		w.setLocalStatus("pending", "Cannot reach the provisioning writer database session.")
		w.retry = true
		return nil //nolint:nilerr // A local database outage leaves provisioning pending until Run can claim the session.
	}
	if !acquired {
		w.reportConflict(ctx)
		return nil
	}
	w.placementPending = w.placeMetadata != nil
	w.retry, err = w.apply(ctx, true)
	if errors.Is(err, errProvisionAuthorityLost) || w.conn.IsClosed() {
		w.loseAuthority()
		return nil
	}
	// Operational failures keep the API available. Invalid local input is
	// the only error the submitting process must fail startup for.
	if _, invalidInput := errors.AsType[*provisionRefusalError](err); err != nil && !invalidInput {
		w.retry = true
		w.setLocalStatus("pending", "Could not write provisioning status. The writer will retry.")
		return nil
	}
	if err == nil && w.placementPending {
		w.tryPlacement(ctx)
	}
	return err
}

func (w *ProvisionWriter) reportConflict(ctx context.Context) {
	const detail = "Another API instance holds provisioning authority. Restart this instance after the current writer stops."
	w.setLocalStatus("refused", detail)
	slog.WarnContext(ctx, detail, "component", "plugins")
}

func (w *ProvisionWriter) Run(ctx context.Context) {
	defer w.Close()
	if w.document == nil || (!w.started && !w.retry) {
		return
	}
	const minDelay = 2 * time.Second
	delay := minDelay
	tick := time.NewTicker(provisionHeartbeat)
	defer tick.Stop()
	for ctx.Err() == nil {
		if w.conn == nil {
			acquired, err := w.tryWriter(ctx)
			if err == nil && !acquired {
				w.reportConflict(ctx)
				return
			}
			if err == nil {
				w.setLocalStatus("", "")
				w.placementPending = w.placeMetadata != nil
			}
		}
		if w.conn != nil && w.retry {
			w.applyOnline(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		if w.conn != nil && w.placementPending {
			w.tryPlacement(ctx)
		}
		if !w.retry && !w.placementPending {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := w.checkAuthority(ctx); err != nil {
					w.loseAuthority()
				}
			}
			continue
		}
		timer := time.NewTimer(delay + rand.N(delay/2)) //nolint:gosec // Retry jitter has no security purpose.
		wait := true
		for wait {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				wait = false
			case <-tick.C:
				if w.conn != nil && w.checkAuthority(ctx) != nil {
					w.loseAuthority()
				}
			}
		}
		delay = min(delay*2, time.Minute)
	}
}

func (w *ProvisionWriter) applyOnline(ctx context.Context) {
	retry, err := w.apply(ctx, false)
	w.retry = retry
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(err, errProvisionAuthorityLost) || w.conn.IsClosed():
		w.loseAuthority()
	case err != nil:
		slog.WarnContext(ctx, "could not persist plugin provisioning status", "component", "plugins")
	default:
		w.setLocalStatus("", "")
	}
}

func (w *ProvisionWriter) tryPlacement(ctx context.Context) {
	changed, err := w.finishPlacement(ctx)
	if errors.Is(err, errProvisionRestoreHeld) {
		w.retry, err = w.recordRestoreHold(ctx)
	}
	if changed && w.service != nil {
		w.service.OnLifecycleChange(ctx)
	}
	if errors.Is(err, errProvisionAuthorityLost) || w.conn.IsClosed() {
		w.loseAuthority()
		return
	}
	if err != nil {
		slog.WarnContext(ctx, "could not place a provisioned metadata provider in library chains; will retry", "component", "plugins")
		return
	}
	w.placementPending = false
}

func (w *ProvisionWriter) finishPlacement(ctx context.Context) (bool, error) {
	readCtx, cancel := context.WithTimeout(ctx, provisionDBTimeout)
	rows, err := w.conn.Query(readCtx, `SELECT plugin_installation_id FROM plugin_provision_placement ORDER BY plugin_installation_id`)
	if err != nil {
		cancel()
		return false, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int])
	cancel()
	if err != nil {
		return false, err
	}
	changed := false
	var failures error
	for _, id := range ids {
		err := w.placeInstallation(ctx, id)
		switch {
		case err == nil:
			changed = true
		case errors.Is(err, ErrInstallationNotFound):
			// Deleting the installation cascades to the queue.
		case ctx.Err() != nil, w.conn.IsClosed(), errors.Is(err, errProvisionAuthorityLost), errors.Is(err, errProvisionRestoreHeld):
			return changed, err
		default:
			failures = errors.Join(failures, err)
		}
	}
	return changed, failures
}

// Commit defaults and acknowledge one installation together. A failed or slow
// placement stays queued without rolling back completed work for other plugins.
func (w *ProvisionWriter) placeInstallation(ctx context.Context, id int) error {
	ctx, cancel := context.WithTimeout(ctx, provisionDBTimeout)
	defer cancel()
	return pgx.BeginFunc(ctx, w.conn, func(tx pgx.Tx) error {
		if err := verifyProvisionAuthority(ctx, tx); err != nil {
			return err
		}
		if err := lockPluginInstallation(ctx, tx, id); err != nil {
			return err
		}
		if err := checkPluginRestoreHold(ctx, tx); err != nil {
			return err
		}
		if err := w.placeMetadata(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM plugin_provision_placement WHERE plugin_installation_id = $1`, id); err != nil {
			return err
		}
		return verifyProvisionAuthority(ctx, tx)
	})
}

func (w *ProvisionWriter) checkAuthority(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, provisionDBTimeout)
	defer cancel()
	return verifyProvisionAuthority(ctx, w.conn)
}

func (w *ProvisionWriter) loseAuthority() {
	w.Close()
	w.retry = true
	w.setLocalStatus("pending", "The provisioning writer lost its database session and will retry after obtaining authority.")
}

func (w *ProvisionWriter) saveFailureStatus(ctx context.Context, state, detail string, refusal *provisionRefusalError) error {
	return pgx.BeginFunc(ctx, w.conn, func(tx pgx.Tx) error {
		statuses, err := w.unappliedStatuses(ctx, tx, nil, state, detail)
		if err != nil {
			return err
		}
		for i := range statuses {
			if refusal != nil && statuses[i].PluginID == refusal.pluginID {
				statuses[i].Detail = refusal.detail
			}
		}
		if err := writeProvisionStatuses(ctx, tx, statuses); err != nil {
			return err
		}
		return verifyProvisionAuthority(ctx, tx)
	})
}

func writeProvisionStatuses(ctx context.Context, tx pgx.Tx, statuses []ProvisionStatus) error {
	ids := make([]string, 0, len(statuses))
	for _, row := range statuses {
		ids = append(ids, row.PluginID)
		if _, err := tx.Exec(ctx, `
			INSERT INTO plugin_provision_status (plugin_id, version, state, detail) VALUES ($1,$2,$3,$4)
			ON CONFLICT (plugin_id) DO UPDATE SET version = EXCLUDED.version, state = EXCLUDED.state, detail = EXCLUDED.detail
			WHERE (plugin_provision_status.version, plugin_provision_status.state, plugin_provision_status.detail)
				IS DISTINCT FROM (EXCLUDED.version, EXCLUDED.state, EXCLUDED.detail)
		`, row.PluginID, row.Version, row.State, row.Detail); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM plugin_provision_status WHERE NOT (plugin_id = ANY($1::text[]))`, ids); err != nil {
		return fmt.Errorf("delete obsolete provisioning status: %w", err)
	}
	return nil
}

// SetProvisionWriter attaches the declaring process's status before serving.
func (s *RuntimeConfigStore) SetProvisionWriter(writer *ProvisionWriter) {
	s.provisionWriter = writer
}

// ListProvisionStatus combines the committed status with a local failure to
// obtain writer authority, if this process submitted a declaration.
func (s *RuntimeConfigStore) ListProvisionStatus(ctx context.Context) ([]ProvisionStatus, error) {
	if s.provisionWriter == nil {
		return ListProvisionStatus(ctx, s.pool)
	}
	state, detail := s.provisionWriter.localStatusReason()
	if state == "" {
		return ListProvisionStatus(ctx, s.pool)
	}
	var statuses []ProvisionStatus
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		committed, err := ListProvisionStatus(ctx, tx)
		if err != nil {
			return err
		}
		statuses, err = s.provisionWriter.unappliedStatuses(ctx, tx, committed, state, detail)
		return err
	})
	if err != nil && len(s.provisionWriter.document.Plugins) != 0 {
		// The declaring process can still explain its own refusal during a database outage.
		return s.provisionWriter.statuses(state, detail), nil
	}
	return statuses, err
}

// A declaration that fails also leaves its requested releases unapplied. Include
// every installation still owned, then overlay the requested versions.
func (w *ProvisionWriter) unappliedStatuses(ctx context.Context, db provisionConfigQuery, committed []ProvisionStatus, state, detail string) ([]ProvisionStatus, error) {
	rows, err := db.Query(ctx, `SELECT i.plugin_id, i.version, $1::text, $2::text FROM plugin_installations i
		JOIN plugin_provision_ownership o ON o.plugin_installation_id = i.id`, state, detail)
	if err != nil {
		return nil, err
	}
	owned, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ProvisionStatus])
	if err != nil {
		return nil, err
	}
	records := make(map[string]ProvisionStatus)
	for _, status := range committed {
		if len(w.document.Plugins) == 0 {
			status.State, status.Detail = state, detail
		}
		records[status.PluginID] = status
	}
	for _, status := range owned {
		records[status.PluginID] = status
	}
	for _, status := range w.statuses(state, detail) {
		records[status.PluginID] = status
	}
	return sortedProvisionStatus(records), nil
}

func sortedProvisionStatus(records map[string]ProvisionStatus) []ProvisionStatus {
	ordered := make([]ProvisionStatus, 0, len(records))
	for _, id := range slices.Sorted(maps.Keys(records)) {
		ordered = append(ordered, records[id])
	}
	return ordered
}
