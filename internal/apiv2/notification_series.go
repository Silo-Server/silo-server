package apiv2

import (
	"cmp"
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/imagesize"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

// The notified-series list: every series whose new episodes the acting
// profile has a reason to hear about, the index episode fanout reads, so a
// profile can see why a series notifies it or why it does not. A profile can
// mute one series, which stops its new-episode notifications whatever its
// reasons, until it unmutes.

const (
	opListNotificationSeries   = "listNotificationSeries"
	opMuteNotificationSeries   = "muteNotificationSeries"
	opUnmuteNotificationSeries = "unmuteNotificationSeries"
)

// The listNotificationSeries sort fields besides the default title.
const (
	notificationSeriesSortYear          = "year"
	notificationSeriesSortAddedAt       = "added_at"
	notificationSeriesSortLatestEpisode = "latest_episode_added"
)

// The listNotificationSeries status filters besides the default "all".
const (
	notificationSeriesActive   = "active"
	notificationSeriesInactive = "inactive"
)

// NotificationSeriesInput is the listNotificationSeries query.
type NotificationSeriesInput struct {
	ArtworkQuery
	LimitParam
	Cursor string `query:"cursor" doc:"Opaque cursor from page.next_cursor" example:"eyJ0IjoiYmVhciJ9"`
	Status string `query:"status" default:"all" enum:"all,active,inactive" doc:"active keeps the series that notify; inactive keeps the ones that do not, because the profile's preferences silence every reason or the profile muted the series"`
	Reason string `query:"reason" enum:"favorite,watchlist,continue_watching,next_up" doc:"Keeps the series with this reason, whether or not the preferences let it notify; absent keeps every reason"`
	Q      string `query:"q" maxLength:"200" doc:"Keeps the series whose title or sort title contains this text, case-insensitively" example:"bear"`
	Sort   string `query:"sort" default:"title" enum:"title,-title,year,-year,added_at,-added_at,latest_episode_added,-latest_episode_added" doc:"One field, descending with a leading '-'. added_at is when the series joined the catalog; latest_episode_added when it last gained an episode. A series without a year or an episode sorts last either way; ties order by title."`
}

// NotificationSeriesReasons are the reasons a profile can hear about a
// series, keyed like an episode notification's reason_flags.
type NotificationSeriesReasons struct {
	Favorite         bool `json:"favorite" doc:"The series is one of the profile's favorites"`
	Watchlist        bool `json:"watchlist" doc:"The series is on the profile's watchlist"`
	ContinueWatching bool `json:"continue_watching" doc:"The profile is partway through the series"`
	NextUp           bool `json:"next_up" doc:"The profile finished an episode and waits for the next"`
}

func notificationSeriesReasonsOf(f notifications.ReasonFlags) NotificationSeriesReasons {
	return NotificationSeriesReasons{Favorite: f.Favorite, Watchlist: f.Watchlist, ContinueWatching: f.ContinueWatching, NextUp: f.NextUp}
}

// NotificationEpisodeRef names an episode by its season and episode number.
type NotificationEpisodeRef struct {
	SeasonNumber  int `json:"season_number" example:"2"`
	EpisodeNumber int `json:"episode_number" example:"9"`
}

// NotificationSeries is one series on the list.
type NotificationSeries struct {
	Item          CatalogItem               `json:"item" doc:"The series as a catalog card"`
	Reasons       NotificationSeriesReasons `json:"reasons" doc:"The profile's relationships to the series"`
	ActiveReasons NotificationSeriesReasons `json:"active_reasons" doc:"The reasons the profile's notification preferences let notify; all false when they silence the series"`
	Notifies      bool                      `json:"notifies" doc:"True when at least one reason is active and the profile has not muted the series"`
	Muted         bool                      `json:"muted" doc:"The profile muted the series: it gets no new-episode notification for it, whatever the reasons, until it unmutes. active_reasons still names the reasons that would notify once unmuted."`
	NextUpAfter   *NotificationEpisodeRef   `json:"next_up_after,omitempty" doc:"With an active Next Up reason, the last episode the profile finished: on that reason alone, only a new episode after it notifies"`
}

// NotificationSeriesCollection is the listNotificationSeries envelope.
type NotificationSeriesCollection struct {
	Collection[NotificationSeries]
}

// NotificationSeriesOutput is the listNotificationSeries response.
type NotificationSeriesOutput struct {
	Body NotificationSeriesCollection
}

// notificationSeriesPosition is the keyset of the last series a page
// emitted: every field an order compares, so the next page resumes strictly
// after it in any sort, even once that series has left the list.
type notificationSeriesPosition struct {
	SortKey  string     `json:"t"`
	Year     int        `json:"y,omitempty"`
	AddedAt  time.Time  `json:"a"`
	Latest   *time.Time `json:"l,omitempty"`
	SeriesID string     `json:"s"`
}

func notificationSeriesPositionOf(s notifications.NotifiedSeries) notificationSeriesPosition {
	return notificationSeriesPosition{SortKey: s.SortKey, Year: s.Year, AddedAt: s.AddedAt, Latest: s.LatestEpisodeAddedAt, SeriesID: s.SeriesID}
}

func (p notificationSeriesPosition) series() notifications.NotifiedSeries {
	return notifications.NotifiedSeries{SeriesID: p.SeriesID, SeriesSortFields: notifications.SeriesSortFields{SortKey: p.SortKey, Year: p.Year, AddedAt: p.AddedAt, LatestEpisodeAddedAt: p.Latest}}
}

// notificationSeriesOrder compares series in a listNotificationSeries sort:
// the field, then sort title and item ID ascending.
func notificationSeriesOrder(sort string) func(a, b notifications.NotifiedSeries) int {
	field, desc := strings.CutPrefix(sort, "-")
	return func(a, b notifications.NotifiedSeries) int {
		var primary int
		switch field {
		case notificationSeriesSortYear:
			primary = orderMissingLast(a.Year == 0, b.Year == 0, cmp.Compare(a.Year, b.Year), desc)
		case notificationSeriesSortAddedAt:
			primary = orderMissingLast(false, false, a.AddedAt.Compare(b.AddedAt), desc)
		case notificationSeriesSortLatestEpisode:
			aMissing, bMissing := a.LatestEpisodeAddedAt == nil, b.LatestEpisodeAddedAt == nil
			c := 0
			if !aMissing && !bMissing {
				c = a.LatestEpisodeAddedAt.Compare(*b.LatestEpisodeAddedAt)
			}
			primary = orderMissingLast(aMissing, bMissing, c, desc)
		default:
			primary = orderMissingLast(false, false, strings.Compare(a.SortKey, b.SortKey), desc)
		}
		return cmp.Or(primary, strings.Compare(a.SortKey, b.SortKey), strings.Compare(a.SeriesID, b.SeriesID))
	}
}

// orderMissingLast applies a direction to c, keeping missing values last.
func orderMissingLast(aMissing, bMissing bool, c int, desc bool) int {
	switch {
	case aMissing && bMissing:
		return 0
	case aMissing:
		return 1
	case bMissing:
		return -1
	case desc:
		return -c
	}
	return c
}

// notificationSeriesMatch reports whether a series passes the query's
// status, reason, and search filters.
func notificationSeriesMatch(in *NotificationSeriesInput, query string) func(notifications.NotifiedSeries) bool {
	return func(s notifications.NotifiedSeries) bool {
		switch {
		case in.Status == notificationSeriesActive && !s.Notifies(),
			in.Status == notificationSeriesInactive && s.Notifies():
			return false
		case in.Reason != "" && !s.Reasons.Has(in.Reason):
			return false
		}
		return query == "" || strings.Contains(strings.ToLower(s.Title), query) || strings.Contains(s.SortKey, query)
	}
}

func registerNotificationSeries(reg *Registry) {
	cursors := NewCursors(reg.deps.CursorSecret)
	op := notificationOperation(http.MethodGet, "/series", opListNotificationSeries)
	op.Summary = "List the series whose new episodes can notify the acting profile."
	op.Description = "Every series the profile favorited, put on its watchlist, is partway through, or waits on a next episode of, and every series it muted, filtered and sorted as asked (by title by default). Each carries its reasons, which of them the profile's notification preferences let notify, and whether the profile muted it. Series the viewer may not see are omitted."
	Register(reg, op, func(ctx context.Context, in *NotificationSeriesInput) (*NotificationSeriesOutput, error) {
		return reg.listNotificationSeries(ctx, cursors, in)
	})

	mute := notificationOperation(http.MethodPut, "/series/{series_id}/mute", opMuteNotificationSeries)
	mute.DefaultStatus = http.StatusNoContent
	mute.Summary = "Mute new-episode notifications for one series."
	mute.Description = "The acting profile gets no new-episode notification for the series on any channel, whatever its reasons, until it unmutes. Favorites, the watchlist, Home, and watch history are unchanged, and other profiles are unaffected. Muting a series already muted succeeds and changes nothing. A series the viewer may not see, or an item that is not a series, is 404."
	Register(reg, mute, reg.muteNotificationSeries)

	unmute := notificationOperation(http.MethodDelete, "/series/{series_id}/mute", opUnmuteNotificationSeries)
	unmute.DefaultStatus = http.StatusNoContent
	unmute.Summary = "Unmute new-episode notifications for one series."
	unmute.Description = "The series notifies the acting profile again for its active reasons. The mute applies when notification delivery evaluates a new episode, normally within a minute of it arriving: an episode evaluated while the series was muted is not announced later, and one still waiting to be evaluated when the profile unmutes is announced. Unmuting a series that is not muted succeeds and changes nothing. No access check runs, so a profile can lift a mute on a series it can no longer see."
	Register(reg, unmute, reg.unmuteNotificationSeries)
}

// NotificationSeriesMuteInput names the series to mute or unmute.
type NotificationSeriesMuteInput struct {
	SeriesID ID `path:"series_id" minLength:"1" doc:"Catalog item identifier of the series" example:"series-tvdb-81189"`
}

func (reg *Registry) muteNotificationSeries(ctx context.Context, in *NotificationSeriesMuteInput) (*struct{}, error) {
	svc, p := reg.notificationInbox()
	if p != nil {
		return nil, p
	}
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("notification series")
	}
	viewer, p := personalListViewer(ctx, imagesize.Request{})
	if p != nil {
		return nil, p
	}
	seriesID := string(in.SeriesID)
	visible, err := reg.deps.PersonalLists.VisibleItemIDs(ctx, viewer, []string{seriesID})
	if err != nil {
		return nil, serviceProblem(err)
	}
	if !visible[seriesID] {
		return nil, NewProblem(TypeNotFound, "Series not found.")
	}
	if err := svc.MuteNotificationSeries(ctx, viewer.UserID, viewer.ProfileID, seriesID); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) unmuteNotificationSeries(ctx context.Context, in *NotificationSeriesMuteInput) (*struct{}, error) {
	svc, p := reg.notificationInbox()
	if p != nil {
		return nil, p
	}
	userID, profileID, p := viewerIdentity(ctx)
	if p != nil {
		return nil, p
	}
	if err := svc.UnmuteNotificationSeries(ctx, userID, profileID, string(in.SeriesID)); err != nil {
		return nil, serviceProblem(err)
	}
	return nil, nil
}

