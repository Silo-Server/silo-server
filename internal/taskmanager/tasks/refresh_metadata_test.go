package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/worker"
)

type fakeRefreshCandidateFinder struct {
	candidates []worker.RefreshCandidate
	batches    [][]worker.RefreshCandidate
	calls      int
	pruneCalls int
}

func (f *fakeRefreshCandidateFinder) FindCandidates(_ context.Context, _ int) ([]worker.RefreshCandidate, error) {
	f.calls++
	if len(f.batches) > 0 {
		batch := f.batches[0]
		f.batches = f.batches[1:]
		return append([]worker.RefreshCandidate(nil), batch...), nil
	}
	return append([]worker.RefreshCandidate(nil), f.candidates...), nil
}

func (f *fakeRefreshCandidateFinder) PruneDisabledLibraryDebt(_ context.Context) error {
	f.pruneCalls++
	return nil
}

type fakeMetadataRefresher struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeMetadataRefresher) RefreshScheduledTarget(_ context.Context, targetType, contentID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if targetType == "" {
		targetType = "item"
	}
	f.calls = append(f.calls, targetType+":"+contentID)
	return nil
}

func (f *fakeMetadataRefresher) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

type noopProgressReporter struct{}

func (noopProgressReporter) Report(float64, string)        {}
func (noopProgressReporter) SetResultData(json.RawMessage) {}

func TestRefreshMetadataTask_NoCandidates(t *testing.T) {
	finder := &fakeRefreshCandidateFinder{}
	refresher := &fakeMetadataRefresher{}
	task := NewRefreshMetadataTask(finder, refresher)

	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if calls := refresher.Calls(); len(calls) != 0 {
		t.Fatalf("expected no refresh calls, got %v", calls)
	}
}

func TestRefreshMetadataTask_DrainsFullBatches(t *testing.T) {
	firstBatch := make([]worker.RefreshCandidate, refreshMetadataBatchSize)
	for i := range firstBatch {
		firstBatch[i] = worker.RefreshCandidate{TargetType: "item", ContentID: fmt.Sprintf("item-%03d", i)}
	}
	secondBatch := []worker.RefreshCandidate{{TargetType: "episode", ContentID: "final-item"}}
	finder := &fakeRefreshCandidateFinder{
		batches: [][]worker.RefreshCandidate{firstBatch, secondBatch},
	}
	refresher := &fakeMetadataRefresher{}
	task := NewRefreshMetadataTask(finder, refresher)

	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	calls := refresher.Calls()
	if len(calls) != refreshMetadataBatchSize+1 {
		t.Fatalf("expected %d refresh calls, got %d", refreshMetadataBatchSize+1, len(calls))
	}
	if finder.pruneCalls != 1 {
		t.Fatalf("expected disabled-library prune once, got %d", finder.pruneCalls)
	}
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		seen[call] = true
	}
	if !seen["item:item-000"] || !seen["item:item-199"] || !seen["episode:final-item"] {
		t.Fatalf("expected calls from both claimed batches, got %v", calls)
	}
}

// The production refresher must keep satisfying the optional batch interface;
// a silent mismatch would leave every claimed batch unbatched.
var _ MetadataRefreshBatcher = (*metadata.MetadataService)(nil)

type batchMarkerKey struct{}

// batchingMetadataRefresher records how the task opens and flushes refresh
// batches around the targets it refreshes.
type batchingMetadataRefresher struct {
	fakeMetadataRefresher
	batchMu        sync.Mutex
	begins         int
	flushes        int
	callsAtFlush   []int
	unbatchedCalls int
}

func (f *batchingMetadataRefresher) BeginScheduledRefreshBatch(ctx context.Context) (context.Context, func(context.Context)) {
	f.batchMu.Lock()
	f.begins++
	batch := f.begins
	f.batchMu.Unlock()
	return context.WithValue(ctx, batchMarkerKey{}, batch), func(context.Context) {
		calls := len(f.Calls())
		f.batchMu.Lock()
		defer f.batchMu.Unlock()
		f.flushes++
		f.callsAtFlush = append(f.callsAtFlush, calls)
	}
}

func (f *batchingMetadataRefresher) RefreshScheduledTarget(ctx context.Context, targetType, contentID string) error {
	if ctx.Value(batchMarkerKey{}) == nil {
		f.batchMu.Lock()
		f.unbatchedCalls++
		f.batchMu.Unlock()
	}
	return f.fakeMetadataRefresher.RefreshScheduledTarget(ctx, targetType, contentID)
}

