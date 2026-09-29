package requests

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Following a title: a profile that finds a title someone else has already
// requested can ask to be notified when it becomes available, instead of
// requesting it again. A follow belongs to the request that was open when it
// was made, since a series can have completed requests still waiting for the
// library beside a newer open request for other seasons; each request's
// notification goes to its own follows, and a profile can follow each of them.
// A follow survives its request failing: the title's next request takes over
// the follows of a failed or replaced one.
// It is cleared once the fulfilled notification has gone out, and when its
// request is declined or withdrawn: the title is then no longer on its way,
// and the follower can request it themselves. The requester is always
// notified and never needs a follow.

// Follower is a profile waiting to hear that a requested title is available.
type Follower struct {
	UserID    int
	ProfileID string
}

// Follow records that the viewer's profile wants to hear when the title
// becomes available. The title must have an open request. Following a title
// the viewer requested is a no-op.
func (s *Service) Follow(ctx context.Context, viewer Viewer, mediaType MediaType, tmdbID int) (RequestState, error) {
	if err := validateViewer(viewer); err != nil {
		return RequestState{}, err
	}
	if strings.TrimSpace(viewer.ProfileID) == "" {
		return RequestState{}, ErrForbidden
	}
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return RequestState{}, err
	}
	if err := s.ensureViewerRequestsAllowed(ctx, viewer.UserID); err != nil {
		return RequestState{}, err
	}
	if blocked, err := s.userLimitBlocked(ctx, viewer.UserID); err != nil {
		return RequestState{}, err
	} else if blocked {
		return RequestState{}, ErrUserBlocked
	}
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return RequestState{}, err
	}
	if tmdbID <= 0 {
		return RequestState{}, fmt.Errorf("%w: tmdb id is required", ErrInvalidInput)
	}
	// Following reaches the same titles requesting does, so the profile's
	// rating ceiling applies to it too.
	if err := s.ensureCreateAllowedByCeiling(ctx, viewer, CreateRequestInput{MediaType: mediaType, TMDBID: tmdbID}); err != nil {
		return RequestState{}, err
	}
	// An open request is what makes a title followable: a series partly in
	// the library can have one for its missing seasons.
	active, err := s.store.ListActiveByTMDB(ctx, mediaType, []int{tmdbID})
	if err != nil {
		return RequestState{}, err
	}
	req := active[tmdbID]
	if req == nil {
		return RequestState{}, ErrNotRequested
	}
	if !req.requestedBy(viewer) {
		if err := s.store.FollowTitle(ctx, mediaType, tmdbID, viewer); err != nil {
			return RequestState{}, err
		}
	}
	state := activeRequestState(viewer, req)
	state.Following = true
	return state, nil
}

// Unfollow removes the viewer's follow. It succeeds whether or not the profile
// followed the title, and whatever state the title's request is in.
func (s *Service) Unfollow(ctx context.Context, viewer Viewer, mediaType MediaType, tmdbID int) error {
	if err := validateViewer(viewer); err != nil {
		return err
	}
	if strings.TrimSpace(viewer.ProfileID) == "" {
		return ErrForbidden
	}
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return err
	}
	if tmdbID <= 0 {
		return fmt.Errorf("%w: tmdb id is required", ErrInvalidInput)
	}
	return s.store.UnfollowTitle(ctx, mediaType, tmdbID, viewer)
}

// followedTitles reports which of the titles with an active request the viewer
// is waiting on, either as the requesting profile or as a follower.
func (s *Service) followedTitles(ctx context.Context, viewer Viewer, mediaType MediaType, active map[int]*Request) (map[int]bool, error) {
	out := map[int]bool{}
	others := map[string]int{}
	for tmdbID, req := range active {
		if req == nil {
			continue
		}
		if req.requestedBy(viewer) {
			out[tmdbID] = true
			continue
		}
		others[req.ID] = tmdbID
	}
	if len(others) == 0 || strings.TrimSpace(viewer.ProfileID) == "" {
		return out, nil
	}
	followed, err := s.store.FollowedRequests(ctx, slices.Collect(maps.Keys(others)), viewer)
	if err != nil {
		return nil, err
	}
	for id, ok := range followed {
		if ok {
			out[others[id]] = true
		}
	}
	return out, nil
}

// FollowTitle inserts the follow only while the title has an open request, in
// the same statement, so a follow cannot land just after the request
// completed and never be told. It answers ErrNotRequested when there is none.
//
// The follow records the open request it read. FOR SHARE holds that request
// until the follow commits. Every transition
// that closes a request (decline, cancel, completion) updates its row, and
// that row lock conflicts with FOR SHARE, so the close cannot commit, and its
// follow cleanup cannot run, between the read and the insert. A follow that
// waited on a close re-checks the updated row, finds it closed, and inserts
// nothing.
func (r *Repository) FollowTitle(ctx context.Context, mediaType MediaType, tmdbID int, viewer Viewer) error {
	var open bool
	if err := r.pool.QueryRow(ctx, `
		WITH open_request AS (
			SELECT id FROM media_requests
			WHERE media_type = $1 AND provider = 'tmdb' AND tmdb_id = $2
			  AND outcome = 'active' AND status <> 'completed'
			LIMIT 1
			FOR SHARE
		), inserted AS (
			INSERT INTO media_request_follows (media_type, tmdb_id, user_id, profile_id, request_id)
			SELECT $1, $2, $3, $4, id FROM open_request
			ON CONFLICT (user_id, profile_id, request_id) DO NOTHING
		)
		SELECT EXISTS (SELECT 1 FROM open_request)
	`, mediaType, tmdbID, viewer.UserID, viewer.ProfileID).Scan(&open); err != nil {
		return fmt.Errorf("follow title: %w", err)
	}
	if !open {
		return ErrNotRequested
	}
	return nil
}

