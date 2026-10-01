package apiv2

import (
	"encoding/json"
	"time"
)

func downloadSubscriptionMutationFixtureCases() []fixtureCase {
	viewer := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	row := syntheticDownloadSubscription()
	tag := downloadSubscriptionOf(row).ETag
	row.Active = true
	row.UpdatedAt = row.UpdatedAt.Add(time.Microsecond)
	updatedTag := downloadSubscriptionOf(row).ETag
	syncBody, _ := json.Marshal(map[string]string{"subscription_id": "monitor", "etag": tag})
	path := Prefix + "/downloads/subscriptions"
	return []fixtureCase{
		{name: "download_subscription_create", operationID: "createDownloadSubscription", method: "POST", path: path, headers: viewer, body: `{"series_id":"series","mode":"all","delete_watched":false,"max_storage_bytes":0}`, status: 200, schema: "#/components/schemas/DownloadSubscription", assertHeaders: []string{"Content-Type", "ETag"}, scenario: "Create returns the persisted monitor; an existing paused monitor retains its current options and validator."},
		{name: "download_subscription_patch", operationID: "updateDownloadSubscription", method: "PATCH", path: path + "/monitor", headers: with(viewer, "If-Match", tag), body: `{"active":true}`, status: 200, schema: "#/components/schemas/DownloadSubscription", assertHeaders: []string{"Content-Type", "ETag"}, scenario: "A guarded edit returns the stored monitor and fresh validator; registration uses explicit bounded sync."},
		{name: "download_subscription_delete", operationID: "deleteDownloadSubscription", method: "DELETE", path: path + "/monitor", headers: with(viewer, "If-Match", updatedTag), status: 204, scenario: "Guarded monitor deletion retains already-registered episode downloads."},
		{name: "download_subscription_sync", operationID: "syncDownloadSubscription", method: "POST", path: path + "/sync", headers: viewer, body: string(syncBody), status: 200, schema: "#/components/schemas/DownloadSubscriptionSync", assertHeaders: []string{"Content-Type"}, scenario: "An examined terminal episode page reports only newly registered entries; retries can return zero."},
	}
}
