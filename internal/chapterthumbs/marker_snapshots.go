package chapterthumbs

import (
	"context"
	"log/slog"
	"slices"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

const markerSnapshotTTL = 15 * time.Minute
const markerSnapshotLimit = 512

type markerThumbnailSnapshot struct {
	file    models.MediaFile
	expires time.Time
}

type markerThumbnailPreparer interface {
	PrepareMarkerThumbnailState(context.Context, *models.MediaFile) (*models.MediaFile, error)
}

// PrepareMarkerFile is called after a successful provider lookup, including a
// cached on-demand lookup. It never fetches providers in the background.
// The caller owns file; refreshing its image metadata also supports watch reads.
func (s *Service) PrepareMarkerFile(ctx context.Context, file *models.MediaFile) {
	if file == nil {
		return
	}
	if file.MarkerThumbnailBaseSegments == nil {
		s.QueueFileIDs(ctx, []int{file.ID})
		return
	}
	repo, ok := s.fileRepo.(markerThumbnailPreparer)
	if !ok {
		return
	}
	prepared, err := repo.PrepareMarkerThumbnailState(ctx, file)
	if err != nil {
		slog.WarnContext(ctx, "prepare on-demand marker thumbnails failed", "component", "chapterthumbs", "file_id", file.ID, "error", err)
		return
	}
	if prepared == nil {
		return
	}
	file.MarkerThumbnails = prepared.MarkerThumbnails
	snapshot := *prepared
	snapshot.MarkerSegments = slices.Clone(models.EffectiveMarkerSegments(prepared))
	snapshot.MarkerThumbnailBaseSegments = slices.Clone(prepared.MarkerThumbnailBaseSegments)
	now := s.now()
	s.mu.Lock()
	if s.markerSnapshots == nil {
		s.markerSnapshots = make(map[int]markerThumbnailSnapshot)
	}
	for id, entry := range s.markerSnapshots {
		if !entry.expires.After(now) {
			delete(s.markerSnapshots, id)
		}
	}
	if len(s.markerSnapshots) >= markerSnapshotLimit {
		var oldestID int
		var oldest time.Time
		for id, entry := range s.markerSnapshots {
			if oldest.IsZero() || entry.expires.Before(oldest) {
				oldestID, oldest = id, entry.expires
			}
		}
		delete(s.markerSnapshots, oldestID)
	}
	s.markerSnapshots[file.ID] = markerThumbnailSnapshot{file: snapshot, expires: now.Add(markerSnapshotTTL)}
	s.mu.Unlock()
	s.QueueFileIDs(ctx, []int{file.ID})
}

// markerSnapshotExpiry changes whenever a lookup stores a new snapshot.
func (s *Service) markerSnapshotExpiry(fileID int) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.markerSnapshots[fileID].expires
}

func (s *Service) withMarkerSnapshot(file *models.MediaFile) *models.MediaFile {
	s.mu.Lock()
	entry, ok := s.markerSnapshots[file.ID]
	s.mu.Unlock()
	if !ok || !entry.expires.After(s.now()) || models.MarkerFileIdentity(file) != models.MarkerFileIdentity(&entry.file) ||
		file.FilePath != entry.file.FilePath || !slices.Equal(models.EffectiveMarkerSegments(file), entry.file.MarkerThumbnailBaseSegments) {
		return file
	}
	overlay := *file
	overlay.MarkerSegments = entry.file.MarkerSegments
	// The snapshot contains the complete effective set. A successful provider
	// miss must not resurrect a removed kind through canonical legacy fallback.
	overlay.IntroStart, overlay.IntroEnd = nil, nil
	overlay.CreditsStart, overlay.CreditsEnd = nil, nil
	overlay.RecapStart, overlay.RecapEnd = nil, nil
	overlay.PreviewStart, overlay.PreviewEnd = nil, nil
	overlay.MarkerThumbnailBaseSegments = entry.file.MarkerThumbnailBaseSegments
	// Never resurrect images from a superseded provider inventory.
	candidates := models.EffectiveMarkerThumbnails(&overlay)
	if len(candidates) != len(file.MarkerThumbnails) {
		return file
	}
	for i := range candidates {
		if candidates[i].Identity != file.MarkerThumbnails[i].Identity {
			return file
		}
	}
	return &overlay
}
