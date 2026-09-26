package apiv2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeNotificationDestinations struct {
	deleted string
	calls   int
	profile string
	limit   int
}

func (f *fakeNotificationDestinations) ListNotificationWebPushPage(_ context.Context, profile string, limit int, after *notifications.Cursor) ([]notifications.WebPushSubscription, error) {
	f.profile, f.limit = profile, limit
	rows := []notifications.WebPushSubscription{{ID: "01", Endpoint: "https://push.example.test/1", P256dh: "sensitive-p256dh", Auth: "sensitive-auth", CreatedAt: fixedTime()}, {ID: "02", Endpoint: "https://push.example.test/2", CreatedAt: fixedTime()}}
	if after != nil {
		rows = rows[1:]
	}
	return rows, nil
}
func (f *fakeNotificationDestinations) ListNotificationWebhookPage(_ context.Context, profile string, limit int, after *notifications.Cursor) ([]notifications.Webhook, error) {
	f.profile, f.limit = profile, limit
	rows := []notifications.Webhook{{ID: "01", ProfileID: profile, Revision: 7, Name: "Example", Type: "generic", URLHost: "example.test", URLCiphertext: "sensitive-url", SigningSecretCiphertext: new("sensitive-secret"), CreatedAt: fixedTime()}, {ID: "02", ProfileID: profile, Revision: 7, Name: "Other", Type: "generic", URLHost: "example.test", CreatedAt: fixedTime()}}
	if after != nil {
		rows = rows[1:]
	}
	return rows, nil
}
func (f *fakeNotificationDestinations) ListNotificationServerChannelPage(_ context.Context, limit int, after *notifications.Cursor) ([]notifications.ServerChannel, error) {
	f.limit = limit
	rows := []notifications.ServerChannel{{ID: "01", Name: "Example", Type: "generic", URLHost: "example.test", URLCiphertext: "sensitive-url", SigningSecretCiphertext: new("sensitive-secret"), CreatedAt: fixedTime()}, {ID: "02", Name: "Other", Type: "generic", URLHost: "example.test", CreatedAt: fixedTime()}}
	if after != nil {
		rows = rows[1:]
	}
	return rows, nil
}

