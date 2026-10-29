package keptsub

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/lang"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// Prefetcher reads, when a playback starts, the embedded text tracks the
// viewer is likely to want soon, and keeps them, so they are ready, and
// what they hold is known, before anyone waits on them:
//
//   - the playing file's plain-text tracks, when the client's native
//     embedded entry declares literal_override_tags, so the next plan or
//     replan knows which of them carry override tags;
//   - the next episode's text tracks in the language of the subtitle the
//     viewer chose.
//
// Reads go through the source's index only (a few megabytes a track), never
// a demux; a track without cues waits for a request. Jobs run detached from
// the request, a few at a time server-wide, at idle I/O priority. A session's
// job is canceled when the session ends, and a session that starts again
// while its job waits doesn't queue another. Across servers the read claim
// keeps two from reading one track.
type Prefetcher struct {
	store *Store
	next  NextFileFunc
	jobs  chan *prefetchJob

	mu       sync.Mutex
	sessions map[string]*prefetchJob
	// failed holds tracks a prefetch couldn't read, until when not to try
	// again: a file without cues would otherwise cost an index read at
	// every playback start.
	failed map[trackKey]time.Time
	// jobContext makes a job's context, which ends when the server stops;
	// nil until Start.
	jobContext func() (context.Context, context.CancelFunc)
}

// NextFileFunc returns the file of the episode after file's, or nil.
type NextFileFunc func(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error)

// PrefetchRequest describes a playback that started.
type PrefetchRequest struct {
	SessionID string
	File      *models.MediaFile
	// Language is the language of the subtitle the viewer chose; empty
	// skips the next episode.
	Language string
	// LearnOverrideTags reads the playing file's plain-text tracks: the
	// client's native embedded entry declares literal_override_tags.
	LearnOverrideTags bool
}

type prefetchJob struct {
	PrefetchRequest
	ctx    context.Context //nolint:containedctx // made when the job is queued, so SessionEnded can cancel it before it runs
	cancel context.CancelFunc
}

type trackKey struct {
	fileID, stream int
}

const (
	// PrefetchWorkers bounds the prefetch jobs running server-wide.
	PrefetchWorkers = 2
	// prefetchQueue bounds the jobs waiting; a full queue drops new ones.
	prefetchQueue = 64
	// maxPrefetchTracks bounds the tracks one job reads.
	maxPrefetchTracks = 8
	// prefetchJobTimeout bounds one job.
	prefetchJobTimeout = 15 * time.Minute
	// prefetchRetryAfter is how long a track a prefetch couldn't read waits.
	prefetchRetryAfter = time.Hour
)

// NewPrefetcher returns a Prefetcher, or nil without a store. next may be
// nil, which skips next episodes.
func NewPrefetcher(store *Store, next NextFileFunc) *Prefetcher {
	if store == nil {
		return nil
	}
	return &Prefetcher{
		store:    store,
		next:     next,
		jobs:     make(chan *prefetchJob, prefetchQueue),
		sessions: map[string]*prefetchJob{},
		failed:   map[trackKey]time.Time{},
	}
}

// Start runs the workers until ctx ends.
func (p *Prefetcher) Start(ctx context.Context) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.jobContext = func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, prefetchJobTimeout)
	}
	p.mu.Unlock()
	for range PrefetchWorkers {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job := <-p.jobs:
					p.run(job)
				}
			}
		}()
	}
}

// SessionStarted queues the reads for a playback that started. It never
// blocks.
func (p *Prefetcher) SessionStarted(req PrefetchRequest) {
	if p == nil || req.File == nil || req.SessionID == "" || (req.Language == "" && !req.LearnOverrideTags) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jobContext == nil || p.sessions[req.SessionID] != nil {
		return
	}
	ctx, cancel := p.jobContext()
	job := &prefetchJob{PrefetchRequest: req, ctx: ctx, cancel: cancel}
	select {
	case p.jobs <- job:
		p.sessions[req.SessionID] = job
	default:
		cancel()
		slog.Debug("subtitle prefetch dropped: queue full", "component", "keptsub", "session", req.SessionID)
	}
}

// SessionEnded cancels a session's job, queued or running.
func (p *Prefetcher) SessionEnded(sessionID string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	job := p.sessions[sessionID]
	delete(p.sessions, sessionID)
	p.mu.Unlock()
	if job != nil {
		job.cancel()
	}
}

