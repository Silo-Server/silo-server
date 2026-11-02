package metadata

import (
	"context"
	"strings"
	"testing"
)

func TestShouldBulkReset(t *testing.T) {
	if shouldBulkReset(0, 0) {
		t.Fatal("empty probe must not trigger bulk reset")
	}
	if shouldBulkReset(100, 94) {
		t.Fatal("94% missing is below the bulk threshold")
	}
	if !shouldBulkReset(100, 95) {
		t.Fatal("95% missing must trigger bulk reset")
	}
	// A probe thinned below the minimum successful-sample bar (transport
	// errors, tiny catalog) must take the safe per-row path even at a 100%
	// miss rate — a handful of surviving 404s is not a mandate to bulk-reset.
	if shouldBulkReset(artworkReconcileBulkMinSample-1, artworkReconcileBulkMinSample-1) {
		t.Fatal("below-minimum sample must not trigger bulk reset")
	}
	if !shouldBulkReset(artworkReconcileBulkMinSample, artworkReconcileBulkMinSample) {
		t.Fatal("all-missing probe at the minimum sample size must trigger bulk reset")
	}
}

func TestBuildSweepBatchQueryUsesNativeNumericKeys(t *testing.T) {
	var peopleSurface, folderSurface artworkSweepSurface
	for _, surface := range artworkSweepSurfaces() {
		switch surface.name {
		case "person photos":
			peopleSurface = surface
		case "library posters":
			folderSurface = surface
		}
	}
	if peopleSurface.name == "" || folderSurface.name == "" {
		t.Fatalf("surface lookup failed: person photos=%q library posters=%q", peopleSurface.name, folderSurface.name)
	}

	query, args, err := buildSweepBatchQuery(peopleSurface, []string{"128111764822294558"})
	if err != nil {
		t.Fatalf("build people query: %v", err)
	}
	if !strings.Contains(query, "SELECT (id)::text") ||
		!strings.Contains(query, "AND (id) > ($1)") ||
		!strings.Contains(query, "ORDER BY id LIMIT $2") {
		t.Fatalf("people query does not keep pagination on native id: %s", query)
	}
	if _, ok := args[0].(int64); !ok {
		t.Fatalf("people cursor type = %T, want int64", args[0])
	}

	_, args, err = buildSweepBatchQuery(folderSurface, []string{"3000000000"})
	if err != nil {
		t.Fatalf("build folder query: %v", err)
	}
	if value, ok := args[0].(int64); !ok || value != 3_000_000_000 {
		t.Fatalf("folder cursor = %v (%T), want int64(3000000000)", args[0], args[0])
	}
}

type scriptedArtworkBatch struct {
	cursor      []string
	done        int
	verified    int
	sweepErrors int
}

type scriptedArtworkChapterBatch struct {
	cursor      int64
	rows        int
	verified    int
	sweepErrors int
}

type scriptedArtworkSweepCall struct {
	name   string
	cursor []string
	done   int
}

type scriptedArtworkChapterSweepCall struct {
	cursor int64
	done   int
}

type scriptedArtworkVerifySweeper struct {
	batches           map[string][]scriptedArtworkBatch
	chapterBatches    []scriptedArtworkChapterBatch
	calls             []scriptedArtworkSweepCall
	chapterCalls      []scriptedArtworkChapterSweepCall
	processed         map[string]int
	chaptersProcessed int
}

func (s *scriptedArtworkVerifySweeper) sweepSurfaceFrom(
	_ context.Context,
	surface artworkSweepSurface,
	stats *ArtworkReconcileStats,
	cursor []string,
	done int,
	onBatch func([]string, int, bool) error,
) error {
	s.calls = append(s.calls, scriptedArtworkSweepCall{
		name:   surface.name,
		cursor: append([]string(nil), cursor...),
		done:   done,
	})
	if s.processed == nil {
		s.processed = make(map[string]int)
	}
	for _, batch := range s.batches[surface.name] {
		s.processed[surface.name]++
		stats.Checked += batch.verified + batch.sweepErrors
		stats.Verified += batch.verified
		stats.Errors += batch.sweepErrors
		stats.SweepErrors += batch.sweepErrors
		if err := onBatch(batch.cursor, batch.done, batch.sweepErrors == 0); err != nil {
			return err
		}
	}
	return nil
}

