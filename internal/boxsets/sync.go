// Package boxsets keeps the TMDB collection data movie box sets are built
// from: which collection each movie of a box set library belongs to, and the
// name, artwork and release order of those collections. Browsing reads only
// the stored data (internal/catalog/box_sets.go); this sync is the one place
// that calls TMDB for it.
package boxsets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/metadata"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

const (
	// movieRecheckAfter is how long a movie's collection lookup stands. A
	// movie joins a collection rarely, usually when a sequel is announced.
	movieRecheckAfter = 30 * 24 * time.Hour
	// collectionRefetchAfter is how long a stored collection stands; new
	// sequels are added to collections on TMDB.
	collectionRefetchAfter = 7 * 24 * time.Hour
	batchSize              = 200
	// tmdbOriginalImageBase is where a collection's original artwork is
	// downloaded from; the cache builds its own sized variants.
	tmdbOriginalImageBase = "https://image.tmdb.org/t/p/original"
	// maxFailures stops a run that keeps failing, usually because TMDB is
	// unreachable; the next run picks up where it stopped.
	maxFailures = 25
)

// TMDB is the slice of *tmdb.Client the sync uses.
type TMDB interface {
	GetMovieCollection(ctx context.Context, movieID int) (*tmdb.CollectionRef, error)
	GetCollection(ctx context.Context, id int) (*tmdb.Collection, error)
}

// Store is the slice of *catalog.BoxSetRepository the sync uses.
type Store interface {
	MoviesNeedingCollectionLookup(ctx context.Context, staleBefore time.Time, limit int, skip []string) ([]catalog.MovieCollectionLookup, error)
	RecordMovieCollection(ctx context.Context, contentID string, tmdbID int64, collectionID *int64) error
	CollectionsNeedingFetch(ctx context.Context, staleBefore time.Time, limit int, skip []int64) ([]int64, error)
	UpsertCollection(ctx context.Context, c catalog.TMDBMovieCollection) error
	TouchCollection(ctx context.Context, id int64) error
	DeleteUnusedCollections(ctx context.Context) (int64, error)
	CollectionArtworkNeedingCache(ctx context.Context, limit int, skip []int64) ([]catalog.CollectionArtworkDue, error)
	SetCollectionArtwork(ctx context.Context, id int64, slot catalog.CollectionArtworkSlot, sourcePath, path, thumbhash string) error
}

// Result counts what one run did.
type Result struct {
	MoviesChecked      int `json:"movies_checked"`
	CollectionsFetched int `json:"collections_fetched"`
	ImagesCached       int `json:"images_cached"`
	Failures           int `json:"failures"`
}

// Syncer looks up and stores TMDB collections.
type Syncer struct {
	store Store
	tmdb  TMDB
	// images returns the image cacher, or nil while artwork is served from
	// the provider (no artwork store, or metadata.cache_images off).
	images func() metadata.ImageCacher
	now    func() time.Time
}

// NewSyncer returns a syncer over store and client.
func NewSyncer(store Store, client TMDB) *Syncer {
	return &Syncer{store: store, tmdb: client, now: time.Now}
}

// WithImages caches collection artwork through the cacher images returns,
// the way item artwork is cached.
func (s *Syncer) WithImages(images func() metadata.ImageCacher) *Syncer {
	s.images = images
	return s
}

// Run deletes the collections no movie belongs to any more, looks up every
// movie of a box set library that is due, then fetches every collection that
// is due and caches their artwork. It repeats while a lookup finds movies
// that became due during the run, such as those of a library that turned box
// sets on, because a start requested meanwhile is dropped. report receives
// progress messages. An item that fails stays due for the next run
// but is not tried again in this one, so it neither counts as a failure in
// every batch nor holds back the items behind it.
func (s *Syncer) Run(ctx context.Context, report func(percent float64, message string)) (Result, error) {
	var res Result
	if s == nil || s.store == nil || s.tmdb == nil {
		return res, nil
	}
	s.deleteUnusedCollections(ctx)
	var failedMovies []string
	var triedCollections []int64
	for pass := 0; ; pass++ {
		checked := res.MoviesChecked
		if err := s.lookUpMovies(ctx, &res, &failedMovies, report); err != nil {
			return res, err
		}
		if pass > 0 && res.MoviesChecked == checked {
			return res, nil
		}
		if err := s.fetchCollections(ctx, &res, &triedCollections, report); err != nil {
			return res, err
		}
		if err := s.cacheArtwork(ctx, &res); err != nil {
			return res, err
		}
	}
}

// deleteUnusedCollections drops the collections no movie belongs to any
// more. A failure is logged; the next run tries again.
func (s *Syncer) deleteUnusedCollections(ctx context.Context) {
	deleted, err := s.store.DeleteUnusedCollections(ctx)
	if err != nil {
		slog.WarnContext(ctx, "box sets: deleting unused collections failed", "component", "boxsets", "error", err)
		return
	}
	if deleted > 0 {
		slog.InfoContext(ctx, "box sets: deleted unused collections", "component", "boxsets", "count", deleted)
	}
}

