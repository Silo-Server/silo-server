package chapterthumbs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobgc"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/scanner"
)

func TestMarkerOnlyThumbnailsAndStaleEditsDB(t *testing.T) {
	pool := chapterURLTestPool(t, nil)
	id, _ := chapterURLTestFile(t, pool)
	_, err := pool.Exec(t.Context(), `UPDATE media_files SET chapters='[]',duration=36,intro_start=6,intro_end=9,credits_start=30,credits_end=33 WHERE id=$1`, id)
	if err != nil {
		t.Fatal(err)
	}
	repo := scanner.NewFileRepository(pool)
	original, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, &testFolderRepo{folder: &models.MediaFolder{Enabled: true, ChapterThumbnailsEnabled: true}}, nil, nil, store, nil, nil, "", "", "", 1)
	queue := blobgc.NewQueue(pool)
	service.SetBlobQueue(queue)
	var captures []float64
	service.extractFrameFunc = func(_ context.Context, _ *models.MediaFile, seconds float64, _ string) ([]byte, string, error) {
		captures = append(captures, seconds)
		return testFrameJPEG(t, 320, 180), "", nil
	}
	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: id}, false); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 2 || captures[0] != 6 || captures[1] != 30 {
		t.Fatalf("captures %v", captures)
	}
	saved, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Chapters) != 0 || len(saved.MarkerThumbnails) != 2 {
		t.Fatalf("chapters=%d marker images=%d", len(saved.Chapters), len(saved.MarkerThumbnails))
	}
	for _, image := range saved.MarkerThumbnails {
		if _, ok := imageKeyGroup(image.ThumbnailPath); !ok {
			t.Fatalf("invalid key %q", image.ThumbnailPath)
		}
	}
	// Issuing a URL is protected before a range edit retires its reference.
	expiry := time.Now().Add(72 * time.Hour)
	resolver := NewURLResolver(pool, chapterTestURLs{expiry: expiry})
	key := saved.MarkerThumbnails[0].ThumbnailPath
	if resolver.ResolveURLs(t.Context(), []string{key})[key].URL == "" {
		t.Fatal("current marker URL not resolved")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET intro_start=12,intro_end=15 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	stale, err := repo.UpdateMarkerThumbnailState(t.Context(), original, saved.MarkerThumbnails)
	if err != nil || stale != nil {
		t.Fatalf("stale persistence: %v %v", stale, err)
	}
	if resolver.ResolveURLs(t.Context(), []string{key})[key].URL != "" {
		t.Fatal("retired marker received a new URL")
	}
	var deadline time.Time
	if err := pool.QueryRow(t.Context(), `SELECT not_before FROM blob_gc_queue WHERE prefix=$1`, key).Scan(&deadline); err != nil {
		t.Fatal(err)
	}
	if deadline.Before(expiry) {
		t.Fatal("range edit shortened issued URL lifetime")
	}
	// New occurrence can fail independently without dropping navigation data.
	service.extractFrameFunc = func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
		return nil, "transcode_node_unavailable", errors.New("unavailable")
	}
	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: id}, false); err != nil {
		t.Fatal(err)
	}
	failed, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(models.EffectiveMarkerSegments(failed)) != 2 || len(failed.MarkerThumbnails) != 2 || failed.MarkerThumbnails[0].ThumbnailRetryAfter == nil {
		t.Fatal("failure lost markers or retry state")
	}
	missing, err := repo.ListMissingChapterThumbnails(t.Context(), 100, "/w300.webp")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range missing {
		if file.ID == id {
			t.Fatal("cooldown did not prevent a hot loop")
		}
	}
}

