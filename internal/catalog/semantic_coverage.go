package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	// semanticCoverageEnableRatio is the per-type vector-coverage fraction at or
	// above which semantic search becomes eligible for that type.
	semanticCoverageEnableRatio = 0.90
	// semanticCoverageDisableRatio is the per-type fraction below which semantic
	// search is disabled for that type. The gap to the enable ratio is the
	// hysteresis band that prevents flapping near the threshold.
	semanticCoverageDisableRatio = 0.80
	// semanticCoverageRefreshInterval is how often the background tracker
	// recomputes the coverage snapshot.
	semanticCoverageRefreshInterval = 2 * time.Minute
)

// coverageQuerier is the read surface the semantic-coverage counts need from a
// database handle. *pgxpool.Pool satisfies it, so the existing callers continue
// to pass their pool unchanged. A later task layers a cached snapshot tracker on
// top of these queries in this same file.
type coverageQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// catalogTypeCoverage is the per-media-type semantic vector coverage: how many
// embed-eligible items exist (Eligible) and how many of those carry a
// current-model embedding (Vectorized). By construction Vectorized <= Eligible
// for every row.
//
// Ratio and Ready are populated by computeCoverageSnapshot, not by the raw
// catalogSemanticCoverageByType count query (which leaves them zero): Ratio is
// Vectorized/Eligible, and Ready is the hysteresis-latched readiness decision
// for the type. Carrying them on the same struct lets a published snapshot store
// counts and the derived gate state together.
type catalogTypeCoverage struct {
	Type       string
	Eligible   int
	Vectorized int
	Ratio      float64
	Ready      bool
}

// semanticCoverageEligibleByTypeSQL counts embed-eligible items per media type
// (the coverage denominator). The eligibility predicate is shared with the
// recommendations population via embeddingvectors.ItemEligibilityWhereClause;
// it is inlined here as a literal so the count stays a single round trip.
const semanticCoverageEligibleByTypeSQL = `
SELECT mi.type, COUNT(*) AS eligible
FROM media_items mi
WHERE ` + catalogSearchExcludeMangaChaptersSQL + `
  AND ($1::text[] IS NULL OR mi.type = ANY($1))
  AND (mi.status = 'matched' OR mi.type IN ('audiobook','ebook'))
GROUP BY mi.type`

// semanticCoverageVectorizedByTypeSQL counts embed-eligible items that already
// carry a current-model embedding per media type (the coverage numerator).
// Applying the same eligibility predicate as the denominator guarantees the
// numerator is a subset of the denominator, so a stale embedding left on a
// now-unmatched item can never push a per-type ratio above 1.
const semanticCoverageVectorizedByTypeSQL = `
SELECT mi.type, COUNT(*) AS vectorized
FROM media_item_embeddings e
JOIN media_items mi ON mi.content_id = e.media_item_id
WHERE ` + catalogSearchExcludeMangaChaptersSQL + `
  AND ($1::text[] IS NULL OR mi.type = ANY($1))
  AND (mi.status = 'matched' OR mi.type IN ('audiobook','ebook'))
  AND ($2 = '' OR e.model = $2)
GROUP BY mi.type`

// catalogSemanticCoverageByType returns per-type eligible and vectorized counts
// for the requested item types (nil/empty => all types) and embedding model
// ("" => count every model). The numerator and denominator share the
// embed-eligibility predicate, so every returned row satisfies
// Vectorized <= Eligible.
func catalogSemanticCoverageByType(ctx context.Context, q coverageQuerier, itemTypes []string, model string) ([]catalogTypeCoverage, error) {
	if q == nil {
		return nil, nil
	}
	typeFilter := normalizeCatalogSearchItemTypes(itemTypes)
	var typeArg any
	if len(typeFilter) > 0 {
		typeArg = typeFilter
	}

	coverage := make(map[string]*catalogTypeCoverage)
	order := make([]string, 0)
	upsert := func(mediaType string) *catalogTypeCoverage {
		if row, ok := coverage[mediaType]; ok {
			return row
		}
		row := &catalogTypeCoverage{Type: mediaType}
		coverage[mediaType] = row
		order = append(order, mediaType)
		return row
	}

	eligibleRows, err := q.Query(ctx, semanticCoverageEligibleByTypeSQL, typeArg)
	if err != nil {
		return nil, fmt.Errorf("query semantic coverage eligible counts: %w", err)
	}
	func() {
		defer eligibleRows.Close()
		for eligibleRows.Next() {
			var mediaType string
			var eligible int
			if err = eligibleRows.Scan(&mediaType, &eligible); err != nil {
				return
			}
			upsert(mediaType).Eligible = eligible
		}
		err = eligibleRows.Err()
	}()
	if err != nil {
		return nil, fmt.Errorf("scan semantic coverage eligible counts: %w", err)
	}

	vectorRows, err := q.Query(ctx, semanticCoverageVectorizedByTypeSQL, typeArg, model)
	if err != nil {
		return nil, fmt.Errorf("query semantic coverage vectorized counts: %w", err)
	}
	func() {
		defer vectorRows.Close()
		for vectorRows.Next() {
			var mediaType string
			var vectorized int
			if err = vectorRows.Scan(&mediaType, &vectorized); err != nil {
				return
			}
			upsert(mediaType).Vectorized = vectorized
		}
		err = vectorRows.Err()
	}()
	if err != nil {
		return nil, fmt.Errorf("scan semantic coverage vectorized counts: %w", err)
	}

	out := make([]catalogTypeCoverage, 0, len(order))
	for _, mediaType := range order {
		out = append(out, *coverage[mediaType])
	}
	return out, nil
}

