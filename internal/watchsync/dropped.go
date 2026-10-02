package watchsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/historyimport"
)

// This file syncs a profile's dropped shows with a provider, both ways. A
// local drop (catalog.DroppedSeriesRepo) is active until the profile watches
// the series again, so an undrop can come from an explicit undo or from new
// watch activity; either reads here as the local value turning false.
//
// Each series is a three-way merge of the local drop, the remote drop, and
// whether both sides last agreed it was dropped (watch_provider_dropped_items).
// Whichever side moved away from the agreed value wins. When both moved, the
// values agree and there is nothing to do, with one exception: a remote drop
// older than the profile's latest watch of the series in Silo is undropped on
// the provider instead of imported, because the profile watched it since.
//
// Reconciliation shares the rating sync lock (WithRatingSyncLock), which
// serializes a connection's merges across nodes. Local writes are
// compare-and-set on the row this run read, so a concurrent dismissal or undo
// wins and is reconsidered next run.

const (
	droppedExportBatchSize = 100
	// droppedCursorSegment marks provider cursor keys that belong to dropped
	// reads, so they reset with the agreed drops on a provider account switch.
	droppedCursorSegment = ".dropped"
)

// droppedStore is the profile's dropped-series table (catalog.DroppedSeriesRepo).
type droppedStore interface {
	ListDropped(ctx context.Context, userID int, profileID string, seriesIDs []string) ([]catalog.DroppedSeries, error)
	LatestActivity(ctx context.Context, userID int, profileID string, seriesIDs []string) (map[string]time.Time, error)
	ImportDrop(ctx context.Context, userID int, profileID, seriesID string, droppedAt time.Time, observed *time.Time) (bool, error)
	DeleteIfUnchanged(ctx context.Context, userID int, profileID, seriesID string, observed time.Time) (bool, error)
	DeleteInactive(ctx context.Context, userID int, profileID string, seriesIDs []string) error
}

func (s *Service) WithDroppedStore(store droppedStore) *Service {
	if s != nil {
		s.dropped = store
	}
	return s
}

type droppedAction uint8

const (
	droppedAgree droppedAction = iota
	droppedExportDrop
	droppedExportUndrop
	droppedImportDrop
	droppedImportUndrop
)

// decideDropped merges one series. base is the agreed value. localActivity is
// the profile's latest watch of the series and remoteAt the remote drop time;
// either may be zero when unknown. endedByWatch means the profile still has a
// drop row that watching ended, as opposed to no row after an undo.
func decideDropped(local, remote, base, endedByWatch bool, localActivity, remoteAt time.Time) droppedAction {
	switch {
	case local == remote:
		return droppedAgree
	case remote && !remoteAt.IsZero() && localActivity.After(remoteAt):
		// Watched in Silo after the provider's drop: watching undrops.
		return droppedExportUndrop
	case remote && endedByWatch && !remoteAt.IsZero() && remoteAt.After(localActivity):
		// Dropped on the provider after the watch that ended the local drop.
		return droppedImportDrop
	case remote == base && local:
		return droppedExportDrop
	case remote == base:
		return droppedExportUndrop
	case remote:
		return droppedImportDrop
	default:
		return droppedImportUndrop
	}
}

// droppedItem is one series in a dropped-shows sync.
type droppedItem struct {
	identity LocalFavorite
	// row is the profile's drop row, active or not; nil when there is none.
	row *catalog.DroppedSeries
	// stored is the agreed-drop row, nil when there is none.
	stored *DroppedSyncState
	// base is the agreed value used for the decision: whether a state row
	// exists and predates the profile's drop.
	base     bool
	remote   bool
	remoteAt time.Time
	// observed means this run read the remote value from the provider.
	observed  bool
	remoteKey string
}

func (item *droppedItem) local() bool { return item.row != nil && item.row.Active }

// droppedAfterAgreement reports whether the profile dropped the series after
// the agreed row was last recorded.
func (item *droppedItem) droppedAfterAgreement() bool {
	return item.row != nil && item.stored != nil && item.row.DroppedAt.After(item.stored.UpdatedAt)
}

