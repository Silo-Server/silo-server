package apiv2

import (
	"net/http"
)

type notificationSeriesPage struct {
	Items []NotificationSeries `json:"items"`
	Page  PageInfo             `json:"page"`
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
		{name: "notification_mute_many_notification_series", operationID: opMuteManyNotificationSeries,
			scenario: "The acting profile mutes several series at once; each distinct series gets an outcome in request order, and a series the viewer may not see is not_found.",
			method:   http.MethodPost, path: "/api/v2/notifications/series/mute", headers: profileOwner(),
			body:   `{"series_ids":["series:bear","series:wire","series:hidden"]}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/NotificationSeriesMuteResults"},
		{name: "notification_unmute_many_notification_series", operationID: opUnmuteManyNotificationSeries,
			scenario: "The acting profile unmutes several series at once; a series that was not muted is unchanged.",
			method:   http.MethodPost, path: "/api/v2/notifications/series/unmute", headers: profileOwner(),
			body:   `{"series_ids":["series:wire","series:bear"]}`,
			status: http.StatusOK, assertHeaders: []string{"Content-Type", "Cache-Control"}, schema: "#/components/schemas/NotificationSeriesMuteResults"},
		{name: "notification_unmute_notification_series", operationID: opUnmuteNotificationSeries,
			scenario: "The acting profile unmutes a series; it notifies again from the next new episode. Unmuting a series that is not muted changes nothing.",
			method:   http.MethodDelete, path: "/api/v2/notifications/series/series:wire/mute", headers: profileOwner(),
			status: http.StatusNoContent, assertHeaders: []string{"Cache-Control"}},
	}
}
