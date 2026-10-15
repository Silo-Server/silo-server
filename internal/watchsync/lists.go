package watchsync

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// This file generalizes the personal-list sync pipeline (import, export,
// removal, and real-time local-change propagation) so it runs once for both the
// favorites list and the watchlist. The only things that differ between the two
// lists are the provider endpoints, the local store operations, the connection
// toggles, and which counters/timestamps a run records — all captured by a
// listBinding.

// listRow is the kind-neutral shape of a local list membership row (a favorite
// or a watchlist entry): just the media item id and its added-at timestamp.
type listRow struct {
	MediaItemID string
	AddedAt     string
}

// listBinding captures everything that differs between the favorites and
// watchlist pipelines so the shared machinery can operate on either list.
type listBinding struct {
	kind ListKind

	importEnabled   func(Connection) bool
	exportEnabled   func(Connection) bool
	removalsEnabled func(Connection) bool

	capImport func(Capabilities) bool
	capExport func(Capabilities) bool
	capRemove func(Capabilities) bool

	// Provider interface resolution. ok=false means the provider does not
	// implement the capability for this list kind.
	fetchBatch  func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider) (batch FavoriteImportBatch, ok bool, err error)
	exportItems func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider, items []LocalFavorite) (result ExportResult, ok bool, err error)
	removeItems func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider, items []LocalFavorite) (result ExportResult, ok bool, err error)

	// Local list operations against the per-user store.
	// localAdd reports whether the item was new to the local list.
	localAdd    func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string, at time.Time) (bool, error)
	localRemove func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string) error
	localList   func(ctx context.Context, store userstore.UserStore, profileID string, limit, offset int) ([]listRow, error)

	setLastSync     func(conn *Connection, at time.Time)
	setImportCounts func(run *SyncRun, found, imported int)
	setExportCounts func(run *SyncRun, found, sent int)
	setRemovalCount func(run *SyncRun, removed int)

	// Optional order mirroring (watchlist only): when all three are set and
	// gated true, importList mirrors the provider's returned item order locally.
	orderEnabled func(Connection) bool
	capOrder     func(Capabilities) bool
	applyOrder   func(ctx context.Context, store userstore.UserStore, profileID string, orderedIDs []string) error
}

func (s *Service) favoritesBinding() listBinding {
	return listBinding{
		kind:            ListKindFavorites,
		importEnabled:   func(c Connection) bool { return c.ImportFavoritesEnabled },
		exportEnabled:   func(c Connection) bool { return c.ExportFavoritesEnabled },
		removalsEnabled: func(c Connection) bool { return c.SyncFavoriteRemovalsEnabled },
		capImport:       func(c Capabilities) bool { return c.ImportFavorites },
		capExport:       func(c Capabilities) bool { return c.ExportFavorites },
		capRemove:       func(c Capabilities) bool { return c.RemoveFavorites },
		fetchBatch: func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider) (FavoriteImportBatch, bool, error) {
			importer, ok := provider.(FavoriteImporter)
			if !ok {
				return FavoriteImportBatch{}, false, nil
			}
			if batchImporter, ok := provider.(FavoriteBatchImporter); ok {
				batch, err := batchImporter.FetchFavoritesBatch(ctx, cfg, conn)
				return batch, true, err
			}
			rows, err := importer.FetchFavorites(ctx, cfg, conn)
			if err != nil {
				return FavoriteImportBatch{}, true, err
			}
			return FavoriteImportBatch{Rows: rows}, true, nil
		},
		exportItems: func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider, items []LocalFavorite) (ExportResult, bool, error) {
			exporter, ok := provider.(FavoriteExporter)
			if !ok {
				return ExportResult{}, false, nil
			}
			res, err := exporter.ExportFavorites(ctx, cfg, conn, items)
			return res, true, err
		},
		removeItems: func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider, items []LocalFavorite) (ExportResult, bool, error) {
			remover, ok := provider.(FavoriteRemover)
			if !ok {
				return ExportResult{}, false, nil
			}
			res, err := remover.RemoveFavorites(ctx, cfg, conn, items)
			return res, true, err
		},
		localAdd: func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string, at time.Time) (bool, error) {
			return store.AddFavoriteAt(ctx, profileID, mediaItemID, at)
		},
		localRemove: func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string) error {
			_, err := store.RemoveFavorite(ctx, profileID, mediaItemID)
			return err
		},
		localList: func(ctx context.Context, store userstore.UserStore, profileID string, limit, offset int) ([]listRow, error) {
			favorites, err := store.ListFavorites(ctx, profileID, limit, offset)
			if err != nil {
				return nil, err
			}
			rows := make([]listRow, 0, len(favorites))
			for _, fav := range favorites {
				rows = append(rows, listRow{MediaItemID: fav.MediaItemID, AddedAt: fav.AddedAt})
			}
			return rows, nil
		},
		setLastSync: func(conn *Connection, at time.Time) { conn.LastFavoritesSyncAt = &at },
		setImportCounts: func(run *SyncRun, found, imported int) {
			run.InboundFavoritesFound, run.InboundFavoritesImported = found, imported
		},
		setExportCounts: func(run *SyncRun, found, sent int) {
			run.OutboundFavoritesFound, run.OutboundFavoritesSent = found, sent
		},
		setRemovalCount: func(run *SyncRun, removed int) { run.FavoriteRemovalsSent = removed },
	}
}

