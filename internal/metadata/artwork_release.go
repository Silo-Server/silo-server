package metadata

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
)

// While metadata.cache_images is off, clients load item artwork straight from
// the providers. Turning it off stops new copies, and refreshes stop keeping
// stored ones, but artwork stored earlier would stay served until each item
// happened to be refreshed. StoredArtworkReleaser moves it back:
//
//   - A slot serving a stored copy of provider artwork serves its provider
//     source again. The revision GC removes the displaced copy later, as for
//     any replaced artwork; nothing here deletes files.
//   - A matched item whose poster, backdrop or logo serves a copy with no
//     provider source (a local sidecar, an image URL an NFO names, or an old
//     copy saved without its source), that waits on a local copy that will
//     now never be made, or that keeps extra backdrops, which are local art
//     only, gets refresh debt. The scheduled refresh then fills the slot from
//     the providers; until then, and for good when no provider has an image,
//     the item keeps what it serves. An item refreshed or tried within the
//     last week is left until the week is up, so artwork no provider has
//     costs at most a refresh a week.
//
// Items with locked artwork keep everything. Banner, landscape thumb, clear
// art and disc art come only from local files, so their stored copies stay.
// Movies and series only: book libraries are beta and store their covers on
// their own terms.
//
// The pass is idempotent and runs daily while caching is off, so a copy a job
// still in flight publishes after a pass, or a node that had not reloaded the
// setting yet, is undone by the next pass. It pages only through rows with
// something to do. Every write re-checks the stored setting, so a pass still
// running when caching comes back on changes nothing more.
type StoredArtworkReleaser struct {
	pool     *pgxpool.Pool
	debts    *RefreshDebtRepository
	pageSize int
	// lock takes the pass's cluster-wide lock; acquired is false when
	// another server holds it.
	lock func(ctx context.Context) (release func(), acquired bool, err error)
}

// StoredArtworkReleaseStats summarizes one pass.
type StoredArtworkReleaseStats struct {
	// Released counts rows that went back to their provider source.
	Released int `json:"released"`
	// RefreshesQueued counts items given refresh debt.
	RefreshesQueued int `json:"refreshes_queued"`
	// Skipped is set when another server holds the pass.
	Skipped bool `json:"skipped"`
}

// storedArtworkReleaseAdvisoryLock keeps one pass running across servers.
const storedArtworkReleaseAdvisoryLock int64 = 0x53494C4F52454C53 // "SILORELS"

const (
	storedArtworkReleasePageSize = 1000
	// storedArtworkReleaseRefreshAge is how long an item's last refresh holds
	// off new refresh debt for its artwork.
	storedArtworkReleaseRefreshAge = 7 * 24 * time.Hour
	// storedArtworkReleaseDebtPriority is the default debt band.
	storedArtworkReleaseDebtPriority = 100
)

// cachingOffSQL holds when the stored metadata.cache_images is off. It
// mirrors the setting's parsing: strconv.ParseBool, and on when unset.
const cachingOffSQL = `lower(coalesce((SELECT value FROM server_settings WHERE key = 'metadata.cache_images'), '')) IN ('0', 'f', 'false')`

// imagesLockedSQL holds when the media_items row aliased alias has FieldImages
// locked.
func imagesLockedSQL(alias string) string {
	return fmt.Sprintf("%d = ANY(%s.locked_fields)", int(FieldImages), alias)
}

func storedCopySQL(pathCol string) string {
	return fmt.Sprintf(`(coalesce(%[1]s, '') NOT IN ('', '-') AND %[1]s NOT LIKE '%%://%%')`, pathCol)
}

func providerSourceSQL(sourceCol string) string {
	return fmt.Sprintf(`(coalesce(%[1]s, '') LIKE '%%://%%' AND lower(%[1]s) NOT LIKE ALL (%[2]s))`, sourceCol, nonProviderImageSchemesSQL)
}

func localSourceSQL(sourceCol string) string {
	return fmt.Sprintf(`(lower(coalesce(%[1]s, '')) LIKE 'file://%%' OR lower(coalesce(%[1]s, '')) LIKE 'nfo+%%')`, sourceCol)
}

// releasableSQL holds when a slot serves a stored copy of provider artwork.
func releasableSQL(pathCol, sourceCol string) string {
	return "(" + storedCopySQL(pathCol) + " AND " + providerSourceSQL(sourceCol) + ")"
}

// sourcelessCopySQL holds when a slot serves a stored copy saved without its
// source, as copies made before source columns existed were.
func sourcelessCopySQL(pathCol, sourceCol string) string {
	return "(" + storedCopySQL(pathCol) + " AND coalesce(" + sourceCol + ", '') = '')"
}

