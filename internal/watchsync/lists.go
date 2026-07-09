package watchsync

import (
	"context"
	"fmt"
	"log/slog"
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
	localAdd    func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string, at time.Time) error
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
		localAdd: func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string, at time.Time) error {
			return store.AddFavoriteAt(ctx, profileID, mediaItemID, at)
		},
		localRemove: func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string) error {
			return store.RemoveFavorite(ctx, profileID, mediaItemID)
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
		localAdd: func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string, at time.Time) error {
			_, err := store.AddToWatchlistAt(ctx, profileID, mediaItemID, at)
			return err
		},
		localRemove: func(ctx context.Context, store userstore.UserStore, profileID, mediaItemID string) error {
			return store.RemoveFromWatchlist(ctx, profileID, mediaItemID)
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
	seenRemoteKeys := make(map[string]bool, len(rows))
	states := make([]ListItemState, 0, len(rows))
	// orderedIDs records matched local ids in the order the provider returned
	// them, so an order-providing list (e.g. MDBList watchlist) can mirror its
	// sort locally.
	orderedIDs := make([]string, 0, len(rows))
	for _, row := range rows {
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
		if err := b.localAdd(ctx, store, conn.ProfileID, match.MediaItemID, row.FavoritedAt); err != nil {
			return result, err
		}
		orderedIDs = append(orderedIDs, match.MediaItemID)
		result.Imported++
		key := row.ProviderItemKey
		if key == "" {
			key = providerItemKeyForRemoteFavorite(row)
		}
		if key != "" {
			seenRemoteKeys[key] = true
		}
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
	if err := s.reconcileMissingRemoteListItems(ctx, conn, store, b, seenRemoteKeys, &result); err != nil {
		return result, err
	}
	if b.applyOrder != nil && b.orderEnabled != nil && b.capOrder != nil &&
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

func (s *Service) reconcileMissingRemoteListItems(ctx context.Context, conn Connection, store userstore.UserStore, b listBinding, seenRemoteKeys map[string]bool, result *ImportListResult) error {
	states, err := s.repo.ListListItemStates(ctx, conn.ID, b.kind)
	if err != nil {
		return err
	}
	now := s.now()
	for _, state := range states {
		if !state.RemotePresent || state.ProviderItemKey == "" || seenRemoteKeys[state.ProviderItemKey] {
			continue
		}
		if b.removalsEnabled(conn) && state.LocalPresent {
			if err := b.localRemove(ctx, store, conn.ProfileID, state.MediaItemID); err != nil {
				return err
			}
			if err := s.repo.MarkListItemLocalRemoved(ctx, conn.ID, b.kind, state.MediaItemID, now); err != nil {
				return err
			}
			result.Removed++
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
		sent := exportResultSentSet(exportResult)
		for _, item := range toSend {
			if sent[item.MediaItemID] || sent[item.ProviderItemKey] {
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
		sent := exportResultSentSet(result)
		for _, item := range items {
			attempted[item.MediaItemID] = true
			// Sent (removed) and NotFound (already absent remotely) both reconcile
			// the row; true failures stay pending for the next run.
			if sent[item.MediaItemID] || sent[item.ProviderItemKey] ||
				containsString(result.NotFound, item.MediaItemID) || containsString(result.NotFound, item.ProviderItemKey) {
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
	type listMediaResolver interface {
		GetListMediaItems(ctx context.Context, mediaItemIDs []string) (map[string]LocalFavorite, error)
	}
	resolver, ok := s.repo.(listMediaResolver)
	if !ok {
		return nil, nil, nil, fmt.Errorf("list media resolver is not configured")
	}
	resolved, err := resolver.GetListMediaItems(ctx, ids)
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
			result, ok, err := b.removeItems(ctx, cfg, conn, provider, event.Items)
			if !ok {
				continue
			}
			if err != nil {
				// Leave remote_present set so the scheduled reconcile retries.
				s.recordLocalWatchEventError(ctx, conn, err)
				continue
			}
			sent := exportResultSentSet(result)
			for _, item := range event.Items {
				if sent[item.MediaItemID] || sent[item.ProviderItemKey] ||
					containsString(result.NotFound, item.MediaItemID) || containsString(result.NotFound, item.ProviderItemKey) {
					if err := s.repo.MarkListItemRemoteRemoved(ctx, conn.ID, b.kind, item.MediaItemID, now); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
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
	sent := exportResultSentSet(result)
	for _, item := range toSend {
		if sent[item.MediaItemID] || sent[item.ProviderItemKey] {
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