func TestNotificationDestinationCursors(t *testing.T) {
	fake := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = fake
	h := NewHandler(deps)
	for _, tc := range []struct {
		path    string
		headers map[string]string
		profile string
	}{
		{Prefix + "/notifications/web-push/subscriptions", profileOwner(), "p-owner"},
		{Prefix + "/notifications/webhooks", profileOwner(), "p-owner"},
		{Prefix + "/admin/notifications/server-channels", bearer(adminToken), ""},
	} {
		t.Run(tc.path, func(t *testing.T) {
			rec := do(t, h, http.MethodGet, tc.path+"?limit=1", "", tc.headers)
			if rec.Code != 200 || fake.limit != 2 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "sensitive") {
				t.Fatal("destination credential exposed")
			}
			var page struct {
				Items []struct {
					ID string `json:"id"`
				} `json:"items"`
				Page PageInfo `json:"page"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 1 || page.Items[0].ID != "01" || !page.Page.HasMore || page.Page.NextCursor == "" {
				t.Fatalf("%+v", page)
			}
			query := "?limit=1&cursor=" + url.QueryEscape(page.Page.NextCursor)
			rec = do(t, h, http.MethodGet, tc.path+query, "", tc.headers)
			if rec.Code != 200 {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Items) != 1 || page.Items[0].ID != "02" || page.Page.HasMore {
				t.Fatalf("%+v", page)
			}
			requireProblem(t, do(t, h, http.MethodGet, tc.path+strings.Replace(query, "limit=1", "limit=2", 1), "", tc.headers), TypeInvalidCursor)
			if tc.profile != "" {
				requireProblem(t, do(t, h, http.MethodGet, tc.path+query, "", with(bearer(memberToken), "X-Profile-Id", "p-primary")), TypeInvalidCursor)
			}
		})
	}
	requireProblem(t, do(t, h, http.MethodGet, Prefix+"/admin/notifications/server-channels", "", profileOwner()), TypePermissionDenied)
}

func notificationDestinationFixtureCases() []fixtureCase {
	cases := []fixtureCase{
		{name: "notification_web_push_subscriptions", operationID: listNotificationWebPushOperation, path: Prefix + "/notifications/web-push/subscriptions", headers: profileOwner(), schema: "#/components/schemas/CollectionNotificationWebPushSubscription"},
		{name: "notification_webhooks", operationID: listNotificationWebhooksOperation, path: Prefix + "/notifications/webhooks", headers: profileOwner(), schema: "#/components/schemas/CollectionNotificationWebhookDestination"},
		{name: "notification_server_channels", operationID: listNotificationServerChannelsOperation, path: Prefix + "/admin/notifications/server-channels", headers: bearer(adminToken), schema: "#/components/schemas/CollectionNotificationServerChannel"},
	}
	for i := range cases {
		cases[i].method = "GET"
		cases[i].status = 200
		cases[i].scenario = "Bounded notification destination metadata excludes reusable keys and webhook URL ciphertext."
		cases[i].assertHeaders = []string{"Content-Type", "Cache-Control"}
	}
	return cases
}

func (f *fakeNotificationDestinations) DeleteNotificationWebPushSubscription(_ context.Context, _ int, profile, id string) error {
	f.calls++
	f.profile, f.deleted = profile, id
	return nil
}
func TestNotificationWebPushDelete(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/web-push/subscriptions/row-one"
	for range 2 {
		rec := do(t, h, http.MethodDelete, path, "", profileOwner())
		if rec.Code != 204 || rec.Body.Len() != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	if f.calls != 2 || f.profile != "p-owner" || f.deleted != "row-one" {
		t.Fatalf("%+v", f)
	}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	if f.calls != 2 {
		t.Fatal("unauthorized delete dispatched")
	}
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodDelete, path, "", profileOwner()), TypeDependencyUnavailable)
}

func (f *fakeNotificationDestinations) UnsubscribeNotificationWebPush(_ context.Context, _ int, profile, endpoint string) error {
	f.calls++
	f.profile, f.deleted = profile, endpoint
	return nil
}
func TestNotificationWebPushUnsubscribe(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/web-push/unsubscribe"
	rec := do(t, h, http.MethodPost, path, `{"endpoint":"https://push.example.test/opaque"}`, profileOwner())
	if rec.Code != 204 || rec.Body.Len() != 0 || f.calls != 1 || f.profile != "p-owner" || f.deleted != "https://push.example.test/opaque" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"endpoint":""}`, profileOwner()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, `{"endpoint":"opaque"}`, nil), TypeAuthenticationRequired)
	if f.calls != 1 {
		t.Fatal("invalid request dispatched")
	}
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, `{"endpoint":"opaque"}`, profileOwner()), TypeDependencyUnavailable)
}

func (f *fakeNotificationDestinations) SubscribeNotificationWebPush(_ context.Context, _ int, profile, endpoint, p256dh, auth, _ string) (*notifications.WebPushSubscription, error) {
	f.calls++
	f.profile, f.deleted = profile, endpoint
	if endpoint == "invalid" {
		return nil, notifications.ErrWebPushInvalid
	}
	return &notifications.WebPushSubscription{ID: "row-one", Endpoint: endpoint, P256dh: p256dh, Auth: auth, CreatedAt: fixedTime()}, nil
}
func TestNotificationWebPushSubscribe(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/web-push/subscriptions"
	body := `{"endpoint":"https://push.example.test/opaque","keys":{"p256dh":"secret-key","auth":"secret-auth"}}`
	rec := do(t, h, http.MethodPost, path, body, profileOwner())
	if rec.Code != 201 || f.calls != 1 || f.profile != "p-owner" || f.deleted != "https://push.example.test/opaque" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	if strings.Contains(rec.Body.String(), "secret-") {
		t.Fatal("write-only credentials exposed")
	}
	requireProblem(t, do(t, h, http.MethodPost, path, `{"endpoint":"opaque","keys":{}}`, profileOwner()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, path, body, nil), TypeAuthenticationRequired)
	if f.calls != 1 {
		t.Fatal("invalid request dispatched")
	}
	requireProblem(t, do(t, h, http.MethodPost, path, strings.Replace(body, "https://push.example.test/opaque", "invalid", 1), profileOwner()), TypeValidationFailed)
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, body, profileOwner()), TypeDependencyUnavailable)
}