func TestRefreshMetadataTask_FlushesEachBatchAfterItsTargets(t *testing.T) {
	firstBatch := make([]worker.RefreshCandidate, refreshMetadataBatchSize)
	for i := range firstBatch {
		firstBatch[i] = worker.RefreshCandidate{TargetType: "episode", ContentID: fmt.Sprintf("episode-%03d", i)}
	}
	secondBatch := []worker.RefreshCandidate{
		{TargetType: "season", ContentID: "season-final"},
		{TargetType: "episode", ContentID: "episode-final"},
	}
	finder := &fakeRefreshCandidateFinder{
		batches: [][]worker.RefreshCandidate{firstBatch, secondBatch},
	}
	refresher := &batchingMetadataRefresher{}
	task := NewRefreshMetadataTask(finder, refresher)

	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if refresher.begins != 2 || refresher.flushes != 2 {
		t.Fatalf("batches begun/flushed = %d/%d, want 2/2", refresher.begins, refresher.flushes)
	}
	wantCallsAtFlush := []int{refreshMetadataBatchSize, refreshMetadataBatchSize + len(secondBatch)}
	if fmt.Sprint(refresher.callsAtFlush) != fmt.Sprint(wantCallsAtFlush) {
		t.Fatalf("refresh calls completed at each flush = %v, want %v", refresher.callsAtFlush, wantCallsAtFlush)
	}
	if refresher.unbatchedCalls != 0 {
		t.Fatalf("%d targets refreshed outside their batch context", refresher.unbatchedCalls)
	}
}

// cancelingBatchRefresher cancels the task's context during the first
// refresh and records the context each flush receives.
type cancelingBatchRefresher struct {
	batchingMetadataRefresher
	cancel     context.CancelFunc
	cancelOnce sync.Once
	flushErrs  []error
}

func (f *cancelingBatchRefresher) BeginScheduledRefreshBatch(ctx context.Context) (context.Context, func(context.Context)) {
	batchCtx, flush := f.batchingMetadataRefresher.BeginScheduledRefreshBatch(ctx)
	return batchCtx, func(flushCtx context.Context) {
		f.batchMu.Lock()
		f.flushErrs = append(f.flushErrs, flushCtx.Err())
		f.batchMu.Unlock()
		flush(flushCtx)
	}
}

func (f *cancelingBatchRefresher) RefreshScheduledTarget(ctx context.Context, targetType, contentID string) error {
	f.cancelOnce.Do(f.cancel)
	return f.batchingMetadataRefresher.RefreshScheduledTarget(ctx, targetType, contentID)
}

func TestRefreshMetadataTask_FlushesACanceledBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	candidates := make([]worker.RefreshCandidate, 50)
	for i := range candidates {
		candidates[i] = worker.RefreshCandidate{TargetType: "episode", ContentID: fmt.Sprintf("episode-%03d", i)}
	}
	finder := &fakeRefreshCandidateFinder{batches: [][]worker.RefreshCandidate{candidates}}
	refresher := &cancelingBatchRefresher{cancel: cancel}
	task := NewRefreshMetadataTask(finder, refresher)

	if err := task.Execute(ctx, noopProgressReporter{}); err == nil {
		t.Fatal("Execute succeeded, want the cancellation error")
	}

	// The batch is still flushed once with the canceled context, so the
	// refresher can decide what to skip instead of losing the batch silently.
	if refresher.begins != 1 || refresher.flushes != 1 {
		t.Fatalf("batches begun/flushed = %d/%d, want 1/1", refresher.begins, refresher.flushes)
	}
	if len(refresher.flushErrs) != 1 || refresher.flushErrs[0] == nil {
		t.Fatalf("flush context errors = %v, want one canceled flush", refresher.flushErrs)
	}
}

// The production finder must keep satisfying the optional renewal interface;
// a silent mismatch would let long batches outlive their claims.
var _ RefreshClaimRenewer = (*worker.RefreshWorker)(nil)

// renewingCandidateFinder reports each claim renewal on a channel.
type renewingCandidateFinder struct {
	fakeRefreshCandidateFinder
	renewals chan []worker.RefreshCandidate
}

func (f *renewingCandidateFinder) RenewClaims(_ context.Context, candidates []worker.RefreshCandidate) error {
	select {
	case f.renewals <- candidates:
	default:
	}
	return nil
}

func (f *renewingCandidateFinder) ClaimRenewInterval() time.Duration { return time.Millisecond }

// renewalWaitingRefresher holds each refresh and the flush until a claim
// renewal arrives, standing in for a batch that outlasts one claim lease.
type renewalWaitingRefresher struct {
	batchingMetadataRefresher
	renewals    <-chan []worker.RefreshCandidate
	flushRenews int
}

func (f *renewalWaitingRefresher) waitForRenewal() bool {
	select {
	case <-f.renewals:
		return true
	case <-time.After(5 * time.Second):
		return false
	}
}

func (f *renewalWaitingRefresher) BeginScheduledRefreshBatch(ctx context.Context) (context.Context, func(context.Context)) {
	batchCtx, flush := f.batchingMetadataRefresher.BeginScheduledRefreshBatch(ctx)
	return batchCtx, func(flushCtx context.Context) {
		if f.waitForRenewal() {
			f.flushRenews++
		}
		flush(flushCtx)
	}
}

func (f *renewalWaitingRefresher) RefreshScheduledTarget(ctx context.Context, targetType, contentID string) error {
	if !f.waitForRenewal() {
		return errors.New("no claim renewal while the target was refreshing")
	}
	return f.batchingMetadataRefresher.RefreshScheduledTarget(ctx, targetType, contentID)
}