// semanticCoverageSnapshot is an immutable, point-in-time view of per-type
// vector coverage for a single embedding model. Every field, including the
// PerType map and its values, is built fresh by computeCoverageSnapshot and is
// never mutated after the snapshot is published via atomic.Pointer.Store, so
// concurrent readers on the search hot path observe it without locking.
type semanticCoverageSnapshot struct {
	PerType   map[string]catalogTypeCoverage
	Overall   float64
	Model     string
	UpdatedAt time.Time
}

// computeCoverageSnapshot derives a fresh snapshot from raw per-type counts. It
// applies hysteresis per type: at or above the enable ratio a type is ready;
// below the disable ratio it is not; inside the band it holds the previous
// snapshot's latch for that type (defaulting to not-ready when no prior latch
// exists). A type with no eligible items is never ready. Overall is the global
// vectorized/eligible fraction across the supplied types.
//
// prev supplies only the previous per-type Ready latches for band entries;
// callers pass nil to start fresh (e.g. immediately after a model collapse) so a
// stale latch can never carry into a new model.
func computeCoverageSnapshot(types []catalogTypeCoverage, model string, prev *semanticCoverageSnapshot, now time.Time) *semanticCoverageSnapshot {
	per := make(map[string]catalogTypeCoverage, len(types))
	sumEligible, sumVectorized := 0, 0
	for _, c := range types {
		ratio := 0.0
		if c.Eligible > 0 {
			ratio = float64(c.Vectorized) / float64(c.Eligible)
		}
		ready := false
		switch {
		case c.Eligible == 0:
			ready = false // no data to gate on
		case ratio >= semanticCoverageEnableRatio:
			ready = true
		case ratio < semanticCoverageDisableRatio:
			ready = false
		default:
			// Hysteresis band [disable, enable): hold the previous latch.
			if prev != nil {
				if pc, ok := prev.PerType[c.Type]; ok {
					ready = pc.Ready
				}
			}
		}
		per[c.Type] = catalogTypeCoverage{
			Type:       c.Type,
			Eligible:   c.Eligible,
			Vectorized: c.Vectorized,
			Ratio:      ratio,
			Ready:      ready,
		}
		sumEligible += c.Eligible
		sumVectorized += c.Vectorized
	}
	overall := 0.0
	if sumEligible > 0 {
		overall = float64(sumVectorized) / float64(sumEligible)
	}
	return &semanticCoverageSnapshot{
		PerType:   per,
		Overall:   overall,
		Model:     model,
		UpdatedAt: now,
	}
}

// semanticCoverageTracker maintains the in-memory coverage snapshot consulted by
// the search hot path. Reads (CoverageReady/Snapshot) are lock-free via an
// atomic.Pointer; refreshes are single-flighted under mu and publish a freshly
// built, never-mutated snapshot. The fetch seam decouples Refresh from any
// concrete database handle so it is unit-testable without a real pool.
type semanticCoverageTracker struct {
	fetch  func(ctx context.Context, model string) ([]catalogTypeCoverage, error)
	models CatalogSemanticModelProvider

	mu   sync.Mutex
	snap atomic.Pointer[semanticCoverageSnapshot]

	clock func() time.Time
}

// Compile-time assertion that the tracker satisfies the hot-path gate contract.
var _ SemanticCoverageGate = (*semanticCoverageTracker)(nil)

