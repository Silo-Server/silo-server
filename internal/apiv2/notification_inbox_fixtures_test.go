package apiv2

import "fmt"

func notificationInboxFixtureCases() []fixtureCase {
	token, err := NewCursors([]byte("fixture-cursor-key")).Encode(CursorScope{OperationID: "notificationReadCutoff", Security: "1/p-owner", Sort: "created_at,id", Tiebreaker: "id"}, fixtureNotificationInbox().cutoff)
	if err != nil {
		panic(err)
	}
	cases := []fixtureCase{
		{name: "notification_apple_push_display", operationID: "getNotificationApplePushDisplay", scenario: "Compact display metadata for an acting-profile delivery.", method: "GET", path: "/api/v2/notifications/push/apple/display/" + notificationFixtureID, headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationPushDisplay", assertHeaders: []string{"Cache-Control"}},
		{name: "notification_list_notifications", operationID: "listNotifications", scenario: "Profile notification inbox with synthetic records.", method: "GET", path: "/api/v2/notifications", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationListOutputBody", assertHeaders: []string{"Cache-Control"}},
		{name: "notification_get_notification_capabilities", operationID: "getNotificationCapabilities", scenario: "Profile notification inbox with synthetic records.", method: "GET", path: "/api/v2/notifications/capabilities", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationCapabilities", assertHeaders: []string{"Cache-Control"}},
		{name: "notification_get_notification_preferences", operationID: "getNotificationPreferences", scenario: "Profile notification inbox with synthetic records.", method: "GET", path: "/api/v2/notifications/preferences", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationPreferences", assertHeaders: []string{"Cache-Control"}},
		{name: "notification_update_notification_preferences", operationID: "updateNotificationPreferences", scenario: "Profile notification inbox with synthetic records.", method: "PUT", path: "/api/v2/notifications/preferences", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationPreferences", assertHeaders: []string{"Cache-Control"}, body: "{\"enabled\":false}"},
		{name: "notification_mark_notifications_read", operationID: "markNotificationsRead", scenario: "Profile notification inbox with synthetic records.", method: "POST", path: "/api/v2/notifications/read-all", headers: profileOwner(), status: 204, schema: "", assertHeaders: []string{"Cache-Control"}, body: fmt.Sprintf(`{"through":%q}`, token)},
		{name: "notification_sync_notifications", operationID: "syncNotifications", scenario: "Profile notification inbox with synthetic records.", method: "GET", path: "/api/v2/notifications/sync", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationSyncOutputBody", assertHeaders: []string{"Cache-Control"}},
		{name: "notification_get_notification_unread_count", operationID: "getNotificationUnreadCount", scenario: "Profile notification inbox with synthetic records.", method: "GET", path: "/api/v2/notifications/unread-count", headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationCountOutputBody", assertHeaders: []string{"Cache-Control"}},
		{name: "notification_get_notification", operationID: "getNotification", scenario: "Profile notification inbox with synthetic records.", method: "GET", path: "/api/v2/notifications/" + notificationFixtureID, headers: profileOwner(), status: 200, schema: "#/components/schemas/NotificationItem", assertHeaders: []string{"Cache-Control"}},
		{name: "notification_mark_notification_read", operationID: "markNotificationRead", scenario: "Profile notification inbox with synthetic records.", method: "POST", path: "/api/v2/notifications/" + notificationFixtureID + "/read", headers: profileOwner(), status: 204, schema: "", assertHeaders: []string{"Cache-Control"}},
	}
	for i := range cases {
		if cases[i].schema != "" {
			cases[i].assertHeaders = append(cases[i].assertHeaders, "Content-Type")
		}
	}
	return cases
}
