package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"

	"github.com/Silo-Server/silo-server/internal/database/pglock"
	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
)

type SearchIndexProgressReporter interface {
	Report(percent float64, message string)
	SetResultData(data json.RawMessage)
}

type CatalogSearchIndexSyncStats struct {
	Configured       bool   `json:"configured"`
	Skipped          bool   `json:"skipped"`
	Reason           string `json:"reason,omitempty"`
	RebuildAttempted bool   `json:"rebuild_attempted"`
	Rebuilt          bool   `json:"rebuilt"`
	Events           int    `json:"events"`
	Upserted         int    `json:"upserted"`
	Deleted          int    `json:"deleted"`
	ActiveIndexUID   string `json:"active_index_uid,omitempty"`
	DocumentCount    int    `json:"document_count"`
	VectorDocCount   int    `json:"vector_document_count"`
	RemovedIndexes   int    `json:"removed_indexes"`
	LastProcessedID  int64  `json:"last_processed_event_id,omitempty"`
}

type CatalogSearchIndexRebuildStats struct {
	Configured     bool   `json:"configured"`
	Skipped        bool   `json:"skipped"`
	Reason         string `json:"reason,omitempty"`
	ActiveIndexUID string `json:"active_index_uid,omitempty"`
	DocumentCount  int    `json:"document_count"`
	VectorDocCount int    `json:"vector_document_count"`
	RemovedIndexes int    `json:"removed_indexes"`
}

type queuedMeilisearchTask struct {
	task     meilisearchTaskRef
	docCount int
	vecCount int
}

// meilisearchMaxDocumentPayloadBytes caps one documents request body, as
// encoded (see forEachCatalogSearchDocumentPayload). Meilisearch rejects a
// body over its http-payload-size-limit, 100 MB (100,000,000 bytes) by
// default; 80 MiB stays under it.
const meilisearchMaxDocumentPayloadBytes = 80 * 1024 * 1024

// meilisearchIndexingTimeout bounds a single indexing HTTP call (document
// batches run up to meilisearchMaxDocumentPayloadBytes). It is deliberately
// independent of catalog.search.meilisearch.timeout_ms: that setting protects
// the interactive search hot path and defaults to 800ms, which is far too
// tight to upload a multi-megabyte rebuild batch to a non-loopback
// Meilisearch — and raising it to make indexing work would loosen search
// fallback latency at the same time.
const meilisearchIndexingTimeout = 2 * time.Minute

// catalogSearchExcludeMangaChaptersSQL excludes per-chapter manga rows from
// catalog search documents and semantic coverage counts; chapters are reached
// through their parent series and would otherwise flood the index. The
// predicate expects media_items to be aliased as `mi`.
const catalogSearchExcludeMangaChaptersSQL = `NOT EXISTS (SELECT 1 FROM manga_chapters mc WHERE mc.chapter_content_id = mi.content_id)`

type CatalogSearchIndexer struct {
	pool          *pgxpool.Pool
	settingsStore SettingsStore
	runtime       *CatalogSearchSettings
	events        *SearchIndexEventRepository
	// models names the locked embedding model. Documents carry only vectors
	// of that model, the ones the coverage gate counts and query vectors are
	// compared with. Nil indexes every stored vector.
	models CatalogSemanticModelProvider
}

func NewCatalogSearchIndexer(pool *pgxpool.Pool, settingsStore SettingsStore) *CatalogSearchIndexer {
	return &CatalogSearchIndexer{
		pool:          pool,
		settingsStore: settingsStore,
		events:        NewSearchIndexEventRepository(pool),
	}
}

// NewCatalogSearchIndexerFromSettings binds scheduled and manual maintenance
// to the same process-lifetime settings snapshot as the serving search
// provider. Catalog search settings are restart-bound; rereading saved values
// on the minute interval could otherwise publish an index the live provider
// does not understand before the server restarts.
func NewCatalogSearchIndexerFromSettings(pool *pgxpool.Pool, settingsStore SettingsStore, settings CatalogSearchSettings) *CatalogSearchIndexer {
	settings.IndexTypes = slices.Clone(settings.IndexTypes)
	return &CatalogSearchIndexer{
		pool:          pool,
		settingsStore: settingsStore,
		runtime:       new(settings),
		events:        NewSearchIndexEventRepository(pool),
	}
}

// WithSemanticModelProvider indexes only vectors of the embedding model
// models reports, the same provider the semantic coverage gate uses, and
// returns the indexer.
func (i *CatalogSearchIndexer) WithSemanticModelProvider(models CatalogSemanticModelProvider) *CatalogSearchIndexer {
	if i != nil {
		i.models = models
	}
	return i
}

func (i *CatalogSearchIndexer) ShouldSyncRun(ctx context.Context) (bool, error) {
	settings, ok, err := i.loadMeilisearchRuntime(ctx)
	if err != nil || !ok || settings.Provider != SearchProviderMeilisearch {
		return false, err
	}
	client, err := newMeilisearchClient(settings.MeilisearchURL, settings.MeilisearchAPIKey, settings.Timeout)
	if err != nil {
		return false, err
	}
	state, err := i.events.GetState(ctx, SearchProviderMeilisearch)
	if err != nil {
		return false, err
	}
	maintenance, err := planCatalogSearchIndexMaintenance(ctx, client, state, settings)
	if err != nil {
		return false, err
	}
	switch maintenance {
	case catalogSearchIndexRebuild:
		if catalogSearchIndexHasNewerSchema(state) {
			return false, nil
		}
		matches, err := i.runtimeMatchesDesiredSettings(ctx, settings)
		if err != nil || !matches {
			return false, err
		}
		return true, nil
	case catalogSearchIndexApplySettings:
		return true, nil
	}
	pending, err := i.events.PendingCount(ctx, SearchProviderMeilisearch)
	if err != nil {
		return false, err
	}
	return pending > 0, nil
}

// SyncOutbox brings the active index up to date: it rebuilds an index that
// needs it, otherwise applies changed index settings in place, then syncs
// pending catalog events.
func (i *CatalogSearchIndexer) SyncOutbox(ctx context.Context, progress SearchIndexProgressReporter) (CatalogSearchIndexSyncStats, error) {
	stats := CatalogSearchIndexSyncStats{}
	settings, client, ok, err := i.loadClient(ctx)
	if err != nil {
		return stats, err
	}
	if !ok {
		stats.Skipped = true
		stats.Reason = "meilisearch is not configured"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "Meilisearch is not configured")
		return stats, nil
	}
	stats.Configured = true

	lock, locked, err := pglock.TryAcquire(ctx, i.pool, searchIndexMaintenanceLockID)
	if err != nil {
		return stats, err
	}
	if !locked {
		stats.Skipped = true
		stats.Reason = "another search index maintenance task is running"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "Another catalog search index maintenance task is already running")
		return stats, nil
	}
	defer func() {
		if err := lock.Release(ctx); err != nil {
			slog.WarnContext(ctx, "releasing catalog search index maintenance lock",
				"component", "catalog", "error", err)
		}
	}()

	state, err := i.events.GetState(ctx, SearchProviderMeilisearch)
	if err != nil {
		return stats, err
	}
	maintenance, err := planCatalogSearchIndexMaintenance(ctx, client, state, settings)
	if err != nil {
		return stats, err
	}
	if maintenance == catalogSearchIndexRebuild {
		return i.rebuildForSync(ctx, progress, settings, client, state, stats)
	}
	var settingsErr error
	if maintenance == catalogSearchIndexApplySettings {
		// A failure leaves the recorded fingerprint, so the next run retries.
		// Pending events still sync: the index keeps serving searches with
		// its current settings meanwhile. The task still reports the failure.
		reportSearchIndexProgress(progress, 0, "Applying catalog search index settings")
		if settingsErr = i.applyIndexSettingsLocked(ctx, client, state.ActiveIndexUID); settingsErr != nil {
			slog.WarnContext(ctx, "catalog search: failed to apply index settings in place; the next sync retries",
				"component", "catalog", "index", state.ActiveIndexUID, "err", settingsErr)
		}
	}
	stats, err = i.syncPendingEventsLocked(ctx, progress, settings, client, state, stats)
	if err == nil && settingsErr != nil {
		err = fmt.Errorf("applying catalog search index settings: %w", settingsErr)
	}
	return stats, err
}

