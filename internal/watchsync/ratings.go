package watchsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// This file syncs a profile's movie and series ratings with a provider. Silo
// stores 1 to 5 stars; providers use integers from 1 to 10. Every decision is
// made in stars, so a remote change inside one star (7 to 8) is not a change,
// and Silo never overwrites a remote 7 with the 8 its 4 stars map to.
//
// Each item is a three-way merge of the local rating, the remote rating, and
// the last rating both sides agreed on (watch_provider_rating_items). Whichever
// side moved away from the agreed rating wins; when both moved to different
// values, a rating beats a removal and otherwise the newer change wins, with
// ties going to Silo.
//
// Reconciliation of one connection is serialized across nodes by a database
// advisory lock (see WithRatingSyncLock). Imports are also compare-and-set on
// the local value this run observed, so a concurrent user edit always wins and
// is reconsidered next run. Provider writes are idempotent desired-state
// pushes.

const (
	ratingExportBatchSize = 100
	// maxRatingResends bounds how many times sendRatings resends ratings that
	// changed while their write was in flight.
	maxRatingResends = 3
	// ratingCursorSegment marks provider cursor keys that belong to rating
	// reads (for example "simkl.ratings.movies"), so they reset together with
	// the agreed ratings when a connection moves to another provider account.
	ratingCursorSegment = ".ratings"
	// ratingImportCursorKey records that the rating read cursors were saved
	// while import was on. Cursors saved in send-only mode may have skipped
	// changes that were never imported, so turning import on reads everything.
	ratingImportCursorKey = "watchsync.ratings.import"
)

// ratingStore is the profile rating table (catalog.RatingsRepo). Imports use the
// compare-and-set writes, which never dispatch rating events, so an imported
// rating is not echoed back to providers.
type ratingStore interface {
	ListAll(ctx context.Context, userID int, profileID string) ([]catalog.UserRating, error)
	Get(ctx context.Context, userID int, profileID, mediaItemID string) (*catalog.UserRating, error)
	SetIfUnchanged(ctx context.Context, userID int, profileID, mediaItemID string, observed catalog.ObservedRating, rating int, ratedAt time.Time) (bool, error)
	DeleteIfUnchanged(ctx context.Context, userID int, profileID, mediaItemID string, observed catalog.ObservedRating) (bool, error)
}

// ratingProfileStaler marks a profile's recommendations stale after imports
// changed its ratings.
type ratingProfileStaler interface {
	MarkProfileStale(ctx context.Context, userID int, profileID string) error
}

func (s *Service) WithRatingStore(store ratingStore, staler ratingProfileStaler) *Service {
	if s != nil {
		s.ratings = store
		s.ratingStaler = staler
	}
	return s
}

// starsFromProviderRating converts a provider rating (1 to 10) to stars,
// rounding half up: 1-2 is 1 star, 3-4 is 2 stars, and 9-10 is 5 stars.
func starsFromProviderRating(rating int) int {
	rating = min(max(rating, 1), 10)
	return (rating + 1) / 2
}

// providerRatingFromStars converts stars to the provider scale.
func providerRatingFromStars(stars int) int {
	return stars * 2
}

type ratingAction uint8

const (
	ratingKeep ratingAction = iota
	ratingRebase
	ratingExport
	ratingImport
)

// decideRating merges one item. local, remote and base are stars, with 0 for
// unrated; base is the last agreed rating. A zero remoteAt is unknown.
func decideRating(local, remote, base int, localAt, remoteAt time.Time) ratingAction {
	switch {
	case local == remote && local == base:
		return ratingKeep
	case local == remote:
		return ratingRebase
	case remote == base:
		return ratingExport
	case local == base:
		return ratingImport
	// Both sides changed to different values. A rating beats a removal so a
	// conflict never deletes; otherwise the newer change wins and ties go to Silo.
	case local == 0:
		return ratingImport
	case remote == 0:
		return ratingExport
	case !remoteAt.IsZero() && remoteAt.After(localAt):
		return ratingImport
	default:
		return ratingExport
	}
}

// ratingItem is one movie or series in a rating sync.
type ratingItem struct {
	identity LocalFavorite
	local    int
	localAt  time.Time
	// stored is the agreed-rating row, nil when there is none.
	stored *RatingSyncState
	// base is the agreed rating used for the decision. It is 0 when there is no
	// row, and also when a complete snapshot shows the provider never held a
	// rating Silo sent, so the rating is sent again instead of deleted.
	base     int
	remote   int
	remoteAt time.Time
	// observed means this run read the remote value from the provider.
	observed bool
	// remoteKey is the provider's own key for the item from this run's read.
	remoteKey string
}

// observedLocal is the local rating this sync read, for compare-and-set writes.
func (item *ratingItem) observedLocal() catalog.ObservedRating {
	return catalog.ObservedRating{Rating: item.local, RatedAt: item.localAt}
}

