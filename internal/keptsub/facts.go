package keptsub

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// SubtitleTrackFacts returns what reading file's embedded tracks taught the
// server. It looks the file's kept tracks up once, the first time planning
// asks, so a plan whose client declares no limitation costs no query. A
// track that isn't kept, or was kept from another version of the file, is
// unknown.
func (s *Store) SubtitleTrackFacts(ctx context.Context, file *models.MediaFile) playback.SubtitleTrackFactsV3 {
	if s == nil || file == nil {
		return playback.UnknownSubtitleTrackFactsV3{}
	}
	return &fileFacts{file: file, lookup: func() ([]Track, error) {
		tracks, err := s.repo.FileTracks(ctx, file.ID)
		if err != nil {
			slog.WarnContext(ctx, "kept subtitle facts lookup failed", "component", "keptsub", "file_id", file.ID, "error", err)
		}
		return tracks, err
	}}
}

type fileFacts struct {
	file *models.MediaFile
	// lookup reads the file's kept tracks, in the planning request.
	lookup func() ([]Track, error)

	once   sync.Once
	tracks map[int]Track
}

func (f *fileFacts) load() {
	tracks, err := f.lookup()
	if err != nil {
		return
	}
	f.tracks = make(map[int]Track, len(tracks))
	for _, t := range tracks {
		f.tracks[t.StreamIndex] = t
	}
}

func (f *fileFacts) OverrideTags(file *models.MediaFile, track models.SubtitleTrack) playback.SubtitleFactV3 {
	if file == nil || file.ID != f.file.ID || file.FileModifiedAt == nil {
		return playback.SubtitleFactUnknownV3
	}
	f.once.Do(f.load)
	t, ok := f.tracks[track.Index]
	if !ok || t.HasOverrideTags == nil || t.ContainerTrackID != track.ContainerTrackID ||
		codecFamily(t.Codec) != codecFamily(track.Codec) || t.SourceSize != file.FileSize ||
		!models.NormalizeFileModifiedAt(time.Unix(0, t.SourceModTime)).Equal(models.NormalizeFileModifiedAt(*file.FileModifiedAt)) {
		return playback.SubtitleFactUnknownV3
	}
	if *t.HasOverrideTags {
		return playback.SubtitleFactYesV3
	}
	return playback.SubtitleFactNoV3
}