// rebuildForSync rebuilds the index for SyncOutbox unless a newer server owns
// it or the saved search settings wait for a restart.
func (i *CatalogSearchIndexer) rebuildForSync(ctx context.Context, progress SearchIndexProgressReporter, settings CatalogSearchSettings, client *meilisearchClient, state SearchIndexState, stats CatalogSearchIndexSyncStats) (CatalogSearchIndexSyncStats, error) {
	if catalogSearchIndexHasNewerSchema(state) {
		stats.Skipped = true
		stats.Reason = "active search index was built by a newer server version"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "A newer server version owns the active search index")
		return stats, nil
	}
	matches, matchErr := i.runtimeMatchesDesiredSettings(ctx, settings)
	if matchErr != nil {
		return stats, matchErr
	}
	if !matches {
		stats.Skipped = true
		stats.Reason = "saved search settings are waiting for a server restart"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "Restart Silo before rebuilding the catalog search index")
		return stats, nil
	}
	stats.RebuildAttempted = true
	rebuildStats, rebuildErr := i.rebuildLocked(ctx, progress, settings, client)
	stats.Rebuilt = !rebuildStats.Skipped && rebuildErr == nil
	stats.Skipped = rebuildStats.Skipped
	stats.Reason = rebuildStats.Reason
	stats.ActiveIndexUID = rebuildStats.ActiveIndexUID
	stats.DocumentCount = rebuildStats.DocumentCount
	stats.VectorDocCount = rebuildStats.VectorDocCount
	stats.RemovedIndexes = rebuildStats.RemovedIndexes
	setSearchIndexTaskResult(progress, stats)
	return stats, rebuildErr
}

// syncPendingEventsLocked applies one batch of pending catalog events to the
// active index. The caller must hold searchIndexMaintenanceLockID.
func (i *CatalogSearchIndexer) syncPendingEventsLocked(ctx context.Context, progress SearchIndexProgressReporter, settings CatalogSearchSettings, client *meilisearchClient, state SearchIndexState, stats CatalogSearchIndexSyncStats) (CatalogSearchIndexSyncStats, error) {
	stats.ActiveIndexUID = state.ActiveIndexUID

	reportSearchIndexProgress(progress, 0, "Loading pending catalog search events")
	events, err := i.events.ListPending(ctx, SearchProviderMeilisearch, settings.SyncBatchSize)
	if err != nil {
		return stats, err
	}
	if len(events) == 0 {
		stats.DocumentCount = state.DocumentCount
		if vectorCount, err := i.countIndexedVectors(ctx, settings.IndexTypes); err == nil {
			stats.VectorDocCount = vectorCount
		}
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "Catalog search index is already current")
		return stats, nil
	}
	stats.Events = len(events)
	ids := make([]int64, 0, len(events))
	maxID := int64(0)
	for _, event := range events {
		ids = append(ids, event.ID)
		if event.ID > maxID {
			maxID = event.ID
		}
	}

	upsertIDs, deleteIDs := coalesceSearchIndexEvents(events)
	reportSearchIndexProgress(progress, 20, "Building changed catalog search documents")
	docs, err := i.LoadDocumentsByIDs(ctx, upsertIDs, settings.IndexTypes, settings.Embedder, settings.SemanticEnabled, settings.BinaryQuantized)
	if err != nil {
		_ = i.events.MarkFailed(ctx, ids, err)
		return stats, err
	}
	found := make(map[string]struct{}, len(docs))
	for _, doc := range docs {
		found[doc.ContentID] = struct{}{}
	}
	for _, id := range upsertIDs {
		if _, ok := found[id]; !ok {
			deleteIDs = append(deleteIDs, id)
		}
	}
	deleteIDs = compactNonEmptyStrings(deleteIDs)

	if len(deleteIDs) > 0 {
		reportSearchIndexProgress(progress, 45, "Deleting stale catalog search documents")
		taskID, err := client.DeleteDocuments(ctx, state.ActiveIndexUID, deleteIDs)
		if err != nil {
			_ = i.events.MarkFailed(ctx, ids, err)
			return stats, err
		}
		if err := client.WaitTask(ctx, taskID); err != nil {
			_ = i.events.MarkFailed(ctx, ids, err)
			return stats, err
		}
		stats.Deleted = len(deleteIDs)
	}
	if len(docs) > 0 {
		reportSearchIndexProgress(progress, 70, "Upserting catalog search documents")
		err := forEachCatalogSearchDocumentPayload(docs, meilisearchMaxDocumentPayloadBytes, func(payload catalogSearchDocumentPayload) error {
			taskID, err := client.AddDocuments(ctx, state.ActiveIndexUID, payload.body)
			if err != nil {
				return err
			}
			if err := client.WaitTask(ctx, taskID); err != nil {
				return err
			}
			stats.Upserted += len(payload.docs)
			return nil
		})
		if err != nil {
			_ = i.events.MarkFailed(ctx, ids, err)
			return stats, err
		}
	}

	docCount, err := client.Stats(ctx, state.ActiveIndexUID)
	if err != nil {
		_ = i.events.MarkFailed(ctx, ids, err)
		return stats, err
	}
	stats.DocumentCount = docCount
	if vectorCount, err := i.countIndexedVectors(ctx, settings.IndexTypes); err == nil {
		stats.VectorDocCount = vectorCount
	}
	stats.LastProcessedID = maxID
	if err := i.events.MarkProcessed(ctx, ids); err != nil {
		return stats, err
	}
	if err := i.events.UpdateStateAfterSync(ctx, SearchProviderMeilisearch, maxID, docCount); err != nil {
		return stats, err
	}
	setSearchIndexTaskResult(progress, stats)
	reportSearchIndexProgress(progress, 100, fmt.Sprintf("Synced %d catalog search events", len(events)))
	return stats, nil
}

// catalogSearchIndexMaintenance is what the sync task must do to the active
// index before it syncs pending events.
type catalogSearchIndexMaintenance uint8

const (
	// catalogSearchIndexCurrentSettings: nothing; the index is current.
	catalogSearchIndexCurrentSettings catalogSearchIndexMaintenance = iota
	// catalogSearchIndexApplySettings: the index has the current schema but
	// was last brought to other index settings; apply them in place.
	catalogSearchIndexApplySettings
	// catalogSearchIndexRebuild: the index is missing or has another schema
	// identity; build a new one from PostgreSQL. A rebuild applies the
	// current settings, so it never also needs an in-place update.
	catalogSearchIndexRebuild
)