func (item *droppedItem) providerKey() string {
	if item.remoteKey != "" {
		return item.remoteKey
	}
	if item.stored != nil && item.stored.ProviderItemKey != "" {
		return item.stored.ProviderItemKey
	}
	return item.identity.ProviderItemKey
}

func (item *droppedItem) sendIdentity() LocalFavorite {
	identity := item.identity
	identity.ProviderItemKey = item.providerKey()
	return identity
}

// SyncDroppedResult summarizes one dropped-shows sync.
type SyncDroppedResult struct {
	RemoteFound int
	Imported    int
	LocalFound  int
	Sent        int
	Warnings    []string
}

// droppedSyncProvider returns the provider's dropped-show reader and writer
// when the connection syncs drops and the provider supports both directions.
func droppedSyncProvider(conn Connection, provider Provider) (DroppedImporter, DroppedExporter, bool) {
	importer, canImport := provider.(DroppedImporter)
	exporter, canExport := provider.(DroppedExporter)
	return importer, exporter, conn.SyncDroppedEnabled && canImport && canExport && provider.Capabilities().SyncDropped
}

// syncDropped runs the scheduled dropped-shows sync for one connection.
func (s *Service) syncDropped(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider) (SyncDroppedResult, error) {
	var result SyncDroppedResult
	if s.dropped == nil {
		return result, fmt.Errorf("dropped series store is not configured")
	}
	if _, _, ok := droppedSyncProvider(conn, provider); !ok {
		return result, nil
	}
	locked, err := s.repo.WithRatingSyncLock(ctx, conn.ID, false, func(ctx context.Context) error {
		var err error
		result, err = s.syncDroppedLocked(ctx, conn, cfg, provider)
		return err
	})
	if err == nil && !locked {
		result.Warnings = append(result.Warnings, "this connection is already reconciling; skipped dropped shows in this run")
	}
	if result.Imported > 0 || result.Sent > 0 {
		slog.InfoContext(ctx, "synced dropped shows", "component", "watchsync", "provider", conn.Provider,
			"connection_id", conn.ID, "remote_found", result.RemoteFound, "imported", result.Imported,
			"local_found", result.LocalFound, "sent", result.Sent)
	}
	return result, err
}

func (s *Service) syncDroppedLocked(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider) (SyncDroppedResult, error) {
	var result SyncDroppedResult
	current, err := s.reloadConnection(ctx, conn)
	if err != nil {
		return result, err
	}
	if current.ProviderAccountID != conn.ProviderAccountID {
		result.Warnings = append(result.Warnings, "the connection moved to another provider account before dropped shows synced; they were not applied")
		return result, nil
	}
	conn = current
	importer, _, ok := droppedSyncProvider(conn, provider)
	if !ok {
		return result, nil
	}
	if s.matcher == nil {
		return result, fmt.Errorf("watch provider matcher is not configured")
	}

	items, warnings, err := s.loadDroppedItems(ctx, conn, nil)
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, warnings...)
	for _, item := range items {
		if item.local() {
			result.LocalFound++
		}
	}

	batch, err := importer.FetchDropped(ctx, cfg, conn)
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, batch.Warnings...)
	for _, row := range batch.Rows {
		if !row.Removed {
			result.RemoteFound++
		}
	}
	warnings, err = s.resolveRemoteDropped(ctx, conn, items, batch)
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, warnings...)

	// A binding changed during the provider read must never receive this merge.
	if current, err := s.reloadConnection(ctx, conn); err != nil {
		return result, err
	} else if current.ProviderAccountID != conn.ProviderAccountID {
		result.Warnings = append(result.Warnings, "the connection moved to another provider account during the sync; dropped shows were not applied")
		return result, nil
	}

	applied, err := s.reconcileDropped(ctx, conn, cfg, provider, items, true, func() error {
		if len(batch.UpdatedCursors) == 0 {
			return nil
		}
		return s.repo.UpdateRatingCursors(ctx, conn.ID, conn.ProviderAccountID, nil, mergeSyncCursors(nil, batch.UpdatedCursors))
	})
	result.Imported = applied.imported
	result.Sent = applied.sent
	result.Warnings = append(result.Warnings, applied.warnings...)
	return result, err
}

