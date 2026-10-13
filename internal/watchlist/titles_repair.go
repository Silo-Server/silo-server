package watchlist

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"

	"github.com/Silo-Server/silo-server/internal/contentid"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
)

// TitleObserver receives the outcome of TMDB detail fetches made for other
// reasons (a Discover detail view), so tracked titles refresh their IDs at no
// extra TMDB cost. requests.Service calls it; *Titles implements it.
type TitleObserver interface {
	ObservedDetail(ctx context.Context, mediaType string, tmdbID int, detail *tmdb.MediaDetail)
	ObservedNotFound(ctx context.Context, mediaType string, tmdbID int)
}

var _ TitleObserver = (*Titles)(nil)

// ScheduleChecks starts background checks for up to maxChecksPerRead overdue
// titles, bounded per node by maxConcurrentChecks. Each title is claimed first
// so only one node checks it. It returns at once; the checks outlive ctx.
func (s *Titles) ScheduleChecks(ctx context.Context, titles []Title) {
	if s == nil || s.tmdb == nil {
		return
	}
	now := s.now()
	var due []int64
	for _, t := range titles {
		if len(due) == maxChecksPerRead {
			break
		}
		if t.NextCheckAt.After(now) {
			continue
		}
		// Hold a slot before claiming so a busy node never claims a check it
		// cannot run; the claim would otherwise idle for the full lease.
		select {
		case s.checkSlots <- struct{}{}:
			due = append(due, t.ID)
		default:
		}
	}
	if len(due) == 0 {
		return
	}
	s.checks.Add(1)
	go func() {
		defer s.checks.Done()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
		defer cancel()
		claimed, err := s.repo.claimDue(ctx, due, now, claimLease)
		if err != nil {
			slog.WarnContext(ctx, "claiming watchlist title checks failed", "component", "watchlist", "error", err)
		}
		release := len(due) - len(claimed)
		for range release {
			<-s.checkSlots
		}
		var wg sync.WaitGroup
		for _, id := range claimed {
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-s.checkSlots }()
				checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
				defer cancel()
				if err := s.checkTitle(checkCtx, id); err != nil {
					slog.WarnContext(checkCtx, "watchlist title check failed", "component", "watchlist", "title_id", id, "error", err)
				}
			}()
		}
		wg.Wait()
	}()
}

// waitChecks blocks until background checks finish. Tests use it.
func (s *Titles) waitChecks() { s.checks.Wait() }

// checkTitle asks TMDB about the title's current ID and records the outcome.
func (s *Titles) checkTitle(ctx context.Context, titleID int64) error {
	t, err := s.repo.titleByID(ctx, titleID)
	if err != nil || t == nil {
		return err
	}
	detail, err := s.tmdb.GetMediaDetail(ctx, t.MediaType, t.TMDBID)
	now := s.now()
	switch {
	case err == nil:
		snap, err := SnapshotFromDetail(detail)
		if err != nil {
			return errors.Join(err, s.repo.backoff(ctx, t.ID, now.Add(errorBackoff)))
		}
		return s.repo.applyDetail(ctx, t.ID, snap, now)
	case errors.Is(err, tmdb.ErrNotFound):
		return s.handleNotFound(ctx, *t)
	default:
		return errors.Join(fmt.Errorf("fetching tmdb detail: %w", err), s.repo.backoff(ctx, t.ID, now.Add(errorBackoff)))
	}
}

// handleNotFound records a 404 for the title's current ID. The first one only
// schedules a confirming check a day later; a second one at least a day after
// the first confirms the deletion and runs recovery.
func (s *Titles) handleNotFound(ctx context.Context, t Title) error {
	now := s.now()
	if t.NotFoundCount == 0 || t.LastNotFoundAt == nil {
		return s.repo.recordNotFound(ctx, t.ID, t.TMDBID, 1, now, now.Add(notFoundConfirmGap))
	}
	if confirmAt := t.LastNotFoundAt.Add(notFoundConfirmGap); now.Before(confirmAt) {
		return s.repo.backoff(ctx, t.ID, confirmAt)
	}
	return s.recover(ctx, t)
}