func (s *Service) watchlistBinding() listBinding {
	return listBinding{
		kind:            ListKindWatchlist,
		importEnabled:   func(c Connection) bool { return c.ImportWatchlistEnabled },
		exportEnabled:   func(c Connection) bool { return c.ExportWatchlistEnabled },
		removalsEnabled: func(c Connection) bool { return c.SyncWatchlistRemovalsEnabled },
		capImport:       func(c Capabilities) bool { return c.ImportWatchlist },
		capExport:       func(c Capabilities) bool { return c.ExportWatchlist },
		capRemove:       func(c Capabilities) bool { return c.RemoveWatchlist },
		fetchBatch: func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider) (FavoriteImportBatch, bool, error) {
			importer, ok := provider.(WatchlistImporter)
			if !ok {
				return FavoriteImportBatch{}, false, nil
			}
			if batchImporter, ok := provider.(WatchlistBatchImporter); ok {
				batch, err := batchImporter.FetchWatchlistBatch(ctx, cfg, conn)
				return batch, true, err
			}
			rows, err := importer.FetchWatchlist(ctx, cfg, conn)
			if err != nil {
				return FavoriteImportBatch{}, true, err
			}
			return FavoriteImportBatch{Rows: rows}, true, nil
		},
		exportItems: func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider, items []LocalFavorite) (ExportResult, bool, error) {
			exporter, ok := provider.(WatchlistExporter)
			if !ok {
				return ExportResult{}, false, nil
			}
			res, err := exporter.ExportWatchlist(ctx, cfg, conn, items)
			return res, true, err
		},
		removeItems: func(ctx context.Context, cfg ServerConfig, conn Connection, provider Provider, items []LocalFavorite) (ExportResult, bool, error) {
			remover, ok := provider.(WatchlistRemover)
			if !ok {
				return ExportResult{}, false, nil
			}
			res, err := remover.RemoveWatchlist(ctx, cfg, conn, items)
			return res, true, err
		},
		localAdd: func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string, at time.Time) (bool, error) {
			return store.AddToWatchlistAt(ctx, profileID, mediaItemID, at)
		},
		localRemove: func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string) error {
			_, err := store.RemoveFromWatchlist(ctx, profileID, mediaItemID)
			return err
		},
		localList: func(ctx context.Context, store userstore.UserStore, profileID string, limit, offset int) ([]listRow, error) {
			entries, err := store.ListWatchlist(ctx, profileID, limit, offset)
			if err != nil {
				return nil, err
			}
			rows := make([]listRow, 0, len(entries))
			for _, entry := range entries {
				rows = append(rows, listRow{MediaItemID: entry.MediaItemID, AddedAt: entry.AddedAt})
			}
			return rows, nil
		},
		setLastSync: func(conn *Connection, at time.Time) { conn.LastWatchlistSyncAt = &at },
		setImportCounts: func(run *SyncRun, found, imported int) {
			run.InboundWatchlistFound, run.InboundWatchlistImported = found, imported
		},
		setExportCounts: func(run *SyncRun, found, sent int) {
			run.OutboundWatchlistFound, run.OutboundWatchlistSent = found, sent
		},
		setRemovalCount: func(run *SyncRun, removed int) { run.WatchlistRemovalsSent = removed },
		orderEnabled:    func(c Connection) bool { return c.SyncWatchlistOrderEnabled },
		capOrder:        func(c Capabilities) bool { return c.ProvidesWatchlistOrder },
		applyOrder: func(ctx context.Context, store userstore.UserStore, profileID string, orderedIDs []string) error {
			return store.ReplaceWatchlistOrder(ctx, profileID, orderedIDs)
		},
	}
}

