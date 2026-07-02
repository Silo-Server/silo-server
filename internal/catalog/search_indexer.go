package catalog

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/embeddingvectors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type SearchIndexProgressReporter interface {
	Report(percent float64, message string)
	SetResultData(data json.RawMessage)
}

type CatalogSearchIndexSyncStats struct {
	Configured      bool   `json:"configured"`
	Skipped         bool   `json:"skipped"`
	Reason          string `json:"reason,omitempty"`
	Events          int    `json:"events"`
	Upserted        int    `json:"upserted"`
	Deleted         int    `json:"deleted"`
	ActiveIndexUID  string `json:"active_index_uid,omitempty"`
	DocumentCount   int    `json:"document_count"`
	VectorDocCount  int    `json:"vector_document_count"`
	LastProcessedID int64  `json:"last_processed_event_id,omitempty"`
}

type CatalogSearchIndexRebuildStats struct {
	Configured     bool   `json:"configured"`
	Skipped        bool   `json:"skipped"`
	Reason         string `json:"reason,omitempty"`
	ActiveIndexUID string `json:"active_index_uid,omitempty"`
	DocumentCount  int    `json:"document_count"`
	VectorDocCount int    `json:"vector_document_count"`
}

type queuedMeilisearchTask struct {
	task     meilisearchTaskRef
	docCount int
	vecCount int
}

const meilisearchMaxDocumentPayloadBytes = 80 * 1024 * 1024

type CatalogSearchIndexer struct {
	pool          *pgxpool.Pool
	settingsStore SettingsStore
	events        *SearchIndexEventRepository
}

func NewCatalogSearchIndexer(pool *pgxpool.Pool, settingsStore SettingsStore) *CatalogSearchIndexer {
	return &CatalogSearchIndexer{
		pool:          pool,
		settingsStore: settingsStore,
		events:        NewSearchIndexEventRepository(pool),
	}
}