func TestOnDemandMarkerThumbnailsDB(t *testing.T) {
	pool := chapterURLTestPool(t, nil)
	id, _ := chapterURLTestFile(t, pool)
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET chapters='[]',duration=36 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	repo := scanner.NewFileRepository(pool)
	canonical, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, &testFolderRepo{folder: &models.MediaFolder{Enabled: true, ChapterThumbnailsEnabled: true}}, nil, nil, store, nil, nil, "", "", "", 1)
	service.SetBlobQueue(blobgc.NewQueue(pool))
	overlay := *canonical
	overlay.MarkerThumbnailBaseSegments = models.EffectiveMarkerSegments(canonical)
	overlay.MarkerSegments = []models.MarkerSegment{{Kind: "intro", StartSeconds: 6, EndSeconds: 9}, {Kind: "credits", StartSeconds: 30, EndSeconds: 33}}
	service.PrepareMarkerFile(t.Context(), &overlay)
	var captures []float64
	service.extractFrameFunc = func(_ context.Context, _ *models.MediaFile, at float64, _ string) ([]byte, string, error) {
		captures = append(captures, at)
		return testFrameJPEG(t, 320, 180), "", nil
	}
	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: id}, false); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 2 || captures[0] != 6 || captures[1] != 30 {
		t.Fatalf("captures %v", captures)
	}
	saved, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(models.EffectiveMarkerSegments(saved)) != 0 {
		t.Fatal("persisted on-demand ranges")
	}
	var raw string
	if err := pool.QueryRow(t.Context(), `SELECT marker_thumbnails::text FROM media_files WHERE id=$1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"start_seconds", "end_seconds", "kind", "source"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("persisted provider metadata: %s", raw)
		}
	}
	service.PrepareMarkerFile(t.Context(), &overlay)
	resolver := NewURLResolver(pool, chapterTestURLs{expiry: time.Now().Add(time.Hour)})
	previews := catalog.BuildMarkerPreviews(t.Context(), &overlay, resolver.ResolveURLs)
	if len(previews) != 2 || previews[0].StartSeconds != 6 || previews[1].StartSeconds != 30 {
		t.Fatalf("previews %+v", previews)
	}
	oldKey := overlay.MarkerThumbnails[0].ThumbnailPath
	stale := overlay
	replacement := overlay
	replacement.MarkerSegments = []models.MarkerSegment{{Kind: "intro", StartSeconds: 12, EndSeconds: 15}}
	service.PrepareMarkerFile(t.Context(), &replacement)
	if got, err := repo.UpdateMarkerThumbnailState(t.Context(), &stale, models.EffectiveMarkerThumbnails(&stale)); err != nil || got != nil {
		t.Fatalf("superseded snapshot accepted: %v %v", got, err)
	}
	if resolver.ResolveURLs(t.Context(), []string{oldKey})[oldKey].URL != "" {
		t.Fatal("superseded URL reissued")
	}
	// A stale API lookup cannot override a manual edit, and neither it nor a
	// disabled library can register new extraction work.
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET intro_start=2,intro_end=4 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.PrepareMarkerThumbnailState(t.Context(), &replacement); err != nil || got != nil {
		t.Fatalf("manual edit ignored: %v %v", got, err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE media_folders SET chapter_thumbnails_enabled=false WHERE id=$1`, canonical.MediaFolderID); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.PrepareMarkerThumbnailState(t.Context(), &replacement); err != nil || got != nil {
		t.Fatalf("disabled library accepted: %v %v", got, err)
	}
}

func TestCanonicalWorkerPreservesOnDemandInventoryDB(t *testing.T) {
	pool := chapterURLTestPool(t, nil)
	id, _ := chapterURLTestFile(t, pool)
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET duration=36,intro_start=2,intro_end=4 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	repo := scanner.NewFileRepository(pool)
	canonical, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	overlay := *canonical
	overlay.MarkerThumbnailBaseSegments = models.EffectiveMarkerSegments(canonical)
	overlay.IntroStart, overlay.IntroEnd = nil, nil
	overlay.MarkerSegments = []models.MarkerSegment{{Kind: "intro", StartSeconds: 6, EndSeconds: 9}, {Kind: "credits", StartSeconds: 30, EndSeconds: 33}}
	registered, err := repo.PrepareMarkerThumbnailState(t.Context(), &overlay)
	if err != nil || registered == nil {
		t.Fatalf("prepare %v %v", registered, err)
	}
	// A replica without the ephemeral overlay cannot add an obsolete canonical
	// occurrence to the registered inventory and invalidate its generation fence.
	if got, err := repo.UpdateMarkerThumbnailState(t.Context(), canonical, models.EffectiveMarkerThumbnails(canonical)); err != nil || got != nil {
		t.Fatalf("old canonical worker changed inventory: %v %v", got, err)
	}
	// Canonical occurrences still present in the effective set may be updated,
	// preserving the independent provider occurrence.
	overlay.MarkerSegments[0] = models.MarkerSegment{Kind: "intro", StartSeconds: 2, EndSeconds: 4}
	registered, err = repo.PrepareMarkerThumbnailState(t.Context(), &overlay)
	if err != nil || registered == nil {
		t.Fatalf("prepare %v %v", registered, err)
	}
	candidates := models.EffectiveMarkerThumbnails(registered)
	candidates[1].ThumbnailThumbhash = "provider-state"
	registered, err = repo.UpdateMarkerThumbnailState(t.Context(), registered, candidates)
	if err != nil || registered == nil {
		t.Fatalf("persist %v %v", registered, err)
	}
	canonical, err = repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	manual := models.EffectiveMarkerThumbnails(canonical)
	manual[0].ThumbnailThumbhash = "manual-state"
	got, err := repo.UpdateMarkerThumbnailState(t.Context(), canonical, manual)
	if err != nil || got == nil {
		t.Fatalf("canonical update %v %v", got, err)
	}
	if len(got.MarkerThumbnails) != 2 || got.MarkerThumbnails[1].ThumbnailThumbhash != "provider-state" {
		t.Fatal("canonical update dropped provider image")
	}
}