// providerKey is the key recorded for an item and sent with its writes: the
// provider's own key once a read returned one, so the provider's tombstones and
// writes can name it, and otherwise Silo's key.
func (item *ratingItem) providerKey() string {
	if item.remoteKey != "" {
		return item.remoteKey
	}
	if item.stored != nil && item.stored.ProviderItemKey != "" {
		return item.stored.ProviderItemKey
	}
	return item.identity.ProviderItemKey
}

// sendIdentity is the item identity sent to the provider, carrying its
// providerKey.
func (item *ratingItem) sendIdentity() LocalFavorite {
	identity := item.identity
	identity.ProviderItemKey = item.providerKey()
	return identity
}

// dropUnsyncedRatingKinds removes the items of kinds the provider does not rate.
func dropUnsyncedRatingKinds(items map[string]*ratingItem, provider Provider) {
	filter, ok := provider.(RatingKindFilter)
	if !ok {
		return
	}
	for id, item := range items {
		if !filter.SyncsRatingKind(item.identity.Kind) {
			delete(items, id)
		}
	}
}

// SyncRatingsResult summarizes one rating sync.
type SyncRatingsResult struct {
	RemoteFound int
	Imported    int
	LocalFound  int
	Sent        int
	Warnings    []string
}

// syncRatings runs the scheduled rating sync for one connection: read the
// provider's ratings, merge each item, apply imports locally, then send local
// changes.
func (s *Service) syncRatings(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider) (SyncRatingsResult, error) {
	var result SyncRatingsResult
	if s.ratings == nil {
		return result, fmt.Errorf("rating store is not configured")
	}
	if !ratingSyncEnabled(conn, provider) {
		return result, nil
	}

	// One reconciliation per connection at a time across every node: runs
	// that overlapped would each merge from their own reads and could leave
	// the agreed ratings older than what the provider holds. A run that finds
	// the lock held leaves ratings to the one already running.
	locked, err := s.repo.WithRatingSyncLock(ctx, conn.ID, false, func(ctx context.Context) error {
		var err error
		result, err = s.syncRatingsLocked(ctx, conn, cfg, provider)
		return err
	})
	if err == nil && !locked {
		result.Warnings = append(result.Warnings, "ratings are already syncing for this connection; skipped them in this run")
	}
	return result, err
}

// ratingSyncEnabled reports whether the connection imports or sends ratings
// that the provider supports.
func ratingSyncEnabled(conn Connection, provider Provider) bool {
	caps := provider.Capabilities()
	_, canImport := provider.(RatingImporter)
	_, canExport := provider.(RatingExporter)
	return (conn.ImportRatingsEnabled && canImport && caps.ImportRatings) ||
		(conn.ExportRatingsEnabled && canExport && caps.ExportRatings)
}

// syncRatingsLocked is syncRatings under the connection's rating sync lock.
// It re-reads the connection first: an account switch waits for the lock, so
// the binding read here holds until the reconciliation ends.
func (s *Service) syncRatingsLocked(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider) (SyncRatingsResult, error) {
	var result SyncRatingsResult
	current, err := s.reloadConnection(ctx, conn)
	if err != nil {
		return result, err
	}
	if current.ProviderAccountID != conn.ProviderAccountID {
		result.Warnings = append(result.Warnings, "the connection moved to another provider account before ratings synced; ratings were not applied")
		return result, nil
	}
	conn = current
	caps := provider.Capabilities()
	importer, canImport := provider.(RatingImporter)
	_, canExport := provider.(RatingExporter)
	canImport = canImport && caps.ImportRatings
	canExport = canExport && caps.ExportRatings
	importAllowed := conn.ImportRatingsEnabled && canImport
	exportAllowed := conn.ExportRatingsEnabled && canExport
	if !importAllowed && !exportAllowed {
		return result, nil
	}

	items, warnings, err := s.loadRatingItems(ctx, conn, nil)
	if err != nil {
		return result, err
	}
	dropUnsyncedRatingKinds(items, provider)
	result.Warnings = append(result.Warnings, warnings...)
	for _, item := range items {
		if item.local > 0 {
			result.LocalFound++
		}
	}

	// A provider that can read ratings is read even when only sending, so Silo
	// does not push values the provider already holds.
	var batch RatingImportBatch
	if canImport {
		if s.matcher == nil {
			return result, fmt.Errorf("watch provider matcher is not configured")
		}
		fetchConn := conn
		if importAllowed && conn.SyncCursors[ratingImportCursorKey] == "" {
			fetchConn.SyncCursors = withoutRatingCursors(conn.SyncCursors)
		}
		batch, err = importer.FetchRatings(ctx, cfg, fetchConn)
		if err != nil {
			return result, err
		}
		result.Warnings = append(result.Warnings, batch.Warnings...)
		for _, row := range batch.Rows {
			if !row.Removed {
				result.RemoteFound++
			}
		}
		warnings, err := s.resolveRemoteRatings(ctx, items, batch)
		if err != nil {
			return result, err
		}
		dropUnsyncedRatingKinds(items, provider)
		result.Warnings = append(result.Warnings, warnings...)
	} else {
		markRemoteUnknown(items)
	}

	// Account switches wait for the lock, but a binding changed outside that
	// path during the provider read must still never receive these ratings.
	if current, err := s.reloadConnection(ctx, conn); err != nil {
		return result, err
	} else if current.ProviderAccountID != conn.ProviderAccountID {
		result.Warnings = append(result.Warnings, "the connection moved to another provider account during the sync; ratings were not applied")
		return result, nil
	}

	applied, err := s.reconcileRatings(ctx, conn, cfg, provider, items, importAllowed, exportAllowed, func() error {
		if !canImport {
			return nil
		}
		return s.saveRatingCursors(ctx, conn, batch.UpdatedCursors, importAllowed)
	})
	result.Imported = applied.imported
	result.Sent = applied.sent
	result.Warnings = append(result.Warnings, applied.warnings...)
	return result, err
}