// forgetTitleFollows removes the follows of a request that was just declined
// or withdrawn.
func forgetTitleFollows(ctx context.Context, exec requestExecutor, closed *Request) error {
	if _, err := exec.Exec(ctx, `DELETE FROM media_request_follows WHERE request_id = $1`, closed.ID); err != nil {
		return fmt.Errorf("forget title follows: %w", err)
	}
	return nil
}

// adoptTitleFollows gives a new request the follows of the title's failed
// requests, so a follow survives its request failing. The caller creates the
// request in the same transaction, before deleting any failed request it
// replaces.
//
// The follows move in place: an UnfollowTitle that waited on a moved row
// re-checks the moved row, still on the title and the profile, and deletes it,
// where a delete and re-insert would leave it a row it cannot see.
func adoptTitleFollows(ctx context.Context, exec requestExecutor, req *Request) error {
	const failed = `SELECT id FROM media_requests
		WHERE media_type = $1 AND provider = 'tmdb' AND tmdb_id = $2 AND outcome = 'failed'`
	// A profile that followed two failed requests keeps its earliest follow.
	if _, err := exec.Exec(ctx, `
		DELETE FROM media_request_follows f
		USING media_request_follows keep
		WHERE f.request_id IN (`+failed+`) AND keep.request_id IN (`+failed+`)
		  AND keep.user_id = f.user_id AND keep.profile_id = f.profile_id
		  AND (keep.created_at, keep.request_id) < (f.created_at, f.request_id)
	`, req.MediaType, req.TMDBID); err != nil {
		return fmt.Errorf("adopt title follows: %w", err)
	}
	if _, err := exec.Exec(ctx, `
		UPDATE media_request_follows SET request_id = $3
		WHERE request_id IN (`+failed+`)
	`, req.MediaType, req.TMDBID, req.ID); err != nil {
		return fmt.Errorf("adopt title follows: %w", err)
	}
	return nil
}

// UnfollowTitle removes the profile's follows on every request of the title.
func (r *Repository) UnfollowTitle(ctx context.Context, mediaType MediaType, tmdbID int, viewer Viewer) error {
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM media_request_follows
		WHERE media_type = $1 AND tmdb_id = $2 AND user_id = $3 AND profile_id = $4
	`, mediaType, tmdbID, viewer.UserID, viewer.ProfileID); err != nil {
		return fmt.Errorf("unfollow title: %w", err)
	}
	return nil
}

// FollowedRequests reports which of the requests the profile follows.
func (r *Repository) FollowedRequests(ctx context.Context, requestIDs []string, viewer Viewer) (map[string]bool, error) {
	out := map[string]bool{}
	if len(requestIDs) == 0 {
		return out, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT request_id FROM media_request_follows
		WHERE request_id = ANY($1) AND user_id = $2 AND profile_id = $3
	`, requestIDs, viewer.UserID, viewer.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("list followed requests: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ListRequestFollowers lists the follows a request's notification goes to.
func (r *Repository) ListRequestFollowers(ctx context.Context, req Request) ([]Follower, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, profile_id FROM media_request_follows
		WHERE request_id = $1
		ORDER BY created_at, user_id, profile_id
	`, req.ID)
	if err != nil {
		return nil, fmt.Errorf("list request followers: %w", err)
	}
	defer rows.Close()
	var out []Follower
	for rows.Next() {
		var f Follower
		if err := rows.Scan(&f.UserID, &f.ProfileID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ClearRequestFollowers removes the listed follows once the request's
// notification has gone out. A profile that unfollowed and followed again
// since, for a newer request, keeps its new follow.
func (r *Repository) ClearRequestFollowers(ctx context.Context, req Request, followers []Follower) error {
	if len(followers) == 0 {
		return nil
	}
	userIDs := make([]int, 0, len(followers))
	profileIDs := make([]string, 0, len(followers))
	for _, f := range followers {
		userIDs = append(userIDs, f.UserID)
		profileIDs = append(profileIDs, f.ProfileID)
	}
	if _, err := r.pool.Exec(ctx, `
		DELETE FROM media_request_follows
		WHERE request_id = $1
		  AND (user_id, profile_id) IN (SELECT * FROM unnest($2::int[], $3::text[]))
	`, req.ID, userIDs, profileIDs); err != nil {
		return fmt.Errorf("clear request followers: %w", err)
	}
	return nil
}