func (s *Service) listBindings() []listBinding {
	return []listBinding{s.favoritesBinding(), s.watchlistBinding()}
}

func (s *Service) bindingForKind(kind ListKind) (listBinding, bool) {
	switch kind {
	case ListKindFavorites:
		return s.favoritesBinding(), true
	case ListKindWatchlist:
		return s.watchlistBinding(), true
	default:
		return listBinding{}, false
	}
}

type ImportListResult struct {
	Found     int
	Imported  int
	Unmatched int
	Removed   int
	Warnings  []string
	// added counts the imported items that were new to the local list;
	// Imported also counts the ones it already held.
	added int
}

// changedLocalList reports whether the import added to or removed from the
// local list.
func (r ImportListResult) changedLocalList() bool {
	return r.added > 0 || r.Removed > 0
}

// importList pulls the provider's list (favorites or watchlist) and mirrors it
// into the local list, recording shadow state and reconciling remote removals.
func (s *Service) importList(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider, b listBinding) (ImportListResult, error) {
	if s.matcher == nil {
		return ImportListResult{}, fmt.Errorf("watch provider matcher is not configured")
	}
	if s.storeProvider == nil {
		return ImportListResult{}, fmt.Errorf("user store provider is not configured")
	}
	store, err := s.storeProvider.ForUser(ctx, conn.UserID)
	if err != nil {
		return ImportListResult{}, fmt.Errorf("open user store: %w", err)
	}
	batch, ok, err := b.fetchBatch(ctx, cfg, conn, provider)
	if err != nil {
		return ImportListResult{}, err
	}
	if !ok {
		return ImportListResult{}, fmt.Errorf("provider %q does not implement %s import", conn.Provider, b.kind)
	}
	rows := batch.Rows
	result := ImportListResult{Found: len(rows), Warnings: append([]string{}, batch.Warnings...)}
	existingByRemoteKey, err := s.listItemStatesByRemoteKey(ctx, conn.ID, b.kind, rows)
	if err != nil {
		return result, err
	}
	removalsPending, err := s.listItemsPendingRemoval(ctx, conn, b)
	if err != nil {
		return result, err
	}
	seenRemote := make(remoteListPresence, len(rows))
	states := make([]ListItemState, 0, len(rows))
	// orderedIDs records matched local ids in the order the provider returned
	// them, so an order-providing list (e.g. MDBList watchlist) can mirror its
	// sort locally.
	orderedIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Removed {
			unlisted, err := s.applyRemoteListTombstone(ctx, conn, store, b, row, existingByRemoteKey, &result)
			if err != nil {
				return result, err
			}
			// The provider no longer lists them, so a later row in this batch
			// that lists one again is a new add, not the listing a pending
			// local removal waits on.
			for _, id := range unlisted {
				delete(removalsPending, id)
			}
			continue
		}
		// The provider still lists the title even when the row matches no
		// single library item, for example when two items carry its id. Its
		// key format can differ from the one stored at export (Trakt keys
		// shows by TVDB id first), so every id the row carries counts.
		key := row.ProviderItemKey
		if key == "" {
			key = providerItemKeyForRemoteFavorite(row)
		}
		seenRemote.add(row.Kind,
			key, prefixedID("imdb", row.IMDbID), prefixedID("tmdb", row.TMDBID), prefixedID("tvdb", row.TVDBID))
		match, reason, err := s.matcher.Match(ctx, row.HistoryRecord())
		if err != nil {
			return result, err
		}
		if match == nil {
			result.Unmatched++
			if reason != "" {
				result.Warnings = append(result.Warnings, reason)
			}
			continue
		}
		// The provider listing the item again after the local removal is a
		// newer add; an older or undated listing is the one the removal will
		// take off.
		if removedAt, pending := removalsPending[match.MediaItemID]; pending &&
			(row.FavoritedAtGuessed || !row.FavoritedAt.After(removedAt)) {
			continue
		}
		added, err := b.localAdd(ctx, store, conn.ProfileID, match.MediaItemID, row.FavoritedAt)
		if err != nil {
			return result, err
		}
		if added {
			result.added++
		}
		orderedIDs = append(orderedIDs, match.MediaItemID)
		result.Imported++
		listedAt := row.FavoritedAt
		states = append(states, ListItemState{
			ConnectionID:     conn.ID,
			ListKind:         b.kind,
			MediaItemID:      match.MediaItemID,
			ProviderItemKey:  key,
			Kind:             row.Kind,
			Title:            row.Title,
			Year:             row.Year,
			RemotePresent:    true,
			LocalPresent:     true,
			LastSeenRemoteAt: &listedAt,
			LastSeenLocalAt:  &listedAt,
		})
	}
	if err := s.repo.UpsertListItemStates(ctx, states); err != nil {
		return result, err
	}
	if !batch.Incremental {
		if err := s.reconcileMissingRemoteListItems(ctx, conn, store, b, seenRemote, &result); err != nil {
			return result, err
		}
	}
	// An incremental response is only a delta; replacing the whole local order
	// with that subset would discard every item absent from the delta.
	if !batch.Incremental && b.applyOrder != nil && b.orderEnabled != nil && b.capOrder != nil &&
		b.orderEnabled(conn) && b.capOrder(provider.Capabilities()) {
		if err := b.applyOrder(ctx, store, conn.ProfileID, orderedIDs); err != nil {
			return result, err
		}
	}
	now := s.now()
	b.setLastSync(&conn, now)
	conn.LastError = ""
	conn.SyncCursors = mergeSyncCursors(conn.SyncCursors, batch.UpdatedCursors)
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return result, err
	}
	return result, nil
}