// newSemanticCoverageTracker wires the tracker to a real query surface. The
// fetch seam closes over q so production code counts coverage through
// catalogSemanticCoverageByType, while tests inject a canned fetch directly.
func newSemanticCoverageTracker(q coverageQuerier, indexTypes []string, models CatalogSemanticModelProvider) *semanticCoverageTracker {
	return &semanticCoverageTracker{
		fetch: func(ctx context.Context, model string) ([]catalogTypeCoverage, error) {
			return catalogSemanticCoverageByType(ctx, q, indexTypes, model)
		},
		models: models,
		clock:  time.Now,
	}
}

// now reads the configured clock, defaulting to time.Now when unset (e.g. a
// struct literal that omits clock).
func (t *semanticCoverageTracker) now() time.Time {
	if t.clock != nil {
		return t.clock()
	}
	return time.Now()
}

// Refresh recomputes and publishes the coverage snapshot. It is single-flighted
// under mu and fails safe: if the active model cannot be resolved or the count
// query errors, the last-good snapshot is retained (no zeroed snapshot is
// published). When the active model changes, the prior snapshot is collapsed to
// not-ready before recompute so stale per-type latches cannot leak across
// models.
func (t *semanticCoverageTracker) Refresh(ctx context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	model := ""
	if t.models != nil {
		m, err := t.models.ActiveEmbeddingModel(ctx)
		if err != nil {
			// Retain last-good: do not publish on a provider error.
			slog.Warn("catalog semantic coverage: active model lookup failed; retaining last snapshot", "err", err)
			return err
		}
		model = m
	}

	if model == "" {
		// No active embedding model (no lock or no provider): publish an empty
		// not-ready snapshot so the gate reports not-ready deterministically.
		t.snap.Store(&semanticCoverageSnapshot{
			PerType:   map[string]catalogTypeCoverage{},
			Model:     "",
			UpdatedAt: t.now(),
		})
		return nil
	}

	prev := t.snap.Load()
	if prev != nil && prev.Model != model {
		// Model changed: collapse immediately and drop stale latches so the
		// recompute below cannot inherit readiness from the previous model.
		t.snap.Store(&semanticCoverageSnapshot{
			PerType:   map[string]catalogTypeCoverage{},
			Model:     model,
			UpdatedAt: t.now(),
		})
		prev = nil
	}

	types, err := t.fetch(ctx, model)
	if err != nil {
		// Retain last-good: do not overwrite a healthy snapshot with zeros.
		slog.Warn("catalog semantic coverage: count query failed; retaining last snapshot", "err", err)
		return err
	}

	t.snap.Store(computeCoverageSnapshot(types, model, prev, t.now()))
	return nil
}

// CoverageReady reports whether semantic search may serve the requested item
// types. It is lock-free and fail-safe: a not-yet-computed (nil/empty) snapshot
// reports not-ready. An explicit scope is an AND over its types; the first
// not-ready type's reason is returned. An empty scope requires every snapshot
// type to be ready. Requested types absent from the snapshot (no eligible items)
// are not gated; a scope consisting only of such types reports not-ready.
func (t *semanticCoverageTracker) CoverageReady(itemTypes []string) (bool, string) {
	s := t.snap.Load()
	if s == nil || len(s.PerType) == 0 {
		return false, "coverage not yet computed"
	}

	requested := normalizeCatalogSearchItemTypes(itemTypes)
	if len(requested) == 0 {
		requested = make([]string, 0, len(s.PerType))
		for k := range s.PerType {
			requested = append(requested, k)
		}
	}

	anyPresent := false
	for _, ty := range requested {
		c, ok := s.PerType[ty]
		if !ok {
			// No eligible items of this type: nothing to gate.
			continue
		}
		anyPresent = true
		if !c.Ready {
			return false, fmt.Sprintf("type %q coverage %.0f%% below threshold", ty, c.Ratio*100)
		}
	}
	if !anyPresent {
		return false, "no embeddable items in requested scope"
	}
	return true, ""
}

// Snapshot returns the current published snapshot, which may be nil before the
// first successful Refresh. Callers must nil-check.
func (t *semanticCoverageTracker) Snapshot() *semanticCoverageSnapshot {
	return t.snap.Load()
}

// Run refreshes once immediately, then on a fixed ticker until ctx is done.
// Refresh errors are logged and swallowed so a transient failure does not stop
// the loop (the last-good snapshot is retained by Refresh).
func (t *semanticCoverageTracker) Run(ctx context.Context) {
	if err := t.Refresh(ctx); err != nil {
		slog.Warn("catalog semantic coverage: initial refresh failed", "err", err)
	}
	ticker := time.NewTicker(semanticCoverageRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := t.Refresh(ctx); err != nil {
				slog.Warn("catalog semantic coverage: refresh failed", "err", err)
			}
		}
	}
}
