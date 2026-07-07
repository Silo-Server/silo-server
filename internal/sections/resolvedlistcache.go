package sections

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// The resolved-list cache stores the *shared*, user-agnostic membership and
// ordering of a home rail once per access scope. It sits at the native section
// fetch choke point (FetchOne → fetchSection) and holds presign-free
// []*models.MediaItem only; the per-user overlay (watched flags, play position,
// presigned poster URLs) is always recomputed afterwards in
// SectionHandler.buildSectionsResponse, so a cached entry never leaks one
// profile's state to another.
//
// It is deliberately process-global (package level) rather than per-Fetcher:
// the process builds several independent Fetcher instances (e.g. native API and
// recommendations), and a struct-scoped cache (as editorialCandidateCache is
// today) could never be shared across them.
const (
	// resolvedListTTL is the hard expiry: past this an entry must be rebuilt
	// synchronously.
	resolvedListTTL = 15 * time.Minute
	// resolvedListRefreshLead is how far ahead of expiry a background rebuild is
	// kicked off, so steady traffic is always served a warm entry.
	resolvedListRefreshLead = 10 * time.Minute
	// resolvedListRefreshAfter is the soft threshold (builtAt + 5min): reaching
	// it serves the cached value and triggers one async rebuild.
	resolvedListRefreshAfter = resolvedListTTL - resolvedListRefreshLead
	// resolvedListBuildTimeout bounds every detached loader run — background
	// refreshes and blocking rebuilds alike — so a stuck query can never pin a
	// pool connection indefinitely.
	resolvedListBuildTimeout = 30 * time.Second
	// resolvedListPruneInterval bounds how often expired entries are swept from
	// the map, so keys for scopes that are never requested again cannot linger
	// for the life of the process.
	resolvedListPruneInterval = time.Minute
)

// resolvedListLoader builds the shared item list for a cache key. It takes a
// context so the async refresh path can run detached from the request that
// triggered it.
type resolvedListLoader func(context.Context) ([]*models.MediaItem, int, error)

type resolvedListEntry struct {
	items        []*models.MediaItem
	total        int
	builtAt      time.Time
	refreshAfter time.Time
	expiresAt    time.Time
}

var (
	resolvedListCacheMu sync.RWMutex
	resolvedListCache   = make(map[string]resolvedListEntry)
	// resolvedListLastPrune is the last time expired entries were swept; guarded
	// by resolvedListCacheMu.
	resolvedListLastPrune time.Time

	// resolvedListGroup collapses concurrent blocking rebuilds (cold miss /
	// expired) for the same key into a single loader call.
	resolvedListGroup singleflight.Group

	// resolvedListRefreshMu guards resolvedListRefreshing, which tracks the keys
	// with an in-flight async rebuild so only one background goroutine per key is
	// ever spawned.
	resolvedListRefreshMu  sync.Mutex
	resolvedListRefreshing = make(map[string]struct{})
)

// getOrRefresh returns the shared item list for key, implementing serve /
// refresh-ahead / block-only-when-dead:
//
//   - now < refreshAfter          → return cached, do nothing.
//   - refreshAfter <= now < expiry → return cached AND trigger one async rebuild.
//   - now >= expiry (or cold miss) → block on the build (singleflight collapses
//     concurrent blockers into one).
//
// Returned slices are defensive copies so a caller mutating the result can never
// corrupt the cached entry.
func getOrRefresh(ctx context.Context, key string, now time.Time, loader resolvedListLoader) ([]*models.MediaItem, int, error) {
	if entry, ok := resolvedListGet(key); ok {
		switch {
		case now.Before(entry.refreshAfter):
			return cloneMediaItems(entry.items), entry.total, nil
		case now.Before(entry.expiresAt):
			scheduleResolvedListRefresh(key, now, loader)
			return cloneMediaItems(entry.items), entry.total, nil
		}
		// Past hard expiry: fall through to the blocking rebuild.
	}
	return blockingResolvedListRebuild(ctx, key, now, loader)
}