// listItemsPendingRemoval maps each item whose local removal has not reached
// the provider yet to when it left the local list, when removals sync. The
// provider still lists these items, so an import must not add them back from
// that listing: a failed removal retries later in the run, and a removal held
// because another library item shares the key stays until that item leaves
// too.
func (s *Service) listItemsPendingRemoval(ctx context.Context, conn Connection, b listBinding) (map[string]time.Time, error) {
	if !b.removalsEnabled(conn) {
		return nil, nil
	}
	states, err := s.repo.ListListItemStates(ctx, conn.ID, b.kind)
	if err != nil {
		return nil, err
	}
	pending := make(map[string]time.Time)
	for _, state := range states {
		if !state.LocalPresent && state.RemotePresent {
			var removedAt time.Time
			if state.LastRemovedLocalAt != nil {
				removedAt = *state.LastRemovedLocalAt
			}
			pending[state.MediaItemID] = removedAt
		}
	}
	return pending, nil
}

func (s *Service) listItemStatesByRemoteKey(
	ctx context.Context,
	connectionID string,
	kind ListKind,
	rows []RemoteFavorite,
) (map[string][]ListItemState, error) {
	hasTombstone := false
	for _, row := range rows {
		if row.Removed {
			hasTombstone = true
			break
		}
	}
	if !hasTombstone {
		return nil, nil
	}
	states, err := s.repo.ListListItemStates(ctx, connectionID, kind)
	if err != nil {
		return nil, err
	}
	// Several library items can share a key, such as two items for one title
	// with the same IMDb id.
	byRemoteKey := make(map[string][]ListItemState, len(states))
	for _, state := range states {
		if key := strings.TrimSpace(state.ProviderItemKey); key != "" {
			byRemoteKey[key] = append(byRemoteKey[key], state)
		}
	}
	return byRemoteKey, nil
}

