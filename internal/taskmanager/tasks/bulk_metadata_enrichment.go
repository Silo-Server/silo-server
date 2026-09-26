package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/librarykind"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/taskmanager"
)

// bulkMetadataEnrichmentAdvisoryLock spells "SILOENRB".
const bulkMetadataEnrichmentAdvisoryLock int64 = 0x53494C4F454E5242

// bulkMetadataEnricher is the slice of *metadata.MetadataService the task uses.
type bulkMetadataEnricher interface {
	HasBulkEnrichmentWork(ctx context.Context) (bool, error)
	RunBulkEnrichment(ctx context.Context, progress metadata.BulkEnrichmentProgress) (metadata.BulkEnrichmentReport, error)
}

// BulkMetadataEnrichmentTask runs the bulk enrichment pass: enrichment-only
// metadata providers that batch their lookups, such as MDBList, look up many
// movies and series at a time, for the items they have not answered for yet.
//
// Every API process runs the task manager and times its own triggers, so an
// advisory lock lets one server run the pass; the others skip. A skipped or
// interrupted pass loses nothing, because the pass records each answer and
// the next run starts from the items still missing one.
type BulkMetadataEnrichmentTask struct {
	enricher bulkMetadataEnricher
	lock     clusterLock
}

// NewBulkMetadataEnrichmentTask constructs the task. A nil pool runs without
// the cluster lock.
func NewBulkMetadataEnrichmentTask(enricher bulkMetadataEnricher, pool *pgxpool.Pool) *BulkMetadataEnrichmentTask {
	t := &BulkMetadataEnrichmentTask{enricher: enricher}
	if pool != nil {
		t.lock = advisoryClusterLock{pool: pool, key: bulkMetadataEnrichmentAdvisoryLock}
	}
	return t
}

func (t *BulkMetadataEnrichmentTask) Key() string  { return "bulk_metadata_enrichment" }
func (t *BulkMetadataEnrichmentTask) Name() string { return "Bulk Metadata Enrichment" }
func (t *BulkMetadataEnrichmentTask) Description() string {
	return "Looks up movies and series with enrichment providers that batch requests, such as MDBList, many titles at a time"
}
func (t *BulkMetadataEnrichmentTask) Category() taskmanager.TaskCategory {
	return taskmanager.TaskCategoryMetadata
}
func (t *BulkMetadataEnrichmentTask) IsHidden() bool { return false }

func (t *BulkMetadataEnrichmentTask) ServesLibrary(libraryType string) bool {
	return librarykind.IsMovie(libraryType) || librarykind.IsTV(libraryType) || librarykind.IsMixed(libraryType)
}

// DefaultTriggers runs hourly. A pass that a spent quota stopped picks up
// within the hour after the provider's quota resets, and ShouldRun keeps the
// idle runs out of task history.
func (t *BulkMetadataEnrichmentTask) DefaultTriggers() []taskmanager.TriggerConfig {
	return []taskmanager.TriggerConfig{
		{Type: taskmanager.TriggerTypeInterval, IntervalMs: 60 * 60 * 1000},
	}
}

func (t *BulkMetadataEnrichmentTask) ShouldRun(ctx context.Context) (bool, error) {
	if t == nil || t.enricher == nil {
		return false, nil
	}
	return t.enricher.HasBulkEnrichmentWork(ctx)
}

func (t *BulkMetadataEnrichmentTask) Execute(ctx context.Context, progress taskmanager.ProgressReporter) error {
	if t.enricher == nil {
		return nil
	}
	if t.lock != nil {
		release, acquired, err := t.lock.TryAcquire(ctx)
		if err != nil {
			return fmt.Errorf("acquiring bulk metadata enrichment lock: %w", err)
		}
		if !acquired {
			progress.Report(100, "Another server is running bulk metadata enrichment")
			return nil
		}
		defer release()
	}

	progress.Report(0, "Looking for items to enrich")
	report, err := t.enricher.RunBulkEnrichment(ctx, func(done, total int, provider string) {
		if total <= 0 {
			return
		}
		progress.Report(min(float64(done)*100/float64(total), 99), fmt.Sprintf("%s: looked up %d of %d items", provider, done, total))
	})
	if result, marshalErr := json.Marshal(report); marshalErr == nil {
		progress.SetResultData(result)
	}
	if err != nil {
		return fmt.Errorf("bulk metadata enrichment: %w", err)
	}

	found := 0
	var stopped []string
	for _, provider := range report.Providers {
		found += provider.Found
		if provider.Stopped != "" {
			stopped = append(stopped, provider.Provider+": "+provider.Stopped)
		}
	}
	message := fmt.Sprintf("Bulk metadata enrichment complete (%d items enriched)", found)
	if len(stopped) > 0 {
		message += "; stopped early: " + strings.Join(stopped, ", ")
	}
	progress.Report(100, message)
	return nil
}