func TestRefreshMetadataTask_RenewsClaimsThroughTheFlush(t *testing.T) {
	candidates := []worker.RefreshCandidate{
		{TargetType: "episode", ContentID: "episode-1", ClaimedAt: time.Unix(100, 0)},
		{TargetType: "season", ContentID: "season-1", ClaimedAt: time.Unix(100, 0)},
	}
	finder := &renewingCandidateFinder{
		fakeRefreshCandidateFinder: fakeRefreshCandidateFinder{batches: [][]worker.RefreshCandidate{candidates}},
		renewals:                   make(chan []worker.RefreshCandidate),
	}
	refresher := &renewalWaitingRefresher{renewals: finder.renewals}
	task := NewRefreshMetadataTask(finder, refresher)

	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	if got := len(refresher.Calls()); got != len(candidates) {
		t.Fatalf("refreshed targets = %d, want %d each renewed while refreshing", got, len(candidates))
	}
	if refresher.flushRenews != 1 {
		t.Fatal("claims were not renewed while the batch flushed")
	}
}

// flakyRenewingFinder fails the given number of claim renewals in a row, then
// renews successfully and reports each success on a channel.
type flakyRenewingFinder struct {
	fakeRefreshCandidateFinder
	failures int
	renewals chan []worker.RefreshCandidate
	mu       sync.Mutex
	calls    int
}

func (f *flakyRenewingFinder) RenewClaims(_ context.Context, candidates []worker.RefreshCandidate) error {
	f.mu.Lock()
	f.calls++
	failing := f.failures < 0 || f.calls <= f.failures
	f.mu.Unlock()
	if failing {
		return errors.New("database unavailable")
	}
	select {
	case f.renewals <- candidates:
	default:
	}
	return nil
}

func (f *flakyRenewingFinder) ClaimRenewInterval() time.Duration { return time.Millisecond }

// claimHoldingRefresher holds each refresh until its context is done, standing
// in for a batch that outlasts its claim lease, and records each flush's
// context error.
type claimHoldingRefresher struct {
	batchingMetadataRefresher
	flushErrs []error
}

func (f *claimHoldingRefresher) BeginScheduledRefreshBatch(ctx context.Context) (context.Context, func(context.Context)) {
	batchCtx, flush := f.batchingMetadataRefresher.BeginScheduledRefreshBatch(ctx)
	return batchCtx, func(flushCtx context.Context) {
		f.batchMu.Lock()
		f.flushErrs = append(f.flushErrs, flushCtx.Err())
		f.batchMu.Unlock()
		flush(flushCtx)
	}
}

func (f *claimHoldingRefresher) RefreshScheduledTarget(ctx context.Context, targetType, contentID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("the batch was not stopped")
	}
}

// A batch whose claims keep failing to renew stops, and its flush runs on the
// stopped context so it settles nothing another node may have claimed since.
func TestRefreshMetadataTask_StopsTheBatchWhenClaimRenewalKeepsFailing(t *testing.T) {
	candidates := []worker.RefreshCandidate{
		{TargetType: "episode", ContentID: "episode-1", ClaimedAt: time.Unix(100, 0)},
		{TargetType: "season", ContentID: "season-1", ClaimedAt: time.Unix(100, 0)},
	}
	finder := &flakyRenewingFinder{
		fakeRefreshCandidateFinder: fakeRefreshCandidateFinder{batches: [][]worker.RefreshCandidate{candidates}},
		failures:                   -1,
		renewals:                   make(chan []worker.RefreshCandidate),
	}
	refresher := &claimHoldingRefresher{}
	task := NewRefreshMetadataTask(finder, refresher)

	err := task.Execute(context.Background(), noopProgressReporter{})
	if !errors.Is(err, errRefreshClaimsLost) {
		t.Fatalf("Execute error = %v, want the lost claims error", err)
	}
	if len(refresher.flushErrs) != 1 || refresher.flushErrs[0] == nil {
		t.Fatalf("flush context errors = %v, want one flush on the stopped batch", refresher.flushErrs)
	}
}

// One failed renewal leaves most of the lease, so the batch carries on.
func TestRefreshMetadataTask_KeepsTheBatchThroughOneFailedRenewal(t *testing.T) {
	candidates := []worker.RefreshCandidate{
		{TargetType: "episode", ContentID: "episode-1", ClaimedAt: time.Unix(100, 0)},
		{TargetType: "season", ContentID: "season-1", ClaimedAt: time.Unix(100, 0)},
	}
	finder := &flakyRenewingFinder{
		fakeRefreshCandidateFinder: fakeRefreshCandidateFinder{batches: [][]worker.RefreshCandidate{candidates}},
		failures:                   1,
		renewals:                   make(chan []worker.RefreshCandidate),
	}
	refresher := &renewalWaitingRefresher{renewals: finder.renewals}
	task := NewRefreshMetadataTask(finder, refresher)

	if err := task.Execute(context.Background(), noopProgressReporter{}); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := len(refresher.Calls()); got != len(candidates) {
		t.Fatalf("refreshed targets = %d, want %d", got, len(candidates))
	}
}