// saveRatingCursors stores the read cursors in send-only mode too, so a
// cursor-gated provider does not re-read every rating on every run, and marks
// whether import was on so turning it on later forces one full read.
func (s *Service) saveRatingCursors(ctx context.Context, conn Connection, updated map[string]string, importAllowed bool) error {
	fresh, err := s.reloadConnection(ctx, conn)
	if err != nil {
		return err
	}
	if fresh.ProviderAccountID != conn.ProviderAccountID {
		return nil
	}
	var remove []string
	if importAllowed && fresh.SyncCursors[ratingImportCursorKey] == "" {
		for key := range fresh.SyncCursors {
			if strings.Contains(key, ratingCursorSegment) {
				remove = append(remove, key)
			}
		}
	}
	set := mergeSyncCursors(nil, updated)
	if importAllowed {
		set[ratingImportCursorKey] = "1"
	} else {
		remove = append(remove, ratingImportCursorKey)
	}
	// Only the rating cursor keys change, and only while the connection is
	// still bound to this account: a full-row write from this snapshot could
	// otherwise restore the old account over a rebind made meanwhile.
	return s.repo.UpdateRatingCursors(ctx, conn.ID, conn.ProviderAccountID, remove, set)
}

// HandleLocalRatingEvent sends a profile's rating changes to the providers
// that receive its ratings. It is fire-and-forget so the originating API
// request never waits on provider I/O.
func (s *Service) HandleLocalRatingEvent(ctx context.Context, event LocalRatingEvent) error {
	if event.UserID == 0 || event.ProfileID == "" || len(event.MediaItemIDs) == 0 {
		return nil
	}
	go func() {
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := s.processLocalRatingEvent(bg, event); err != nil {
			slog.WarnContext(ctx, "failed to dispatch local rating provider event", "component", "watchsync", "user_id", event.UserID, "profile_id", event.ProfileID, "error", err)
		}
	}()
	return nil
}