func (i *CatalogSearchIndexer) ShouldSyncRun(ctx context.Context) (bool, error) {
	settings, ok, err := i.loadMeilisearchRuntime(ctx)
	if err != nil || !ok || settings.Provider != SearchProviderMeilisearch {
		return false, err
	}
	state, err := i.events.GetState(ctx, SearchProviderMeilisearch)
	if err != nil {
		return false, err
	}
	if state.ActiveIndexUID == "" || state.SchemaVersion != catalogSearchMeilisearchSchemaVersion(settings.Embedder, settings.IndexTypes, settings.SemanticEnabled) {
		return false, nil
	}
	pending, err := i.events.PendingCount(ctx, SearchProviderMeilisearch)
	if err != nil {
		return false, err
	}
	return pending > 0, nil
}

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

	lock, locked, err := i.events.TryAdvisoryLock(ctx, searchIndexMaintenanceLockID)
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
	defer lock.Close(context.Background())

	state, err := i.events.GetState(ctx, SearchProviderMeilisearch)
	if err != nil {
		return stats, err
	}
	if state.ActiveIndexUID == "" || state.SchemaVersion != catalogSearchMeilisearchSchemaVersion(settings.Embedder, settings.IndexTypes, settings.SemanticEnabled) {
		stats.Skipped = true
		stats.Reason = "active search index is missing or stale; run rebuild_catalog_search_index"
		setSearchIndexTaskResult(progress, stats)
		reportSearchIndexProgress(progress, 100, "Catalog search index needs a rebuild")
		return stats, nil
	}
	stats.ActiveIndexUID = state.ActiveIndexUID

	reportSearchIndexProgress(progress, 0, "Loading pending catalog search events")
	events, err := i.events.ListPending(ctx, SearchProviderMeilisearch, settings.SyncBatchSize)
	if err != nil {
		return stats, err
	}
	if len(events) == 0 {
		stats.DocumentCount = state.DocumentCount
		if vectorCount, err := countCatalogSearchVectorDocuments(ctx, i.pool, settings.IndexTypes, ""); err == nil {
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
	docs, err := i.LoadDocumentsByIDs(ctx, upsertIDs, settings.IndexTypes, settings.Embedder, settings.SemanticEnabled)
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
		for _, batch := range catalogSearchDocumentPayloadBatches(docs, meilisearchMaxDocumentPayloadBytes) {
			taskID, err := client.AddDocuments(ctx, state.ActiveIndexUID, batch)
			if err != nil {
				_ = i.events.MarkFailed(ctx, ids, err)
				return stats, err
			}
			if err := client.WaitTask(ctx, taskID); err != nil {
				_ = i.events.MarkFailed(ctx, ids, err)
				return stats, err
			}
			stats.Upserted += len(batch)
		}
	}

	docCount, err := client.Stats(ctx, state.ActiveIndexUID)
	if err != nil {
		_ = i.events.MarkFailed(ctx, ids, err)
		return stats, err
	}
	stats.DocumentCount = docCount
	if vectorCount, err := countCatalogSearchVectorDocuments(ctx, i.pool, settings.IndexTypes, ""); err == nil {
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

	lock, locked, err := i.events.TryAdvisoryLock(ctx, searchIndexMaintenanceLockID)
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
	defer lock.Close(context.Background())

	rebuildEventHighWater, err := i.events.MaxEventID(ctx, SearchProviderMeilisearch)
	if err != nil {
		return stats, err
	}

	buildIndexUID := fmt.Sprintf("%s_rebuild_%d", settings.MeilisearchIndex, time.Now().Unix())
	stats.ActiveIndexUID = buildIndexUID
	reportSearchIndexProgress(progress, 0, "Creating catalog search index")
	taskID, err := client.CreateIndex(ctx, buildIndexUID)
	if err != nil {
		return stats, err
	}
	if err := client.WaitTask(ctx, taskID); err != nil {
		return stats, err
	}
	taskID, err = client.UpdateSettings(ctx, buildIndexUID, catalogSearchMeilisearchSettings(settings.Embedder, settings.SemanticEnabled))
	if err != nil {
		return stats, err
	}
	if err := client.WaitTask(ctx, taskID); err != nil {
		return stats, err
	}

	lastID := ""
	queuedTasks := make([]queuedMeilisearchTask, 0, settings.RebuildQueueDepth)
	for {
		docs, err := i.LoadDocumentsAfter(ctx, lastID, settings.RebuildBatchSize, settings.IndexTypes, settings.Embedder, settings.SemanticEnabled)
		if err != nil {
			return stats, err
		}
		if len(docs) == 0 {
			break
		}
		for _, batch := range catalogSearchDocumentPayloadBatches(docs, meilisearchMaxDocumentPayloadBytes) {
			taskID, err := client.AddDocuments(ctx, buildIndexUID, batch)
			if err != nil {
				return stats, err
			}
			queuedTasks = append(queuedTasks, queuedMeilisearchTask{
				task:     taskID,
				docCount: len(batch),
				vecCount: catalogSearchVectorDocumentCount(batch),
			})
			reportSearchIndexProgress(progress, 25, fmt.Sprintf("Submitted %d catalog items", stats.DocumentCount+queuedDocumentCount(queuedTasks)))
			if len(queuedTasks) >= settings.RebuildQueueDepth {
				if err := waitNextMeilisearchTask(ctx, client, &queuedTasks, &stats, progress); err != nil {
					return stats, err
				}
			}
		}
		lastID = docs[len(docs)-1].ContentID
	}
	for len(queuedTasks) > 0 {
		if err := waitNextMeilisearchTask(ctx, client, &queuedTasks, &stats, progress); err != nil {
			return stats, err
		}
	}
	docCount, err := client.Stats(ctx, buildIndexUID)
	if err != nil {
		return stats, err
	}
	stats.DocumentCount = docCount
	if vectorCount, err := countCatalogSearchVectorDocuments(ctx, i.pool, settings.IndexTypes, ""); err == nil {
		stats.VectorDocCount = vectorCount
	}
	if err := i.events.MarkProcessedThrough(ctx, SearchProviderMeilisearch, rebuildEventHighWater); err != nil {
		return stats, err
	}
	if err := i.events.UpdateStateAfterRebuild(ctx, SearchProviderMeilisearch, buildIndexUID, catalogSearchMeilisearchSchemaVersion(settings.Embedder, settings.IndexTypes, settings.SemanticEnabled), docCount, rebuildEventHighWater); err != nil {
		return stats, err
	}
	setSearchIndexTaskResult(progress, stats)
	reportSearchIndexProgress(progress, 100, fmt.Sprintf("Rebuilt catalog search index with %d documents", docCount))
	return stats, nil
}

func waitNextMeilisearchTask(
	ctx context.Context,
	client *meilisearchClient,
	queue *[]queuedMeilisearchTask,
	stats *CatalogSearchIndexRebuildStats,
	progress SearchIndexProgressReporter,
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
	reportSearchIndexProgress(progress, 25, fmt.Sprintf("Indexed %d catalog items", stats.DocumentCount))
	return nil
}

func queuedDocumentCount(queue []queuedMeilisearchTask) int {
	total := 0
	for _, task := range queue {
		total += task.docCount
	}
	return total
}

func catalogSearchDocumentPayloadBatches(docs []catalogSearchDocument, maxBytes int) [][]catalogSearchDocument {
	if len(docs) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		return [][]catalogSearchDocument{docs}
	}
	batches := make([][]catalogSearchDocument, 0, int(math.Ceil(float64(len(docs))/1000)))
	current := make([]catalogSearchDocument, 0, len(docs))
	currentBytes := 2
	for _, doc := range docs {
		docBytes := estimateCatalogSearchDocumentJSONBytes(doc)
		if len(current) > 0 && currentBytes+docBytes+1 > maxBytes {
			batches = append(batches, current)
			current = nil
			currentBytes = 2
		}
		current = append(current, doc)
		currentBytes += docBytes + 1
	}
	if len(current) > 0 {
		batches = append(batches, current)
	}
	return batches
}