// blockingResolvedListRebuild rebuilds the entry for key, using singleflight so
// concurrent cold/expired callers collapse into a single loader call.
func blockingResolvedListRebuild(ctx context.Context, key string, now time.Time, loader resolvedListLoader) ([]*models.MediaItem, int, error) {
	type buildResult struct {
		items []*models.MediaItem
		total int
	}

	value, err, _ := resolvedListGroup.Do(key, func() (any, error) {
		// A concurrent async refresh may have installed a still-usable entry
		// between the outer read and acquiring the flight; reuse it rather than
		// hitting the database again.
		if entry, ok := resolvedListGet(key); ok && now.Before(entry.expiresAt) {
			return buildResult{items: entry.items, total: entry.total}, nil
		}
		// Run the loader detached from the leader's request cancellation:
		// singleflight shares this one build across every collapsed waiter, so
		// the leader's client disconnecting (or its deadline firing) must not
		// fail all the other requests riding on the flight. WithoutCancel keeps
		// the leader's context values (tracing, logging) while dropping its
		// cancellation; the timeout re-bounds the detached work.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), resolvedListBuildTimeout)
		defer cancel()
		items, total, err := loader(loadCtx)
		if err != nil {
			return nil, err
		}
		// Never cache an empty membership: some builders (trending, most-watched,
		// new-to-library) can be transiently empty mid-refresh, and freezing an
		// empty rail for the full TTL would starve it. Serve the empty result for
		// this request but keep rebuilding until the row has content.
		if len(items) > 0 {
			resolvedListSet(key, items, total, now)
		}
		return buildResult{items: items, total: total}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	res := value.(buildResult)
	return cloneMediaItems(res.items), res.total, nil
}

// scheduleResolvedListRefresh kicks off at most one background rebuild per key.
// The rebuild runs on a detached context so it survives the request that
// triggered it; a loader error or panic keeps the existing (stale-but-usable)
// entry rather than taking down the process.
func scheduleResolvedListRefresh(key string, now time.Time, loader resolvedListLoader) {
	resolvedListRefreshMu.Lock()
	if _, inflight := resolvedListRefreshing[key]; inflight {
		resolvedListRefreshMu.Unlock()
		return
	}
	resolvedListRefreshing[key] = struct{}{}
	resolvedListRefreshMu.Unlock()

	go func() {
		defer func() {
			resolvedListRefreshMu.Lock()
			delete(resolvedListRefreshing, key)
			resolvedListRefreshMu.Unlock()
		}()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("resolved list cache refresh panicked", "key_hash", resolvedListLogKey(key), "panic", r)
			}
		}()

		ctx, cancel := context.WithTimeout(context.Background(), resolvedListBuildTimeout)
		defer cancel()

		items, total, err := loader(ctx)
		if err != nil {
			slog.Warn("resolved list cache refresh failed", "key_hash", resolvedListLogKey(key), "error", err)
			return
		}
		// A transiently empty refresh preserves the existing (stale-but-usable)
		// entry rather than overwriting a good rail with nothing.
		if len(items) == 0 {
			return
		}
		resolvedListSet(key, items, total, now)
	}()
}

func resolvedListGet(key string) (resolvedListEntry, bool) {
	resolvedListCacheMu.RLock()
	entry, ok := resolvedListCache[key]
	resolvedListCacheMu.RUnlock()
	return entry, ok
}

func resolvedListSet(key string, items []*models.MediaItem, total int, now time.Time) {
	resolvedListCacheMu.Lock()
	pruneExpiredResolvedListEntriesLocked(now)
	resolvedListCache[key] = resolvedListEntry{
		items:        cloneMediaItems(items),
		total:        total,
		builtAt:      now,
		refreshAfter: now.Add(resolvedListRefreshAfter),
		expiresAt:    now.Add(resolvedListTTL),
	}
	resolvedListCacheMu.Unlock()
}

// pruneExpiredResolvedListEntriesLocked sweeps expired entries at most once per
// resolvedListPruneInterval. The caller must hold resolvedListCacheMu. Keys for
// scopes that are never looked up again would otherwise remain forever, so this
// bounds the map to roughly the set of scopes seen within one TTL window.
func pruneExpiredResolvedListEntriesLocked(now time.Time) {
	if !resolvedListLastPrune.IsZero() && now.Sub(resolvedListLastPrune) < resolvedListPruneInterval {
		return
	}
	for k, entry := range resolvedListCache {
		if !now.Before(entry.expiresAt) {
			delete(resolvedListCache, k)
		}
	}
	resolvedListLastPrune = now
}

// cloneMediaItems returns a shallow copy of the slice: it protects the cached
// entry against reordering/appending/truncating (which the diversity and
// seasonal filters do), but NOT against in-place mutation of a pointed-to
// *models.MediaItem. That is safe because the native overlay
// (buildSectionsResponse) only reads item fields — presign/user-state/images
// land in separate maps. Any future consumer that mutates item fields in place
// must instead take a deep struct copy here.
func cloneMediaItems(items []*models.MediaItem) []*models.MediaItem {
	if items == nil {
		return nil
	}
	return append([]*models.MediaItem(nil), items...)
}