func (f *fakeNotificationDestinations) DeleteNotificationWebhook(_ context.Context, profile, id string, check func(int64) error) error {
	if err := check(7); err != nil {
		return err
	}
	f.calls++
	f.profile, f.deleted = profile, id
	return nil
}
func TestNotificationWebhookDelete(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/webhooks/01"
	page := do(t, h, http.MethodGet, Prefix+"/notifications/webhooks?limit=1", "", profileOwner())
	var observed struct {
		Items []struct {
			ETag string `json:"etag"`
		} `json:"items"`
	}
	if err := json.Unmarshal(page.Body.Bytes(), &observed); err != nil || len(observed.Items) != 1 {
		t.Fatalf("list: %s %v", page.Body.String(), err)
	}
	for range 2 {
		rec := do(t, h, http.MethodDelete, path, "", with(profileOwner(), "If-Match", observed.Items[0].ETag))
		if rec.Code != 204 || rec.Body.Len() != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	if f.calls != 2 || f.profile != "p-owner" || f.deleted != "01" {
		t.Fatalf("%+v", f)
	}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", bearer(memberToken)), TypeValidationFailed)
	if f.calls != 2 {
		t.Fatal("unauthorized dispatch")
	}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", profileOwner()), TypePreconditionRequired)
	stale := do(t, h, http.MethodDelete, path, "", with(profileOwner(), "If-Match", notificationWebhookTag("p-owner", "01", 6).String()))
	requireProblem(t, stale, TypePreconditionFailed)
	if stale.Header().Get("ETag") != notificationWebhookTag("p-owner", "01", 7).String() || f.calls != 2 {
		t.Fatal("stale deletion passed or validator missing")
	}
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodDelete, path, "", profileOwner()), TypeDependencyUnavailable)
}

func (f *fakeNotificationDestinations) DeleteNotificationServerChannel(_ context.Context, id string) error {
	f.calls++
	f.deleted = id
	return nil
}
func TestNotificationServerChannelDelete(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/admin/notifications/server-channels/row-one"
	for range 2 {
		rec := do(t, h, http.MethodDelete, path, "", bearer(adminToken))
		if rec.Code != 204 || rec.Body.Len() != 0 {
			t.Fatalf("%d %s", rec.Code, rec.Body.String())
		}
	}
	if f.calls != 2 || f.deleted != "row-one" {
		t.Fatalf("%+v", f)
	}
	requireProblem(t, do(t, h, http.MethodDelete, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodDelete, path, "", profileOwner()), TypePermissionDenied)
	if f.calls != 2 {
		t.Fatal("unauthorized dispatch")
	}
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodDelete, path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

func (f *fakeNotificationDestinations) RotateNotificationWebhookSecret(_ context.Context, profile, id string) (string, error) {
	f.calls++
	f.profile, f.deleted = profile, id
	if id == "discord" {
		return "", notifications.ErrWebhookInvalid
	}
	if id == "missing" {
		return "", notifications.ErrWebhookNotFound
	}
	return "synthetic-secret", nil
}
func TestNotificationWebhookRotate(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/webhooks/row-one/rotate-secret"
	rec := do(t, h, http.MethodPost, path, "", profileOwner())
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"signing_secret":"synthetic-secret"`) || f.calls != 1 || f.profile != "p-owner" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, "", nil), TypeAuthenticationRequired)
	if f.calls != 1 {
		t.Fatal("unauthorized dispatch")
	}
	requireProblem(t, do(t, h, http.MethodPost, strings.Replace(path, "row-one", "discord", 1), "", profileOwner()), TypeValidationFailed)
	requireProblem(t, do(t, h, http.MethodPost, strings.Replace(path, "row-one", "missing", 1), "", profileOwner()), TypeNotFound)
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, "", profileOwner()), TypeDependencyUnavailable)
}

func (f *fakeNotificationDestinations) UpdateNotificationWebhook(_ context.Context, profile, id string, input notifications.WebhookInput, check func(int64) error) (*notifications.Webhook, error) {
	f.calls++
	if id == "missing" {
		return nil, notifications.ErrWebhookNotFound
	}
	if err := check(7); err != nil {
		return nil, err
	}
	if input.Name != nil && *input.Name == "" {
		return nil, notifications.ErrWebhookInvalid
	}
	return &notifications.Webhook{ID: id, ProfileID: profile, Name: "updated", Type: "generic", Revision: 8}, nil
}

func TestNotificationWebhookUpdate(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/notifications/webhooks/row-one"
	tag := notificationWebhookTag("p-owner", "row-one", 7).String()
	headers := profileOwner()
	headers["If-Match"] = tag
	rec := do(t, h, http.MethodPut, path, `{"name":"updated","enabled":false}`, headers)
	if rec.Code != 200 || rec.Header().Get("ETag") != notificationWebhookTag("p-owner", "row-one", 8).String() || !strings.Contains(rec.Body.String(), `"name":"updated"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	requireProblem(t, do(t, h, http.MethodPut, path, `{}`, profileOwner()), TypePreconditionRequired)
	headers["If-Match"] = notificationWebhookTag("p-owner", "row-one", 6).String()
	requireProblem(t, do(t, h, http.MethodPut, path, `{}`, headers), TypePreconditionFailed)
	requireProblem(t, do(t, h, http.MethodPut, Prefix+"/notifications/webhooks/missing", `{}`, profileOwner()), TypeNotFound)
	headers["If-Match"] = tag
	requireProblem(t, do(t, h, http.MethodPut, path, `{"name":""}`, headers), TypeValidationFailed)
	before := f.calls
	requireProblem(t, do(t, h, http.MethodPut, path, `{}`, nil), TypeAuthenticationRequired)
	if f.calls != before {
		t.Fatal("unauthorized dispatch")
	}
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPut, path, `{}`, headers), TypeDependencyUnavailable)
}

