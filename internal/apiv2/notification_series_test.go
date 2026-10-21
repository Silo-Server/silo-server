package apiv2

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type notificationSeriesPage struct {
	Items []NotificationSeries `json:"items"`
	Page  PageInfo             `json:"page"`
}

func notificationSeriesHandler(t *testing.T) (http.Handler, *fakePersonalLists) {
	t.Helper()
	lists := &fakePersonalLists{missing: map[string]bool{"series:hidden": true}}
	deps := pilotDeps(nil, nil)
	deps.NotificationInbox = fixtureNotificationInbox()
	deps.PersonalLists = lists
	return NewHandler(deps), lists
}

func getNotificationSeries(t *testing.T, h http.Handler, query string) notificationSeriesPage {
	t.Helper()
	rec := do(t, h, http.MethodGet, Prefix+"/notifications/series"+query, "", profileOwner())
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var page notificationSeriesPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func notificationSeriesIDs(items []NotificationSeries) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = item.Item.ContentID
	}
	return ids
}

func TestNotificationSeriesSearchAndReasonFilter(t *testing.T) {
	h, _ := notificationSeriesHandler(t)
	for query, want := range map[string][]string{
		"?q=BEAR":                        {"series:bear"},
		"?q=+horses+":                    {"series:slow-horses"},
		"?reason=watchlist":              {"series:slow-horses"},
		"?reason=favorite":               {"series:bear", "series:wire"},
		"?reason=favorite&status=active": {"series:bear"},
		"?reason=next_up&status=active":  {"series:andor"},
		"?q=zzz":                         {},
	} {
		if got := notificationSeriesIDs(getNotificationSeries(t, h, query).Items); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", query, got, want)
		}
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/notifications/series?reason=other", "", profileOwner()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/notifications/series?sort=rating", "", profileOwner()), TypeValidationFailed)
}

// Every sort orders its field with missing values last, then pages through
// the same order.
func TestNotificationSeriesSorts(t *testing.T) {
	h, _ := notificationSeriesHandler(t)
	for sort, want := range map[string][]string{
		"-title":                {"series:wire", "series:slow-horses", "series:bear", "series:andor"},
		"year":                  {"series:wire", "series:andor", "series:slow-horses", "series:bear"},
		"-year":                 {"series:bear", "series:slow-horses", "series:andor", "series:wire"},
		"-added_at":             {"series:wire", "series:slow-horses", "series:bear", "series:andor"},
		"latest_episode_added":  {"series:slow-horses", "series:wire", "series:andor", "series:bear"},
		"-latest_episode_added": {"series:bear", "series:andor", "series:wire", "series:slow-horses"},
	} {
		if got := notificationSeriesIDs(getNotificationSeries(t, h, "?sort="+sort).Items); !slices.Equal(got, want) {
			t.Errorf("sort %s = %v, want %v", sort, got, want)
		}
		var paged []string
		query := "?limit=1&sort=" + url.QueryEscape(sort)
		for range 6 {
			page := getNotificationSeries(t, h, query)
			paged = append(paged, notificationSeriesIDs(page.Items)...)
			if !page.Page.HasMore {
				break
			}
			query = "?limit=1&sort=" + url.QueryEscape(sort) + "&cursor=" + url.QueryEscape(page.Page.NextCursor)
		}
		if !slices.Equal(paged, want) {
			t.Errorf("sort %s paged = %v, want %v", sort, paged, want)
		}
	}
	first := getNotificationSeries(t, h, "?limit=1&sort=-year")
	for _, changed := range []string{"&sort=year", "&sort=-year&q=a", "&sort=-year&reason=favorite"} {
		requireProblem(t, do(t, h, http.MethodGet, Prefix+"/notifications/series?limit=1"+changed+"&cursor="+url.QueryEscape(first.Page.NextCursor), "", profileOwner()), TypeInvalidCursor)
	}
}

