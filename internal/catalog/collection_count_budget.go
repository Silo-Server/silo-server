package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCollectionCountBudget identifies list construction, deadline and execution
// admission exhaustion. Missing counts are unavailable, never a stored zero.
var ErrCollectionCountBudget = errors.New("collection count budget exhausted")

const (
	personalCollectionCountStatements = 2
	personalCollectionCountBatchSize  = 32
	collectionCountMaxDefinitions     = 128
	collectionCountMaxRules           = 16384
	collectionCountTimeout            = 4 * time.Second
	collectionCountMaxSQLBytes        = 256 << 10
	collectionCountMaxArgs            = 8192
	collectionCountSmartType          = "smart"
)

type collectionCountBudgetKey struct{}

type collectionCountResult struct {
	done  chan struct{}
	count int
	err   error
}

type collectionCountReference struct {
	id     string
	result *collectionCountResult
}

type collectionCountBudget struct {
	ctx     context.Context //nolint:containedctx // Request-owned workers must keep the one shared list deadline.
	mu      sync.Mutex
	entries map[[32]byte]*collectionCountResult
	rules   int
	once    sync.Once
	jobs    chan collectionCountJob
}

// WithCollectionCountBudget creates one ledger and deadline for the entire
// list, including every owner's construction and execution. Reusing it never
// renews the deadline. Its two workers and bounded queue are request-local;
// statement admission is shared by this process, not distributed across nodes.
func WithCollectionCountBudget(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Value(collectionCountBudgetKey{}).(*collectionCountBudget); ok {
		return context.WithCancel(ctx)
	}
	ctx, cancel := context.WithTimeoutCause(ctx, collectionCountTimeout, ErrCollectionCountBudget)
	b := &collectionCountBudget{ctx: ctx, entries: make(map[[32]byte]*collectionCountResult), jobs: make(chan collectionCountJob, collectionCountMaxDefinitions)}
	return context.WithValue(ctx, collectionCountBudgetKey{}, b), cancel
}

func collectionCountContextError(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	return errors.Join(ctx.Err(), context.Cause(ctx))
}

func (b *collectionCountBudget) reserve(key [32]byte, rules int) (*collectionCountResult, bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := collectionCountContextError(b.ctx); err != nil {
		return nil, false, err
	}
	if previous, ok := b.entries[key]; ok {
		return previous, false, nil
	}
	if len(b.entries) >= collectionCountMaxDefinitions || b.rules+rules > collectionCountMaxRules {
		return nil, false, ErrCollectionCountBudget
	}
	result := &collectionCountResult{done: make(chan struct{})}
	b.entries[key] = result
	b.rules += rules
	return result, true, nil
}

func (b *collectionCountBudget) start(pool *pgxpool.Pool) {
	b.once.Do(func() {
		// A one-connection pool schedules serially instead of rejecting its
		// own second worker. Normal pools always use exactly two workers.
		for range min(personalCollectionCountStatements, int(pool.Config().MaxConns)) {
			go func() {
				for {
					select {
					case <-b.ctx.Done():
						return
					case job := <-b.jobs:
						counts, err := executeCollectionCountBatch(job.ctx, job.pool, job.accountID, job.sql, job.args)
						for _, query := range job.queries {
							query.result.count, query.result.err = counts[query.id], err
							close(query.result.done)
						}
					}
				}
			}()
		}
	})
}

// effectiveCollectionCountKey is called only after raw structural admission.
// Account and the full access filter participate; smart aliases only omit their
// collection ID because it does not affect SQL membership.
func effectiveCollectionCountKey(accountID int, c PersonalCollectionDefinition, access AccessFilter) ([32]byte, int, error) {
	source := ""
	if IsLiveQueryType(c.CollectionType) {
		source = c.QueryDefinition
	}
	if err := ValidatePersonalCollectionQueryBudget(source, c.DisplayQueryDefinition); err != nil {
		return [32]byte{}, 0, err
	}
	display, err := NormalizeDisplayQueryFragment([]byte(c.DisplayQueryDefinition))
	if err != nil {
		return [32]byte{}, 0, err
	}
	var definition QueryDefinition
	if source != "" || IsLiveQueryType(c.CollectionType) {
		definition, err = parseCatalogCollectionQueryDefinition([]byte(source))
		if err != nil {
			return [32]byte{}, 0, err
		}
		definition = ApplySmartCollectionItemLimit(definition)
		c.ID = ""
		c.CollectionType = collectionCountSmartType
	} else {
		c.CollectionType = libraryCollectionTypeManual
	}
	c.QueryDefinition = ""
	c.DisplayQueryDefinition = display
	raw, err := json.Marshal(struct {
		Account    int
		Collection PersonalCollectionDefinition
		Definition QueryDefinition
		Access     AccessFilter
	}{accountID, c, definition, access})
	if err != nil {
		return [32]byte{}, 0, err
	}
	var fragment QueryDefinition
	if display != "" {
		if err := json.Unmarshal([]byte(display), &fragment); err != nil {
			return [32]byte{}, 0, err
		}
	}
	rules := 0
	for _, def := range []QueryDefinition{definition, fragment} {
		for _, group := range def.Groups {
			rules += len(group.Rules)
		}
	}
	return sha256.Sum256(raw), rules, nil
}

type collectionCountStatement struct {
	id, sql string
	args    []any
	result  *collectionCountResult
}

type collectionCountJob struct {
	ctx       context.Context //nolint:containedctx // A bounded worker job carries its caller's cancellation.
	pool      *pgxpool.Pool
	accountID int
	sql       string
	args      []any
	queries   []collectionCountStatement
}