// applyRemoteListTombstone applies a provider removal to the rows with its key
// and returns the media items it marked as no longer listed by the provider.
func (s *Service) applyRemoteListTombstone(
	ctx context.Context,
	conn Connection,
	store userstore.UserStore,
	b listBinding,
	row RemoteFavorite,
	existingByRemoteKey map[string][]ListItemState,
	result *ImportListResult,
) ([]string, error) {
	key := strings.TrimSpace(row.ProviderItemKey)
	// Point into the map's rows so a later tombstone for the key in this
	// batch sees them already removed.
	existing := existingByRemoteKey[key]
	var states []*ListItemState
	for i := range existing {
		if row.Kind == "" || existing[i].Kind == "" || existing[i].Kind == row.Kind {
			states = append(states, &existing[i])
		}
	}
	if key == "" || len(states) == 0 {
		result.Warnings = append(result.Warnings, "watch sync provider returned an unknown list removal")
		return nil, nil
	}
	// Copies of one title share a kind and all leave. A movie and a series
	// with the same key are different titles, and a tombstone without a kind
	// cannot say which one left.
	if !sameListItemKind(states) {
		result.Warnings = append(result.Warnings, "watch sync provider returned a list removal for "+key+" that matches more than one title")
		return nil, nil
	}
	now := s.now()
	unlisted := make([]string, 0, len(states))
	for _, state := range states {
		if b.removalsEnabled(conn) && state.LocalPresent {
			if err := b.localRemove(ctx, store, conn.ProfileID, state.MediaItemID); err != nil {
				return nil, err
			}
			// Counted once the local list changed, so a failed bookkeeping
			// write below still reports the change.
			result.Removed++
			if err := s.repo.MarkListItemLocalRemoved(ctx, conn.ID, b.kind, state.MediaItemID, now); err != nil {
				return nil, err
			}
			state.LocalPresent = false
		}
		if err := s.repo.MarkListItemRemoteRemoved(ctx, conn.ID, b.kind, state.MediaItemID, now); err != nil {
			return nil, err
		}
		state.RemotePresent = false
		unlisted = append(unlisted, state.MediaItemID)
	}
	return unlisted, nil
}

// sameListItemKind reports whether the rows name one kind of title. A row
// without a kind matches any.
func sameListItemKind(states []*ListItemState) bool {
	kind := ""
	for _, state := range states {
		switch {
		case state.Kind == "":
		case kind == "":
			kind = state.Kind
		case state.Kind != kind:
			return false
		}
	}
	return true
}

// remoteListPresence records the keys a complete provider read listed, with
// the kinds of title listed under each. TMDB and TVDB number movies and series
// separately, so a movie row does not keep a series with the same key.
type remoteListPresence map[string]map[string]bool

func (p remoteListPresence) add(kind string, keys ...string) {
	for _, key := range keys {
		if key == "" {
			continue
		}
		if p[key] == nil {
			p[key] = make(map[string]bool)
		}
		p[key][kind] = true
	}
}

// listed reports whether the read listed a title of this kind under key. A
// row or stored state without a kind matches any kind.
func (p remoteListPresence) listed(kind, key string) bool {
	kinds := p[key]
	return kinds[kind] || kinds[""] || (kind == "" && len(kinds) > 0)
}

func (s *Service) reconcileMissingRemoteListItems(ctx context.Context, conn Connection, store userstore.UserStore, b listBinding, seenRemote remoteListPresence, result *ImportListResult) error {
	states, err := s.repo.ListListItemStates(ctx, conn.ID, b.kind)
	if err != nil {
		return err
	}
	now := s.now()
	for _, state := range states {
		if !state.RemotePresent || state.ProviderItemKey == "" || seenRemote.listed(state.Kind, state.ProviderItemKey) {
			continue
		}
		if b.removalsEnabled(conn) && state.LocalPresent {
			if err := b.localRemove(ctx, store, conn.ProfileID, state.MediaItemID); err != nil {
				return err
			}
			result.Removed++
			if err := s.repo.MarkListItemLocalRemoved(ctx, conn.ID, b.kind, state.MediaItemID, now); err != nil {
				return err
			}
		}
		if err := s.repo.MarkListItemRemoteRemoved(ctx, conn.ID, b.kind, state.MediaItemID, now); err != nil {
			return err
		}
	}
	return nil
}

type ExportListResult struct {
	LocalFound int
	Queued     int
	Sent       int
	Failed     int
	Warnings   []string
}

