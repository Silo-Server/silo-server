package requests

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/metadata/tmdb"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// Watchlist requests: adding a title the library doesn't have to a watchlist
// also requests it, or follows the request someone else already made. The
// watchlist entry is written first and is kept whatever happens here; these
// calls only report the request state the entry's card shows.

// withdrawnFromWatchlist is the outcome reason on a request canceled because
// its title left the watchlist that created it.
const withdrawnFromWatchlist = "Removed from the watchlist"

// WatchlistPreference reads a profile's own opt-out,
// requests.watchlist_auto_request.
type WatchlistPreference interface {
	WatchlistAutoRequest(ctx context.Context, userID int, profileID string) bool
}

// TitleObserver hears about TMDB detail fetches, so a tracked watchlist title
// refreshes its IDs at no extra TMDB cost. watchlist.Titles implements it.
type TitleObserver interface {
	ObservedDetail(ctx context.Context, mediaType string, tmdbID int, detail *tmdb.MediaDetail)
	ObservedNotFound(ctx context.Context, mediaType string, tmdbID int)
}

// SetWatchlistPreference wires the profile opt-out. Without one every profile
// counts as opted in.
func (s *Service) SetWatchlistPreference(p WatchlistPreference) { s.watchlistPref = p }

// SetTitleObserver wires the observer GetDetail reports its TMDB reads to.
func (s *Service) SetTitleObserver(o TitleObserver) { s.titleObserver = o }

// StoreWatchlistPreference resolves the opt-out from the profile's stored
// settings. A read failure counts as opted out: a request is a side effect
// the profile may have turned off.
type StoreWatchlistPreference struct {
	Stores userstore.UserStoreProvider
}

// WatchlistAutoRequest implements WatchlistPreference.
func (p StoreWatchlistPreference) WatchlistAutoRequest(ctx context.Context, userID int, profileID string) bool {
	if p.Stores == nil || profileID == "" {
		return false
	}
	fail := func(msg string, err error) bool {
		slog.WarnContext(ctx, "watchlist request preference unavailable",
			"component", "requests", "step", msg, "profile_id", profileID, "error", err)
		return false
	}
	store, err := p.Stores.ForUser(ctx, userID)
	if err != nil {
		return fail("opening user store failed", err)
	}
	contract, err := settingscontract.Load()
	if err != nil {
		return fail("loading settings contract failed", err)
	}
	resolved, err := settingsresolve.New(contract).Resolve(ctx, store,
		settingsresolve.Context{ProfileID: profileID},
		[]string{settingskeys.RequestsWatchlistAutoRequest}, nil)
	if err != nil {
		return fail("reading setting values failed", err)
	}
	if len(resolved) != 1 {
		return false
	}
	var enabled bool
	if err := json.Unmarshal(resolved[0].Value, &enabled); err != nil {
		return fail("decoding the setting failed", err)
	}
	return enabled
}

func (s *Service) watchlistAutoRequest(ctx context.Context, viewer Viewer) bool {
	if s.watchlistPref == nil {
		return true
	}
	return s.watchlistPref.WatchlistAutoRequest(ctx, viewer.UserID, viewer.ProfileID)
}

// WatchlistRequestsEnabled reports whether a watchlist add requests for the
// viewer: requests and watchlist requests are on for the server, the profile
// has not opted out, and the account may request.
func (s *Service) WatchlistRequestsEnabled(ctx context.Context, viewer Viewer) (bool, error) {
	settings, err := s.store.GetSettings(ctx)
	if err != nil {
		return false, err
	}
	if !settings.RequestsEnabled || !settings.WatchlistRequests {
		return false, nil
	}
	if !s.watchlistAutoRequest(ctx, viewer) {
		return false, nil
	}
	return s.RequestCapabilityAllowed(ctx, viewer)
}

// WatchlistCeiling gates the watchlist-title operations, which live on the
// requests surface: it answers ErrRequestsDisabled while requests are off,
// and otherwise the viewer's parental rating ceiling ("" = unrestricted).
// A title above the ceiling is hidden from the viewer's watchlist the way
// Discover hides it.
func (s *Service) WatchlistCeiling(ctx context.Context, viewer Viewer) (string, error) {
	if err := validateViewer(viewer); err != nil {
		return "", err
	}
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return "", err
	}
	return s.viewerContentCeiling(ctx, viewer)
}