// loadDroppedItems gathers the profile's drop rows and the connection's agreed
// drops, keyed by series. onlyIDs limits both to the listed series; nil loads
// everything.
func (s *Service) loadDroppedItems(ctx context.Context, conn Connection, onlyIDs []string) (map[string]*droppedItem, []string, error) {
	rows, err := s.dropped.ListDropped(ctx, conn.UserID, conn.ProfileID, onlyIDs)
	if err != nil {
		return nil, nil, err
	}
	states, err := s.repo.ListDroppedSyncStates(ctx, conn.ID, conn.ProviderAccountID, onlyIDs)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(rows)+len(states))
	for _, row := range rows {
		ids = append(ids, row.SeriesID)
	}
	for _, state := range states {
		ids = append(ids, state.SeriesID)
	}
	resolved, err := s.resolveListMediaItems(ctx, ids)
	if err != nil {
		return nil, nil, err
	}

	items := make(map[string]*droppedItem, len(ids))
	var warnings []string
	// skipped are drop rows this sync cannot name to the provider. An agreed
	// row for one of them must not read as a local undrop.
	skipped := make(map[string]*catalog.DroppedSeries)
	for i := range rows {
		row := rows[i]
		identity, ok := resolved[row.SeriesID]
		if !ok || identity.Kind != historyimport.KindSeries || identity.ProviderItemKey == "" {
			skipped[row.SeriesID] = &row
			if ok && identity.Kind == historyimport.KindSeries && row.Active {
				warnings = append(warnings, "dropped series has no provider ids: "+row.SeriesID)
			}
			continue
		}
		items[row.SeriesID] = &droppedItem{identity: identity, row: &row}
	}
	for i := range states {
		state := states[i]
		item, ok := items[state.SeriesID]
		if !ok {
			identity, found := resolved[state.SeriesID]
			if !found || identity.ProviderItemKey == "" {
				identity = LocalFavorite{MediaItemID: state.SeriesID, Kind: historyimport.KindSeries, ProviderItemKey: state.ProviderItemKey}
			}
			if identity.ProviderItemKey == "" {
				continue
			}
			item = &droppedItem{identity: identity, row: skipped[state.SeriesID]}
			items[state.SeriesID] = item
		}
		item.stored = &state
		// A drop made after the agreement (a dismissal after watching ended
		// the agreed drop) is a local change, however the agreement reads.
		item.base = !item.droppedAfterAgreement()
	}
	return items, warnings, nil
}

