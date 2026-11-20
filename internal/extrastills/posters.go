package extrastills

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/imageutil"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/models"
)

// framePosterKind keys a movie file's failed frame posters among the media
// analysis artifacts. Only failures are stored there; a saved frame lives on
// the item, marked by catalog.StillSourceFramePrefix in poster_source_path.
const framePosterKind = "frame_poster"

var framePosterKey = mediaartifact.Key{
	Kind:             framePosterKind,
	AlgorithmVersion: 1,
	ConfigHash:       mediaartifact.ConfigHash(framePosterKind, "frame=tenth;crop=2:3"),
}

// framePosterRecorder records every frame poster failure. One name for every
// server keeps a failure's backoff, and its growth over repeated failures,
// the same whichever replica runs the task, also after a restart.
const framePosterRecorder = "frame-posters"

// maxPosterFailuresInARow ends the poster pass once this many movies in a row
// failed in a way that may pass. A share that is offline fails every file,
// each after up to most of a minute.
const maxPosterFailuresInARow = 3

// PosterCacher stores a movie's frame poster; imagecache.Cacher.
type PosterCacher interface {
	CacheFramePoster(ctx context.Context, data []byte, contentID string) (storedPath string, thumbhash string, err error)
}

// FailureStore records a file's failed frame posters; mediaartifact.Store.
type FailureStore interface {
	Upsert(ctx context.Context, a mediaartifact.Artifact) error
	RecordFailure(ctx context.Context, failure mediaartifact.Failure) error
}

// posterStore lists, saves and withdraws frame posters; catalog.ItemRepository.
type posterStore interface {
	ClearStaleFramePosters(ctx context.Context) (int64, error)
	ListPendingFramePosters(ctx context.Context, failures mediaartifact.Key, limit int) ([]catalog.PendingFramePoster, error)
	SetFramePosterIfSource(ctx context.Context, contentID, expectedSource, path, source, thumbhash string) (bool, error)
}

// posterPass gives movies with no poster a frame of their own video.
type posterPass struct {
	items    posterStore
	files    FileLoader
	frames   FrameExtractor
	cacher   PosterCacher
	failures FailureStore
}

// WithPosters adds the poster pass to every Generate run.
func (g *Generator) WithPosters(cacher PosterCacher, failures FailureStore) *Generator {
	g.posters = &posterPass{
		items: catalog.NewItemRepository(g.pool), files: g.files, frames: g.frames,
		cacher: cacher, failures: failures,
	}
	return g
}

type posterOutcome int

const (
	posterSaved posterOutcome = iota
	// posterNotSaved: another image took the slot first, the file cannot be
	// decoded, or the run is ending. None says the files are out of reach.
	posterNotSaved
	// posterFailed: a failure that may pass.
	posterFailed
	// posterSkipped: the file could not be loaded. A deleted file drops out of
	// the list by itself, and a database error says nothing about the share,
	// so it neither counts toward the stop nor clears the count.
	posterSkipped
)

// run gives up to limit movies a frame poster and reports how many it saved.
// It stops early when no transcode node can be reserved, after
// maxPosterFailuresInARow failures in a row, and with an error when a poster
// cannot be saved.
func (p *posterPass) run(ctx context.Context, limit int) (int, error) {
	// A frame came from one file in one library, and the poster is shown in
	// every library its movie is in: it goes once that file is no longer the
	// movie's live file, or the movie spans libraries. If the cleanup cannot
	// run, take no new frames.
	cleared, err := p.items.ClearStaleFramePosters(ctx)
	if err != nil {
		return 0, err
	}
	if cleared > 0 {
		slog.InfoContext(ctx, "frame posters: removed frames whose file is gone or whose movie is now in more than one library",
			"component", "extrastills", "cleared", cleared)
	}
	pending, err := p.items.ListPendingFramePosters(ctx, framePosterKey, limit)
	if err != nil {
		return 0, err
	}
	saved, failedInARow := 0, 0
	for _, movie := range pending {
		if ctx.Err() != nil {
			return saved, ctx.Err()
		}
		outcome, err := p.generate(ctx, movie)
		if errors.Is(err, chapterthumbs.ErrExtractionUnavailable) {
			slog.InfoContext(ctx, "frame posters: no transcode node could be reserved; trying again next run",
				"component", "extrastills", "content_id", movie.ContentID, "error", err)
			return saved, nil
		}
		if err != nil {
			return saved, err
		}
		switch outcome {
		case posterSaved:
			saved++
			failedInARow = 0
		case posterNotSaved:
			failedInARow = 0
		case posterFailed:
			failedInARow++
			if failedInARow >= maxPosterFailuresInARow {
				slog.WarnContext(ctx, "frame posters: several movies in a row failed; trying again next run",
					"component", "extrastills", "failures", failedInARow)
				return saved, nil
			}
		}
	}
	return saved, nil
}