// WatchlistTitleDetail fetches the TMDB detail of a title being added to the
// viewer's watchlist. It answers ErrNotFound when TMDB has no such title or
// the title is above the viewer's rating ceiling, as GetDetail does, and
// reports the read to the title observer.
func (s *Service) WatchlistTitleDetail(ctx context.Context, viewer Viewer, mediaType MediaType, tmdbID int) (*tmdb.MediaDetail, error) {
	if s == nil || s.store == nil || s.tmdb == nil {
		return nil, fmt.Errorf("request service is not configured")
	}
	ceiling, err := s.WatchlistCeiling(ctx, viewer)
	if err != nil {
		return nil, err
	}
	mediaType, err = normalizeMediaType(mediaType)
	if err != nil {
		return nil, err
	}
	if tmdbID <= 0 {
		return nil, fmt.Errorf("%w: tmdb id is required", ErrInvalidInput)
	}
	raw, err := s.tmdb.GetMediaDetail(ctx, string(mediaType), tmdbID)
	s.observeDetail(ctx, mediaType, tmdbID, raw, err)
	switch {
	case errors.Is(err, tmdb.ErrNotFound):
		return nil, ErrNotFound
	case err != nil:
		return nil, err
	case raw == nil:
		return nil, ErrNotFound
	}
	// The US certification alone, as the discovery filter and the stored
	// watchlist snapshot use it; a missing one fails closed.
	if ceiling != "" && !access.RatingAllowed(raw.USCertification, ceiling) {
		return nil, ErrNotFound
	}
	return raw, nil
}

// WatchlistTitle is what the watchlist knows about a title it asks to
// request: its IDs and the stored display fields.
type WatchlistTitle struct {
	MediaType    MediaType
	TMDBID       int
	IMDbID       string
	TVDBID       int
	Title        string
	Year         int
	PosterPath   string
	BackdropPath string
	Overview     string
	// FormerTMDBIDs are IDs TMDB replaced; a request made under one of them
	// is still the title's request.
	FormerTMDBIDs []int
}

// tmdbIDs returns the title's current TMDB ID followed by its former ones.
func (t WatchlistTitle) tmdbIDs() []int {
	return append([]int{t.TMDBID}, t.FormerTMDBIDs...)
}

// requestOf returns the title's active request: the one under its current
// TMDB ID, else one under a former ID.
func (t WatchlistTitle) requestOf(active map[int]*Request) *Request {
	for _, id := range t.tmdbIDs() {
		if req := active[id]; req != nil {
			return req
		}
	}
	return nil
}

func (t WatchlistTitle) presenceCandidate() PresenceCandidate {
	candidate := PresenceCandidate{TMDBID: t.TMDBID, IMDbID: strings.TrimSpace(t.IMDbID)}
	if t.TVDBID > 0 {
		tvdb := t.TVDBID
		candidate.TVDBID = &tvdb
	}
	return candidate
}

// WatchlistKey names a title in the WatchlistRequestStates answer.
type WatchlistKey struct {
	MediaType MediaType
	TMDBID    int
}

// RequestFromWatchlist applies watchlist requests to a title just added to
// the viewer's watchlist, and returns the title's request state.
//
//   - No open request: request it as the viewer, recorded as source watchlist.
//     A series gets the default seasons, every aired season still missing.
//   - Someone else's open request: follow it.
//   - The viewer's own open request: nothing.
//
// A refused request (quota, blocked, requests disabled for the account) is
// logged and reported through the state's reason; it never fails the call,
// so the caller keeps the entry. Repeating the call is a no-op once the
// request or follow exists.
func (s *Service) RequestFromWatchlist(ctx context.Context, viewer Viewer, title WatchlistTitle) (RequestState, error) {
	if err := validateViewer(viewer); err != nil {
		return RequestState{}, err
	}
	ctx = withPolicyCache(ctx)
	if err := s.ensureRequestsEnabled(ctx); err != nil {
		return RequestState{}, err
	}
	mediaType, err := normalizeMediaType(title.MediaType)
	if err != nil {
		return RequestState{}, err
	}
	title.MediaType = mediaType
	enabled, err := s.WatchlistRequestsEnabled(ctx, viewer)
	if err != nil {
		return RequestState{}, err
	}
	refusal := ""
	if enabled {
		refusal = s.applyWatchlistRequest(ctx, viewer, title)
	}
	states, err := s.WatchlistRequestStates(ctx, viewer, []WatchlistTitle{title})
	if err != nil {
		return RequestState{}, err
	}
	state := states[WatchlistKey{MediaType: mediaType, TMDBID: title.TMDBID}]
	if refusal != "" && state.RequestID == "" && state.Status == "" {
		state.Requestable = false
		state.Reason = refusal
	}
	return state, nil
}