// exportList pushes the local list (favorites or watchlist) to the provider,
// sending only items not yet known-present remotely.
func (s *Service) exportList(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider, b listBinding) (ExportListResult, error) {
	if s.storeProvider == nil {
		return ExportListResult{}, fmt.Errorf("user store provider is not configured")
	}
	store, err := s.storeProvider.ForUser(ctx, conn.UserID)
	if err != nil {
		return ExportListResult{}, fmt.Errorf("open user store: %w", err)
	}
	rows, err := b.localList(ctx, store, conn.ProfileID, 10000, 0)
	if err != nil {
		return ExportListResult{}, err
	}
	result := ExportListResult{LocalFound: len(rows)}
	items, states, warnings, err := s.localItemsFromRows(ctx, conn, b, rows)
	if err != nil {
		return result, err
	}
	result.Warnings = append(result.Warnings, warnings...)
	if err := s.repo.UpsertListItemStates(ctx, states); err != nil {
		return result, err
	}
	for {
		pending, err := s.repo.ListPendingListItemExports(ctx, conn.ID, b.kind, 100)
		if err != nil {
			return result, err
		}
		if len(pending) == 0 {
			break
		}
		byMedia := make(map[string]LocalFavorite, len(items))
		for _, item := range items {
			byMedia[item.MediaItemID] = item
		}
		toSend := make([]LocalFavorite, 0, len(pending))
		for _, state := range pending {
			item, ok := byMedia[state.MediaItemID]
			if !ok {
				if err := s.repo.MarkListItemLocalRemoved(ctx, conn.ID, b.kind, state.MediaItemID, s.now()); err != nil {
					return result, err
				}
				continue
			}
			toSend = append(toSend, item)
		}
		if len(toSend) == 0 {
			continue
		}
		result.Queued += len(toSend)
		exportResult, ok, err := b.exportItems(ctx, cfg, conn, provider, toSend)
		if !ok {
			return result, fmt.Errorf("provider %q does not implement %s export", conn.Provider, b.kind)
		}
		if err != nil {
			// Rate-limited items are not failures: leave them pending so the
			// next run (after the deferral) retries without churning state.
			if _, limited := AsRateLimited(err); limited {
				return result, err
			}
			for _, item := range toSend {
				_ = s.repo.MarkListItemError(ctx, conn.ID, b.kind, item.MediaItemID, err.Error())
			}
			result.Failed += len(toSend)
			return result, err
		}
		now := s.now()
		for _, item := range toSend {
			if sent, _ := exportItemOutcome(exportResult, item.MediaItemID, item.ProviderItemKey); sent {
				if err := s.repo.MarkListItemExported(ctx, conn.ID, b.kind, item.MediaItemID, now); err != nil {
					return result, err
				}
				result.Sent++
				continue
			}
			// Anything not confirmed sent (not_found, failed, or omitted from the
			// provider response) is marked with an error so it leaves the pending
			// set this run; the next run's UpsertListItemStates clears the error
			// and re-attempts, so transient failures still retry.
			msg := exportFailureReason(exportResult, item, b.kind)
			if err := s.repo.MarkListItemError(ctx, conn.ID, b.kind, item.MediaItemID, msg); err != nil {
				return result, err
			}
			result.Warnings = append(result.Warnings, msg+": "+item.MediaItemID)
		}
	}
	now := s.now()
	b.setLastSync(&conn, now)
	conn.LastError = ""
	if _, err := s.repo.UpsertConnection(ctx, conn); err != nil {
		return result, err
	}
	return result, nil
}

// removePendingListItems pushes pending local removals to the provider (items
// dropped locally but still present remotely).
func (s *Service) removePendingListItems(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider, b listBinding) (int, error) {
	removed := 0
	// Removal failures intentionally do not set last_error (which would strand
	// the row from ListPendingListItemRemovals, since there is no per-run upsert
	// to clear it). Instead we track items attempted this run in memory so the
	// loop terminates, and leave failed rows pending for the next scheduled run.
	attempted := make(map[string]bool)
	for {
		pending, err := s.repo.ListPendingListItemRemovals(ctx, conn.ID, b.kind, 100)
		if err != nil {
			return removed, err
		}
		items := make([]LocalFavorite, 0, len(pending))
		for _, state := range pending {
			if attempted[state.MediaItemID] {
				continue
			}
			items = append(items, LocalFavorite{
				MediaItemID:     state.MediaItemID,
				ProviderItemKey: state.ProviderItemKey,
				Kind:            state.Kind,
				Title:           state.Title,
				Year:            state.Year,
			})
		}
		if len(items) == 0 {
			return removed, nil
		}
		result, ok, err := b.removeItems(ctx, cfg, conn, provider, items)
		if !ok {
			return removed, fmt.Errorf("provider %q does not implement %s removal", conn.Provider, b.kind)
		}
		if err != nil {
			// Leave the rows pending so the next scheduled run retries them.
			return removed, err
		}
		now := s.now()
		for _, item := range items {
			attempted[item.MediaItemID] = true
			// Sent (removed) and NotFound (already absent remotely) both reconcile
			// the row; true failures stay pending for the next run.
			if sent, notFound := exportItemOutcome(result, item.MediaItemID, item.ProviderItemKey); sent || notFound {
				if err := s.repo.MarkListItemRemoteRemoved(ctx, conn.ID, b.kind, item.MediaItemID, now); err != nil {
					return removed, err
				}
				removed++
			}
		}
	}
}