// needsProviderArtworkSQL holds when only a refresh can give a slot provider
// artwork: it serves a copy with no provider source, or it is empty and waits
// on a local copy.
func needsProviderArtworkSQL(pathCol, sourceCol string) string {
	return fmt.Sprintf("((%s AND NOT %s) OR (coalesce(%s, '') = '' AND %s))",
		storedCopySQL(pathCol), providerSourceSQL(sourceCol), pathCol, localSourceSQL(sourceCol))
}

// releaseSet returns SET assignments that release each slot of the row
// aliased alias that qualifies. The slot keeps its thumbhash: clients show it
// as a placeholder, and the provider image it now serves is the one the copy
// was made from, or, when a refresh moved the source on, a newer pick that
// replaces the placeholder as soon as it loads.
//
// With clearSourceless, a stored copy with no source at all is cleared: a
// localization row's empty slot falls back to the item's or season's own
// artwork, which this pass and refreshes already move to the providers.
func releaseSet(alias string, slots []artworkReleaseSlot, clearSourceless bool) string {
	parts := make([]string, 0, len(slots)+1)
	for _, slot := range slots {
		path, source := alias+"."+slot.path, alias+"."+slot.source
		clear := ""
		if clearSourceless {
			clear = fmt.Sprintf(" WHEN %s THEN ''", sourcelessCopySQL(path, source))
		}
		parts = append(parts, fmt.Sprintf("%s = CASE WHEN %s THEN %s%s ELSE %s END", slot.path, releasableSQL(path, source), source, clear, path))
	}
	parts = append(parts, "updated_at = NOW()")
	return strings.Join(parts, ", ")
}

func anyReleasable(alias string, slots []artworkReleaseSlot, clearSourceless bool) string {
	conds := make([]string, 0, 2*len(slots))
	for _, slot := range slots {
		path, source := alias+"."+slot.path, alias+"."+slot.source
		conds = append(conds, releasableSQL(path, source))
		if clearSourceless {
			conds = append(conds, sourcelessCopySQL(path, source))
		}
	}
	return "(" + strings.Join(conds, " OR ") + ")"
}

type artworkReleaseSlot struct{ path, source string }

// releaseSlot names a slot's columns from their base name.
func releaseSlot(base string) artworkReleaseSlot {
	return artworkReleaseSlot{path: base + "_path", source: base + "_source_path"}
}

var (
	itemLocalizationReleaseSlots = []artworkReleaseSlot{
		releaseSlot("poster"), releaseSlot("backdrop"), releaseSlot("logo"),
	}
	itemReleaseSlots = append(slices.Clone(itemLocalizationReleaseSlots),
		releaseSlot("banner"), releaseSlot("thumb"), releaseSlot("clearart"), releaseSlot("disc"))
	seasonReleaseSlots  = []artworkReleaseSlot{releaseSlot("poster")}
	episodeReleaseSlots = []artworkReleaseSlot{releaseSlot("still")}
)

const (
	releaseItemOwnerSQL   = "mi.content_id = t.content_id"
	releaseSeriesOwnerSQL = "mi.content_id = t.series_id"

	releaseItemLocalizationsTable = "media_item_localizations"
	releaseEpisodesTable          = "episodes"
)

// artworkReleaseTable is one table the pass pages through by content_id.
// owner joins the media_items row mi, whose type and lock decide, to a row t.
type artworkReleaseTable struct {
	table, owner string
	slots        []artworkReleaseSlot
	// localized, when set, is the table of t's localizations, keyed to t by
	// localizedKey; every language's row is updated.
	localized, localizedKey string
	// needsRefresh, when set, selects the rows t that only a refresh can give
	// provider artwork.
	needsRefresh string
}

// candidateSQL holds for a row t with a slot to release or that only a
// refresh can fix. Paging only through such rows keeps a pass with nothing to
// do to one ordered read of each table.
func (t artworkReleaseTable) candidateSQL() string {
	cond := anyReleasable("t", t.slots, false)
	if t.localized != "" {
		cond = fmt.Sprintf("EXISTS (SELECT 1 FROM %s l WHERE l.%s = t.content_id AND %s)",
			t.localized, t.localizedKey, anyReleasable("l", t.slots, true))
	}
	if t.needsRefresh != "" {
		cond = "(" + cond + " OR " + t.needsRefresh + ")"
	}
	return cond
}

func (t artworkReleaseTable) name() string {
	if t.localized != "" {
		return t.localized
	}
	return t.table
}