func estimateCatalogSearchDocumentJSONBytes(doc catalogSearchDocument) int {
	size := 512 +
		len(doc.ContentID) +
		len(doc.Type) +
		len(doc.Title) +
		len(doc.SortTitle) +
		len(doc.OriginalTitle) +
		len(doc.Overview) +
		len(doc.Tagline)
	size += estimateStringSliceJSONBytes(doc.TitleVariants)
	size += estimateStringSliceJSONBytes(doc.Genres)
	size += estimateStringSliceJSONBytes(doc.Studios)
	size += estimateStringSliceJSONBytes(doc.Networks)
	size += estimateStringSliceJSONBytes(doc.Countries)
	size += estimateStringSliceJSONBytes(doc.Keywords)
	size += estimateStringSliceJSONBytes(doc.People)
	for embedder, vector := range doc.Vectors {
		size += len(embedder) + 64 + len(vector)*24
	}
	return size
}

func estimateStringSliceJSONBytes(values []string) int {
	size := 2
	for _, value := range values {
		size += len(value) + 4
	}
	return size
}

func (i *CatalogSearchIndexer) loadClient(ctx context.Context) (CatalogSearchSettings, *meilisearchClient, bool, error) {
	settings, ok, err := i.loadMeilisearchRuntime(ctx)
	if err != nil || !ok {
		return settings, nil, ok, err
	}
	client, err := newMeilisearchClient(settings.MeilisearchURL, settings.MeilisearchAPIKey, settings.Timeout)
	if err != nil {
		return settings, nil, false, err
	}
	return settings, client, true, nil
}

func (i *CatalogSearchIndexer) loadMeilisearchRuntime(ctx context.Context) (CatalogSearchSettings, bool, error) {
	settings, err := LoadCatalogSearchSettings(ctx, i.settingsStore)
	if err != nil {
		return settings, false, err
	}
	if settings.Provider != SearchProviderMeilisearch || strings.TrimSpace(settings.MeilisearchURL) == "" {
		return settings, false, nil
	}
	return settings, true, nil
}