// CountPersonalCollections returns only counts evaluated successfully under a
// shared list budget. A failed batch is unavailable in full and is never retried.
// There is no durable quota, membership limit or cross-request count cache.
func CountPersonalCollections(ctx context.Context, pool *pgxpool.Pool, userID int, collections []PersonalCollectionDefinition, access AccessFilter) (map[string]int, error) {
	ctx, cancel := WithCollectionCountBudget(ctx)
	defer cancel()
	counts := make(map[string]int)
	if err := collectionCountContextError(ctx); err != nil {
		return counts, err
	}
	budget, ok := ctx.Value(collectionCountBudgetKey{}).(*collectionCountBudget)
	if !ok {
		return counts, errors.New("collection count budget context missing")
	}
	var references []collectionCountReference
	var queries []collectionCountStatement
	var failure error
	for _, c := range collections {
		if err := collectionCountContextError(ctx); err != nil {
			failure = joinCollectionCountError(failure, err)
			break
		}
		key, rules, err := effectiveCollectionCountKey(userID, c, access)
		if err != nil {
			failure = joinCollectionCountError(failure, err)
			continue
		}
		result, fresh, err := budget.reserve(key, rules)
		if err != nil {
			failure = joinCollectionCountError(failure, err)
			continue
		}
		references = append(references, collectionCountReference{c.ID, result})
		if !fresh {
			continue
		}
		sql, args, err := personalCollectionCountQuery(userID, c, access)
		if err != nil {
			result.err = err
			close(result.done)
			continue
		}
		queries = append(queries, collectionCountStatement{id: c.ID, sql: sql, args: args, result: result})
	}
	jobs := buildCollectionCountJobs(ctx, pool, userID, queries)
	if len(jobs) > 0 && pool == nil {
		for _, job := range jobs {
			failCollectionCountJob(job, errors.New("collection counts require a database pool"))
		}
	} else if len(jobs) > 0 {
		budget.start(pool)
		for _, job := range jobs {
			select {
			case <-ctx.Done():
				failCollectionCountJob(job, collectionCountContextError(ctx))
			case budget.jobs <- job:
			}
		}
	}
	return counts, collectCollectionCounts(ctx, references, counts, failure)
}

func collectCollectionCounts(ctx context.Context, references []collectionCountReference, counts map[string]int, failure error) error {
	for _, ref := range references {
		if err := awaitCollectionCountResult(ctx, ref.result); err != nil {
			failure = joinCollectionCountError(failure, err)
			continue
		}
		if ref.result.err != nil {
			failure = joinCollectionCountError(failure, ref.result.err)
		} else {
			counts[ref.id] = ref.result.count
		}
	}
	return failure
}

func awaitCollectionCountResult(ctx context.Context, result *collectionCountResult) error {
	select {
	case <-result.done:
		return nil
	default:
	}
	select {
	case <-result.done:
		return nil
	case <-ctx.Done():
		// A slower earlier batch cannot hide independently completed counts
		// when the shared deadline expires. Only unfinished results are absent.
		select {
		case <-result.done:
			return nil
		default:
			return collectionCountContextError(ctx)
		}
	}
}

func failCollectionCountJob(job collectionCountJob, err error) {
	for _, query := range job.queries {
		query.result.err = err
		close(query.result.done)
	}
}

// Keep error aggregation bounded even when a list has many invalid legacy
// collections, while retaining both exported classifications for HTTP callers.
func joinCollectionCountError(previous, next error) error {
	if previous == nil {
		return next
	}
	for _, kind := range []error{ErrQueryComplexity, ErrCollectionCountBudget, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(next, kind) && !errors.Is(previous, kind) {
			return errors.Join(previous, next)
		}
	}
	return previous
}

func buildCollectionCountJobs(ctx context.Context, pool *pgxpool.Pool, accountID int, queries []collectionCountStatement) []collectionCountJob {
	var jobs []collectionCountJob
	job := collectionCountJob{ctx: ctx, pool: pool, accountID: accountID}
	for _, query := range queries {
		if err := collectionCountContextError(ctx); err != nil {
			query.result.err = err
			close(query.result.done)
			continue
		}
		if len(query.sql) > collectionCountMaxSQLBytes || len(query.args) >= collectionCountMaxArgs {
			query.result.err = fmt.Errorf("%w: count statement size", ErrCollectionCountBudget)
			close(query.result.done)
			continue
		}
		part := collectionCountSQLPart(query, len(job.args))
		if len(job.queries) > 0 && (len(job.queries) == personalCollectionCountBatchSize || len(job.sql)+len(" UNION ALL ")+len(part) > collectionCountMaxSQLBytes || len(job.args)+1+len(query.args) > collectionCountMaxArgs) {
			jobs = append(jobs, job)
			job = collectionCountJob{ctx: ctx, pool: pool, accountID: accountID}
			part = collectionCountSQLPart(query, 0)
		}
		if len(part) > collectionCountMaxSQLBytes || 1+len(query.args) > collectionCountMaxArgs {
			query.result.err = fmt.Errorf("%w: count statement size", ErrCollectionCountBudget)
			close(query.result.done)
			continue
		}
		if len(job.queries) > 0 {
			job.sql += " UNION ALL "
		}
		job.sql += part
		job.args = append(job.args, query.id)
		job.args = append(job.args, query.args...)
		job.queries = append(job.queries, query)
	}
	if len(job.queries) > 0 {
		jobs = append(jobs, job)
	}
	return jobs
}

func collectionCountSQLPart(query collectionCountStatement, offset int) string {
	return fmt.Sprintf("SELECT $%d::text, (%s)", offset+1, rebindSQLPlaceholders(query.sql, offset+1))
}