func (s *Service) localItemsFromRows(ctx context.Context, conn Connection, b listBinding, rows []listRow) ([]LocalFavorite, []ListItemState, []string, error) {
	ids := make([]string, 0, len(rows))
	addedAtByID := make(map[string]time.Time, len(rows))
	for _, row := range rows {
		ids = append(ids, row.MediaItemID)
		if addedAt, err := time.Parse(time.RFC3339, row.AddedAt); err == nil {
			addedAtByID[row.MediaItemID] = addedAt
		}
	}
	resolved, err := s.resolveListMediaItems(ctx, ids)
	if err != nil {
		return nil, nil, nil, err
	}
	items := make([]LocalFavorite, 0, len(rows))
	states := make([]ListItemState, 0, len(rows))
	var warnings []string
	for _, row := range rows {
		item, ok := resolved[row.MediaItemID]
		if !ok {
			warnings = append(warnings, string(b.kind)+" media item not found: "+row.MediaItemID)
			continue
		}
		item.FavoritedAt = addedAtByID[row.MediaItemID]
		if item.FavoritedAt.IsZero() {
			item.FavoritedAt = s.now()
		}
		if item.Kind != historyimport.KindMovie && item.Kind != historyimport.KindSeries {
			warnings = append(warnings, string(b.kind)+" kind is not supported by provider: "+row.MediaItemID)
			continue
		}
		if item.ProviderItemKey == "" {
			warnings = append(warnings, string(b.kind)+" item has no provider ids: "+row.MediaItemID)
			continue
		}
		items = append(items, item)
		listedAt := item.FavoritedAt
		states = append(states, ListItemState{
			ConnectionID:    conn.ID,
			ListKind:        b.kind,
			MediaItemID:     item.MediaItemID,
			ProviderItemKey: item.ProviderItemKey,
			Kind:            item.Kind,
			Title:           item.Title,
			Year:            item.Year,
			RemotePresent:   false,
			LocalPresent:    true,
			LastSeenLocalAt: &listedAt,
		})
	}
	return items, states, warnings, nil
}

type listMediaResolver interface {
	GetListMediaItems(ctx context.Context, mediaItemIDs []string) (map[string]LocalFavorite, error)
}

// resolveListMediaItems loads the identity (kind, title, external ids) of
// movies and series by media item id. Unknown ids are absent from the result.
func (s *Service) resolveListMediaItems(ctx context.Context, ids []string) (map[string]LocalFavorite, error) {
	resolver, ok := s.repo.(listMediaResolver)
	if !ok {
		return nil, fmt.Errorf("list media resolver is not configured")
	}
	return resolver.GetListMediaItems(ctx, ids)
}

// HandleLocalListEvent mirrors a real-time local list change (add/remove of a
// favorite or watchlist item) to the providers bound to that list kind. It is
// fire-and-forget so the originating API request is never blocked on provider
// I/O.
func (s *Service) HandleLocalListEvent(ctx context.Context, event LocalListEvent) error {
	if event.UserID == 0 || event.ProfileID == "" || len(event.Items) == 0 {
		return nil
	}
	if event.List == "" {
		event.List = ListKindFavorites
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.processLocalListEvent(bg, event); err != nil {
			slog.WarnContext(ctx, "failed to dispatch local list provider event", "component", "watchsync", "list", event.List, "change", event.Change, "user_id", event.UserID, "profile_id", event.ProfileID, "error", err)
		}
	}()
	return nil
}