// recover looks the title up on TMDB by its stored IMDb IDs, then its TVDB
// IDs, after its TMDB ID was confirmed deleted. One candidate of the right
// media type repoints the title (merging it into another title that already
// holds that ID); several need the user; none mark it removed.
func (s *Titles) recover(ctx context.Context, t Title) error {
	aliases, err := s.repo.aliasesOf(ctx, t.ID)
	if err != nil {
		return err
	}
	now := s.now()
	var candidates []int
	for _, source := range []struct{ provider, tmdbSource string }{
		{contentid.ProviderIMDB, tmdb.ExternalSourceIMDb},
		{contentid.ProviderTVDB, tmdb.ExternalSourceTVDB},
	} {
		for _, a := range aliases {
			if a.Provider != source.provider {
				continue
			}
			results, err := s.tmdb.FindByExternalID(ctx, source.tmdbSource, a.ProviderID)
			if err != nil && !errors.Is(err, tmdb.ErrNotFound) {
				return errors.Join(fmt.Errorf("finding tmdb title by %s id: %w", source.provider, err),
					s.repo.backoff(ctx, t.ID, now.Add(errorBackoff)))
			}
			for _, r := range results {
				if r.MediaType == t.MediaType && r.ID > 0 && r.ID != t.TMDBID && !containsInt(candidates, r.ID) {
					candidates = append(candidates, r.ID)
				}
			}
		}
		if len(candidates) > 0 {
			break
		}
	}
	notFound := t.NotFoundCount + 1
	switch len(candidates) {
	case 0:
		return s.repo.setState(ctx, t.ID, t.TMDBID, TitleRemoved, notFound, now, now.Add(checkSettled))
	case 1:
		survivor, err := s.repo.repoint(ctx, t.ID, t.TMDBID, candidates[0], now)
		if err != nil || survivor != t.ID {
			return err
		}
		// Refresh the snapshot from the new ID now; if this fails the title
		// is already due and the next read retries.
		detail, detailErr := s.tmdb.GetMediaDetail(ctx, t.MediaType, candidates[0])
		if detailErr == nil {
			if snap, snapErr := SnapshotFromDetail(detail); snapErr == nil {
				return s.repo.applyDetail(ctx, t.ID, snap, s.now())
			}
		}
		return nil
	default:
		slog.InfoContext(ctx, "watchlist title needs review: its tmdb id was deleted and several titles match its other ids",
			"component", "watchlist", "title_id", t.ID, "tmdb_id", t.TMDBID, "candidates", candidates)
		return s.repo.setState(ctx, t.ID, t.TMDBID, TitleNeedsReview, notFound, now, now.Add(checkSettled))
	}
}

// ObservedDetail refreshes a tracked title from a detail TMDB returned for its
// current ID. It writes only when something changed and never merges titles:
// an IMDb or TVDB ID another title already holds is not taken over.
func (s *Titles) ObservedDetail(ctx context.Context, mediaType string, tmdbID int, detail *tmdb.MediaDetail) {
	if s == nil || detail == nil {
		return
	}
	t, err := s.repo.titleByTMDB(ctx, mediaType, tmdbID)
	if err != nil {
		slog.WarnContext(ctx, "watchlist title observation failed", "component", "watchlist", "tmdb_id", tmdbID, "error", err)
		return
	}
	if t == nil || t.TMDBID != tmdbID {
		return
	}
	snap, err := SnapshotFromDetail(detail)
	if err != nil || snap.TMDBID != t.TMDBID || snap.MediaType != t.MediaType {
		return
	}
	if !snapshotChanged(*t, snap) {
		return
	}
	if err := s.repo.applyDetail(ctx, t.ID, snap, s.now()); err != nil {
		slog.WarnContext(ctx, "watchlist title observation failed", "component", "watchlist", "title_id", t.ID, "error", err)
	}
}

// ObservedNotFound records a 404 TMDB returned for a tracked title's current
// ID. The first 404 schedules the confirming check; once the confirming check
// is due, a background check runs recovery.
func (s *Titles) ObservedNotFound(ctx context.Context, mediaType string, tmdbID int) {
	if s == nil {
		return
	}
	t, err := s.repo.titleByTMDB(ctx, mediaType, tmdbID)
	if err != nil {
		slog.WarnContext(ctx, "watchlist title observation failed", "component", "watchlist", "tmdb_id", tmdbID, "error", err)
		return
	}
	if t == nil || t.TMDBID != tmdbID {
		return
	}
	now := s.now()
	switch {
	case t.NotFoundCount == 0 || t.LastNotFoundAt == nil:
		err = s.repo.recordNotFound(ctx, t.ID, t.TMDBID, 1, now, now.Add(notFoundConfirmGap))
	case t.State == TitleActive && !now.Before(t.LastNotFoundAt.Add(notFoundConfirmGap)) && !t.NextCheckAt.After(now):
		s.ScheduleChecks(ctx, []Title{*t})
	}
	if err != nil {
		slog.WarnContext(ctx, "watchlist title observation failed", "component", "watchlist", "title_id", t.ID, "error", err)
	}
}

func snapshotChanged(t Title, snap Snapshot) bool {
	if t.State != TitleActive || t.NotFoundCount != 0 {
		return true
	}
	if t.IMDbID != snap.IMDbID || t.TVDBID != snap.TVDBID || t.Title != snap.Title || t.Year != snap.Year ||
		t.PosterPath != snap.PosterPath || t.Certification != snap.Certification {
		return true
	}
	if (t.ReleaseDate == nil) != (snap.ReleaseDate == nil) ||
		(t.ReleaseDate != nil && !t.ReleaseDate.Equal(*snap.ReleaseDate)) {
		return true
	}
	return roundedVote(t.VoteAverage) != roundedVote(snap.VoteAverage)
}

// roundedVote compares ratings at the stored precision, numeric(3,1), rounding
// halves away from zero as Postgres does.
func roundedVote(v *float64) float64 {
	if v == nil {
		return -1
	}
	return math.Round(*v*10) / 10
}

func containsInt(values []int, v int) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