// lookUpMovies records the collection of every due movie. failed collects
// the movies whose lookup failed in this run.
func (s *Syncer) lookUpMovies(ctx context.Context, res *Result, failed *[]string, report func(float64, string)) error {
	cutoff := s.now().Add(-movieRecheckAfter)
	for {
		due, err := s.store.MoviesNeedingCollectionLookup(ctx, cutoff, batchSize, *failed)
		if err != nil || len(due) == 0 {
			return err
		}
		for _, movie := range due {
			if err := ctx.Err(); err != nil {
				return err
			}
			ref, err := s.tmdb.GetMovieCollection(ctx, int(movie.TMDBID))
			if err != nil && !errors.Is(err, tmdb.ErrNotFound) {
				res.Failures++
				*failed = append(*failed, movie.ContentID)
				slog.WarnContext(ctx, "box sets: movie collection lookup failed", "component", "boxsets", "content_id", movie.ContentID, "tmdb_id", movie.TMDBID, "error", err)
				if res.Failures >= maxFailures {
					return fmt.Errorf("box sets: stopping after %d failed TMDB lookups: %w", res.Failures, err)
				}
				continue
			}
			var collectionID *int64
			if ref != nil {
				collectionID = new(int64(ref.ID))
			}
			if err := s.store.RecordMovieCollection(ctx, movie.ContentID, movie.TMDBID, collectionID); err != nil {
				return err
			}
			res.MoviesChecked++
		}
		report(25, fmt.Sprintf("Looked up the collections of %d movies", res.MoviesChecked))
	}
}

// fetchCollections stores every due collection. tried collects the
// collections this run could not store: failed fetches, and ones TMDB no
// longer serves that were never stored.
func (s *Syncer) fetchCollections(ctx context.Context, res *Result, tried *[]int64, report func(float64, string)) error {
	cutoff := s.now().Add(-collectionRefetchAfter)
	for {
		due, err := s.store.CollectionsNeedingFetch(ctx, cutoff, batchSize, *tried)
		if err != nil || len(due) == 0 {
			return err
		}
		for _, id := range due {
			if err := ctx.Err(); err != nil {
				return err
			}
			c, err := s.tmdb.GetCollection(ctx, int(id))
			if errors.Is(err, tmdb.ErrNotFound) {
				// Keep a stored copy rather than refetching a collection TMDB
				// no longer serves on every run. One never stored stays due
				// for the next run.
				if err := s.store.TouchCollection(ctx, id); err != nil {
					return err
				}
				*tried = append(*tried, id)
				continue
			}
			if err != nil {
				res.Failures++
				*tried = append(*tried, id)
				slog.WarnContext(ctx, "box sets: collection fetch failed", "component", "boxsets", "tmdb_collection_id", id, "error", err)
				if res.Failures >= maxFailures {
					return fmt.Errorf("box sets: stopping after %d failed TMDB lookups: %w", res.Failures, err)
				}
				continue
			}
			if err := s.store.UpsertCollection(ctx, storedCollection(id, c)); err != nil {
				return err
			}
			res.CollectionsFetched++
		}
		report(75, fmt.Sprintf("Fetched %d collections", res.CollectionsFetched))
	}
}

// cacheArtwork stores a copy of each collection image still served from
// TMDB. A failed image stays on the provider URL and is tried again next run.
func (s *Syncer) cacheArtwork(ctx context.Context, res *Result) error {
	if s.images == nil {
		return nil
	}
	cacher := s.images()
	if cacher == nil {
		return nil
	}
	// A collection with an image that failed is left for the next run.
	var failed []int64
	for {
		due, err := s.store.CollectionArtworkNeedingCache(ctx, batchSize, failed)
		if err != nil || len(due) == 0 {
			return err
		}
		for _, image := range due {
			if err := ctx.Err(); err != nil {
				return err
			}
			imageType := metadata.ImagePoster
			if image.Slot == catalog.BoxSetBackdropSlot {
				imageType = metadata.ImageBackdrop
			}
			result, err := cacher.CacheImage(ctx, metadata.CacheImageRequest{
				SourceURL:   image.SourcePath,
				ProviderID:  "tmdb",
				ContentType: "collections",
				ContentID:   strconv.FormatInt(image.CollectionID, 10),
				ImageType:   imageType,
			})
			if err != nil {
				res.Failures++
				failed = append(failed, image.CollectionID)
				slog.WarnContext(ctx, "box sets: caching collection artwork failed", "component", "boxsets", "tmdb_collection_id", image.CollectionID, "slot", image.Slot, "error", err)
				if res.Failures >= maxFailures {
					return fmt.Errorf("box sets: stopping after %d failures: %w", res.Failures, err)
				}
				continue
			}
			if err := s.store.SetCollectionArtwork(ctx, image.CollectionID, image.Slot, image.SourcePath, metadata.CachedImageOriginalPath(result), result.Thumbhash); err != nil {
				return err
			}
			res.ImagesCached++
		}
	}
}

func tmdbOriginalURL(path string) string {
	if path == "" {
		return ""
	}
	return tmdbOriginalImageBase + path
}

// storedCollection converts a fetched collection, listing its movies in
// release order. TMDB returns parts in no reliable order, and a part without
// a release date (an announced sequel) goes last.
func storedCollection(id int64, c *tmdb.Collection) catalog.TMDBMovieCollection {
	parts := append([]tmdb.CollectionPart(nil), c.Parts...)
	sort.SliceStable(parts, func(i, j int) bool {
		a, b := parts[i].ReleaseDate, parts[j].ReleaseDate
		if a == "" || b == "" {
			return a != "" && b == ""
		}
		return a < b
	})
	members := make([]int64, 0, len(parts))
	for _, p := range parts {
		if p.ID > 0 {
			members = append(members, int64(p.ID))
		}
	}
	return catalog.TMDBMovieCollection{
		ID:                 id,
		Name:               c.Name,
		Overview:           c.Overview,
		PosterSourcePath:   tmdbOriginalURL(c.PosterPath),
		BackdropSourcePath: tmdbOriginalURL(c.BackdropPath),
		MemberTMDBIDs:      members,
	}
}