// resolvedListCacheKey builds the access-scope cache key. It is security
// critical: it must capture every access boundary and nothing that is per-user.
//
// Keying is identity-independent: an entry is keyed by what fully determines the
// shared membership and ordering — the section TYPE, its CONFIG (hashed), the
// requested ItemLimit, and the full access scope (library scope + rating +
// excluded types + content allow-list + name prefix). It deliberately EXCLUDES
// resolved.ID. Every cacheable section type derives its membership purely from
// TYPE + CONFIG + limit + scope: none of the cacheable fetch helpers
// (recently_added / recently_released, genre / custom_filter,
// critically_acclaimed, award_winners, format_showcase, seasonal_themed,
// mood_collection, trending_on_server, new_to_library, most_watched,
// trending_discover, admin_curated_list, or the library-collection path) reads
// the section's own ID to determine which items it contains — the sole s.ID read
// lives in the non-cacheable user-collection branch. Dropping the arbitrary ID
// lets two sections that share type+config+limit+scope collapse to ONE shared
// entry: e.g. a natively configured "recently added" library rail and the
// jellyfin-compat /Items/Latest for that same library are built once and reused
// across both surfaces.
//
// userID/profileID are still excluded so entries are shared across everyone with
// the same access; the per-user overlay is always recomputed downstream, so a
// cached entry never leaks one profile's state to another.
func resolvedListCacheKey(resolved ResolvedSection, libraryID *int, libraryIDs []int, filter catalog.AccessFilter) string {
	var b strings.Builder
	b.WriteString("type=")
	b.WriteString(string(resolved.SectionType))

	b.WriteString("|config=")
	b.WriteString(hashSectionConfig(resolved.Config))

	b.WriteString("|limit=")
	b.WriteString(strconv.Itoa(resolved.ItemLimit))

	b.WriteString("|library=")
	if libraryID == nil {
		b.WriteString("all")
	} else {
		b.WriteString(strconv.Itoa(*libraryID))
	}

	b.WriteString("|libraries=")
	writeOptionalSortedInts(&b, libraryIDs)

	// Every access boundary (library scope, rating, excluded types, content
	// allow-list, name prefix) is serialized by the shared catalog helper so
	// this cache can never drift from the other access-scoped caches when
	// AccessFilter grows a new boundary field.
	filter.WriteAccessScopeCacheKey(&b)

	return b.String()
}

func hashSectionConfig(config json.RawMessage) string {
	if len(config) == 0 {
		return "none"
	}
	// Canonicalize before hashing so semantically identical configs that differ
	// only in whitespace or field order share a cache entry (the whole point of
	// keying native and jellycompat fetches to the same rail). Fall back to the
	// raw bytes when the config isn't valid JSON.
	if canonical, err := canonicalJSON(config); err == nil {
		config = canonical
	}
	sum := sha256.Sum256(config)
	return hex.EncodeToString(sum[:])
}

// canonicalJSON returns a stable encoding of raw by decoding and re-marshaling
// it, so object key order and insignificant whitespace no longer affect the
// bytes. encoding/json marshals map keys in sorted order, giving a deterministic
// form.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}

// resolvedListLogKey returns a short digest of a cache key for logging. The raw
// key embeds user-controlled access-scope fields (e.g. NamePrefix), so only the
// digest is emitted to logs, not the raw input.
func resolvedListLogKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// isCacheableSectionType reports whether a resolved section's shared item list
// may be cached. Cacheability is derived from userAgnosticSectionFetcher — the
// same table fetchSection dispatches through — so the "may this row be shared
// across profiles?" decision lives in exactly one place and cannot drift from
// the fetch implementation. Random is excluded from that table so its
// per-request shuffle is preserved; per-user rows (continue watching, next-up,
// recommendations, hidden gems, forgotten favorites, activity feed) have no
// shared base; user collections are profile-scoped and excluded via the
// UserCollectionID check.
func (f *Fetcher) isCacheableSectionType(resolved ResolvedSection) bool {
	if f.userAgnosticSectionFetcher(resolved.SectionType) != nil {
		return true
	}
	switch resolved.SectionType {
	case SectionGenre, SectionCustomFilter:
		// These route through fetchFiltered → ParseQueryDefinition, whose
		// user-supplied QueryDefinition can carry personalized (per-profile)
		// rules/sorts (watched, favorited, in_watchlist, in_progress,
		// last_watched; sorts progress/date_viewed/plays) that inject
		// EXISTS(... user_id/profile_id ...) predicates. Such a rail's
		// membership differs per profile and must never be served from the
		// user-agnostic shared cache, whose key excludes userID/profileID.
		// Non-personalized definitions stay cacheable. Use the same parser
		// fetchFiltered uses so cacheability tracks execution exactly.
		def, err := ParseQueryDefinition(resolved.Config)
		if err != nil {
			// Unparseable config: fetchFiltered would also fail (nothing gets
			// cached), so stay off the cache path to be safe.
			return false
		}
		return !def.IsPersonalized()
	case SectionCollection:
		// Only library collections are shared; user collections are
		// profile-scoped and must never be served across profiles.
		cfg := ParseCollectionConfig(resolved.Config)
		return strings.TrimSpace(cfg.UserCollectionID) == ""
	default:
		return false
	}
}

// resetResolvedListCacheForTest clears all process-global cache state. Tests
// call it between cases so entries and in-flight refreshes never leak across.
func resetResolvedListCacheForTest() {
	resolvedListCacheMu.Lock()
	resolvedListCache = make(map[string]resolvedListEntry)
	resolvedListLastPrune = time.Time{}
	resolvedListCacheMu.Unlock()

	resolvedListRefreshMu.Lock()
	resolvedListRefreshing = make(map[string]struct{})
	resolvedListRefreshMu.Unlock()
}