func (i *CatalogSearchIndexer) CheckConnection(ctx context.Context, settings CatalogSearchSettings) error {
	if settings.Provider != SearchProviderMeilisearch {
		return nil
	}
	client, err := newMeilisearchClient(settings.MeilisearchURL, settings.MeilisearchAPIKey, settings.Timeout)
	if err != nil {
		return err
	}
	return client.Health(ctx)
}

func catalogSearchMeilisearchSettings(embedder string, semanticEnabled bool) map[string]any {
	settings := map[string]any{
		"displayedAttributes":  []string{"content_id", "type"},
		"filterableAttributes": []string{"type"},
		"searchableAttributes": []string{
			"title",
			"original_title",
			"sort_title",
			"title_variants",
			"people",
			"studios",
			"networks",
			"genres",
			"keywords",
			"overview",
			"tagline",
		},
		"pagination": map[string]any{
			"maxTotalHits": meilisearchDefaultCandidateScanCap,
		},
	}
	if semanticEnabled {
		embedder, err := NormalizeCatalogSearchEmbedderName(embedder)
		if err != nil {
			embedder = DefaultMeilisearchEmbedder
		}
		settings["embedders"] = catalogSearchMeilisearchEmbedderSettings(embedder)
	}
	return settings
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
	TitleVariants []string             `json:"title_variants,omitempty"`
	Year          int                  `json:"year,omitempty"`
	Overview      string               `json:"overview,omitempty"`
	Tagline       string               `json:"tagline,omitempty"`
	Genres        []string             `json:"genres,omitempty"`
	Studios       []string             `json:"studios,omitempty"`
	Networks      []string             `json:"networks,omitempty"`
	Countries     []string             `json:"countries,omitempty"`
	Keywords      []string             `json:"keywords,omitempty"`
	People        []string             `json:"people,omitempty"`
	SchemaVersion int                  `json:"schema_version"`
	Vectors       map[string][]float32 `json:"_vectors,omitempty"`
}