// applyWatchlistRequest creates or follows the title's request and returns
// the reason a refused request gives, if any.
func (s *Service) applyWatchlistRequest(ctx context.Context, viewer Viewer, title WatchlistTitle) string {
	active, err := s.store.ListActiveByTMDB(ctx, title.MediaType, title.tmdbIDs())
	if err != nil {
		s.logWatchlistRequest(ctx, "reading the title's request failed", viewer, title, err)
		return ""
	}
	if req := title.requestOf(active); req != nil {
		s.followFromWatchlist(ctx, viewer, title, req)
		return ""
	}
	input := CreateRequestInput{
		MediaType:    title.MediaType,
		TMDBID:       title.TMDBID,
		IMDbID:       title.IMDbID,
		Title:        title.Title,
		Overview:     title.Overview,
		PosterPath:   title.PosterPath,
		BackdropPath: title.BackdropPath,
		Source:       SourceWatchlist,
	}
	if title.TVDBID > 0 {
		tvdb := title.TVDBID
		input.TVDBID = &tvdb
	}
	if title.Year > 0 {
		year := title.Year
		input.Year = &year
	}
	_, err = s.CreateRequest(ctx, viewer, input)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrAlreadyRequested):
		// Another request landed between the read and the create.
		active, err := s.store.ListActiveByTMDB(ctx, title.MediaType, title.tmdbIDs())
		if err != nil {
			s.logWatchlistRequest(ctx, "reading the title's request failed", viewer, title, err)
			return ""
		}
		if req := title.requestOf(active); req != nil {
			s.followFromWatchlist(ctx, viewer, title, req)
		}
		return ""
	}
	s.logWatchlistRequest(ctx, "watchlist request refused", viewer, title, err)
	return watchlistRefusalReason(err)
}

func (s *Service) followFromWatchlist(ctx context.Context, viewer Viewer, title WatchlistTitle, req *Request) {
	if req.requestedBy(viewer) {
		return
	}
	// Follow under the request's own ID, which differs from the title's once
	// TMDB repointed the title.
	if _, err := s.Follow(ctx, viewer, title.MediaType, req.TMDBID); err != nil && !errors.Is(err, ErrNotRequested) {
		s.logWatchlistRequest(ctx, "following the title's request failed", viewer, title, err)
	}
}

func (s *Service) logWatchlistRequest(ctx context.Context, msg string, viewer Viewer, title WatchlistTitle, err error) {
	slog.InfoContext(ctx, "watchlist request not made", "component", "requests", "outcome", msg,
		"user_id", viewer.UserID, "profile_id", viewer.ProfileID,
		"media_type", title.MediaType, "tmdb_id", title.TMDBID, "error", err)
}

// Request-state reasons a refused watchlist request reports.
const (
	reasonQuotaExceeded    = "quota_exceeded"
	reasonBlocked          = "blocked"
	reasonRequestsDisabled = "requests_disabled"
	reasonAlreadyAvailable = "already_available"
)

// watchlistRefusalReason maps a refused create onto the request-state reasons
// clients already know. An error without one (a transient failure) leaves the
// state as computed.
func watchlistRefusalReason(err error) string {
	var quota QuotaError
	switch {
	case errors.As(err, &quota), errors.Is(err, ErrQuotaExceeded):
		return reasonQuotaExceeded
	case errors.Is(err, ErrUserBlocked):
		return reasonBlocked
	case errors.Is(err, ErrRequestsDisabled), errors.Is(err, ErrForbidden):
		return reasonRequestsDisabled
	case errors.Is(err, ErrAlreadyAvailable):
		return reasonAlreadyAvailable
	default:
		return ""
	}
}

