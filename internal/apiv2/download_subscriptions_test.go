package apiv2

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/downloads"
)

type fakeDownloadSubscriptions struct {
	rows                []*downloads.Subscription
	row                 *downloads.Subscription
	user, limit         int
	profile, device, id string
	after               *downloads.RegistryPosition
	err                 error
}

func (f *fakeDownloadSubscriptions) ListSubscriptionsPage(_ context.Context, user int, profile, device string, after *downloads.RegistryPosition, limit int) ([]*downloads.Subscription, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.after = after
	f.limit = limit
	return f.rows, f.err
}
func (f *fakeDownloadSubscriptions) GetSubscription(_ context.Context, user int, profile, device, id string) (*downloads.Subscription, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	return f.row, f.err
}
func syntheticDownloadSubscription() *downloads.Subscription {
	return &downloads.Subscription{ID: "monitor", UserID: 1, ProfileID: "p-owner", DeviceID: "device-one", SeriesID: "series", Mode: downloads.SubModeSpecificSeasons, SeasonNumbers: []int{0, 2}, DeleteWatched: true, MaxStorageBytes: 1024, Active: false, CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC), UpdatedAt: time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)}
}
func TestDownloadSubscriptionReads(t *testing.T) {
	row := syntheticDownloadSubscription()
	other := *row
	other.ID = "older"
	svc := &fakeDownloadSubscriptions{row: row, rows: []*downloads.Subscription{row, &other}}
	deps := pilotDeps(nil, nil)
	deps.DownloadSubscriptions = svc
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	device := with(viewer, "X-Silo-Device-Id", "device-one")
	path := Prefix + "/downloads/subscriptions"
	rec := do(t, h, "GET", path+"/monitor", "", device)
	if rec.Code != 200 || rec.Header().Get("ETag") == "" {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var got DownloadSubscription
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ETag != rec.Header().Get("ETag") || got.Active || got.SeasonNumbers[0] != 0 || svc.id != "monitor" {
		t.Fatalf("%+v %+v", got, svc)
	}
	rec = do(t, h, "GET", path+"?limit=1", "", device)
	var page Collection[DownloadSubscription]
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || len(page.Items) != 1 || !page.Page.HasMore || page.Page.NextCursor == "" || svc.limit != 2 || svc.user != 1 || svc.profile != "p-owner" || svc.device != "device-one" {
		t.Fatalf("%d %+v %+v", rec.Code, page, svc)
	}
	svc.rows = []*downloads.Subscription{&other}
	rec = do(t, h, "GET", path+"?limit=1&cursor="+page.Page.NextCursor, "", device)
	if rec.Code != 200 || svc.after == nil || svc.after.ID != row.ID || !svc.after.CreatedAt.Equal(row.CreatedAt) {
		t.Fatalf("%d %+v", rec.Code, svc)
	}
	rec = do(t, h, "GET", path+"?cursor="+page.Page.NextCursor, "", with(viewer, "X-Silo-Device-Id", "other"))
	if rec.Code != 400 {
		t.Fatalf("foreign cursor: %d", rec.Code)
	}
	for _, suffix := range []string{"", "/monitor"} {
		rec = do(t, h, "GET", path+suffix, "", viewer)
		if rec.Code != 422 {
			t.Fatalf("missing device: %d", rec.Code)
		}
	}
	rec = do(t, h, "GET", path+"?limit=101", "", device)
	if rec.Code != 422 {
		t.Fatalf("unbounded: %d", rec.Code)
	}
	svc.err = downloads.ErrSubscriptionNotFound
	rec = do(t, h, "GET", path+"/missing", "", device)
	if rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
	svc.err = downloads.ErrSubscriptionsUnavailable
	rec = do(t, h, "GET", path, "", device)
	if rec.Code != 503 {
		t.Fatalf("unavailable: %d", rec.Code)
	}
}
func TestDownloadSubscriptionValidator(t *testing.T) {
	row := syntheticDownloadSubscription()
	tag := downloadSubscriptionOf(row).ETag
	// Two writes may share the millisecond returned to the client.
	row.UpdatedAt = row.UpdatedAt.Add(time.Microsecond)
	if downloadSubscriptionOf(row).ETag == tag {
		t.Fatal("lost database timestamp precision")
	}
	row = syntheticDownloadSubscription()
	row.ProfileID = "other"
	if downloadSubscriptionOf(row).ETag == tag {
		t.Fatal("validator not identity scoped")
	}
	row = syntheticDownloadSubscription()
	row.Active = true
	if downloadSubscriptionOf(row).ETag == tag {
		t.Fatal("validator omitted mutable options")
	}
}