func (s *Service) processLocalRatingEvent(ctx context.Context, event LocalRatingEvent) error {
	if s.ratings == nil {
		return nil
	}
	conns, err := s.repo.ListRatingEventConnections(ctx, event.UserID, event.ProfileID)
	if err != nil {
		return err
	}
	for _, conn := range conns {
		provider, ok := s.registry.Get(conn.Provider)
		if !ok || !provider.Capabilities().ExportRatings {
			continue
		}
		if _, ok := provider.(RatingExporter); !ok {
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
		// node, so the send works from settled agreed ratings.
		_, err = s.repo.WithRatingSyncLock(ctx, conn.ID, true, func(ctx context.Context) error {
			return s.sendLocalRatings(ctx, conn, cfg, provider, event.MediaItemIDs)
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

// recordRatingEventError records a rating event's error on a fresh read of
// the connection: the reconciliation the event waited for may have saved
// cursors that a write from the older snapshot would overwrite. The error is
// only logged when the connection cannot be re-read or changed account.
func (s *Service) recordRatingEventError(ctx context.Context, conn Connection, err error) {
	fresh, reloadErr := s.reloadConnection(ctx, conn)
	if reloadErr != nil || fresh.ProviderAccountID != conn.ProviderAccountID {
		slog.WarnContext(ctx, "local rating provider event failed", "component", "watchsync", "provider", conn.Provider, "connection_id", conn.ID, "error", err, "reload_error", reloadErr)
		return
	}
	s.recordLocalWatchEventError(ctx, fresh, err)
}

// sendLocalRatings sends a local rating event's new ratings under the
// connection's rating sync lock. The connection is re-read first, since it
// may have moved to another account or stopped sending while this waited.
func (s *Service) sendLocalRatings(ctx context.Context, conn Connection, cfg ServerConfig, provider Provider, mediaItemIDs []string) error {
	current, err := s.reloadConnection(ctx, conn)
	if err != nil {
		return err
	}
	if current.ProviderAccountID != conn.ProviderAccountID || !current.ExportRatingsEnabled {
		return nil
	}
	conn = current
	items, _, err := s.loadRatingItems(ctx, conn, mediaItemIDs)
	if err != nil {
		return err
	}
	dropUnsyncedRatingKinds(items, provider)
	// Only the local side is known here. A removal waits for the scheduled
	// merge, which can see whether the provider changed the rating since
	// (a rating beats a removal); a new rating is sent now.
	for id, item := range items {
		if item.local == 0 {
			delete(items, id)
		}
	}
	markRemoteUnknown(items)
	_, err = s.reconcileRatings(ctx, conn, cfg, provider, items, false, true, nil)
	return err
}

// loadRatingItems gathers the profile's movie and series ratings and the
// connection's agreed ratings, keyed by media item. onlyIDs limits both to the
// listed items; nil loads everything.
func (s *Service) loadRatingItems(ctx context.Context, conn Connection, onlyIDs []string) (map[string]*ratingItem, []string, error) {
	var local []catalog.UserRating
	if onlyIDs != nil {
		for _, id := range onlyIDs {
			rating, err := s.ratings.Get(ctx, conn.UserID, conn.ProfileID, id)
			if err != nil {
				return nil, nil, err
			}
			if rating != nil {
				local = append(local, *rating)
			}
		}
	} else {
		// One read gives a consistent snapshot; paging could miss a rating
		// that a concurrent edit moves, which would read as a local removal.
		var err error
		if local, err = s.ratings.ListAll(ctx, conn.UserID, conn.ProfileID); err != nil {
			return nil, nil, err
		}
	}
	states, err := s.repo.ListRatingSyncStates(ctx, conn.ID, conn.ProviderAccountID, onlyIDs)
	if err != nil {
		return nil, nil, err
	}

	ids := make([]string, 0, len(local)+len(states))
	for _, rating := range local {
		ids = append(ids, rating.MediaItemID)
	}
	for _, state := range states {
		ids = append(ids, state.MediaItemID)
	}
	resolved, err := s.resolveListMediaItems(ctx, ids)
	if err != nil {
		return nil, nil, err
	}

	items := make(map[string]*ratingItem, len(ids))
	var warnings []string
	ratedLocally := make(map[string]bool, len(local))
	for _, rating := range local {
		ratedLocally[rating.MediaItemID] = true
		identity, ok := resolved[rating.MediaItemID]
		if !ok || !ratingSyncKind(identity.Kind) {
			continue
		}
		if identity.ProviderItemKey == "" {
			warnings = append(warnings, "rated item has no provider ids: "+rating.MediaItemID)
			continue
		}
		items[rating.MediaItemID] = &ratingItem{identity: identity, local: rating.Rating, localAt: rating.RatedAt}
	}
	for i := range states {
		state := states[i]
		item, ok := items[state.MediaItemID]
		if !ok {
			// A rated item this sync skipped (no longer in the catalog, or
			// without external ids) must not read as a local removal.
			if ratedLocally[state.MediaItemID] {
				continue
			}
			// Agreed but no longer rated locally. The media item may be gone, so
			// fall back to the identity the agreed row recorded.
			identity, found := resolved[state.MediaItemID]
			if !found || identity.ProviderItemKey == "" {
				identity = LocalFavorite{MediaItemID: state.MediaItemID, Kind: state.Kind, ProviderItemKey: state.ProviderItemKey}
			}
			if !ratingSyncKind(identity.Kind) || identity.ProviderItemKey == "" {
				continue
			}
			item = &ratingItem{identity: identity}
			items[state.MediaItemID] = item
		}
		item.stored = &state
		item.base = state.SyncedRating
	}
	return items, warnings, nil
}

// markRemoteUnknown treats every remote value as unchanged since the agreed
// rating, for merges that did not read the provider.
func markRemoteUnknown(items map[string]*ratingItem) {
	for _, item := range items {
		item.remote = item.base
	}
}

// resolveRemoteRatings sets each item's remote value from a provider read.
// Matched rows give the value directly and explicit tombstones remove it. An
// item missing from the read counts as unrated only when the read is a complete
// snapshot of the item's kind, no row shares one of its ids (a row the matcher
// could not place may be this item), and a previous read confirmed the provider
// held the agreed rating. Every other item is unknown and keeps its agreed value.
func (s *Service) resolveRemoteRatings(ctx context.Context, items map[string]*ratingItem, batch RatingImportBatch) ([]string, error) {
	var warnings []string
	snapshot := make(map[string]bool, len(batch.SnapshotKinds))
	for _, kind := range batch.SnapshotKinds {
		snapshot[kind] = true
	}
	// Tombstones resolve by provider key within their kind: the same key (for
	// example tmdb:101) can name a movie and a series. A tombstone without a
	// kind resolves by key alone and must then name a single title.
	byKey := make(map[string][]*ratingItem, len(items))
	byKindKey := make(map[string][]*ratingItem, len(items))
	for _, item := range items {
		if item.stored != nil && item.stored.ProviderItemKey != "" {
			key := item.stored.ProviderItemKey
			kind := item.stored.Kind
			if kind == "" {
				kind = item.identity.Kind
			}
			byKey[key] = append(byKey[key], item)
			byKindKey[kind+"\x00"+key] = append(byKindKey[kind+"\x00"+key], item)
		}
	}

	type remoteMatch struct {
		row RemoteRating
		id  string
	}
	seenTokens := make(map[string]bool)
	rowsPerKind := make(map[string]int)
	unidentified := make(map[string]bool)
	var matches []remoteMatch
	var unresolved []string
	for _, row := range batch.Rows {
		if row.Removed {
			key := strings.TrimSpace(row.ProviderItemKey)
			candidates := byKey[key]
			if row.Kind != "" {
				candidates = byKindKey[row.Kind+"\x00"+key]
			}
			if len(candidates) != 1 {
				warnings = append(warnings, "watch sync provider returned a rating removal that matches no single title")
				continue
			}
			item := candidates[0]
			item.remote, item.remoteAt, item.observed = 0, time.Time{}, true
			continue
		}
		if !ratingSyncKind(row.Kind) {
			continue
		}
		// Record the row's ids before any check, so a row Silo cannot use
		// still keeps its title from reading as removed.
		for _, token := range ratingIdentityTokens(row.Kind, row.IMDbID, row.TMDBID, row.TVDBID, row.ProviderItemKey) {
			seenTokens[token] = true
		}
		if row.IMDbID == "" && row.TMDBID == "" && row.TVDBID == "" {
			// A title known only by provider ids could be any local item, so
			// absence from this read proves nothing for its kind.
			unidentified[row.Kind] = true
			continue
		}
		if row.Rating < 1 || row.Rating > 10 {
			warnings = append(warnings, fmt.Sprintf("watch sync provider returned an out-of-range rating %d", row.Rating))
			continue
		}
		// Only usable rows show the snapshot is not empty; an invalid row
		// must not switch off the empty-snapshot guard below.
		rowsPerKind[row.Kind]++
		match, reason, err := s.matcher.Match(ctx, row.HistoryRecord())
		if err != nil {
			return warnings, err
		}
		if match == nil {
			if reason != "" {
				warnings = append(warnings, reason)
			}
			continue
		}
		matches = append(matches, remoteMatch{row: row, id: match.MediaItemID})
		if _, ok := items[match.MediaItemID]; !ok {
			unresolved = append(unresolved, match.MediaItemID)
		}
	}

	// Remote-only ratings become items with no local rating and no agreement.
	if len(unresolved) > 0 {
		resolved, err := s.resolveListMediaItems(ctx, unresolved)
		if err != nil {
			return warnings, err
		}
		for _, id := range unresolved {
			identity, ok := resolved[id]
			if !ok || !ratingSyncKind(identity.Kind) || items[id] != nil {
				continue
			}
			items[id] = &ratingItem{identity: identity}
		}
	}
	for _, match := range matches {
		item := items[match.id]
		if item == nil {
			continue
		}
		// Several rows can match one item; the newest rating wins.
		if item.observed && !match.row.RatedAt.After(item.remoteAt) {
			continue
		}
		item.remote = starsFromProviderRating(match.row.Rating)
		item.remoteAt = match.row.RatedAt
		item.observed = true
		item.remoteKey = strings.TrimSpace(match.row.ProviderItemKey)
	}

	for kind := range unidentified {
		if snapshot[kind] {
			delete(snapshot, kind)
			warnings = append(warnings, "watch sync provider returned "+kind+" ratings without an IMDb, TMDB, or TVDB id; skipped rating removals for that kind")
		}
	}
	// A complete snapshot that returned nothing for a kind is more likely a
	// failed read than a mass removal when it would remove several confirmed
	// ratings Silo still holds, so it is not trusted. One such rating, or a
	// removal Silo already sent, is an ordinary change and goes through.
	for kind := range snapshot {
		if rowsPerKind[kind] > 0 {
			continue
		}
		held := 0
		for _, item := range items {
			if item.identity.Kind == kind && item.local > 0 && item.stored != nil && item.stored.RemoteSeen {
				held++
			}
		}
		if held > 1 {
			delete(snapshot, kind)
			warnings = append(warnings, "watch sync provider returned no "+kind+" ratings; skipped rating removals for that kind")
		}
	}

	for _, item := range items {
		if item.observed {
			continue
		}
		absent := snapshot[item.identity.Kind]
		if absent {
			identity := item.identity
			for _, token := range ratingIdentityTokens(identity.Kind, identity.IMDbID, identity.TMDBID, identity.TVDBID, identity.ProviderItemKey, item.providerKey()) {
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
			item.remote = 0
		default:
			// The provider never confirmed holding this rating, so it was not
			// agreed: send it again rather than delete it locally.
			item.base = 0
			item.remote = 0
		}
	}
	return warnings, nil
}

// ratingIdentityTokens are the kind-qualified ids and keys of an item. TMDB and
// TVDB number movies and series separately, so ids only compare within a kind.
func ratingIdentityTokens(kind, imdbID, tmdbID, tvdbID string, keys ...string) []string {
	tokens := make([]string, 0, 3+len(keys))
	for _, id := range append([]string{
		prefixedID("imdb", imdbID), prefixedID("tmdb", tmdbID), prefixedID("tvdb", tvdbID),
	}, keys...) {
		if id = strings.TrimSpace(id); id != "" {
			tokens = append(tokens, kind+":"+id)
		}
	}
	return tokens
}

func prefixedID(namespace, id string) string {
	if strings.TrimSpace(id) == "" {
		return ""
	}
	return namespace + ":" + strings.TrimSpace(id)
}

func ratingSyncKind(kind string) bool {
	return kind == historyimport.KindMovie || kind == historyimport.KindSeries
}

type ratingReconcileResult struct {
	imported int
	sent     int
	warnings []string
}

// reconcileRatings applies the merge decision for every item: imports first,
// then the agreed-rating bookkeeping, then afterImport (the scheduled run's
// cursor update), then provider writes. Decisions a direction does not allow
// are skipped without recording agreement, so they are reconsidered when the
// direction is turned on.
func (s *Service) reconcileRatings(
	ctx context.Context,
	conn Connection,
	cfg ServerConfig,
	provider Provider,
	items map[string]*ratingItem,
	importAllowed, exportAllowed bool,
	afterImport func() error,
) (ratingReconcileResult, error) {
	var result ratingReconcileResult
	var upserts []RatingSyncState
	var deletes []string
	var sets []*ratingItem
	var removals []*ratingItem
	agree := func(item *ratingItem, stars int, seen bool) {
		switch {
		case stars == 0 && item.stored != nil:
			deletes = append(deletes, item.identity.MediaItemID)
		case stars == 0:
		case item.stored == nil || item.stored.SyncedRating != stars || item.stored.RemoteSeen != seen:
			upserts = append(upserts, RatingSyncState{
				ConnectionID:      conn.ID,
				ProviderAccountID: conn.ProviderAccountID,
				MediaItemID:       item.identity.MediaItemID,
				Kind:              item.identity.Kind,
				ProviderItemKey:   item.providerKey(),
				SyncedRating:      stars,
				RemoteSeen:        seen,
			})
		}
	}

	// Imports commit one by one, so recommendations are marked stale once any
	// applied, even if the bookkeeping after them fails or the run's context
	// ends; the mark gets a short context of its own.
	defer func() {
		if result.imported > 0 && s.ratingStaler != nil {
			markCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := s.ratingStaler.MarkProfileStale(markCtx, conn.UserID, conn.ProfileID); err != nil {
				slog.WarnContext(ctx, "failed to mark profile stale after rating import", "component", "watchsync", "user_id", conn.UserID, "profile_id", conn.ProfileID, "error", err)
			}
		}
	}()
	for _, item := range items {
		switch decideRating(item.local, item.remote, item.base, item.localAt, item.remoteAt) {
		case ratingKeep:
			// Record a first confirmation that the provider holds the rating.
			seen := item.stored != nil && item.stored.RemoteSeen
			agree(item, item.base, seen || (item.observed && item.base > 0))
		case ratingRebase:
			agree(item, item.local, item.observed)
		case ratingImport:
			if !importAllowed {
				continue
			}
			applied, err := s.importRating(ctx, conn, item)
			if err != nil {
				return result, err
			}
			if applied {
				result.imported++
				agree(item, item.remote, item.observed)
			}
		case ratingExport:
			if !exportAllowed {
				continue
			}
			if item.local > 0 {
				sets = append(sets, item)
			} else {
				removals = append(removals, item)
			}
		}
	}
	if err := s.repo.UpsertRatingSyncStates(ctx, upserts); err != nil {
		return result, err
	}
	if err := s.repo.DeleteRatingSyncStates(ctx, conn.ID, conn.ProviderAccountID, deletes); err != nil {
		return result, err
	}
	if afterImport != nil {
		if err := afterImport(); err != nil {
			return result, err
		}
	}
	if len(sets) == 0 && len(removals) == 0 {
		return result, nil
	}

	exporter, ok := provider.(RatingExporter)
	if !ok {
		return result, fmt.Errorf("provider %q does not implement rating export", conn.Provider)
	}
	sets, deferred, err := s.gateRatingExports(ctx, conn, provider, sets)
	if err != nil {
		return result, err
	}
	result.warnings = append(result.warnings, deferred...)
	sent, warnings, err := s.sendRatings(ctx, conn, cfg, exporter, sets, removals, maxRatingResends)
	result.sent = sent
	result.warnings = append(result.warnings, warnings...)
	return result, err
}

// importRating writes the remote value locally if the local rating is still the
// value this run observed.
func (s *Service) importRating(ctx context.Context, conn Connection, item *ratingItem) (bool, error) {
	id := item.identity.MediaItemID
	if item.remote == 0 {
		return s.ratings.DeleteIfUnchanged(ctx, conn.UserID, conn.ProfileID, id, item.observedLocal())
	}
	ratedAt := item.remoteAt
	if ratedAt.IsZero() {
		ratedAt = s.now()
	}
	return s.ratings.SetIfUnchanged(ctx, conn.UserID, conn.ProfileID, id, item.observedLocal(), item.remote, ratedAt)
}

// gateRatingExports holds back new ratings that a provider would record as a
// watch until the profile has a completed play of the title. A title the
// provider already holds a rating for is already on its list, so changing that
// rating records nothing new and is not held back.
func (s *Service) gateRatingExports(ctx context.Context, conn Connection, provider Provider, sets []*ratingItem) ([]*ratingItem, []string, error) {
	gate, ok := provider.(RatingExportWatchGate)
	if !ok || len(sets) == 0 {
		return sets, nil, nil
	}
	gated := func(item *ratingItem) bool {
		heldRemotely := (item.observed && item.remote > 0) ||
			(item.stored != nil && item.stored.RemoteSeen && item.stored.SyncedRating > 0)
		return !heldRemotely && gate.RatingExportRequiresWatched(item.identity.Kind)
	}
	var gatedIDs []string
	for _, item := range sets {
		if gated(item) {
			gatedIDs = append(gatedIDs, item.identity.MediaItemID)
		}
	}
	if len(gatedIDs) == 0 {
		return sets, nil, nil
	}
	if s.storeProvider == nil {
		return nil, nil, fmt.Errorf("user store provider is not configured")
	}
	store, err := s.storeProvider.ForUser(ctx, conn.UserID)
	if err != nil {
		return nil, nil, fmt.Errorf("open user store: %w", err)
	}
	history, err := listAllCompletedHistory(ctx, store, userstore.CompletedHistoryQuery{ProfileID: conn.ProfileID, MediaItemIDs: gatedIDs})
	if err != nil {
		return nil, nil, err
	}
	watched := make(map[string]bool, len(history))
	for _, entry := range history {
		watched[entry.MediaItemID] = true
	}
	kept := sets[:0]
	var warnings []string
	for _, item := range sets {
		if gated(item) && !watched[item.identity.MediaItemID] {
			warnings = append(warnings, "rating waits for a completed play before it is sent: "+item.identity.MediaItemID)
			continue
		}
		kept = append(kept, item)
	}
	return kept, warnings, nil
}

// sendRatings pushes sets and removals in batches. A confirmed set is recorded
// as agreed only if the local rating still has the value sent, so a change
// made while the write was in flight is not masked; it is agreed but not seen
// until a later read confirms the provider kept it. A confirmed removal keeps
// its agreed row: the next read either finds the title unrated, which clears
// the row, or finds another provider entry still rated, which is removed in
// turn instead of being imported back.
//
// A rating changed while its write was in flight may have been sent by a
// newer event already, which this older write has just overwritten on the
// provider. The current value is sent again, up to resends more times, so the
// provider ends on it.
func (s *Service) sendRatings(ctx context.Context, conn Connection, cfg ServerConfig, exporter RatingExporter, sets, removals []*ratingItem, resends int) (int, []string, error) {
	sent := 0
	var warnings []string
	var changedSets, changedRemovals []*ratingItem
	// What this call last confirmed on the provider for each changed item, in
	// case the resends run out.
	var lastSent []RatingSyncState
	var lastRemoved []string
	for start := 0; start < len(sets); start += ratingExportBatchSize {
		batch := sets[start:min(start+ratingExportBatchSize, len(sets))]
		payload := make([]LocalRating, 0, len(batch))
		for _, item := range batch {
			payload = append(payload, LocalRating{
				LocalFavorite: item.sendIdentity(),
				Rating:        providerRatingFromStars(item.local),
				RatedAt:       item.localAt,
			})
		}
		result, err := exporter.ExportRatings(ctx, cfg, conn, payload)
		if err != nil {
			return sent, warnings, err
		}
		states := make([]RatingSyncState, 0, len(batch))
		for _, item := range batch {
			identity := item.sendIdentity()
			if confirmed, _ := exportItemOutcome(result, identity.MediaItemID, identity.ProviderItemKey); !confirmed {
				warnings = append(warnings, exportFailureReason(result, identity, "rating")+": "+identity.MediaItemID)
				continue
			}
			sent++
			current, err := s.ratings.Get(ctx, conn.UserID, conn.ProfileID, identity.MediaItemID)
			if err != nil {
				return sent, warnings, err
			}
			if current == nil || current.Rating != item.local {
				changed := *item
				changed.local, changed.localAt = 0, time.Time{}
				if current != nil {
					changed.local, changed.localAt = current.Rating, current.RatedAt
				}
				if changed.local > 0 {
					changedSets = append(changedSets, &changed)
				} else {
					changedRemovals = append(changedRemovals, &changed)
				}
				lastSent = append(lastSent, RatingSyncState{
					ConnectionID:      conn.ID,
					ProviderAccountID: conn.ProviderAccountID,
					MediaItemID:       identity.MediaItemID,
					Kind:              identity.Kind,
					ProviderItemKey:   identity.ProviderItemKey,
					SyncedRating:      item.local,
				})
				continue
			}
			states = append(states, RatingSyncState{
				ConnectionID:      conn.ID,
				ProviderAccountID: conn.ProviderAccountID,
				MediaItemID:       identity.MediaItemID,
				Kind:              identity.Kind,
				ProviderItemKey:   identity.ProviderItemKey,
				SyncedRating:      item.local,
			})
		}
		if err := s.repo.UpsertRatingSyncStates(ctx, states); err != nil {
			return sent, warnings, err
		}
	}
	for start := 0; start < len(removals); start += ratingExportBatchSize {
		batch := removals[start:min(start+ratingExportBatchSize, len(removals))]
		payload := make([]LocalFavorite, 0, len(batch))
		for _, item := range batch {
			payload = append(payload, item.sendIdentity())
		}
		result, err := exporter.RemoveRatings(ctx, cfg, conn, payload)
		if err != nil {
			return sent, warnings, err
		}
		for _, item := range batch {
			identity := item.sendIdentity()
			// A provider that no longer knows the title has no rating to clear.
			if confirmed, missing := exportItemOutcome(result, identity.MediaItemID, identity.ProviderItemKey); confirmed || missing {
				sent++
				current, err := s.ratings.Get(ctx, conn.UserID, conn.ProfileID, identity.MediaItemID)
				if err != nil {
					return sent, warnings, err
				}
				if current != nil {
					changed := *item
					changed.local, changed.localAt = current.Rating, current.RatedAt
					changedSets = append(changedSets, &changed)
					lastRemoved = append(lastRemoved, identity.MediaItemID)
				}
				continue
			}
			warnings = append(warnings, exportFailureReason(result, identity, "rating removal")+": "+identity.MediaItemID)
		}
	}
	if len(changedSets) == 0 && len(changedRemovals) == 0 {
		return sent, warnings, nil
	}
	if resends > 0 {
		more, moreWarnings, err := s.sendRatings(ctx, conn, cfg, exporter, changedSets, changedRemovals, resends-1)
		return sent + more, append(warnings, moreWarnings...), err
	}
	// The rating kept changing through every resend. The agreed row is set to
	// what this call last confirmed on the provider, so the next merge reads
	// the newer local value, rating or removal, as a local change and sends it,
	// instead of importing a value the provider holds from an older write.
	if err := s.repo.UpsertRatingSyncStates(ctx, lastSent); err != nil {
		return sent, warnings, err
	}
	if err := s.repo.DeleteRatingSyncStates(ctx, conn.ID, conn.ProviderAccountID, lastRemoved); err != nil {
		return sent, warnings, err
	}
	warnings = append(warnings, fmt.Sprintf("%d ratings changed while being sent and are left for the next sync", len(changedSets)+len(changedRemovals)))
	return sent, warnings, nil
}

// withoutRatingCursors drops the rating read cursors, used together with
// ClearRatingSyncStates when a connection changes provider account.
func withoutRatingCursors(cursors map[string]string) map[string]string {
	kept := make(map[string]string, len(cursors))
	for key, value := range cursors {
		if !strings.Contains(key, ratingCursorSegment) {
			kept[key] = value
		}
	}
	return kept
}