// WithdrawWatchlistRequest undoes watchlist requests for a title leaving the
// viewer's watchlist: it cancels the viewer's open request for it when the
// watchlist created that request and nothing has been sent for it yet, then
// drops the viewer's follows. Any other request is left alone.
func (s *Service) WithdrawWatchlistRequest(ctx context.Context, viewer Viewer, mediaType MediaType, tmdbID int) error {
	if err := validateViewer(viewer); err != nil {
		return err
	}
	mediaType, err := normalizeMediaType(mediaType)
	if err != nil {
		return err
	}
	active, err := s.store.ListActiveByTMDB(ctx, mediaType, []int{tmdbID})
	if err != nil {
		return err
	}
	if req := active[tmdbID]; req != nil && req.Source == SourceWatchlist && req.requestedBy(viewer) {
		// The guard decides under the row lock: a request sent meanwhile
		// stays in the pipeline.
		if _, err := s.store.SetOutcome(ctx, req.ID, guardWithdrawable, OutcomeCancelled, viewer, withdrawnFromWatchlist); err != nil &&
			!errors.Is(err, ErrInvalidState) && !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return s.store.UnfollowTitle(ctx, mediaType, tmdbID, viewer)
}

// WithdrawProfileWatchlistRequests cancels the requests a profile's watchlist
// made that nothing has been sent for yet, for a profile being deleted. It
// needs no watchlist entries, which the profile delete removes: a request
// names the profile that made it. Requests already sent stay in the pipeline,
// as they do when the profile removes a title.
func (s *Service) WithdrawProfileWatchlistRequests(ctx context.Context, userID int, profileID string) error {
	reqs, err := s.store.ListProfileWatchlistRequests(ctx, userID, profileID)
	if err != nil {
		return err
	}
	actor := Viewer{UserID: userID, ProfileID: profileID}
	var errs []error
	for _, req := range reqs {
		if _, err := s.store.SetOutcome(ctx, req.ID, guardWithdrawable, OutcomeCancelled, actor, withdrawnFromWatchlist); err != nil &&
			!errors.Is(err, ErrInvalidState) && !errors.Is(err, ErrNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// WatchlistRequestStates returns the request state of each watchlist title for
// the viewer, the way Discover hydrates a page, plus the download progress of
// titles being downloaded from one read of the page's targets. It uses the
// stored IDs and makes no TMDB call.
func (s *Service) WatchlistRequestStates(ctx context.Context, viewer Viewer, titles []WatchlistTitle) (map[WatchlistKey]RequestState, error) {
	out := make(map[WatchlistKey]RequestState, len(titles))
	if len(titles) == 0 {
		return out, nil
	}
	ctx = withPolicyCache(ctx)
	policy, err := s.EffectivePolicy(ctx, viewer.UserID)
	if err != nil {
		return nil, err
	}
	byType := map[MediaType][]WatchlistTitle{}
	for _, t := range titles {
		mediaType, err := normalizeMediaType(t.MediaType)
		if err != nil || t.TMDBID <= 0 {
			continue
		}
		t.MediaType = mediaType
		byType[mediaType] = append(byType[mediaType], t)
	}
	downloading := map[string]WatchlistKey{}
	for mediaType, group := range byType {
		ids := make([]int, 0, len(group))
		candidates := make([]PresenceCandidate, len(group))
		for i, t := range group {
			ids = append(ids, t.tmdbIDs()...)
			candidates[i] = t.presenceCandidate()
		}
		presence, err := s.lookupPresence(ctx, mediaType, candidates)
		if err != nil {
			return nil, err
		}
		active, err := s.store.ListActiveByTMDB(ctx, mediaType, ids)
		if err != nil {
			return nil, err
		}
		following, err := s.followedTitles(ctx, viewer, mediaType, active)
		if err != nil {
			return nil, err
		}
		for _, t := range group {
			req := t.requestOf(active)
			state := requestStateFor(viewer, policy, presence[t.TMDBID].Available, req)
			state.Following = req != nil && following[req.TMDBID]
			key := WatchlistKey{MediaType: mediaType, TMDBID: t.TMDBID}
			out[key] = state
			if req != nil && req.Outcome == OutcomeActive && (req.Status == StatusQueued || req.Status == StatusDownloading) {
				downloading[req.ID] = key
			}
		}
	}
	if len(downloading) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(downloading))
	for id := range downloading {
		ids = append(ids, id)
	}
	targets, err := s.store.ListTargetsForRequests(ctx, ids)
	if err != nil {
		return nil, err
	}
	for id, key := range downloading {
		state := out[key]
		state.Download = (&Request{Targets: targets[id]}).Download()
		out[key] = state
	}
	return out, nil
}

// observeDetail reports a GetDetail TMDB read to the title observer.
func (s *Service) observeDetail(ctx context.Context, mediaType MediaType, tmdbID int, detail *tmdb.MediaDetail, err error) {
	if s.titleObserver == nil {
		return
	}
	switch {
	case errors.Is(err, tmdb.ErrNotFound):
		s.titleObserver.ObservedNotFound(ctx, string(mediaType), tmdbID)
	case err == nil && detail != nil:
		s.titleObserver.ObservedDetail(ctx, string(mediaType), tmdbID, detail)
	}
}