// Credits may end within a second of the truncated duration; markers that can
// never be previewed must not hold a backfill slot on every sweep.
func TestMarkerThumbnailDurationEdgesDB(t *testing.T) {
	pool := chapterURLTestPool(t, nil)
	id, _ := chapterURLTestFile(t, pool)
	repo := scanner.NewFileRepository(pool)
	listed := func() bool {
		t.Helper()
		missing, err := repo.ListMissingChapterThumbnails(t.Context(), 100, "/w300.webp")
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range missing {
			if file.ID == id {
				return true
			}
		}
		return false
	}
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET chapters='[]',duration=36,credits_start=40,credits_end=44 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if listed() {
		t.Fatal("a marker past the end was listed for backfill")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET credits_start=30,credits_end=36.5 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if !listed() {
		t.Fatal("credits ending within the duration tolerance were not listed")
	}
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, &testFolderRepo{folder: &models.MediaFolder{Enabled: true, ChapterThumbnailsEnabled: true}}, nil, nil, store, nil, nil, "", "", "", 1)
	service.SetBlobQueue(blobgc.NewQueue(pool))
	service.extractFrameFunc = func(context.Context, *models.MediaFile, float64, string) ([]byte, string, error) {
		return testFrameJPEG(t, 320, 180), "", nil
	}
	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: id}, false); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if images := models.EffectiveMarkerThumbnails(saved); len(images) != 1 || images[0].ThumbnailPath == "" {
		t.Fatalf("credits preview missing: %+v", images)
	}
	if listed() {
		t.Fatal("a file with every preview generated stayed listed")
	}
}

// Review edge cases: a marker starting in the truncated final second, file
// deletion of a marker-only file, and images whose range only an expired
// provider snapshot knew.
func TestMarkerThumbnailLifecycleEdgesDB(t *testing.T) {
	pool := chapterURLTestPool(t, nil)
	repo := scanner.NewFileRepository(pool)
	store, err := blobstore.NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, &testFolderRepo{folder: &models.MediaFolder{Enabled: true, ChapterThumbnailsEnabled: true}}, nil, nil, store, nil, nil, "", "", "", 1)
	service.SetBlobQueue(blobgc.NewQueue(pool))
	var captures []float64
	service.extractFrameFunc = func(_ context.Context, _ *models.MediaFile, at float64, _ string) ([]byte, string, error) {
		captures = append(captures, at)
		return testFrameJPEG(t, 320, 180), "", nil
	}

	finalSecond, _ := chapterURLTestFile(t, pool)
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET chapters='[]',duration=36,credits_start=36.2,credits_end=36.7 WHERE id=$1`, finalSecond); err != nil {
		t.Fatal(err)
	}
	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: finalSecond}, false); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 1 || captures[0] != 36.2 {
		t.Fatalf("final-second marker captures %v", captures)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM media_files WHERE id=$1`, finalSecond); err != nil {
		t.Fatal(err)
	}
	var queued bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM blob_gc_queue WHERE prefix=$1)`, fmt.Sprintf("chapter-images/%d/", finalSecond)).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if !queued {
		t.Fatal("deleting a marker-only file left its images unqueued")
	}

	id, _ := chapterURLTestFile(t, pool)
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET chapters='[]',duration=36 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	canonical, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	overlay := *canonical
	overlay.MarkerThumbnailBaseSegments = models.EffectiveMarkerSegments(canonical)
	overlay.MarkerSegments = []models.MarkerSegment{{Kind: "intro", StartSeconds: 6, EndSeconds: 9}}
	identity := models.EffectiveMarkerThumbnails(&overlay)[0].Identity
	oldWidth := fmt.Sprintf("chapter-images/%d/marker-%s-%s/w200.webp", id, identity, strings.Repeat("a", 64))
	if _, err := pool.Exec(t.Context(), `UPDATE media_files SET marker_thumbnails=jsonb_build_array(jsonb_build_object('identity',$2::text,'thumbnail_path',$3::text)) WHERE id=$1`, id, identity, oldWidth); err != nil {
		t.Fatal(err)
	}
	listedAtOtherWidth := func() bool {
		t.Helper()
		files, _, err := repo.ListChapterThumbnailsAtOtherWidths(t.Context(), 100, "/w300.webp", 0, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if file.ID == id {
				return true
			}
		}
		return false
	}
	if !listedAtOtherWidth() {
		t.Fatal("old-width provider image was not listed")
	}
	// No live snapshot: the range cannot be rebuilt, so the image waits.
	captures = nil
	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: id}, false); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 0 || listedAtOtherWidth() {
		t.Fatalf("unrecoverable image was retried: captures %v", captures)
	}
	// A new lookup supplies the range and lifts the deferral.
	service.PrepareMarkerFile(t.Context(), &overlay)
	if _, err := service.processRequest(t.Context(), ChapterThumbnailRequest{FileID: id}, false); err != nil {
		t.Fatal(err)
	}
	saved, err := repo.GetByID(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(captures) != 1 || captures[0] != 6 || len(saved.MarkerThumbnails) != 1 || !strings.HasSuffix(saved.MarkerThumbnails[0].ThumbnailPath, "/w300.webp") {
		t.Fatalf("lookup did not regenerate: captures %v images %+v", captures, saved.MarkerThumbnails)
	}
}