func (i *CatalogSearchIndexer) LoadDocumentsAfter(ctx context.Context, afterContentID string, limit int, itemTypes []string, embedder string, semanticEnabled bool) ([]catalogSearchDocument, error) {
	if i == nil || i.pool == nil || limit <= 0 {
		return nil, nil
	}
	typeFilter := normalizeCatalogSearchItemTypes(itemTypes)
	whereClause := `
			WHERE ($1::text = '' OR mi.content_id > $1)
			  AND NOT EXISTS (
				SELECT 1 FROM manga_chapters mc
				WHERE mc.chapter_content_id = mi.content_id
			  )`
	args := []any{afterContentID, limit}
	if len(typeFilter) > 0 {
		whereClause += `
			  AND mi.type = ANY($3)`
		args = append(args, typeFilter)
	}
	rows, err := i.pool.Query(ctx, catalogSearchDocumentSelectSQL(whereClause, `
			ORDER BY mi.content_id ASC
			LIMIT $2
		`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs, err := scanCatalogSearchDocuments(rows)
	if err != nil {
		return nil, err
	}
	setCatalogSearchDocumentSchemaVersion(docs, catalogSearchMeilisearchSchemaVersion(embedder, itemTypes, semanticEnabled))
	if err := i.attachDocumentVectors(ctx, docs, embedder, semanticEnabled); err != nil {
		return nil, err
	}
	return docs, nil
}

func (i *CatalogSearchIndexer) LoadDocumentsByIDs(ctx context.Context, contentIDs []string, itemTypes []string, embedder string, semanticEnabled bool) ([]catalogSearchDocument, error) {
	contentIDs = compactNonEmptyStrings(contentIDs)
	if i == nil || i.pool == nil || len(contentIDs) == 0 {
		return nil, nil
	}
	typeFilter := normalizeCatalogSearchItemTypes(itemTypes)
	whereClause := `
			WHERE mi.content_id = ANY($1)
			  AND NOT EXISTS (
				SELECT 1 FROM manga_chapters mc
				WHERE mc.chapter_content_id = mi.content_id
			  )`
	args := []any{contentIDs}
	if len(typeFilter) > 0 {
		whereClause += `
			  AND mi.type = ANY($2)`
		args = append(args, typeFilter)
	}
	rows, err := i.pool.Query(ctx, catalogSearchDocumentSelectSQL(whereClause, `
			ORDER BY mi.content_id ASC
		`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	docs, err := scanCatalogSearchDocuments(rows)
	if err != nil {
		return nil, err
	}
	setCatalogSearchDocumentSchemaVersion(docs, catalogSearchMeilisearchSchemaVersion(embedder, itemTypes, semanticEnabled))
	if err := i.attachDocumentVectors(ctx, docs, embedder, semanticEnabled); err != nil {
		return nil, err
	}
	return docs, nil
}

func catalogSearchDocumentSelectSQL(whereClause string, tailClause string) string {
	return `
		SELECT
			mi.content_id,
			mi.type,
			COALESCE(mi.title, ''),
			COALESCE(mi.sort_title, ''),
			COALESCE(mi.original_title, ''),
			COALESCE(mi.year, 0),
			COALESCE(mi.overview, ''),
			COALESCE(mi.tagline, ''),
			COALESCE(mi.genres, ARRAY[]::text[]),
			COALESCE(mi.studios, ARRAY[]::text[]),
			COALESCE(mi.networks, ARRAY[]::text[]),
			COALESCE(mi.countries, ARRAY[]::text[]),
			COALESCE(mi.keywords, ARRAY[]::text[]),
			COALESCE(array_agg(DISTINCT p.name) FILTER (WHERE p.name IS NOT NULL AND p.name <> ''), ARRAY[]::text[]) AS people
		FROM media_items mi
		LEFT JOIN item_people ip ON ip.content_id = mi.content_id
		LEFT JOIN people p ON p.id = ip.person_id
	` + whereClause + `
		GROUP BY
			mi.content_id, mi.type, mi.title, mi.sort_title, mi.original_title, mi.year,
			mi.overview, mi.tagline, mi.genres, mi.studios, mi.networks, mi.countries, mi.keywords
	` + tailClause
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
			&doc.Year,
			&doc.Overview,
			&doc.Tagline,
			&doc.Genres,
			&doc.Studios,
			&doc.Networks,
			&doc.Countries,
			&doc.Keywords,
			&doc.People,
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
		if strings.TrimSpace(doc.ContentID) != "" {
			ids = append(ids, doc.ContentID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	vectors, err := loadCatalogSearchVectors(ctx, i.pool, ids)
	if err != nil {
		return err
	}
	setCatalogSearchDocumentVectors(docs, vectors, embedder)
	return nil
}

func loadCatalogSearchVectors(ctx context.Context, pool *pgxpool.Pool, contentIDs []string) (map[string][]float32, error) {
	contentIDs = compactNonEmptyStrings(contentIDs)
	if pool == nil || len(contentIDs) == 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT media_item_id, embedding
		FROM media_item_embeddings
		WHERE media_item_id = ANY($1)
	`, contentIDs)
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
		WHERE NOT EXISTS (SELECT 1 FROM manga_chapters mc WHERE mc.chapter_content_id = mi.content_id)
		  AND ($1::text[] IS NULL OR mi.type = ANY($1))
		  AND (mi.status = 'matched' OR mi.type IN ('audiobook','ebook'))
		  AND ($2 = '' OR e.model = $2)
	`, typeArg, model).Scan(&count); err != nil {
		return 0, fmt.Errorf("count catalog search vector documents: %w", err)
	}
	return count, nil
}

func catalogSearchTitleVariants(doc catalogSearchDocument) []string {
	return compactNonEmptyStrings([]string{
		doc.Title,
		doc.SortTitle,
		doc.OriginalTitle,
		normalizeTitleForComparison(doc.Title),
		normalizeTitleForComparison(doc.SortTitle),
		normalizeTitleForComparison(doc.OriginalTitle),
	})
}