// resolveRemoteDropped sets each series' remote value from a provider read.
// An explicit tombstone (an incremental read's undrop) undrops the series
// whose agreed row or matched drop in this read carries the same provider
// key. Drops under other keys remain active; a tombstone that names neither
// changes nothing. A series missing from the read counts as undropped only
// when the read is complete, no row shares one of its ids (a row the matcher
// could not place may be this series), and a previous read confirmed the
// provider held the agreed drop. Every other absent series is unknown and
// keeps its agreed value.
//
// A drop Silo sent that no read has confirmed stays agreed rather than being
// sent again: Trakt's read omits drops that apps make, so such a drop is never
// confirmed, and resending it every run would drop the show again each time
// the user undrops it on Trakt.
func (s *Service) resolveRemoteDropped(ctx context.Context, conn Connection, items map[string]*droppedItem, batch DroppedImportBatch) ([]string, error) {
	var warnings []string
	complete := batch.Complete
	seenTokens := make(map[string]bool)
	usable := 0
	byKey := make(map[string][]string)
	for id, item := range items {
		if item.stored != nil && item.stored.ProviderItemKey != "" {
			byKey[item.stored.ProviderItemKey] = append(byKey[item.stored.ProviderItemKey], id)
		}
	}
	// remoteChange is a drop matched to a series, or an undrop tombstone
	// resolved after matching all drops in the read. Changes apply in read
	// order for each provider key, so a later undrop wins over an earlier drop
	// and the other way round.
	type remoteChange struct {
		row RemoteDropped
		id  string
	}
	var changes []remoteChange
	var unresolved []string
	for _, row := range batch.Rows {
		if row.Removed {
			changes = append(changes, remoteChange{row: row})
			continue
		}
		if row.Kind != historyimport.KindSeries {
			continue
		}
		for _, token := range ratingIdentityTokens(row.Kind, row.IMDbID, row.TMDBID, row.TVDBID, row.ProviderItemKey) {
			seenTokens[token] = true
		}
		if row.IMDbID == "" && row.TMDBID == "" && row.TVDBID == "" {
			// Known only by provider ids, so it could be any local series:
			// absence from this read proves nothing.
			if complete {
				complete = false
				warnings = append(warnings, "watch sync provider returned dropped shows without an IMDb, TMDB, or TVDB id; skipped undrops from this read")
			}
			continue
		}
		usable++
		match, _, err := s.matcher.Match(ctx, row.HistoryRecord())
		if err != nil {
			return warnings, err
		}
		if match == nil {
			// Dropped shows outside the library are expected and irrelevant.
			continue
		}
		changes = append(changes, remoteChange{row: row, id: match.MediaItemID})
		if key := strings.TrimSpace(row.ProviderItemKey); key != "" && !slices.Contains(byKey[key], match.MediaItemID) {
			byKey[key] = append(byKey[key], match.MediaItemID)
		}
		if _, ok := items[match.MediaItemID]; !ok {
			unresolved = append(unresolved, match.MediaItemID)
		}
	}

	if len(unresolved) > 0 {
		resolved, err := s.resolveListMediaItems(ctx, unresolved)
		if err != nil {
			return warnings, err
		}
		for _, id := range unresolved {
			identity, ok := resolved[id]
			if !ok || identity.Kind != historyimport.KindSeries || items[id] != nil {
				continue
			}
			items[id] = &droppedItem{identity: identity}
		}
	}
	// Resolve each provider record before combining records for a series. A
	// tombstone for one key must not erase a drop under another key.
	type recordKey struct{ seriesID, providerKey string }
	latest := make(map[recordKey]int)
	for i := range changes {
		change := &changes[i]
		key := strings.TrimSpace(change.row.ProviderItemKey)
		if change.row.Removed {
			switch candidates := byKey[key]; len(candidates) {
			case 0:
				// An echo of an undrop whose agreement Silo already forgot
				// names nothing and is not worth a warning.
				continue
			case 1:
				change.id = candidates[0]
			default:
				warnings = append(warnings, "watch sync provider returned an undrop that matches more than one series")
				continue
			}
		}
		if items[change.id] == nil {
			continue
		}
		record := recordKey{change.id, key}
		if previous, ok := latest[record]; ok && !change.row.Removed && !changes[previous].row.Removed &&
			!change.row.DroppedAt.After(changes[previous].row.DroppedAt) {
			continue
		}
		latest[record] = i
	}
	for i, change := range changes {
		key := strings.TrimSpace(change.row.ProviderItemKey)
		if last, ok := latest[recordKey{change.id, key}]; !ok || last != i {
			continue
		}
		item := items[change.id]
		if change.row.Removed {
			// A different saved key remains unknown when this read has no
			// surviving drop. Let the usual absence safeguards handle it.
			if item.stored == nil || item.stored.ProviderItemKey == key {
				item.observed = true
			}
			continue
		}
		// Of the surviving drop rows for one series, the later drop time wins.
		if item.remote && !change.row.DroppedAt.After(item.remoteAt) {
			continue
		}
		item.remote = true
		item.remoteAt = change.row.DroppedAt
		item.observed = true
		item.remoteKey = key
	}

	// A complete read with no usable rows is more likely a failed read than a
	// mass undrop when it would undrop several confirmed drops Silo still
	// holds, so it is not trusted.
	if complete && usable == 0 {
		held := 0
		for _, item := range items {
			if item.local() && item.stored != nil && item.stored.RemoteSeen {
				held++
			}
		}
		if held > 1 {
			complete = false
			warnings = append(warnings, "watch sync provider returned no dropped shows; skipped undrops from this read")
		}
	}

	for _, item := range items {
		if item.observed {
			continue
		}
		absent := complete
		if absent {
			identity := item.identity
			for _, token := range ratingIdentityTokens(historyimport.KindSeries, identity.IMDbID, identity.TMDBID, identity.TVDBID, identity.ProviderItemKey, item.providerKey()) {
				if seenTokens[token] {
					absent = false
					break
				}
			}
		}
		switch {
		case !absent:
			item.remote = item.base
		case item.stored != nil && item.stored.RemoteSeen:
			item.remote = false
		default:
			item.remote = item.base
		}
	}
	return warnings, nil
}