func planCatalogSearchIndexMaintenance(
	ctx context.Context,
	client *meilisearchClient,
	state SearchIndexState,
	settings CatalogSearchSettings,
) (catalogSearchIndexMaintenance, error) {
	rebuildRequired, err := catalogSearchIndexRequiresRebuildOnTarget(
		ctx,
		client,
		state,
		catalogSearchMeilisearchSchemaVersion(settings.Embedder, settings.IndexTypes, settings.SemanticEnabled, settings.BinaryQuantized),
		settings.MeilisearchIndex,
	)
	switch {
	case err != nil:
		return 0, err
	case rebuildRequired:
		return catalogSearchIndexRebuild, nil
	case state.SettingsFingerprint != catalogSearchMeilisearchSettingsFingerprint:
		return catalogSearchIndexApplySettings, nil
	default:
		return catalogSearchIndexCurrentSettings, nil
	}
}

// applyIndexSettingsLocked applies catalogSearchMeilisearchIndexSettings to
// the active index, waits for Meilisearch to finish, and records the
// fingerprint. The embedder is left alone: it is part of the schema identity.
// The caller must hold searchIndexMaintenanceLockID.
//
// Meilisearch reindexes every document in place for some settings (searchable
// attributes, typo tolerance), which on a large index takes as long as
// indexing does, so the wait is bounded like a rebuild's task waits, not by
// the request timeout. Searches keep using the index meanwhile; see
// MeilisearchSearchProvider.searchSettingsCurrent.
func (i *CatalogSearchIndexer) applyIndexSettingsLocked(ctx context.Context, client *meilisearchClient, indexUID string) error {
	task, err := client.UpdateSettings(ctx, indexUID, catalogSearchMeilisearchIndexSettings())
	if err != nil {
		return err
	}
	if err := client.WaitTask(ctx, task); err != nil {
		return err
	}
	return i.events.UpdateSettingsFingerprint(ctx, SearchProviderMeilisearch, indexUID, catalogSearchMeilisearchSettingsFingerprint)
}

func catalogSearchIndexRequiresRebuildOnTarget(
	ctx context.Context,
	client *meilisearchClient,
	state SearchIndexState,
	expectedSchemaVersion int,
	indexPrefix string,
) (bool, error) {
	if catalogSearchIndexStateRequiresRebuild(state, expectedSchemaVersion, indexPrefix) {
		return true, nil
	}
	if client == nil {
		return false, fmt.Errorf("checking active Meilisearch index %q: client is not configured", state.ActiveIndexUID)
	}
	if _, err := client.Stats(ctx, state.ActiveIndexUID); err != nil {
		if isMeilisearchIndexNotFound(err) {
			return true, nil
		}
		return false, fmt.Errorf("checking active Meilisearch index %q: %w", state.ActiveIndexUID, err)
	}
	return false, nil
}

func catalogSearchIndexStateRequiresRebuild(state SearchIndexState, expectedSchemaVersion int, indexPrefix string) bool {
	return state.ActiveIndexUID == "" ||
		state.SchemaVersion != expectedSchemaVersion ||
		!catalogSearchIndexUIDBelongsToPrefix(state.ActiveIndexUID, indexPrefix)
}

func catalogSearchIndexUIDBelongsToPrefix(uid, indexPrefix string) bool {
	uid = strings.TrimSpace(uid)
	indexPrefix = strings.TrimSpace(indexPrefix)
	if uid == "" || indexPrefix == "" {
		return false
	}
	if uid == indexPrefix {
		return true
	}
	suffix, ok := strings.CutPrefix(uid, indexPrefix+"_rebuild_")
	if !ok || suffix == "" {
		return false
	}
	timestamp, err := strconv.ParseInt(suffix, 10, 64)
	return err == nil && timestamp > 0
}

func catalogSearchIndexHasNewerSchema(state SearchIndexState) bool {
	return state.ActiveIndexUID != "" && state.SchemaVersion/1_000_000 > SearchMeilisearchSchemaVersion
}

func (i *CatalogSearchIndexer) Rebuild(ctx context.Context, progress SearchIndexProgressReporter) (CatalogSearchIndexRebuildStats, error) {
	stats := CatalogSearchIndexRebuildStats{}
	settings, client, ok, err := i.loadClient(ctx)
	if err != nil {
		return stats, err
	}
	if !ok {
		stats.Skipped = true
		stats.Reason = "meilisearch is not configured"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "Meilisearch is not configured")
		return stats, nil
	}
	stats.Configured = true
	matches, err := i.runtimeMatchesDesiredSettings(ctx, settings)
	if err != nil {
		return stats, err
	}
	if !matches {
		stats.Skipped = true
		stats.Reason = "saved search settings are waiting for a server restart"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "Restart Silo before rebuilding the catalog search index")
		return stats, nil
	}

	lock, locked, err := pglock.TryAcquire(ctx, i.pool, searchIndexMaintenanceLockID)
	if err != nil {
		return stats, err
	}
	if !locked {
		stats.Skipped = true
		stats.Reason = "another search index maintenance task is running"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "Another catalog search index maintenance task is already running")
		return stats, nil
	}
	defer func() {
		if err := lock.Release(ctx); err != nil {
			slog.WarnContext(ctx, "releasing catalog search index maintenance lock",
				"component", "catalog", "error", err)
		}
	}()
	state, err := i.events.GetState(ctx, SearchProviderMeilisearch)
	if err != nil {
		return stats, err
	}
	if catalogSearchIndexHasNewerSchema(state) {
		stats.Skipped = true
		stats.Reason = "active search index was built by a newer server version"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "A newer server version owns the active search index")
		return stats, nil
	}

	return i.rebuildLocked(ctx, progress, settings, client)
}