func storedArtworkReleaseTables() []artworkReleaseTable {
	needs := func(slots ...artworkReleaseSlot) string {
		conds := make([]string, len(slots))
		for i, slot := range slots {
			conds[i] = needsProviderArtworkSQL("t."+slot.path, "t."+slot.source)
		}
		return strings.Join(conds, " OR ")
	}
	const items, seasons = "media_items", "seasons"
	return []artworkReleaseTable{
		{
			table: items, owner: releaseItemOwnerSQL, slots: itemReleaseSlots,
			needsRefresh: "(" + needs(itemLocalizationReleaseSlots...) +
				" OR EXISTS (SELECT 1 FROM media_item_backdrops b WHERE b.content_id = t.content_id)" +
				// Storage recovery clears a missing copy it cannot re-download
				// to empty slot and source and nulls last_refreshed.
				" OR (t.last_refreshed IS NULL AND (" + emptySlotSQL("t", itemLocalizationReleaseSlots) + ")))",
		},
		{
			table: items, owner: releaseItemOwnerSQL, slots: itemLocalizationReleaseSlots,
			localized: releaseItemLocalizationsTable, localizedKey: "content_id",
		},
		{table: seasons, owner: releaseSeriesOwnerSQL, slots: seasonReleaseSlots, needsRefresh: needs(seasonReleaseSlots...)},
		{
			table: seasons, owner: releaseSeriesOwnerSQL, slots: seasonReleaseSlots,
			localized: "season_localizations", localizedKey: "season_content_id",
		},
		{table: releaseEpisodesTable, owner: releaseSeriesOwnerSQL, slots: episodeReleaseSlots, needsRefresh: needs(episodeReleaseSlots...)},
	}
}

// emptySlotSQL holds when any slot of the row aliased alias has neither a path
// nor a source.
func emptySlotSQL(alias string, slots []artworkReleaseSlot) string {
	conds := make([]string, len(slots))
	for i, slot := range slots {
		conds[i] = fmt.Sprintf("(coalesce(%[1]s.%[2]s, '') = '' AND coalesce(%[1]s.%[3]s, '') = '')", alias, slot.path, slot.source)
	}
	return strings.Join(conds, " OR ")
}

// NewStoredArtworkReleaser returns nil without a database.
func NewStoredArtworkReleaser(pool *pgxpool.Pool) *StoredArtworkReleaser {
	if pool == nil {
		return nil
	}
	return &StoredArtworkReleaser{
		pool:     pool,
		debts:    NewRefreshDebtRepository(pool),
		pageSize: storedArtworkReleasePageSize,
		lock: func(ctx context.Context) (func(), bool, error) {
			lock, acquired, err := pglock.TryAcquire(ctx, pool, storedArtworkReleaseAdvisoryLock)
			if err != nil || !acquired {
				return nil, false, err
			}
			return func() {
				if err := lock.Release(context.WithoutCancel(ctx)); err != nil {
					slog.WarnContext(ctx, "stored artwork release: releasing lock failed", "component", "metadata", "error", err)
				}
			}, true, nil
		},
	}
}

// CachingOff reports whether the stored metadata.cache_images is off.
func (r *StoredArtworkReleaser) CachingOff(ctx context.Context) (bool, error) {
	if r == nil {
		return false, nil
	}
	var off bool
	if err := r.pool.QueryRow(ctx, "SELECT "+cachingOffSQL).Scan(&off); err != nil {
		return false, fmt.Errorf("reading metadata.cache_images: %w", err)
	}
	return off, nil
}