// markDroppedRemoteUnknown treats every remote value as unchanged since the
// agreed value, for merges that did not read the provider.
func markDroppedRemoteUnknown(items map[string]*droppedItem) {
	for _, item := range items {
		item.remote = item.base
	}
}

type droppedReconcileResult struct {
	imported int
	sent     int
	warnings []string
}

// reconcileDropped applies the merge decision for every series: local writes
// and agreed-drop bookkeeping first, then afterImport (the scheduled run's
// cursor update), then provider writes. importAllowed is false for local
// events, which read no remote values.
func (s *Service) reconcileDropped(
	ctx context.Context,
	conn Connection,
	cfg ServerConfig,
	provider Provider,
	items map[string]*droppedItem,
	importAllowed bool,
	afterImport func() error,
) (droppedReconcileResult, error) {
	var result droppedReconcileResult
	var upserts []DroppedSyncState
	var deletes, inactive []string
	var drops, undrops []*droppedItem
	agreeDropped := func(item *droppedItem, seen bool) {
		if item.stored == nil || item.stored.RemoteSeen != seen || item.stored.ProviderItemKey != item.providerKey() || item.droppedAfterAgreement() {
			upserts = append(upserts, DroppedSyncState{
				ConnectionID:      conn.ID,
				ProviderAccountID: conn.ProviderAccountID,
				SeriesID:          item.identity.MediaItemID,
				ProviderItemKey:   item.providerKey(),
				RemoteSeen:        seen,
			})
		}
	}
	agreeNotDropped := func(item *droppedItem) {
		if item.stored != nil {
			deletes = append(deletes, item.identity.MediaItemID)
		}
		if item.row != nil && !item.row.Active {
			inactive = append(inactive, item.identity.MediaItemID)
		}
	}

	var activity map[string]time.Time
	if importAllowed {
		var candidates []string
		for id, item := range items {
			if item.remote && !item.local() {
				candidates = append(candidates, id)
			}
		}
		var err error
		if activity, err = s.dropped.LatestActivity(ctx, conn.UserID, conn.ProfileID, candidates); err != nil {
			return result, err
		}
	}

	for id, item := range items {
		local := item.local()
		endedByWatch := item.row != nil && !item.row.Active
		switch decideDropped(local, item.remote, item.base, endedByWatch, activity[id], item.remoteAt) {
		case droppedAgree:
			if local {
				seen := item.stored != nil && item.stored.RemoteSeen
				agreeDropped(item, seen || item.observed)
			} else {
				agreeNotDropped(item)
			}
		case droppedExportDrop:
			drops = append(drops, item)
		case droppedExportUndrop:
			undrops = append(undrops, item)
		case droppedImportDrop:
			if !importAllowed {
				continue
			}
			droppedAt := item.remoteAt
			if now := s.now(); droppedAt.IsZero() || droppedAt.After(now) {
				droppedAt = now
			}
			var observed *time.Time
			if item.row != nil {
				observed = &item.row.DroppedAt
			}
			applied, err := s.dropped.ImportDrop(ctx, conn.UserID, conn.ProfileID, id, droppedAt, observed)
			if err != nil {
				return result, err
			}
			if applied {
				result.imported++
				// Agree on the drop as imported, so a restored drop newer
				// than the old agreement does not read as a local change.
				item.row = &catalog.DroppedSeries{SeriesID: id, DroppedAt: droppedAt, Active: true}
				agreeDropped(item, item.observed)
			}
		case droppedImportUndrop:
			if !importAllowed || item.row == nil {
				continue
			}
			applied, err := s.dropped.DeleteIfUnchanged(ctx, conn.UserID, conn.ProfileID, id, item.row.DroppedAt)
			if err != nil {
				return result, err
			}
			if applied {
				result.imported++
				if item.stored != nil {
					deletes = append(deletes, id)
				}
			}
		}
	}
	if err := s.repo.UpsertDroppedSyncStates(ctx, upserts); err != nil {
		return result, err
	}
	if err := s.repo.DeleteDroppedSyncStates(ctx, conn.ID, conn.ProviderAccountID, deletes); err != nil {
		return result, err
	}
	if err := s.dropped.DeleteInactive(ctx, conn.UserID, conn.ProfileID, inactive); err != nil {
		return result, err
	}
	if afterImport != nil {
		if err := afterImport(); err != nil {
			return result, err
		}
	}
	if len(drops) == 0 && len(undrops) == 0 {
		return result, nil
	}
	exporter, ok := provider.(DroppedExporter)
	if !ok {
		return result, fmt.Errorf("provider %q does not implement dropped show export", conn.Provider)
	}
	sent, warnings, err := s.sendDropped(ctx, conn, cfg, exporter, drops, undrops)
	result.sent = sent
	result.warnings = append(result.warnings, warnings...)
	return result, err
}

