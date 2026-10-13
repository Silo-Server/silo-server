package chapterthumbs

import (
	"context"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type markerThumbnailRepository interface {
	UpdateMarkerThumbnailState(context.Context, *models.MediaFile, []models.MarkerThumbnail) (*models.MediaFile, error)
}

type markerThumbnailNotifier interface {
	MarkerThumbnailReady(context.Context, int, models.MarkerThumbnail)
}

func hasEligibleMarker(file *models.MediaFile, now time.Time, width int) bool {
	for _, marker := range models.EffectiveMarkerThumbnails(file) {
		if isChapterEligible(marker.MediaChapter, now, width) {
			return true
		}
	}
	return false
}

// deferUnrecoverableMarkerImages puts pending images from an expired provider
// snapshot on the retry cooldown. Their ranges were never stored, so only a new
// lookup can rebuild them; without the cooldown the width and backfill sweeps
// would list them on every pass.
func (s *Service) deferUnrecoverableMarkerImages(ctx context.Context, file *models.MediaFile, width int, now time.Time) {
	repo, ok := s.fileRepo.(markerThumbnailRepository)
	if !ok || len(file.MarkerThumbnails) == 0 {
		return
	}
	live := make(map[string]bool)
	for _, marker := range models.EffectiveMarkerThumbnails(file) {
		live[marker.Identity] = true
	}
	var deferred []models.MarkerThumbnail
	for _, image := range file.MarkerThumbnails {
		if !live[image.Identity] && isChapterEligible(image.MediaChapter, now, width) {
			recordChapterFailure(&image.MediaChapter, now, models.MarkerSnapshotExpired, nil)
			deferred = append(deferred, image)
		}
	}
	if len(deferred) == 0 {
		return
	}
	if _, err := repo.UpdateMarkerThumbnailState(ctx, file, deferred); err != nil {
		slog.WarnContext(ctx, "marker thumbnail deferral failed", "component", "chapterthumbs", "file_id", file.ID, "error", err)
	}
}

// processMarkers shares the request's bounded frame budget and hardware/storage
// policy. Navigation metadata remains available when extraction or storage fails.
func (s *Service) processMarkers(ctx context.Context, file *models.MediaFile, req ChapterThumbnailRequest, priority bool, budget int, hdrPolicy string, width int, now time.Time) (bool, error) {
	repo, ok := s.fileRepo.(markerThumbnailRepository)
	if !ok {
		return false, nil
	}
	markers := models.EffectiveMarkerThumbnails(file)
	if budget <= 0 {
		return hasEligibleMarker(file, now, width), nil
	}
	var selected []int
	for i, marker := range markers {
		if isChapterEligible(marker.MediaChapter, now, width) {
			selected = append(selected, i)
		}
	}
	if priority && req.TargetSeconds != nil {
		sort.SliceStable(selected, func(i, j int) bool {
			return math.Abs(markers[selected[i]].StartSeconds-*req.TargetSeconds) < math.Abs(markers[selected[j]].StartSeconds-*req.TargetSeconds)
		})
	}
	if len(selected) > budget {
		selected = selected[:budget]
	}
	if len(selected) == 0 {
		return false, nil
	}
	generated := make(map[string]bool)
	for _, i := range selected {
		marker := &markers[i]
		frame, reason, err := s.extractFrame(ctx, file, marker.StartSeconds, hdrPolicy)
		if err == nil {
			var path, hash string
			path, hash, err = s.uploadThumbnail(ctx, file.ID, "marker-"+marker.Identity, frame, width)
			if err == nil {
				applyChapterSuccess(&marker.MediaChapter, path, hash)
				generated[marker.Identity] = true
				continue
			}
			reason = "marker_upload_failed"
		}
		recordChapterFailure(&marker.MediaChapter, now, reason, err)
		slog.WarnContext(ctx, "marker thumbnail generation failed", "component", "chapterthumbs", "file_id", file.ID, "kind", marker.Kind, "start_seconds", marker.StartSeconds, "reason", reason, "error", err)
	}
	saved, err := repo.UpdateMarkerThumbnailState(ctx, file, markers)
	if err != nil {
		return false, err
	}
	if saved == nil {
		return false, nil
	} // Superseded snapshot; trigger/sweep will retry current markers.
	if notifier, ok := s.notifier.(markerThumbnailNotifier); ok {
		for _, marker := range models.EffectiveMarkerThumbnails(saved) {
			if generated[marker.Identity] {
				notifier.MarkerThumbnailReady(ctx, file.ID, marker)
			}
		}
	}
	return hasEligibleMarker(saved, now, width), nil
}