func (reg *Registry) listNotificationSeries(ctx context.Context, cursors *Cursors, in *NotificationSeriesInput) (*NotificationSeriesOutput, error) {
	svc, p := reg.notificationInbox()
	if p != nil {
		return nil, p
	}
	if reg.deps.PersonalLists == nil {
		return nil, unavailable("notification series")
	}
	viewer, p := personalListViewer(ctx, in.Artwork())
	if p != nil {
		return nil, p
	}
	query := strings.ToLower(strings.TrimSpace(in.Q))
	scope := CursorScope{
		OperationID: opListNotificationSeries,
		Security:    strconv.Itoa(viewer.UserID) + "/" + viewer.ProfileID + "/" + viewerScopeDigest(ctx),
		Filter:      in.Status + "|" + in.Reason + "|" + query,
		Sort:        in.Sort,
		Tiebreaker:  tiebreakerItemID,
	}
	var after *notificationSeriesPosition
	if in.Cursor != "" {
		after = new(notificationSeriesPosition)
		if p := cursors.Decode(scope, in.Cursor, after); p != nil {
			return nil, p
		}
	}
	all, err := svc.ListNotifiedSeries(ctx, viewer.UserID, viewer.ProfileID)
	if err != nil {
		return nil, serviceProblem(err)
	}
	order := notificationSeriesOrder(in.Sort)
	keep := notificationSeriesMatch(in, query)
	candidates := make([]notifications.NotifiedSeries, 0, len(all))
	ids := make([]string, 0, len(all))
	for _, s := range all {
		if keep(s) {
			candidates = append(candidates, s)
			ids = append(ids, s.SeriesID)
		}
	}
	// Pages hold only series the viewer may see, so a cursor never carries a
	// hidden series' sort fields and no page comes back empty with more behind it.
	visible, err := reg.deps.PersonalLists.VisibleItemIDs(ctx, viewer, ids)
	if err != nil {
		return nil, serviceProblem(err)
	}
	matches := slices.DeleteFunc(candidates, func(s notifications.NotifiedSeries) bool { return !visible[s.SeriesID] })
	slices.SortFunc(matches, order)
	start := 0
	if after != nil {
		last := after.series()
		start = len(matches)
		if i := slices.IndexFunc(matches, func(s notifications.NotifiedSeries) bool { return order(s, last) > 0 }); i >= 0 {
			start = i
		}
	}
	// A limit+1 probe decides has_more.
	page := matches[start:min(start+in.Limit+1, len(matches))]
	next := ""
	if len(page) > in.Limit {
		page = page[:in.Limit]
		if next, err = cursors.Encode(scope, notificationSeriesPositionOf(page[len(page)-1])); err != nil {
			return nil, serviceProblem(err)
		}
	}
	pageIDs := make([]string, len(page))
	for i, s := range page {
		pageIDs[i] = s.SeriesID
	}
	cards, err := reg.deps.PersonalLists.ResolveCards(ctx, viewer, pageIDs)
	if err != nil {
		return nil, serviceProblem(err)
	}
	sel := reg.ratingSelection(ctx)
	byID := make(map[string]int, len(cards))
	for i, card := range cards {
		byID[card.ContentID] = i
	}
	items := make([]NotificationSeries, 0, len(cards))
	for _, s := range page {
		if i, ok := byID[s.SeriesID]; ok {
			items = append(items, notificationSeriesOf(s, catalogItemOfListing(cards[i], sel)))
		}
	}
	return &NotificationSeriesOutput{Body: NotificationSeriesCollection{Collection: Paginated(items, next)}}, nil
}

func notificationSeriesOf(s notifications.NotifiedSeries, item CatalogItem) NotificationSeries {
	out := NotificationSeries{
		Item:          item,
		Reasons:       notificationSeriesReasonsOf(s.Reasons),
		ActiveReasons: notificationSeriesReasonsOf(s.Active),
		Notifies:      s.Notifies(),
		Muted:         s.Muted,
	}
	// Next Up waits for any episode keyed at or past last-finished + 1, which
	// need not exist (after a season finale the next is the following season's
	// first), so the contract names the finished episode instead.
	if s.NextExpectedEpisodeKey != nil {
		season, episode := notifications.SplitEpisodeKey(*s.NextExpectedEpisodeKey - 1)
		out.NextUpAfter = &NotificationEpisodeRef{SeasonNumber: season, EpisodeNumber: episode}
	}
	return out
}