// sendDropped pushes drops and undrops in batches. A confirmed drop is agreed
// but not seen until a later read confirms the provider kept it. A confirmed
// undrop, or one for a show the provider does not know, removes the profile's
// inactive drop row. When a read had confirmed the drop, the agreed row stays
// until a read confirms the show is no longer dropped: provider reads can be
// served from a cache older than the write (Trakt caches them for up to an
// hour), and a stale read that still lists the show then reads as the undrop
// not yet applied and sends it again, instead of importing the drop back. A
// drop no read ever confirmed cannot come back from a stale read, so its
// agreed row is forgotten right away. A dismissal
// or undo made while a write was in flight dispatches its own event, which
// merges again once this run releases the lock.
func (s *Service) sendDropped(ctx context.Context, conn Connection, cfg ServerConfig, exporter DroppedExporter, drops, undrops []*droppedItem) (int, []string, error) {
	sent := 0
	var warnings []string
	for start := 0; start < len(drops); start += droppedExportBatchSize {
		batch := drops[start:min(start+droppedExportBatchSize, len(drops))]
		payload := make([]LocalFavorite, 0, len(batch))
		for _, item := range batch {
			payload = append(payload, item.sendIdentity())
		}
		result, err := exporter.ExportDropped(ctx, cfg, conn, payload)
		if err != nil {
			return sent, warnings, err
		}
		states := make([]DroppedSyncState, 0, len(batch))
		for _, item := range batch {
			identity := item.sendIdentity()
			if confirmed, _ := exportItemOutcome(result, identity.MediaItemID, identity.ProviderItemKey); !confirmed {
				warnings = append(warnings, exportFailureReason(result, identity, "dropped show")+": "+identity.MediaItemID)
				continue
			}
			sent++
			states = append(states, DroppedSyncState{
				ConnectionID:      conn.ID,
				ProviderAccountID: conn.ProviderAccountID,
				SeriesID:          identity.MediaItemID,
				ProviderItemKey:   identity.ProviderItemKey,
			})
		}
		if err := s.repo.UpsertDroppedSyncStates(ctx, states); err != nil {
			return sent, warnings, err
		}
	}
	for start := 0; start < len(undrops); start += droppedExportBatchSize {
		batch := undrops[start:min(start+droppedExportBatchSize, len(undrops))]
		payload := make([]LocalFavorite, 0, len(batch))
		for _, item := range batch {
			payload = append(payload, item.sendIdentity())
		}
		result, err := exporter.RemoveDropped(ctx, cfg, conn, payload)
		if err != nil {
			return sent, warnings, err
		}
		var done, unconfirmed []string
		for _, item := range batch {
			identity := item.sendIdentity()
			if confirmed, missing := exportItemOutcome(result, identity.MediaItemID, identity.ProviderItemKey); !confirmed && !missing {
				warnings = append(warnings, exportFailureReason(result, identity, "dropped show undrop")+": "+identity.MediaItemID)
				continue
			}
			sent++
			done = append(done, identity.MediaItemID)
			if item.stored == nil || !item.stored.RemoteSeen {
				unconfirmed = append(unconfirmed, identity.MediaItemID)
			}
		}
		if err := s.repo.DeleteDroppedSyncStates(ctx, conn.ID, conn.ProviderAccountID, unconfirmed); err != nil {
			return sent, warnings, err
		}
		if err := s.dropped.DeleteInactive(ctx, conn.UserID, conn.ProfileID, done); err != nil {
			return sent, warnings, err
		}
	}
	return sent, warnings, nil
}