// rebuildLocked builds and atomically publishes a replacement index. The
// caller must hold searchIndexMaintenanceLockID so the scheduled sync path and
// manual forced rebuilds cannot race across server nodes.
//
// A build index that fails before it is published is deleted again, and a
// rebuild first deletes `<prefix>_rebuild_*` leftovers of earlier runs (a
// process that died mid-rebuild never reached its own cleanup). The sync task
// retries a required rebuild every minute, so without both each failed attempt
// would leave another partial copy of the catalog on the Meilisearch instance.
func (i *CatalogSearchIndexer) rebuildLocked(
	ctx context.Context,
	progress SearchIndexProgressReporter,
	settings CatalogSearchSettings,
	client *meilisearchClient,
) (stats CatalogSearchIndexRebuildStats, err error) {
	stats = CatalogSearchIndexRebuildStats{Configured: true}

	rebuildEventHighWater, err := i.events.MaxEventID(ctx, SearchProviderMeilisearch)
	if err != nil {
		return stats, err
	}
	priorState, err := i.events.GetState(ctx, SearchProviderMeilisearch)
	if err != nil {
		return stats, err
	}
	totalDocs, err := countCatalogSearchEligibleDocuments(ctx, i.pool, settings.IndexTypes)
	if err != nil {
		return stats, err
	}

	reportSearchIndexProgress(progress, 0, "Removing unfinished catalog search indexes")
	removed, err := cleanupSupersededMeilisearchIndexes(ctx, client, settings.MeilisearchIndex, priorState.ActiveIndexUID, "")
	stats.RemovedIndexes = removed
	if err != nil {
		// Like the cleanup after a rebuild: disk on the Meilisearch instance,
		// not correctness. The next attempt retries it.
		slog.WarnContext(ctx, "catalog search: failed to remove unfinished meilisearch rebuild indexes", "component", "catalog", "err", err, "removed", removed)
	}

	buildIndexUID := fmt.Sprintf("%s_rebuild_%d", settings.MeilisearchIndex, time.Now().Unix())
	stats.ActiveIndexUID = buildIndexUID
	reportSearchIndexProgress(progress, 0, "Creating catalog search index")
	taskID, err := client.CreateIndex(ctx, buildIndexUID)
	if err != nil {
		return stats, err
	}
	published := false
	defer func() {
		// CreateIndex accepts an existing index, so a build uid that collides
		// with the active index (two rebuilds in one second) must never be
		// deleted here.
		if err != nil && !published && buildIndexUID != priorState.ActiveIndexUID {
			discardMeilisearchBuildIndex(ctx, client, buildIndexUID)
		}
	}()
	if err := client.WaitTask(ctx, taskID); err != nil {
		return stats, err
	}
	taskID, err = client.UpdateSettings(ctx, buildIndexUID, catalogSearchMeilisearchSettings(settings.Embedder, settings.SemanticEnabled, settings.BinaryQuantized))
	if err != nil {
		return stats, err
	}
	if err := client.WaitTask(ctx, taskID); err != nil {
		return stats, err
	}

	lastID := ""
	queuedTasks := make([]queuedMeilisearchTask, 0, settings.RebuildQueueDepth)
	for {
		docs, err := i.LoadDocumentsAfter(ctx, lastID, settings.RebuildBatchSize, settings.IndexTypes, settings.Embedder, settings.SemanticEnabled, settings.BinaryQuantized)
		if err != nil {
			return stats, err
		}
		if len(docs) == 0 {
			break
		}
		err = forEachCatalogSearchDocumentPayload(docs, meilisearchMaxDocumentPayloadBytes, func(payload catalogSearchDocumentPayload) error {
			taskID, err := client.AddDocuments(ctx, buildIndexUID, payload.body)
			if err != nil {
				return err
			}
			queuedTasks = append(queuedTasks, queuedMeilisearchTask{
				task:     taskID,
				docCount: len(payload.docs),
				vecCount: catalogSearchVectorDocumentCount(payload.docs),
			})
			submitted := stats.DocumentCount + queuedDocumentCount(queuedTasks)
			reportSearchIndexProgress(progress, rebuildIndexingPercent(submitted, totalDocs), fmt.Sprintf("Submitted %d of %d catalog items", submitted, totalDocs))
			if len(queuedTasks) >= settings.RebuildQueueDepth {
				return waitNextMeilisearchTask(ctx, client, &queuedTasks, &stats, progress, totalDocs)
			}
			return nil
		})
		if err != nil {
			return stats, err
		}
		lastID = docs[len(docs)-1].ContentID
	}
	for len(queuedTasks) > 0 {
		if err := waitNextMeilisearchTask(ctx, client, &queuedTasks, &stats, progress, totalDocs); err != nil {
			return stats, err
		}
	}
	docCount, err := client.Stats(ctx, buildIndexUID)
	if err != nil {
		return stats, err
	}
	stats.DocumentCount = docCount
	if vectorCount, err := i.countIndexedVectors(ctx, settings.IndexTypes); err == nil {
		stats.VectorDocCount = vectorCount
	}
	// Swap the state pointer BEFORE marking events processed. If the process
	// dies between the two, still-pending events simply replay into the new
	// active index as idempotent upserts on the next sync. The reverse order
	// would mark events processed while the old index is still active, losing
	// those changes from the served index until the next rebuild.
	//
	// The build index stops being discardable before the state write, not
	// after it: a write that committed but reported an error would otherwise
	// delete the index searches now use. If the write did fail, the next
	// rebuild's leftover cleanup removes the index.
	published = true
	if err := i.events.UpdateStateAfterRebuild(ctx, SearchProviderMeilisearch, buildIndexUID, catalogSearchMeilisearchSchemaVersion(settings.Embedder, settings.IndexTypes, settings.SemanticEnabled, settings.BinaryQuantized), catalogSearchMeilisearchSettingsFingerprint, docCount, rebuildEventHighWater); err != nil {
		return stats, err
	}
	if err := i.events.MarkProcessedThrough(ctx, SearchProviderMeilisearch, rebuildEventHighWater); err != nil {
		return stats, err
	}
	reportSearchIndexProgress(progress, 95, "Removing superseded catalog search indexes")
	removed, err = cleanupSupersededMeilisearchIndexes(ctx, client, settings.MeilisearchIndex, buildIndexUID, priorState.ActiveIndexUID)
	stats.RemovedIndexes += removed
	if err != nil {
		// The new index is already active; a failed cleanup costs disk on the
		// Meilisearch instance, not correctness, and the next rebuild retries.
		slog.WarnContext(ctx, "catalog search: failed to remove superseded meilisearch indexes", "component", "catalog", "err", err, "removed", removed)
	}
	setSearchIndexTaskResult(progress, stats)
	reportSearchIndexProgress(progress, 100, fmt.Sprintf("Rebuilt catalog search index with %d documents", docCount))
	return stats, nil
}

// rebuildIndexingPercent maps rebuild document progress onto the 5-90% band of
// the task's progress bar (index creation sits below, finalization above).
func rebuildIndexingPercent(done, total int) float64 {
	if total <= 0 {
		return 50
	}
	if done > total {
		done = total
	}
	return 5 + 85*float64(done)/float64(total)
}

// cleanupSupersededMeilisearchIndexes deletes indexes this rebuild has made
// unreachable: the previously active index and any `<prefix>_rebuild_*`
// leftovers from failed or superseded runs. Without it, every rebuild leaks a
// full copy of the catalog on the Meilisearch instance. With previousActiveUID
// empty it removes only the `<prefix>_rebuild_*` leftovers other than
// activeUID, which is how a rebuild clears them before it starts.
func cleanupSupersededMeilisearchIndexes(ctx context.Context, client *meilisearchClient, indexPrefix, activeUID, previousActiveUID string) (int, error) {
	uids, err := client.ListIndexUIDs(ctx)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, uid := range staleCatalogSearchIndexUIDs(uids, indexPrefix, activeUID, previousActiveUID) {
		task, err := client.DeleteIndex(ctx, uid)
		if err != nil {
			return removed, err
		}
		if err := client.WaitTask(ctx, task); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// discardMeilisearchBuildIndex deletes the index of a failed rebuild, best
// effort. It runs on a context detached from ctx's cancellation, because a
// canceled rebuild is one of the failures it cleans up after, and bounded by
// meilisearchIndexingTimeout. A failure is logged; the next rebuild's leftover
// cleanup removes the index then.
func discardMeilisearchBuildIndex(ctx context.Context, client *meilisearchClient, uid string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), meilisearchIndexingTimeout)
	defer cancel()
	task, err := client.DeleteIndex(cleanupCtx, uid)
	if err == nil {
		err = client.WaitTask(cleanupCtx, task)
	}
	if err != nil {
		slog.WarnContext(ctx, "catalog search: failed to delete the index of a failed rebuild", "component", "catalog", "index", uid, "err", err)
	}
}