// Run makes one pass over every table. It returns early, with what it did,
// once caching is back on.
func (r *StoredArtworkReleaser) Run(ctx context.Context) (StoredArtworkReleaseStats, error) {
	var stats StoredArtworkReleaseStats
	if r == nil {
		return stats, nil
	}
	release, acquired, err := r.lock(ctx)
	if err != nil {
		return stats, fmt.Errorf("stored artwork release: acquiring lock: %w", err)
	}
	if !acquired {
		stats.Skipped = true
		return stats, nil
	}
	defer release()

	for _, table := range storedArtworkReleaseTables() {
		if err := r.releaseTable(ctx, table, &stats); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (r *StoredArtworkReleaser) releaseTable(ctx context.Context, table artworkReleaseTable, stats *StoredArtworkReleaseStats) error {
	cursor := ""
	for {
		if off, err := r.CachingOff(ctx); err != nil || !off {
			return err
		}
		keys, err := r.queryStrings(ctx,
			"SELECT content_id FROM "+table.table+" t WHERE content_id > $1 AND "+table.candidateSQL()+
				" ORDER BY content_id LIMIT $2",
			cursor, r.pageSize)
		if err != nil {
			return fmt.Errorf("stored artwork release: paging %s: %w", table.name(), err)
		}
		if len(keys) == 0 {
			return nil
		}
		cursor = keys[len(keys)-1]

		released, err := r.releasePage(ctx, table, keys)
		if err != nil {
			return err
		}
		stats.Released += released
		if table.needsRefresh != "" {
			queued, err := r.queueRefreshes(ctx, table, keys)
			if err != nil {
				return err
			}
			stats.RefreshesQueued += queued
		}
		if len(keys) < r.pageSize {
			return nil
		}
	}
}

// ownerScopeSQL limits rows to unlocked movies and series, while caching is
// off.
func ownerScopeSQL(table artworkReleaseTable) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM media_items mi WHERE %s AND mi.type IN ('movie', 'series') AND NOT %s) AND %s`,
		table.owner, imagesLockedSQL("mi"), cachingOffSQL)
}

// releasePage releases a page's slots. An Edit Metadata selection writes the
// artwork and locks it in one transaction, and an UPDATE that waited on that
// row would judge the lock from the snapshot it started with and release the
// selection. So the page's rows are locked first, in the order a selection
// takes them (the target row, then the item it locks), each in a statement of
// its own, and the UPDATE then reads the committed lock. A deadlock with
// another writer retries the page.
func (r *StoredArtworkReleaser) releasePage(ctx context.Context, table artworkReleaseTable, keys []string) (released int, err error) {
	var query string
	if table.localized != "" {
		query = fmt.Sprintf(`
			UPDATE %[1]s l SET %[2]s
			FROM %[3]s t
			WHERE t.content_id = ANY($1) AND l.%[4]s = t.content_id
			  AND %[5]s AND %[6]s`,
			table.localized, releaseSet("l", table.slots, true), table.table, table.localizedKey,
			anyReleasable("l", table.slots, true), ownerScopeSQL(table))
	} else {
		query = fmt.Sprintf(`
			UPDATE %[1]s t SET %[2]s
			WHERE t.content_id = ANY($1) AND %[3]s AND %[4]s`,
			table.table, releaseSet("t", table.slots, false), anyReleasable("t", table.slots, false), ownerScopeSQL(table))
	}
	err = retryOnDeadlock(ctx, func() error {
		return pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
			for _, lock := range []string{
				fmt.Sprintf(`SELECT 1 FROM %s t WHERE t.content_id = ANY($1) ORDER BY t.content_id FOR UPDATE`, table.table),
				fmt.Sprintf(`
					SELECT 1 FROM media_items mi
					WHERE EXISTS (SELECT 1 FROM %s t WHERE t.content_id = ANY($1) AND %s)
					ORDER BY mi.content_id
					FOR SHARE`, table.table, table.owner),
			} {
				if _, err := tx.Exec(ctx, lock, keys); err != nil {
					return err
				}
			}
			tag, err := tx.Exec(ctx, query, keys)
			released = int(tag.RowsAffected())
			return err
		})
	})
	if err != nil {
		return 0, fmt.Errorf("stored artwork release: releasing %s: %w", table.name(), err)
	}
	return released, nil
}

func (r *StoredArtworkReleaser) queryStrings(ctx context.Context, sql string, args ...any) ([]string, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func (r *StoredArtworkReleaser) queueRefreshes(ctx context.Context, table artworkReleaseTable, keys []string) (int, error) {
	ids, err := r.queryStrings(ctx, fmt.Sprintf(`
		SELECT DISTINCT mi.content_id
		FROM %[1]s t
		JOIN media_items mi ON %[2]s
		WHERE t.content_id = ANY($1) AND %[3]s
		  AND mi.type IN ('movie', 'series') AND mi.status = 'matched' AND NOT %[4]s
		  AND (mi.last_refreshed IS NULL OR mi.last_refreshed < NOW() - $2::interval)
		  AND %[5]s`,
		table.table, table.owner, table.needsRefresh, imagesLockedSQL("mi"), cachingOffSQL),
		keys, intervalLiteral(storedArtworkReleaseRefreshAge))
	if err != nil {
		return 0, fmt.Errorf("stored artwork release: finding %s to refresh: %w", table.name(), err)
	}
	if err := r.debts.RequestDueMany(ctx, RefreshTargetItem, ids, storedArtworkReleaseDebtPriority,
		RefreshDebtReasonProviderArtwork, time.Now().UTC(), storedArtworkReleaseRefreshAge); err != nil {
		return 0, err
	}
	return len(ids), nil
}