// HandleLocalDroppedEvent sends a profile's drops and undrops to the providers
// that sync its dropped shows. It is fire-and-forget so the originating API
// request never waits on provider I/O.
func (s *Service) HandleLocalDroppedEvent(ctx context.Context, event LocalDroppedEvent) error {
	if event.UserID == 0 || event.ProfileID == "" || len(event.SeriesIDs) == 0 {
		return nil
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.processLocalDroppedEvent(bg, event); err != nil {
			slog.WarnContext(ctx, "failed to dispatch local dropped-show provider event", "component", "watchsync", "user_id", event.UserID, "profile_id", event.ProfileID, "error", err)
		}
	}()
	return nil
}

func (s *Service) processLocalDroppedEvent(ctx context.Context, event LocalDroppedEvent) error {
	if s.dropped == nil {
		return nil
	}
	conns, err := s.repo.ListDroppedEventConnections(ctx, event.UserID, event.ProfileID)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		provider, ok := s.registry.Get(conn.Provider)
		if !ok {
			continue
		}
		if _, _, ok := droppedSyncProvider(conn, provider); !ok {
			continue
		}
		cfg, err := s.serverConfig(ctx, conn.Provider)
		if err != nil {
			s.recordLocalWatchEventError(ctx, conn, err)
			continue
		}
		refreshed, err := s.refreshConnectionIfNeeded(ctx, provider, cfg, conn)
		if err != nil {
			s.recordLocalWatchEventError(ctx, conn, err)
			continue
		}
		conn = refreshed
		// Wait for any reconciliation of this connection to finish, on any
		// node, so the send works from settled agreed drops.
		_, err = s.repo.WithRatingSyncLock(ctx, conn.ID, true, func(ctx context.Context) error {
			return s.sendLocalDropped(ctx, conn, cfg, provider, event.SeriesIDs)
		})
		if err != nil {
			if limited, ok := AsRateLimited(err); ok {
				if deferErr := s.deferRateLimitedConnection(ctx, conn, limited); deferErr != nil {
					s.recordRatingEventError(ctx, conn, errors.Join(err, deferErr))
				}
				continue
			}
			s.recordRatingEventError(ctx, conn, err)
		}
	}
	return nil
}

// sendLocalDropped merges the event's series with the remote side unknown, so
// it only sends local changes. The connection is re-read first, since it may
// have moved to another account or stopped syncing drops while this waited.
func (s *Service) sendLocalDropped(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider, seriesIDs []string) error {
	current, err := s.reloadConnection(ctx, conn)
	if err != nil {
		return err
	}
	if current.ProviderAccountID != conn.ProviderAccountID || !current.SyncDroppedEnabled {
		return nil
	}
	items, _, err := s.loadDroppedItems(ctx, current, seriesIDs)
	if err != nil {
		return err
	}
	markDroppedRemoteUnknown(items)
	_, err = s.reconcileDropped(ctx, current, cfg, provider, items, false, nil)
	return err
}

// withoutDroppedCursors drops the dropped-show read cursors, used together
// with ClearDroppedSyncStates when a connection changes provider account.
func withoutDroppedCursors(cursors map[string]string) map[string]string {
	kept := make(map[string]string, len(cursors))
	for key, value := range cursors {
		if !strings.Contains(key, droppedCursorSegment) {
			kept[key] = value
		}
	}
	return kept
}