// staleCatalogSearchIndexUIDs selects which index uids a finished rebuild
// should delete: every `<prefix>_rebuild_` index except the newly active one,
// plus the previously active index (which may predate the rebuild naming
// scheme). Indexes outside the prefix are never touched, so a shared
// Meilisearch instance stays safe.
func staleCatalogSearchIndexUIDs(uids []string, indexPrefix, activeUID, previousActiveUID string) []string {
	rebuildPrefix := indexPrefix + "_rebuild_"
	var stale []string
	for _, uid := range uids {
		if uid == "" || uid == activeUID {
			continue
		}
		if strings.HasPrefix(uid, rebuildPrefix) || uid == previousActiveUID {
			stale = append(stale, uid)
		}
	}
	return stale
}

func waitNextMeilisearchTask(
	ctx context.Context,
	client *meilisearchClient,
	queue *[]queuedMeilisearchTask,
	stats *CatalogSearchIndexRebuildStats,
	progress SearchIndexProgressReporter,
	totalDocs int,
) error {
	if len(*queue) == 0 {
		return nil
	}
	next := (*queue)[0]
	*queue = (*queue)[1:]
	if err := client.WaitTask(ctx, next.task); err != nil {
		return err
	}
	stats.DocumentCount += next.docCount
	stats.VectorDocCount += next.vecCount
	reportSearchIndexProgress(progress, rebuildIndexingPercent(stats.DocumentCount, totalDocs), fmt.Sprintf("Indexed %d of %d catalog items", stats.DocumentCount, totalDocs))
	return nil
}

func queuedDocumentCount(queue []queuedMeilisearchTask) int {
	total := 0
	for _, task := range queue {
		total += task.docCount
	}
	return total
}

// catalogSearchDocumentPayload is one documents request: docs encoded as the
// JSON array body.
type catalogSearchDocumentPayload struct {
	body json.RawMessage
	docs []catalogSearchDocument
}

// forEachCatalogSearchDocumentPayload encodes docs once, in order, and hands
// send consecutive payloads whose bodies are each at most maxBytes long
// (maxBytes <= 0 means one payload). A document too large for maxBytes on its
// own goes alone. The size is the encoded size, so a payload of vector
// documents fills the cap whatever the model's real dimensions: a vector
// zero-padded to the canonical dimensions encodes far smaller than a full one.
// Only the payload being built is held, and send gets it before the next one
// starts.
func forEachCatalogSearchDocumentPayload(docs []catalogSearchDocument, maxBytes int, send func(catalogSearchDocumentPayload) error) error {
	var (
		body    []byte
		start   int
		scratch bytes.Buffer
	)
	enc := json.NewEncoder(&scratch)
	for idx := range docs {
		scratch.Reset()
		if err := enc.Encode(&docs[idx]); err != nil {
			return fmt.Errorf("encode catalog search document %q: %w", docs[idx].ContentID, err)
		}
		encoded := bytes.TrimSuffix(scratch.Bytes(), []byte("\n"))
		// The separator before the document and the closing bracket after it.
		if idx > start && maxBytes > 0 && len(body)+1+len(encoded)+1 > maxBytes {
			if err := send(catalogSearchDocumentPayload{body: append(body, ']'), docs: docs[start:idx]}); err != nil {
				return err
			}
			body, start = nil, idx
		}
		if idx == start {
			body = append(body, '[')
		} else {
			body = append(body, ',')
		}
		body = append(body, encoded...)
	}
	if start < len(docs) {
		return send(catalogSearchDocumentPayload{body: append(body, ']'), docs: docs[start:]})
	}
	return nil
}

func (i *CatalogSearchIndexer) loadClient(ctx context.Context) (CatalogSearchSettings, *meilisearchClient, bool, error) {
	settings, ok, err := i.loadMeilisearchRuntime(ctx)
	if err != nil || !ok {
		return settings, nil, ok, err
	}
	client, err := newMeilisearchClient(settings.MeilisearchURL, settings.MeilisearchAPIKey, meilisearchIndexingTimeout)
	if err != nil {
		return settings, nil, false, err
	}
	return settings, client, true, nil
}

func (i *CatalogSearchIndexer) loadMeilisearchRuntime(ctx context.Context) (CatalogSearchSettings, bool, error) {
	if i != nil && i.runtime != nil {
		settings := *i.runtime
		if i.settingsStore != nil {
			var err error
			settings, err = LoadCatalogSearchSettings(ctx, i.settingsStore)
			if err != nil {
				return settings, false, err
			}
		}
		applyCatalogSearchStartupSettings(&settings, *i.runtime)
		if settings.Provider != SearchProviderMeilisearch || strings.TrimSpace(settings.MeilisearchURL) == "" {
			return settings, false, nil
		}
		return settings, true, nil
	}
	settings, err := LoadCatalogSearchSettings(ctx, i.settingsStore)
	if err != nil {
		return settings, false, err
	}
	if settings.Provider != SearchProviderMeilisearch || strings.TrimSpace(settings.MeilisearchURL) == "" {
		return settings, false, nil
	}
	return settings, true, nil
}

func (i *CatalogSearchIndexer) runtimeMatchesDesiredSettings(ctx context.Context, runtime CatalogSearchSettings) (bool, error) {
	if i == nil || i.runtime == nil || i.settingsStore == nil {
		return true, nil
	}
	desired, err := LoadCatalogSearchSettings(ctx, i.settingsStore)
	if err != nil {
		return false, err
	}
	return catalogSearchMaintenanceTargetEqual(runtime, desired), nil
}

func applyCatalogSearchStartupSettings(settings *CatalogSearchSettings, startup CatalogSearchSettings) {
	settings.Provider = startup.Provider
	settings.MeilisearchURL = startup.MeilisearchURL
	settings.MeilisearchAPIKey = startup.MeilisearchAPIKey
	settings.MeilisearchIndex = startup.MeilisearchIndex
	settings.IndexTypes = slices.Clone(startup.IndexTypes)
	settings.SemanticEnabled = startup.SemanticEnabled
	settings.Embedder = startup.Embedder
	settings.BinaryQuantized = startup.BinaryQuantized
}

func catalogSearchMaintenanceTargetEqual(left, right CatalogSearchSettings) bool {
	return left.Provider == right.Provider &&
		left.MeilisearchURL == right.MeilisearchURL &&
		left.MeilisearchAPIKey == right.MeilisearchAPIKey &&
		left.MeilisearchIndex == right.MeilisearchIndex &&
		slices.Equal(left.IndexTypes, right.IndexTypes) &&
		left.SemanticEnabled == right.SemanticEnabled &&
		left.Embedder == right.Embedder &&
		left.BinaryQuantized == right.BinaryQuantized
}

func (i *CatalogSearchIndexer) CheckConnection(ctx context.Context, settings CatalogSearchSettings) error {
	if settings.Provider != SearchProviderMeilisearch {
		return nil
	}
	client, err := newMeilisearchClient(settings.MeilisearchURL, settings.MeilisearchAPIKey, settings.Timeout)
	if err != nil {
		return err
	}
	return client.checkConnection(ctx, settings.MeilisearchIndex)
}

// meilisearchPeopleAttribute is the indexed document's cast and crew names.
const meilisearchPeopleAttribute = "people"

// catalogSearchMeilisearchSettings is every index setting Silo applies: the
// index settings plus, with semantic search on, the embedder.
func catalogSearchMeilisearchSettings(embedder string, semanticEnabled, binaryQuantized bool) map[string]any {
	settings := catalogSearchMeilisearchIndexSettings()
	if semanticEnabled {
		embedder, err := NormalizeCatalogSearchEmbedderName(embedder)
		if err != nil {
			embedder = DefaultMeilisearchEmbedder
		}
		settings["embedders"] = catalogSearchMeilisearchEmbedderSettings(embedder, binaryQuantized)
	}
	return settings
}

