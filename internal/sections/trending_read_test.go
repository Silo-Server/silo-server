package sections

import (
	"context"
	"errors"
	"testing"
)

type fakeSnapshotGetter struct {
	snap  TrendingSnapshot
	found bool
	err   error
}

func (f fakeSnapshotGetter) Get(context.Context, string, string) (TrendingSnapshot, bool, error) {
	return f.snap, f.found, f.err
}

func TestLoadTrendingDiscoverContentIDsReadsSnapshot(t *testing.T) {
	f := &Fetcher{TrendingSnapshots: fakeSnapshotGetter{
		snap:  TrendingSnapshot{ContentIDs: []string{"a", "b"}},
		found: true,
	}}
	ids, err := f.loadTrendingDiscoverContentIDs(context.Background(), "tmdb", "week")
	if err != nil {
		t.Fatalf("loadTrendingDiscoverContentIDs: %v", err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ids = %v; want [a b]", ids)
	}
}

func TestLoadTrendingDiscoverContentIDsNilGetter(t *testing.T) {
	f := &Fetcher{}
	ids, err := f.loadTrendingDiscoverContentIDs(context.Background(), "tmdb", "week")
	if err != nil {
		t.Fatalf("loadTrendingDiscoverContentIDs: %v", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v; want nil for nil getter", ids)
	}
}

func TestLoadTrendingDiscoverContentIDsNotFound(t *testing.T) {
	f := &Fetcher{TrendingSnapshots: fakeSnapshotGetter{found: false}}
	ids, err := f.loadTrendingDiscoverContentIDs(context.Background(), "tmdb", "week")
	if err != nil {
		t.Fatalf("loadTrendingDiscoverContentIDs: %v", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v; want nil when no snapshot exists", ids)
	}
}

func TestLoadTrendingDiscoverContentIDsPropagatesError(t *testing.T) {
	boom := errors.New("boom")
	f := &Fetcher{TrendingSnapshots: fakeSnapshotGetter{err: boom}}
	ids, err := f.loadTrendingDiscoverContentIDs(context.Background(), "tmdb", "week")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v; want boom", err)
	}
	if ids != nil {
		t.Fatalf("ids = %v; want nil on error", ids)
	}
}