func notificationSeriesMuteHandler(t *testing.T) (http.Handler, *fakeNotificationInbox) {
	t.Helper()
	inbox := fixtureNotificationInbox()
	deps := pilotDeps(nil, nil)
	deps.NotificationInbox = inbox
	deps.PersonalLists = &fakePersonalLists{missing: map[string]bool{"series:hidden": true}}
	return NewHandler(deps), inbox
}

// A series the viewer may not see and an item that is not a series are 404
// on mute, and the hidden one is never written. Unmute runs no access check,
// so a profile can lift a mute on a series it can no longer see.
func TestNotificationSeriesMuteRefusals(t *testing.T) {
	h, inbox := notificationSeriesMuteHandler(t)
	requireProblem(t, do(t, h, http.MethodPut, Prefix+"/notifications/series/series:hidden/mute", "", profileOwner()), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPut, Prefix+"/notifications/series/"+notificationNotASeriesID+"/mute", "", profileOwner()), TypeNotFound)
	if len(inbox.mutes) != 0 {
		t.Fatalf("mutes = %v", inbox.mutes)
	}
	if rec := do(t, h, http.MethodDelete, Prefix+"/notifications/series/series:hidden/mute", "", profileOwner()); rec.Code != http.StatusNoContent {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !slices.Equal(inbox.unmutes, []string{"1/p-owner/series:hidden"}) {
		t.Fatalf("unmutes = %v", inbox.unmutes)
	}
}

func notificationSeriesFixtureCases() []fixtureCase {
	return []fixtureCase{{name: "notification_list_notification_series", operationID: opListNotificationSeries, scenario: "Series that can notify the acting profile, with one the viewer may not see omitted.", method: http.MethodGet, path: "/api/v2/notifications/series", headers: profileOwner(), status: http.StatusOK, schema: "#/components/schemas/NotificationSeriesCollection", assertHeaders: []string{"Content-Type", "Cache-Control"}}}
}

// notificationSeriesMuteFixtureCases pin muting and unmuting one series and
// the refusal of a series the viewer may not see.
func notificationSeriesMuteFixtureCases() []fixtureCase {
	problem := "#/components/schemas/Problem"
	return []fixtureCase{
		{name: "notification_mute_notification_series", operationID: opMuteNotificationSeries,
			scenario: "The acting profile mutes one series: no new-episode notification for it on any channel until it unmutes. Repeating the call changes nothing.",
			method:   http.MethodPut, path: "/api/v2/notifications/series/series:bear/mute", headers: profileOwner(),
			status: http.StatusNoContent, assertHeaders: []string{"Cache-Control"}},
		{name: "notification_mute_notification_series_not_found", operationID: opMuteNotificationSeries,
			scenario: "A series the viewer may not see cannot be muted.",
			method:   http.MethodPut, path: "/api/v2/notifications/series/series:hidden/mute", headers: profileOwner(),
			status: http.StatusNotFound, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: problem},
		{name: "notification_unmute_notification_series", operationID: opUnmuteNotificationSeries,
			scenario: "The acting profile unmutes a series; it notifies again from the next new episode. Unmuting a series that is not muted changes nothing.",
			method:   http.MethodDelete, path: "/api/v2/notifications/series/series:wire/mute", headers: profileOwner(),
			status: http.StatusNoContent, assertHeaders: []string{"Cache-Control"}},
	}
}

// Next Up waits for any episode past the last finished one; after a season
// finale that is the next season's first, so the response names the finale.
func TestNotificationSeriesNextUpNamesTheFinishedEpisode(t *testing.T) {
	expected := notifications.EpisodeKey(1, 10) + 1
	got := notificationSeriesOf(notifications.NotifiedSeries{Active: notifications.ReasonFlags{NextUp: true}, NextExpectedEpisodeKey: &expected}, CatalogItem{})
	if got.NextUpAfter == nil || *got.NextUpAfter != (NotificationEpisodeRef{SeasonNumber: 1, EpisodeNumber: 10}) {
		t.Fatalf("next_up_after = %+v, want S1E10", got.NextUpAfter)
	}
}