// catalogSearchMeilisearchSettingsFingerprint identifies
// catalogSearchMeilisearchIndexSettings. catalog_search_index_state records the
// fingerprint the active index was last brought to; the sync task applies the
// settings in place when the two differ. json.Marshal writes map keys sorted,
// so equal settings give equal fingerprints.
var catalogSearchMeilisearchSettingsFingerprint = func() string {
	data, err := json.Marshal(catalogSearchMeilisearchIndexSettings())
	if err != nil {
		panic(fmt.Sprintf("encode catalog search index settings: %v", err))
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}()

// catalogSearchMeilisearchIndexSettings is the part of the index settings that
// Meilisearch can change on an existing index: everything but the embedder,
// which is part of the schema identity (catalogSearchMeilisearchSchemaVersion)
// because changing it needs new vectors. A change here needs no rebuild and no
// schema version bump; the sync task applies it in place.
func catalogSearchMeilisearchIndexSettings() map[string]any {
	return map[string]any{
		"displayedAttributes":  []string{"content_id", "type"},
		"filterableAttributes": []string{"type", "library_ids"},
		// The overview and tagline are not indexed. A word that appears
		// once in a synopsis made unrelated titles outrank the one searched
		// for, and descriptive searches drew enough synopsis hits to bury
		// the matches by meaning. The item's embedding covers the synopsis
		// by sense; genres and keywords keep themes searchable by word.
		"searchableAttributes": []string{
			"title",
			"original_title",
			"sort_title",
			"title_variants",
			meilisearchPeopleAttribute,
			"studios",
			"networks",
			"genres",
			"keywords",
			// Last, so a title that is a number ("1917") still ranks its
			// title match above items released that year, while "dune
			// 2021" can match the year of Dune (2021).
			"year", //nolint:goconst // A Meilisearch document attribute, not a sort field.
		},
		// Meilisearch's default rules, then popularity as the final tiebreak
		// between hits that tie on everything else (same title, no year in
		// the search). The default order is deliberate: typo ahead of
		// attributeRank keeps an exact cast match ("tom hanks") above titles
		// that are one letter off ("Idle Hands"). A custom rule needs no
		// sortableAttributes entry. Searches cut a keyword ranking at its
		// first hit that matches fewer words, with more typos, or with its
		// words further apart than the best hit (meilisearchKeywordMatchCount),
		// which is right only while words, typo and proximity lead this list.
		"rankingRules": []string{
			"words",
			"typo",
			"proximity",
			"attributeRank",
			"sort",
			"wordPosition",
			"exactness",
			"popularity:desc",
		},
		// A typo combined with prefix matching turns a short word into
		// unrelated names ("scrubs" matched "Srubshchik" and "Shrubsole"),
		// so people match only as typed. Titles keep typo tolerance.
		"typoTolerance": map[string]any{
			"disableOnAttributes": []string{meilisearchPeopleAttribute},
		},
		"pagination": map[string]any{
			"maxTotalHits": meilisearchCandidateScanCap,
		},
	}
}

func coalesceSearchIndexEvents(events []SearchIndexEvent) (upsertIDs []string, deleteIDs []string) {
	ops := make(map[string]string, len(events))
	for _, event := range events {
		switch event.Action {
		case SearchIndexEventRename:
			if event.PreviousContentID != "" {
				ops[event.PreviousContentID] = SearchIndexEventDelete
			}
			if event.ContentID != "" {
				ops[event.ContentID] = SearchIndexEventUpsert
			}
		case SearchIndexEventDelete:
			ops[event.ContentID] = SearchIndexEventDelete
		case SearchIndexEventUpsert:
			ops[event.ContentID] = SearchIndexEventUpsert
		}
	}
	for id, op := range ops {
		switch op {
		case SearchIndexEventDelete:
			deleteIDs = append(deleteIDs, id)
		case SearchIndexEventUpsert:
			upsertIDs = append(upsertIDs, id)
		}
	}
	return compactNonEmptyStrings(upsertIDs), compactNonEmptyStrings(deleteIDs)
}

func reportSearchIndexProgress(progress SearchIndexProgressReporter, percent float64, message string) {
	if progress != nil {
		progress.Report(percent, message)
	}
}

func setSearchIndexTaskResult(progress SearchIndexProgressReporter, result any) {
	if progress == nil {
		return
	}
	data, err := json.Marshal(result)
	if err == nil {
		progress.SetResultData(data)
	}
}

type catalogSearchDocument struct {
	ContentID     string               `json:"content_id"`
	Type          string               `json:"type"`
	Title         string               `json:"title"`
	SortTitle     string               `json:"sort_title,omitempty"`
	OriginalTitle string               `json:"original_title,omitempty"`
	Aliases       []string             `json:"-"`
	TitleVariants []string             `json:"title_variants,omitempty"`
	Year          int                  `json:"year,omitempty"`
	Genres        []string             `json:"genres,omitempty"`
	Studios       []string             `json:"studios,omitempty"`
	Networks      []string             `json:"networks,omitempty"`
	Countries     []string             `json:"countries,omitempty"`
	Keywords      []string             `json:"keywords,omitempty"`
	People        []string             `json:"people,omitempty"`
	LibraryIDs    []int32              `json:"library_ids,omitempty"`
	SchemaVersion int                  `json:"schema_version"`
	Vectors       map[string][]float32 `json:"_vectors,omitempty"`
	// Popularity orders hits that tie on every other ranking rule. Always
	// sent: Meilisearch leaves a custom ranking rule undefined for documents
	// that lack its attribute.
	Popularity int64 `json:"popularity"`
}

func (i *CatalogSearchIndexer) LoadDocumentsAfter(ctx context.Context, afterContentID string, limit int, itemTypes []string, embedder string, semanticEnabled, binaryQuantized bool) ([]catalogSearchDocument, error) {
	if i == nil || i.pool == nil || limit <= 0 {
		return nil, nil
	}
	typeFilter := normalizeCatalogSearchItemTypes(itemTypes)
	args := []any{afterContentID, limit}
	typeArg := 0
	if len(typeFilter) > 0 {
		typeArg = 3
		args = append(args, typeFilter)
	}
	rows, err := i.pool.Query(ctx, mixedCatalogSearchDocumentSQL(
		`mi.content_id > $1`, `e.content_id > $1`, typeArg, "LIMIT $2"), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs, err := scanCatalogSearchDocuments(rows)
	if err != nil {
		return nil, err
	}
	setCatalogSearchDocumentSchemaVersion(docs, catalogSearchMeilisearchSchemaVersion(embedder, itemTypes, semanticEnabled, binaryQuantized))
	if err := i.attachDocumentVectors(ctx, docs, embedder, semanticEnabled); err != nil {
		return nil, err
	}
	return docs, nil
}

func (i *CatalogSearchIndexer) LoadDocumentsByIDs(ctx context.Context, contentIDs []string, itemTypes []string, embedder string, semanticEnabled, binaryQuantized bool) ([]catalogSearchDocument, error) {
	contentIDs = compactNonEmptyStrings(contentIDs)
	if i == nil || i.pool == nil || len(contentIDs) == 0 {
		return nil, nil
	}
	typeFilter := normalizeCatalogSearchItemTypes(itemTypes)
	args := []any{contentIDs}
	typeArg := 0
	if len(typeFilter) > 0 {
		typeArg = 2
		args = append(args, typeFilter)
	}
	rows, err := i.pool.Query(ctx, mixedCatalogSearchDocumentSQL(
		`mi.content_id = ANY($1)`, `e.content_id = ANY($1)`, typeArg, ""), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs, err := scanCatalogSearchDocuments(rows)
	if err != nil {
		return nil, err
	}
	setCatalogSearchDocumentSchemaVersion(docs, catalogSearchMeilisearchSchemaVersion(embedder, itemTypes, semanticEnabled, binaryQuantized))
	if err := i.attachDocumentVectors(ctx, docs, embedder, semanticEnabled); err != nil {
		return nil, err
	}
	return docs, nil
}

// mixedCatalogSearchDocumentSQL first pages narrow IDs across both physical
// sources, then aggregates people and library memberships only for that batch.
// This keeps rebuild work proportional to RebuildBatchSize rather than the
// total number of catalog rows.
func mixedCatalogSearchDocumentSQL(mediaPredicate, episodePredicate string, typeArg int, limitClause string) string {
	mediaTypePredicate := ""
	episodeTypePredicate := ""
	if typeArg > 0 {
		mediaTypePredicate = fmt.Sprintf(" AND mi.type = ANY($%d)", typeArg)
		episodeTypePredicate = fmt.Sprintf(" AND 'episode' = ANY($%d)", typeArg)
	}
	return fmt.Sprintf(`
		WITH candidates AS (
			SELECT mi.content_id, mi.type
			FROM media_items mi
			WHERE %s
			  AND %s%s
			UNION ALL
			SELECT e.content_id, 'episode'::text AS type
			FROM episodes e
			JOIN media_items si ON si.content_id = e.series_id AND si.type = 'series'
			WHERE %s%s
			  AND EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id = e.content_id)
			ORDER BY content_id ASC
			%s
		)
		SELECT * FROM (
			SELECT
				mi.content_id,
				mi.type,
				COALESCE(mi.title, ''),
				COALESCE(mi.sort_title, ''),
				COALESCE(mi.original_title, ''),
				COALESCE(aliases.titles, ARRAY[]::text[]),
				COALESCE(mi.year, 0),
				COALESCE(mi.genres, ARRAY[]::text[]),
				COALESCE(mi.studios, ARRAY[]::text[]),
				COALESCE(mi.networks, ARRAY[]::text[]),
				COALESCE(mi.countries, ARRAY[]::text[]),
				COALESCE(mi.keywords, ARRAY[]::text[]),
				COALESCE(people.names, ARRAY[]::text[]),
				COALESCE(libraries.ids, ARRAY[]::integer[]),
				-- Popularity is the largest vote count any rating source
				-- holds for the item; an item no source counts votes for
				-- ranks as 0. It reads the sources, not
				-- media_items.tmdb_vote_count, because IMDb's count covers
				-- more of a library than TMDB's. A vote-count change writes
				-- no search event (it would flood the outbox on every
				-- metadata refresh), so the indexed value refreshes when the
				-- document is reindexed for another reason or rebuilt.
				COALESCE(popularity.votes, 0)
			FROM candidates c
			JOIN media_items mi ON c.type <> 'episode' AND mi.content_id = c.content_id
			LEFT JOIN LATERAL (
				SELECT array_agg(DISTINCT mia.title ORDER BY mia.title) FILTER (WHERE btrim(mia.title) <> '') AS titles
				FROM media_item_aliases mia
				WHERE mia.content_id = mi.content_id
			) aliases ON true
			-- People are listed in billing order: each person's best credit
			-- position, with the lead actor, director and writer of an item
			-- sharing the front. Meilisearch ranks a match nearer the start
			-- of an attribute higher, so a search for a name puts the titles
			-- that person leads ahead of ones they have a small part in. An
			-- alphabetical list would instead favor items with short casts.
			LEFT JOIN LATERAL (
				SELECT array_agg(credited.name ORDER BY credited.billing, credited.kind, credited.name) AS names
				FROM (
					SELECT p.name,
					       min(COALESCE(ip.sort_order, 2147483647)) AS billing,
					       min(ip.kind) AS kind
					FROM item_people ip
					JOIN people p ON p.id = ip.person_id
					WHERE ip.content_id = mi.content_id
					  AND p.name IS NOT NULL AND p.name <> ''
					GROUP BY p.name
				) credited
			) people ON true
			LEFT JOIN LATERAL (
				SELECT array_agg(DISTINCT mil.media_folder_id ORDER BY mil.media_folder_id) AS ids
				FROM media_item_libraries mil
				WHERE mil.content_id = mi.content_id
			) libraries ON true
			LEFT JOIN LATERAL (
				SELECT max(rs.votes) AS votes
				FROM media_item_rating_sources rs
				WHERE rs.content_id = mi.content_id
			) popularity ON true
			UNION ALL
			SELECT
				e.content_id,
				'episode'::text,
				COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text),
				COALESCE(NULLIF(BTRIM(e.title), ''), 'Episode ' || e.episode_number::text),
				''::text,
				ARRAY[]::text[],
				COALESCE(EXTRACT(YEAR FROM e.air_date)::integer, 0),
				ARRAY[]::text[], ARRAY[]::text[], ARRAY[]::text[], ARRAY[]::text[], ARRAY[]::text[], ARRAY[]::text[],
				COALESCE(libraries.ids, ARRAY[]::integer[]),
				0::bigint
			FROM candidates c
			JOIN episodes e ON c.type = 'episode' AND e.content_id = c.content_id
			LEFT JOIN LATERAL (
				SELECT array_agg(DISTINCT el.media_folder_id ORDER BY el.media_folder_id) AS ids
				FROM episode_libraries el
				WHERE el.episode_id = e.content_id
			) libraries ON true
		) documents
		ORDER BY content_id ASC`,
		mediaPredicate, catalogSearchExcludeMangaChaptersSQL, mediaTypePredicate,
		episodePredicate, episodeTypePredicate, limitClause)
}

func scanCatalogSearchDocuments(rows pgx.Rows) ([]catalogSearchDocument, error) {
	var docs []catalogSearchDocument
	for rows.Next() {
		var doc catalogSearchDocument
		if err := rows.Scan(
			&doc.ContentID,
			&doc.Type,
			&doc.Title,
			&doc.SortTitle,
			&doc.OriginalTitle,
			&doc.Aliases,
			&doc.Year,
			&doc.Genres,
			&doc.Studios,
			&doc.Networks,
			&doc.Countries,
			&doc.Keywords,
			&doc.People,
			&doc.LibraryIDs,
			&doc.Popularity,
		); err != nil {
			return nil, err
		}
		doc.SchemaVersion = SearchMeilisearchSchemaVersion
		doc.TitleVariants = catalogSearchTitleVariants(doc)
		docs = append(docs, doc)
	}
	return docs, rows.Err()
}

func (i *CatalogSearchIndexer) attachDocumentVectors(ctx context.Context, docs []catalogSearchDocument, embedder string, semanticEnabled bool) error {
	if !semanticEnabled || i == nil || i.pool == nil || len(docs) == 0 {
		return nil
	}
	embedder, err := NormalizeCatalogSearchEmbedderName(embedder)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(docs))
	for _, doc := range docs {
		if doc.Type != "episode" && strings.TrimSpace(doc.ContentID) != "" {
			ids = append(ids, doc.ContentID)
		}
	}
	var vectors map[string][]float32
	if len(ids) > 0 {
		model, err := i.activeModel(ctx)
		if err != nil {
			return err
		}
		if vectors, err = loadCatalogSearchVectors(ctx, i.pool, ids, model); err != nil {
			return err
		}
	}
	// Always run the per-document pass: even an episode-only batch needs the
	// explicit `_vectors.<embedder>: null` opt-out on every document.
	setCatalogSearchDocumentVectors(docs, vectors, embedder)
	return nil
}

// activeModel returns the embedding model whose vectors documents carry, or
// "" for every model.
func (i *CatalogSearchIndexer) activeModel(ctx context.Context) (string, error) {
	if i.models == nil {
		return "", nil
	}
	model, err := i.models.ActiveEmbeddingModel(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve active embedding model: %w", err)
	}
	return model, nil
}

// countIndexedVectors counts the documents of itemTypes that carry a vector,
// the way attachDocumentVectors chooses them.
func (i *CatalogSearchIndexer) countIndexedVectors(ctx context.Context, itemTypes []string) (int, error) {
	model, err := i.activeModel(ctx)
	if err != nil {
		return 0, err
	}
	return countCatalogSearchVectorDocuments(ctx, i.pool, itemTypes, model)
}

// loadCatalogSearchVectors returns the stored vectors of contentIDs made by
// model ("" => any model). An item whose vector came from another model is
// absent, so its document opts out of semantic search until it is re-embedded.
func loadCatalogSearchVectors(ctx context.Context, pool *pgxpool.Pool, contentIDs []string, model string) (map[string][]float32, error) {
	contentIDs = compactNonEmptyStrings(contentIDs)
	if pool == nil || len(contentIDs) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT media_item_id, embedding
		FROM media_item_embeddings
		WHERE media_item_id = ANY($1)
		  AND ($2 = '' OR model = $2)
	`, contentIDs, model)
	if err != nil {
		return nil, fmt.Errorf("load catalog search vectors: %w", err)
	}
	defer rows.Close()

	vectors := make(map[string][]float32, len(contentIDs))
	for rows.Next() {
		var contentID string
		var vector pgvector.Vector
		if err := rows.Scan(&contentID, &vector); err != nil {
			return nil, fmt.Errorf("scan catalog search vector: %w", err)
		}
		canonical, err := embeddingvectors.EnsureCanonicalDimensions(vector.Slice())
		if err != nil {
			return nil, fmt.Errorf("canonicalize catalog search vector for %s: %w", contentID, err)
		}
		vectors[contentID] = canonical
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog search vectors: %w", err)
	}
	return vectors, nil
}

func setCatalogSearchDocumentVectors(docs []catalogSearchDocument, vectors map[string][]float32, embedder string) int {
	if len(docs) == 0 || strings.TrimSpace(embedder) == "" {
		return 0
	}
	count := 0
	for idx := range docs {
		if docs[idx].Type == "episode" {
			// Episodes are keyword-only, but a userProvided embedder requires
			// every document to either supply vectors or opt out explicitly
			// with `_vectors.<embedder>: null`; omitting _vectors entirely
			// fails the whole indexing task.
			docs[idx].Vectors = map[string][]float32{embedder: nil}
			continue
		}
		vector := vectors[docs[idx].ContentID]
		docs[idx].Vectors = map[string][]float32{embedder: nil}
		if len(vector) > 0 {
			docs[idx].Vectors[embedder] = vector
			count++
		}
	}
	return count
}

func setCatalogSearchDocumentSchemaVersion(docs []catalogSearchDocument, schemaVersion int) {
	for idx := range docs {
		docs[idx].SchemaVersion = schemaVersion
	}
}

func catalogSearchVectorDocumentCount(docs []catalogSearchDocument) int {
	count := 0
	for _, doc := range docs {
		for _, vector := range doc.Vectors {
			if len(vector) > 0 {
				count++
				break
			}
		}
	}
	return count
}

// countCatalogSearchVectorDocuments returns the total number of embed-eligible
// items carrying a current-model embedding across the requested item types
// (nil/empty => all types). model="" counts every model. This is the numerator
// of catalogSemanticCoverageByType summed across types; it applies the same
// embed-eligibility predicate so it never counts vectors on ineligible
// (unmatched, non-book) items.
func countCatalogSearchVectorDocuments(ctx context.Context, q coverageQuerier, itemTypes []string, model string) (int, error) {
	if q == nil {
		return 0, nil
	}
	// Shares the eligibility + model + type predicate with
	// semanticCoverageVectorizedByTypeSQL (the per-type numerator); this is the
	// same count summed across types. $1 is always referenced (the explicit
	// ::text[] cast lets Postgres infer the type when it is NULL), so a nil type
	// filter does not trip an "undetermined parameter" error.
	var typeArg any
	if typeFilter := normalizeCatalogSearchItemTypes(itemTypes); len(typeFilter) > 0 {
		typeArg = typeFilter
	}
	var count int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM media_item_embeddings e
		JOIN media_items mi ON mi.content_id = e.media_item_id
		WHERE `+catalogSearchExcludeMangaChaptersSQL+`
		  AND ($1::text[] IS NULL OR mi.type = ANY($1))
		  AND (mi.status = 'matched' OR mi.type IN ('audiobook','ebook'))
		  AND ($2 = '' OR e.model = $2)
	`, typeArg, model).Scan(&count); err != nil {
		return 0, fmt.Errorf("count catalog search vector documents: %w", err)
	}
	return count, nil
}

// countCatalogSearchEligibleDocuments counts the items a rebuild will index
// (nil/empty itemTypes => all types). It applies the same predicate as the
// document loaders — NOT the vector-eligibility predicate — so the total is an
// exact denominator for rebuild progress reporting. It takes the pool itself,
// not an interface, so the nil check sees a nil pool.
func countCatalogSearchEligibleDocuments(ctx context.Context, q *pgxpool.Pool, itemTypes []string) (int, error) {
	if q == nil {
		return 0, nil
	}
	var typeArg any
	if typeFilter := normalizeCatalogSearchItemTypes(itemTypes); len(typeFilter) > 0 {
		typeArg = typeFilter
	}
	var count int
	if err := q.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*)
			 FROM media_items mi
			 WHERE `+catalogSearchExcludeMangaChaptersSQL+`
			   AND ($1::text[] IS NULL OR mi.type = ANY($1)))
			+
			(SELECT COUNT(*)
			 FROM episodes e
			 JOIN media_items si ON si.content_id = e.series_id AND si.type = 'series'
			 WHERE ($1::text[] IS NULL OR 'episode' = ANY($1))
			   AND EXISTS (SELECT 1 FROM episode_libraries el WHERE el.episode_id = e.content_id))
	`, typeArg).Scan(&count); err != nil {
		return 0, fmt.Errorf("count catalog search eligible documents: %w", err)
	}
	return count, nil
}

func catalogSearchTitleVariants(doc catalogSearchDocument) []string {
	variants := []string{
		doc.Title,
		doc.SortTitle,
		doc.OriginalTitle,
		normalizeTitleForComparison(doc.Title),
		normalizeTitleForComparison(doc.SortTitle),
		normalizeTitleForComparison(doc.OriginalTitle),
	}
	for _, alias := range doc.Aliases {
		variants = append(variants, alias, normalizeTitleForComparison(alias))
	}
	return compactNonEmptyStrings(variants)
}