func (s *scriptedArtworkVerifySweeper) sweepChapterThumbnailsFrom(
	_ context.Context,
	stats *ArtworkReconcileStats,
	cursor int64,
	done int,
	onBatch func(int64, int, bool) error,
) error {
	s.chapterCalls = append(s.chapterCalls, scriptedArtworkChapterSweepCall{cursor: cursor, done: done})
	for _, batch := range s.chapterBatches {
		if batch.cursor <= cursor {
			continue
		}
		s.chaptersProcessed++
		stats.Checked += batch.verified + batch.sweepErrors
		stats.Verified += batch.verified
		stats.Errors += batch.sweepErrors
		stats.SweepErrors += batch.sweepErrors
		done += batch.rows
		if err := onBatch(batch.cursor, done, batch.sweepErrors == 0); err != nil {
			return err
		}
	}
	return nil
}

func TestArtworkReconcileStopsAtUnsafeBatchAndResumesFromLastCheckpoint(t *testing.T) {
	surfaces := []artworkSweepSurface{{name: "posters"}, {name: "later surface"}}
	checkpoint := ArtworkReconcileCheckpoint{
		Version: artworkReconcileCheckpointVersion,
		Totals:  []int{1001, 1},
		Stats:   ArtworkReconcileStats{Mode: ArtworkReconcileModeVerify},
	}
	firstRun := &scriptedArtworkVerifySweeper{batches: map[string][]scriptedArtworkBatch{
		"posters": {
			{cursor: []string{"0499"}, done: 500, verified: 500},
			{cursor: []string{"0999"}, done: 1000, verified: 499, sweepErrors: 1},
			{cursor: []string{"1000"}, done: 1001, verified: 1},
		},
		"later surface": {{cursor: []string{"later"}, done: 1, verified: 1}},
	}}
	var saved ArtworkReconcileCheckpoint
	saveCount := 0
	stats, err := runArtworkVerifySweep(context.Background(), firstRun, surfaces, checkpoint,
		func(next ArtworkReconcileCheckpoint) error {
			saveCount++
			saved = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, func(float64, string) {})
	if err == nil || !strings.Contains(err.Error(), "last saved checkpoint") {
		t.Fatalf("first sweep error = %v, want checkpoint stop", err)
	}
	if stats.SweepErrors != 1 {
		t.Fatalf("first sweep errors = %d, want 1", stats.SweepErrors)
	}
	if firstRun.processed["posters"] != 2 || firstRun.processed["later surface"] != 0 || firstRun.chaptersProcessed != 0 {
		t.Fatalf("work after unsafe batch: processed=%v chapter_batches=%d", firstRun.processed, firstRun.chaptersProcessed)
	}
	if saveCount != 1 || saved.SurfaceDone != 500 || len(saved.SurfaceCursor) != 1 || saved.SurfaceCursor[0] != "0499" {
		t.Fatalf("saved checkpoint = %#v (save count %d), want safe first batch", saved, saveCount)
	}

	resumedRun := &scriptedArtworkVerifySweeper{batches: map[string][]scriptedArtworkBatch{
		"posters": {
			{cursor: []string{"0999"}, done: 1000, verified: 500},
			{cursor: []string{"1000"}, done: 1001, verified: 1},
		},
		"later surface": {{cursor: []string{"later"}, done: 1, verified: 1}},
	}}
	var completed ArtworkReconcileCheckpoint
	stats, err = runArtworkVerifySweep(context.Background(), resumedRun, surfaces, saved,
		func(next ArtworkReconcileCheckpoint) error {
			completed = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, func(float64, string) {})
	if err != nil {
		t.Fatalf("resumed sweep: %v", err)
	}
	if len(resumedRun.calls) == 0 || resumedRun.calls[0].done != 500 ||
		len(resumedRun.calls[0].cursor) != 1 || resumedRun.calls[0].cursor[0] != "0499" {
		t.Fatalf("resume call = %#v, want cursor 0499 at 500 rows", resumedRun.calls)
	}
	if stats.Verified != 1002 || !completed.Complete() {
		t.Fatalf("resumed stats/checkpoint = %#v / %#v, want 1002 verified and complete", stats, completed)
	}
}

func TestArtworkReconcileStopsAndResumesChapterThumbnailsFromLastCheckpoint(t *testing.T) {
	checkpoint := ArtworkReconcileCheckpoint{
		Version:      artworkReconcileCheckpointVersion,
		ChapterTotal: 3,
		Stats:        ArtworkReconcileStats{Mode: ArtworkReconcileModeVerify},
	}
	firstRun := &scriptedArtworkVerifySweeper{chapterBatches: []scriptedArtworkChapterBatch{
		{cursor: 10, rows: 1, verified: 1},
		{cursor: 20, rows: 1, sweepErrors: 1},
		{cursor: 30, rows: 1, verified: 1},
	}}
	var saved ArtworkReconcileCheckpoint
	saveCount := 0

	stats, err := runArtworkVerifySweep(context.Background(), firstRun, nil, checkpoint,
		func(next ArtworkReconcileCheckpoint) error {
			saveCount++
			saved = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, func(float64, string) {})
	if err == nil || !strings.Contains(err.Error(), "last saved checkpoint") {
		t.Fatalf("first chapter sweep error = %v, want checkpoint stop", err)
	}
	if stats.SweepErrors != 1 || firstRun.chaptersProcessed != 2 {
		t.Fatalf("first chapter sweep stats/work = %#v / %d batches, want 1 error after 2 batches", stats, firstRun.chaptersProcessed)
	}
	if saveCount != 1 || saved.ChapterCursor != 10 || saved.ChapterDone != 1 {
		t.Fatalf("saved chapter checkpoint = %#v (save count %d), want cursor 10 at 1 row", saved, saveCount)
	}

	resumedRun := &scriptedArtworkVerifySweeper{chapterBatches: []scriptedArtworkChapterBatch{
		{cursor: 10, rows: 1, verified: 1},
		{cursor: 20, rows: 1, verified: 1},
		{cursor: 30, rows: 1, verified: 1},
	}}
	var completed ArtworkReconcileCheckpoint
	stats, err = runArtworkVerifySweep(context.Background(), resumedRun, nil, saved,
		func(next ArtworkReconcileCheckpoint) error {
			completed = cloneArtworkReconcileCheckpoint(next)
			return nil
		}, func(float64, string) {})
	if err != nil {
		t.Fatalf("resumed chapter sweep: %v", err)
	}
	if len(resumedRun.chapterCalls) != 1 || resumedRun.chapterCalls[0].cursor != 10 || resumedRun.chapterCalls[0].done != 1 {
		t.Fatalf("chapter resume call = %#v, want cursor 10 at 1 row", resumedRun.chapterCalls)
	}
	if resumedRun.chaptersProcessed != 2 {
		t.Fatalf("resumed chapter batches = %d, want 2 (saved batch skipped)", resumedRun.chaptersProcessed)
	}
	if stats.Verified != 3 || !completed.Complete() {
		t.Fatalf("resumed chapter stats/checkpoint = %#v / %#v, want 3 verified and complete", stats, completed)
	}
}

func TestArtworkReconcileWithoutSaverContinuesAfterUnsafeBatches(t *testing.T) {
	surfaces := []artworkSweepSurface{{name: "posters"}, {name: "later surface"}}
	checkpoint := ArtworkReconcileCheckpoint{
		Version:      artworkReconcileCheckpointVersion,
		Totals:       []int{1, 1},
		ChapterTotal: 2,
		Stats:        ArtworkReconcileStats{Mode: ArtworkReconcileModeVerify},
	}
	sweeper := &scriptedArtworkVerifySweeper{
		batches: map[string][]scriptedArtworkBatch{
			"posters":       {{cursor: []string{"poster"}, done: 1, sweepErrors: 1}},
			"later surface": {{cursor: []string{"later"}, done: 1, verified: 1}},
		},
		chapterBatches: []scriptedArtworkChapterBatch{
			{cursor: 10, rows: 1, sweepErrors: 1},
			{cursor: 20, rows: 1, verified: 1},
		},
	}

	stats, err := runArtworkVerifySweep(
		context.Background(), sweeper, surfaces, checkpoint, nil, func(float64, string) {},
	)
	if err != nil {
		t.Fatalf("sweep without saver: %v", err)
	}
	if sweeper.processed["posters"] != 1 || sweeper.processed["later surface"] != 1 || sweeper.chaptersProcessed != 2 {
		t.Fatalf("work after unsafe batches: surfaces=%v chapter_batches=%d", sweeper.processed, sweeper.chaptersProcessed)
	}
	if stats.Verified != 2 || stats.SweepErrors != 2 {
		t.Fatalf("stats = %#v, want 2 verified and 2 sweep errors", stats)
	}
}