func (s *Service) processLocalListEvent(ctx context.Context, event LocalListEvent) error {
	b, ok := s.bindingForKind(event.List)
	if !ok {
		return fmt.Errorf("unknown list kind %q", event.List)
	}
	conns, err := s.repo.ListListEventConnections(ctx, event.UserID, event.ProfileID, event.List)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		provider, ok := s.registry.Get(conn.Provider)
		if !ok {
			continue
		}
		cfg, err := s.serverConfig(ctx, conn.Provider)
		if err != nil {
			s.recordLocalWatchEventError(ctx, conn, err)
			continue
		}
		conn, err = s.refreshConnectionIfNeeded(ctx, provider, cfg, conn)
		if err != nil {
			s.recordLocalWatchEventError(ctx, conn, err)
			continue
		}
		switch event.Change {
		case ListChangeAdded:
			if !b.capExport(provider.Capabilities()) {
				continue
			}
			if err := s.exportLocalListItems(ctx, conn, cfg, provider, b, event.Items); err != nil {
				s.recordLocalWatchEventError(ctx, conn, err)
			}
		case ListChangeRemoved:
			now := s.now()
			for _, item := range event.Items {
				if err := s.repo.MarkListItemLocalRemoved(ctx, conn.ID, b.kind, item.MediaItemID, now); err != nil {
					return err
				}
			}
			if !b.removalsEnabled(conn) || !b.capRemove(provider.Capabilities()) {
				continue
			}
			toRemove, err := s.listItemsReleasedLocally(ctx, conn, b, event.Items)
			if err != nil {
				return err
			}
			if len(toRemove) == 0 {
				continue
			}
			result, ok, err := b.removeItems(ctx, cfg, conn, provider, toRemove)
			if !ok {
				continue
			}
			if err != nil {
				// Leave remote_present set so the scheduled reconcile retries.
				s.recordLocalWatchEventError(ctx, conn, err)
				continue
			}
			for _, item := range toRemove {
				if sent, notFound := exportItemOutcome(result, item.MediaItemID, item.ProviderItemKey); sent || notFound {
					if err := s.repo.MarkListItemRemoteRemoved(ctx, conn.ID, b.kind, item.MediaItemID, now); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// listItemsReleasedLocally drops the removed items whose provider key another
// item on the local list still holds, since removing that key would take the
// other item off the provider's list too. Their rows stay pending removal.
func (s *Service) listItemsReleasedLocally(ctx context.Context, conn Connection, b listBinding, items []LocalFavorite) ([]LocalFavorite, error) {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.MediaItemID)
	}
	held, err := s.repo.ListListItemsWithSharedKeyHeld(ctx, conn.ID, b.kind, ids)
	if err != nil {
		return nil, err
	}
	released := make([]LocalFavorite, 0, len(items))
	for _, item := range items {
		if !held[item.MediaItemID] {
			released = append(released, item)
		}
	}
	return released, nil
}

func (s *Service) exportLocalListItems(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider, b listBinding, items []LocalFavorite) error {
	states := make([]ListItemState, 0, len(items))
	// toSend carries the normalized items (with a computed ProviderItemKey) so
	// the provider receives the same keys we record in shadow state.
	toSend := make([]LocalFavorite, 0, len(items))
	for _, item := range items {
		if item.ProviderItemKey == "" {
			item.ProviderItemKey = providerItemKeyForLocalFavorite(item)
		}
		if item.ProviderItemKey == "" {
			continue
		}
		listedAt := item.FavoritedAt
		if listedAt.IsZero() {
			listedAt = s.now()
		}
		states = append(states, ListItemState{
			ConnectionID:    conn.ID,
			ListKind:        b.kind,
			MediaItemID:     item.MediaItemID,
			ProviderItemKey: item.ProviderItemKey,
			Kind:            item.Kind,
			Title:           item.Title,
			Year:            item.Year,
			RemotePresent:   false,
			LocalPresent:    true,
			LastSeenLocalAt: &listedAt,
		})
		toSend = append(toSend, item)
	}
	if err := s.repo.UpsertListItemStates(ctx, states); err != nil {
		return err
	}
	result, ok, err := b.exportItems(ctx, cfg, conn, provider, toSend)
	if !ok {
		return nil
	}
	if err != nil {
		return err
	}
	now := s.now()
	for _, item := range toSend {
		if sent, _ := exportItemOutcome(result, item.MediaItemID, item.ProviderItemKey); sent {
			if err := s.repo.MarkListItemExported(ctx, conn.ID, b.kind, item.MediaItemID, now); err != nil {
				return err
			}
		}
	}
	b.setLastSync(&conn, now)
	conn.LastError = ""
	_, err = s.repo.UpsertConnection(ctx, conn)
	return err
}
