package handlers

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

// SubtitleInventoryResolver supplies the playback package with the two
// repositories it needs to rebuild a file's combined-ordinal subtitle
// inventory outside a request: the media file itself and the downloaded
// subtitle rows that occupy the tail of the ordinal space.
//
// It exists so realtime subtitle events can publish the ordinal a newly
// generated track will hold in the next plan, instead of leaving each client to
// reconstruct one by counting the tracks it can see.
type SubtitleInventoryResolver struct {
	files     FilePathResolver
	subtitles subtitles.Repository
}

// NewSubtitleInventoryResolver returns a resolver, or nil when no file lookup
// is available. A nil subtitle repository is fine: the inventory then covers
// only the file's external and embedded tracks.
func NewSubtitleInventoryResolver(files FilePathResolver, repo subtitles.Repository) *SubtitleInventoryResolver {
	if files == nil {
		return nil
	}
	return &SubtitleInventoryResolver{files: files, subtitles: repo}
}

// MediaFile loads the file whose subtitle inventory is being resolved.
func (r *SubtitleInventoryResolver) MediaFile(ctx context.Context, fileID int) (*models.MediaFile, error) {
	if r == nil || r.files == nil {
		return nil, errors.New("subtitle inventory resolver has no media file lookup")
	}
	return r.files.GetByID(ctx, fileID)
}

// AdditionalSubtitles returns the downloaded and generated tracks that follow
// the file's external and embedded tracks in the combined ordinal space.
func (r *SubtitleInventoryResolver) AdditionalSubtitles(ctx context.Context, file *models.MediaFile) ([]playback.SubtitleInventoryEntryV3, error) {
	if r == nil || r.subtitles == nil || file == nil {
		return nil, nil
	}
	downloaded, err := r.subtitles.ListDownloadedSubtitles(ctx, file.ID)
	if err != nil {
		return nil, err
	}
	return downloadedSubtitleEntriesV3(file, downloaded), nil
}