func (f *fakeNotificationDestinations) RotateNotificationServerChannelSecret(_ context.Context, id string) (string, error) {
	f.calls++
	f.deleted = id
	if id == "missing" {
		return "", notifications.ErrServerChannelNotFound
	}
	if id == "discord" {
		return "", notifications.ErrServerChannelInvalid
	}
	return "synthetic-secret", nil
}
func TestNotificationServerChannelRotate(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/admin/notifications/server-channels/row-one/rotate-secret"
	rec := do(t, h, http.MethodPost, path, "", bearer(adminToken))
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"signing_secret":"synthetic-secret"`) || f.calls != 1 || f.deleted != "row-one" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	requireProblem(t, do(t, h, http.MethodPost, path, "", nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPost, path, "", profileOwner()), TypePermissionDenied)
	if f.calls != 1 {
		t.Fatal("unauthorized dispatch")
	}
	requireProblem(t, do(t, h, http.MethodPost, strings.Replace(path, "row-one", "missing", 1), "", bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPost, strings.Replace(path, "row-one", "discord", 1), "", bearer(adminToken)), TypeValidationFailed)
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPost, path, "", bearer(adminToken)), TypeDependencyUnavailable)
}

func (f *fakeNotificationDestinations) UpdateNotificationServerChannel(_ context.Context, id string, input notifications.ServerChannelInput) (*notifications.ServerChannel, error) {
	f.calls++
	f.deleted = id
	if id == "missing" {
		return nil, notifications.ErrServerChannelNotFound
	}
	if input.Name != nil && *input.Name == "" {
		return nil, notifications.ErrServerChannelInvalid
	}
	return &notifications.ServerChannel{ID: id, Name: "updated", Type: "generic", CreatedAt: time.Unix(1, 0)}, nil
}
func TestNotificationServerChannelUpdate(t *testing.T) {
	f := new(fakeNotificationDestinations)
	deps := pilotDeps(nil, nil)
	deps.NotificationDestinations = f
	h := NewHandler(deps)
	path := Prefix + "/admin/notifications/server-channels/row-one"
	rec := do(t, h, http.MethodPut, path, `{"name":"updated","enabled":false}`, bearer(adminToken))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"name":"updated"`) || f.calls != 1 || f.deleted != "row-one" {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), f)
	}
	requireProblem(t, do(t, h, http.MethodPut, path, `{}`, nil), TypeAuthenticationRequired)
	requireProblem(t, do(t, h, http.MethodPut, path, `{}`, profileOwner()), TypePermissionDenied)
	if f.calls != 1 {
		t.Fatal("unauthorized dispatch")
	}
	requireProblem(t, do(t, h, http.MethodPut, Prefix+"/admin/notifications/server-channels/missing", `{}`, bearer(adminToken)), TypeNotFound)
	requireProblem(t, do(t, h, http.MethodPut, path, `{"name":""}`, bearer(adminToken)), TypeValidationFailed)
	deps.NotificationDestinations = nil
	requireProblem(t, do(t, NewHandler(deps), http.MethodPut, path, `{}`, bearer(adminToken)), TypeDependencyUnavailable)
}
