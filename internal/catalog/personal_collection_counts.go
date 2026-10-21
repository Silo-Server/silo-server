package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// personalMembershipSourceWhere limits a query to one personal collection's
// hand-picked or imported members in the Postgres user store. Its SourceArgs
// are the account ID and the collection ID.
const personalMembershipSourceWhere = "EXISTS (SELECT 1 FROM user_personal_collection_items cursor_membership WHERE cursor_membership.user_id=$1 AND cursor_membership.collection_id=$2 AND cursor_membership.sub_item_id='' AND cursor_membership.media_item_id=mi.content_id)"

// PersonalCollectionDefinition is the part of a personal collection that
// decides which items it shows.
type PersonalCollectionDefinition struct {
	ID                     string
	CollectionType         string
	QueryDefinition        string
	DisplayQueryDefinition string
}

// personalCollectionCountQuery uses the catalog view's predicates for visible
// members or smart matches, including its display filter and item limit.
func personalCollectionCountQuery(userID int, c PersonalCollectionDefinition, access AccessFilter) (string, []any, error) {
	display := QueryDefinition{}.Normalize()
	canonical, err := NormalizeDisplayQueryFragment([]byte(c.DisplayQueryDefinition))
	if err != nil {
		return "", nil, err
	}
	if canonical != "" {
		if err := json.Unmarshal([]byte(canonical), &display); err != nil {
			return "", nil, err
		}
	}
	executor := &QueryExecutor{BaseRelationSQL: catalogBaseRelationForScope("")}
	if !IsLiveQueryType(c.CollectionType) {
		executor.SourceWhere = personalMembershipSourceWhere
		executor.SourceArgs = []any{userID, c.ID}
		return executor.buildCountQuery(display, access)
	}
	def, err := parseCatalogCollectionQueryDefinition([]byte(c.QueryDefinition))
	if err != nil {
		return "", nil, err
	}
	def = ApplySmartCollectionItemLimit(def)
	base := &QueryExecutor{Scope: def.MediaScope, BaseRelationSQL: catalogBaseRelationForScope(def.MediaScope)}
	if canonical == "" {
		return base.buildCountQuery(def, access)
	}
	predicate, args, err := collectionDefinitionPredicate(base, def, access)
	if err != nil {
		return "", nil, err
	}
	executor.Scope = def.MediaScope
	executor.SourceWhere = predicate
	executor.SourceArgs = args
	display.MediaScope = def.MediaScope
	return executor.buildCountQuery(display, access)
}

// Personal collection counts run as at most personalCollectionCountStatements
// concurrent statements of at most personalCollectionCountBatchSize
// definitions each. One statement evaluates its counts one after another, so a
// single batch makes the list wait for the sum of every smart definition;
// splitting it bounds the wait by the slowest batch while keeping round trips
// per call small.
const (
	personalCollectionCountStatements = 4
	personalCollectionCountBatchSize  = 32
)

type collectionCountStatement struct {
	id, sql string
	args    []any
}

// CountPersonalCollections batches dynamic counts, runs the batches
// concurrently, and evaluates identical smart definitions once per call.
// Counts remain scoped to this viewer and request; catalog/watch changes are
// never hidden behind a cross-request cache. Failed definitions are absent
// from the returned map so callers can preserve their existing fallback.
func CountPersonalCollections(ctx context.Context, pool *pgxpool.Pool, userID int, collections []PersonalCollectionDefinition, access AccessFilter) (map[string]int, error) {
	counts := make(map[string]int, len(collections))
	seen := make(map[PersonalCollectionDefinition]string, len(collections))
	aliases := make(map[string][]string)
	var queries []collectionCountStatement
	var failures []error
	for _, c := range collections {
		key := c
		if IsLiveQueryType(c.CollectionType) {
			key.ID = ""
		}
		if id, ok := seen[key]; ok {
			aliases[id] = append(aliases[id], c.ID)
			continue
		}
		sql, args, err := personalCollectionCountQuery(userID, c, access)
		if err != nil {
			failures = append(failures, fmt.Errorf("collection %s: %w", c.ID, err))
			continue
		}
		seen[key] = c.ID
		queries = append(queries, collectionCountStatement{id: c.ID, sql: sql, args: args})
	}
	if len(queries) > 0 {
		batchSize := min(personalCollectionCountBatchSize, (len(queries)+personalCollectionCountStatements-1)/personalCollectionCountStatements)
		var (
			mu  sync.Mutex
			wg  sync.WaitGroup
			sem = make(chan struct{}, personalCollectionCountStatements)
		)
		for start := 0; start < len(queries); start += batchSize {
			batch := queries[start:min(start+batchSize, len(queries))]
			wg.Go(func() {
				sem <- struct{}{}
				defer func() { <-sem }()
				batchCounts, batchFailures := countPersonalCollectionBatch(ctx, pool, batch)
				mu.Lock()
				defer mu.Unlock()
				maps.Copy(counts, batchCounts)
				failures = append(failures, batchFailures...)
			})
		}
		wg.Wait()
	}
	for id, copies := range aliases {
		if count, ok := counts[id]; ok {
			for _, copyID := range copies {
				counts[copyID] = count
			}
		}
	}
	return counts, errors.Join(failures...)
}

// countPersonalCollectionBatch evaluates one batch in a single statement. A
// bad persisted definition must not suppress unrelated counts in its batch, so
// a failed batch is retried one definition at a time.
func countPersonalCollectionBatch(ctx context.Context, pool *pgxpool.Pool, batch []collectionCountStatement) (map[string]int, []error) {
	counts := make(map[string]int, len(batch))
	parts := make([]string, 0, len(batch))
	var args []any
	for _, q := range batch {
		idArg := len(args) + 1
		args = append(args, q.id)
		parts = append(parts, fmt.Sprintf("SELECT $%d::text, (%s)", idArg, rebindSQLPlaceholders(q.sql, len(args))))
		args = append(args, q.args...)
	}
	rows, err := pool.Query(ctx, strings.Join(parts, " UNION ALL "), args...)
	if err == nil {
		for rows.Next() {
			var id string
			var count int
			if err = rows.Scan(&id, &count); err != nil {
				break
			}
			counts[id] = count
		}
		rows.Close()
		if err == nil {
			err = rows.Err()
		}
	}
	if err == nil {
		return counts, nil
	}
	if ctx.Err() != nil {
		return nil, []error{ctx.Err()}
	}
	clear(counts)
	var failures []error
	for _, q := range batch {
		var count int
		if err := pool.QueryRow(ctx, q.sql, q.args...).Scan(&count); err != nil {
			failures = append(failures, fmt.Errorf("collection %s: %w", q.id, err))
			continue
		}
		counts[q.id] = count
	}
	return counts, failures
}