func (p *Prefetcher) run(job *prefetchJob) {
	defer func() {
		job.cancel()
		p.mu.Lock()
		if p.sessions[job.SessionID] == job {
			delete(p.sessions, job.SessionID)
		}
		p.mu.Unlock()
	}()
	ctx := job.ctx
	for _, target := range p.targets(ctx, job.PrefetchRequest) {
		if ctx.Err() != nil {
			return
		}
		key := trackKey{fileID: target.ref.MediaFileID, stream: target.ref.StreamIndex}
		p.mu.Lock()
		retryAt, failed := p.failed[key]
		p.mu.Unlock()
		if failed && time.Now().Before(retryAt) {
			continue
		}
		err := p.store.Prefetch(ctx, target.path, target.ref)
		switch {
		case err == nil, ctx.Err() != nil, errors.Is(err, errClaimedElsewhere):
		default:
			slog.DebugContext(ctx, "subtitle prefetch skipped a track", "component", "keptsub",
				"file_id", key.fileID, "stream_index", key.stream, "reason", err)
			p.mu.Lock()
			if len(p.failed) >= startTimesKept {
				clear(p.failed)
			}
			p.failed[key] = time.Now().Add(prefetchRetryAfter)
			p.mu.Unlock()
		}
	}
}

type prefetchTarget struct {
	path string
	ref  playback.EmbeddedSubtitleRef
}

// targets lists the tracks a job reads, the playing file's first.
func (p *Prefetcher) targets(ctx context.Context, req PrefetchRequest) []prefetchTarget {
	var targets []prefetchTarget
	add := func(file *models.MediaFile, want func(models.SubtitleTrack) bool) {
		for ordinal, track := range file.SubtitleTracks {
			if len(targets) < maxPrefetchTracks && want(track) {
				targets = append(targets, prefetchTarget{path: file.FilePath, ref: *playback.EmbeddedSubtitleRefFor(file, ordinal)})
			}
		}
	}
	if req.LearnOverrideTags {
		add(req.File, func(t models.SubtitleTrack) bool { return !t.External && overrideTagsApply(t.Codec) })
	}
	if req.Language != "" && p.next != nil {
		next, err := p.next(ctx, req.File)
		if err != nil && ctx.Err() == nil {
			slog.DebugContext(ctx, "next episode lookup failed", "component", "keptsub", "file_id", req.File.ID, "error", err)
		}
		if next != nil && next.ID != req.File.ID {
			add(next, func(t models.SubtitleTrack) bool {
				return !t.External && textCodec(t.Codec) && sameLanguage(t.Language, req.Language)
			})
		}
	}
	return targets
}

// overrideTagsApply reports whether override-tag facts are kept for codec.
func overrideTagsApply(codec string) bool {
	switch codecFamily(codec) {
	case codecSubRip, codecWebVTT, codecMovText:
		return true
	}
	return false
}

// textCodec reports a text codec the index reads; PGS tracks are large and
// never prefetched.
func textCodec(codec string) bool {
	family := codecFamily(codec)
	return family != "" && family != codecPGS
}

// sameLanguage compares languages by primary subtag, so eng, en and en-US
// match.
func sameLanguage(a, b string) bool {
	pa, pb := lang.PrimaryLanguage(a), lang.PrimaryLanguage(b)
	return pa != "" && pa == pb
}

// EpisodeLookup is the catalog the next episode is found in.
type EpisodeLookup interface {
	GetByID(ctx context.Context, contentID string) (*models.Episode, error)
	ListAdjacentInSeries(ctx context.Context, seriesID string, seasonNumber, episodeNumber int) ([]*models.Episode, error)
}

// EpisodeFiles lists the files that serve an episode.
type EpisodeFiles interface {
	GetByEpisodeID(ctx context.Context, episodeID string) ([]*models.MediaFile, error)
}

// NextEpisodeFile finds the next available episode in the series, across
// seasons, and its file: the one in the playing file's library when it has
// several.
func NextEpisodeFile(episodes EpisodeLookup, files EpisodeFiles) NextFileFunc {
	if episodes == nil || files == nil {
		return nil
	}
	return func(ctx context.Context, file *models.MediaFile) (*models.MediaFile, error) {
		if file == nil || file.EpisodeID == "" {
			return nil, nil
		}
		current, err := episodes.GetByID(ctx, file.EpisodeID)
		if err != nil || current == nil {
			return nil, err
		}
		adjacent, err := episodes.ListAdjacentInSeries(ctx, current.SeriesID, current.SeasonNumber, current.EpisodeNumber)
		if err != nil {
			return nil, err
		}
		var next *models.Episode
		for _, ep := range adjacent {
			if ep.SeasonNumber > current.SeasonNumber || ep.SeasonNumber == current.SeasonNumber && ep.EpisodeNumber > current.EpisodeNumber {
				next = ep
				break
			}
		}
		if next == nil {
			return nil, nil
		}
		candidates, err := files.GetByEpisodeID(ctx, next.ContentID)
		if err != nil || len(candidates) == 0 {
			return nil, err
		}
		for _, f := range candidates {
			if f.MediaFolderID == file.MediaFolderID {
				return f, nil
			}
		}
		return candidates[0], nil
	}
}