// generate takes, crops, stores and saves one movie's frame poster. A failed
// extraction, crop or store is recorded on the file (recordFailure), so the
// next run moves past the movie, except when no transcode node is available:
// that error is returned and stops the run. A file that cannot be loaded is
// skipped. A failed save is a database error, not the movie's: it is
// returned and ends the pass, rather than spending more extractions on frames
// that cannot be saved.
func (p *posterPass) generate(ctx context.Context, movie catalog.PendingFramePoster) (posterOutcome, error) {
	attrs := []any{"component", "extrastills", "content_id", movie.ContentID, "file_id", movie.FileID}
	file, err := p.files.GetByID(ctx, movie.FileID)
	if err != nil {
		slog.WarnContext(ctx, "frame posters: loading file failed", append(attrs, "error", err)...)
		return posterSkipped, nil
	}
	poster, err := p.frames.ExtractStill(ctx, file, frameSeconds(movie.Duration))
	if err == nil {
		poster, err = imageutil.CropToPoster(poster)
	}
	if err != nil {
		return p.recordFailure(ctx, file, err, attrs)
	}
	path, thumbhash, err := p.cacher.CacheFramePoster(ctx, poster, movie.ContentID)
	if err != nil {
		return p.recordFailure(ctx, file, err, attrs)
	}
	saved, err := p.items.SetFramePosterIfSource(ctx, movie.ContentID, movie.PosterSource, path,
		catalog.StillSourceFramePrefix+movie.FrameKey, thumbhash)
	if err != nil {
		return posterFailed, fmt.Errorf("saving frame poster for %s: %w", movie.ContentID, err)
	}
	if !saved {
		// Another image took the slot meanwhile; the artwork revision GC
		// collects the stored frame.
		return posterNotSaved, nil
	}
	return posterSaved, nil
}

// recordFailure records a failed extraction, crop or store on the file. It
// returns err instead when no transcode node is available or the run was
// canceled, since neither says anything about the file, and records nothing
// when the image budget turned the image away.
func (p *posterPass) recordFailure(ctx context.Context, file *models.MediaFile, err error, attrs []any) (posterOutcome, error) {
	if errors.Is(err, chapterthumbs.ErrExtractionUnavailable) || ctx.Err() != nil {
		return posterNotSaved, err
	}
	if errors.Is(err, imageutil.ErrBusy) {
		slog.WarnContext(ctx, "frame posters: image processing busy; trying again next run", append(attrs, "error", err)...)
		return posterFailed, nil
	}
	identity := mediaartifact.Identity{FileHash: file.FileHash, FileSize: file.FileSize, DurationSeconds: float64(file.Duration)}
	if errors.Is(err, chapterthumbs.ErrUndecodableFile) {
		slog.WarnContext(ctx, "frame posters: file cannot be decoded; skipped until it changes", append(attrs, "error", err)...)
		if err := p.failures.Upsert(ctx, mediaartifact.Artifact{
			MediaFileID: file.ID, Key: framePosterKey, Identity: identity,
			Status: mediaartifact.StatusUnusable, Detail: "undecodable", RecordedBy: framePosterRecorder,
		}); err != nil {
			slog.WarnContext(ctx, "frame posters: recording failure failed", append(attrs, "error", err)...)
		}
		return posterNotSaved, nil
	}
	slog.WarnContext(ctx, "frame posters: making a poster failed; trying again later", append(attrs, "error", err)...)
	if err := p.failures.RecordFailure(ctx, mediaartifact.Failure{
		MediaFileID: file.ID, Key: framePosterKey, Identity: identity, RecordedBy: framePosterRecorder, Error: err.Error(),
	}); err != nil {
		slog.WarnContext(ctx, "frame posters: recording failure failed", append(attrs, "error", err)...)
	}
	return posterFailed, nil
}
