package trickplay

import (
	"context"
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/mediasample"
)

func TestLocalExtractorCancellationDoesNotStatInput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	req := mediasample.Request{Input: "/missing-trickplay-fixture/input.mkv", Samples: &mediasample.Samples{Seconds: []float64{1}}, Sheets: &mediasample.SheetsOutput{TileWidth: 300, TileHeight: 168, Columns: 10, Rows: 10, Quality: Quality}}
	_, err := NewLocalExtractor(nil).Extract(ctx, nil, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost behind input preflight: %v", err)
	}
}

type tallSheetExtractor struct{ fakeExtractor }

func (e *tallSheetExtractor) Extract(ctx context.Context, job *Job, req mediasample.Request) (mediasample.Result, error) {
	result, err := e.fakeExtractor.Extract(ctx, job, req)
	result.SheetTileHeight = 1920
	return result, err
}

func TestGenerateBoundsChunksByActualDisplayGeometry(t *testing.T) {
	q := newFakeQueue()
	e := &tallSheetExtractor{}
	s := testService(q, &fakeStore{}, e, fakeSettings{IntervalSetting: "10"})
	s.process(t.Context(), testJob(42, 18000))
	if _, ok := q.published[42]; !ok {
		t.Fatal("tall preview was not published")
	}
	for _, req := range e.requests {
		perSheet := req.Sheets.Columns * req.Sheets.Rows
		count := (len(req.Samples.Seconds) + perSheet - 1) / perSheet
		pixels := req.Sheets.TileWidth * 1920 * perSheet * count
		if pixels > 64<<20 {
			t.Fatalf("request carries %d pixels across %d sheets", pixels, count)
		}
	}
}
