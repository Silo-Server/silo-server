package tasks

import (
	"context"
	"encoding/json"
	"testing"
)

type aliasBackfillPage struct {
	next  string
	count int
}

type fakeAliasBackfiller struct {
	pages   []aliasBackfillPage
	cursors []string
	resets  int
}

func (f *fakeAliasBackfiller) ResetCompletedBackfill(context.Context) error {
	f.resets++
	return nil
}

func (f *fakeAliasBackfiller) BackfillBatch(_ context.Context, cursor string, limit int) (string, int, error) {
	f.cursors = append(f.cursors, cursor)
	if limit != mediaItemAliasBackfillBatchSize {
		panic("unexpected batch size")
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page.next, page.count, nil
}

type aliasBackfillProgress struct {
	result json.RawMessage
}

func (*aliasBackfillProgress) Report(float64, string) {}
func (p *aliasBackfillProgress) SetResultData(data json.RawMessage) {
	p.result = append(p.result[:0], data...)
}

func TestBackfillMediaItemAliasesTaskAdvancesCursorUntilShortBatch(t *testing.T) {
	t.Parallel()

	backfiller := &fakeAliasBackfiller{pages: []aliasBackfillPage{
		{next: "item-0500", count: mediaItemAliasBackfillBatchSize},
		{next: "item-0502", count: 2},
	}}
	progress := &aliasBackfillProgress{}
	if err := NewBackfillMediaItemAliasesTask(backfiller).Execute(context.Background(), progress); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(backfiller.cursors) != 2 || backfiller.cursors[0] != "" || backfiller.cursors[1] != "item-0500" {
		t.Fatalf("cursors = %#v", backfiller.cursors)
	}
	if backfiller.resets != 1 {
		t.Fatalf("completed-state resets = %d, want exactly 1 before batching", backfiller.resets)
	}
	var result map[string]any
	if err := json.Unmarshal(progress.result, &result); err != nil {
		t.Fatalf("result JSON = %q: %v", progress.result, err)
	}
	if result["processed_items"] != float64(502) || result["last_content_id"] != "item-0502" {
		t.Fatalf("result = %#v", result)
	}
}
