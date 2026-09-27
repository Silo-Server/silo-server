package apiv2

import (
	"context"
	"encoding/json"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type fakeSubscriptionSync struct {
	row                 *downloads.Subscription
	page                downloads.SubscriptionSyncPage
	after               *catalogpkg.EpisodePagePosition
	user, limit         int
	profile, device, id string
}

func (f *fakeSubscriptionSync) SyncSubscriptionPage(_ context.Context, user int, profile, device, id string, after *catalogpkg.EpisodePagePosition, limit int, _ catalogpkg.AccessFilter, check func(*downloads.Subscription) error) (downloads.SubscriptionSyncPage, error) {
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	f.limit = limit
	f.after = after
	if err := check(f.row); err != nil {
		return downloads.SubscriptionSyncPage{}, err
	}
	return f.page, nil
}
func TestSubscriptionSyncTransport(t *testing.T) {
	row := syntheticDownloadSubscription()
	svc := &fakeSubscriptionSync{row: row, page: downloads.SubscriptionSyncPage{Examined: 1, Next: &catalogpkg.EpisodePagePosition{SeasonNumber: 0, EpisodeNumber: 1, ContentID: "special"}}}
	deps := pilotDeps(nil, nil)
	deps.DownloadSubscriptionSync = svc
	h := newTestHandler(t, deps)
	device := with(with(bearer(memberToken), "X-Profile-Id", "p-owner"), "X-Silo-Device-Id", "device-one")
	body, _ := json.Marshal(map[string]string{"subscription_id": "monitor", "etag": downloadSubscriptionOf(row).ETag})
	path := Prefix + "/downloads/subscriptions/sync"
	rec := do(t, h, "POST", path+"?limit=1", string(body), device)
	var got DownloadSubscriptionSync
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || got.Registered != 0 || !got.Page.HasMore || got.Page.NextCursor == "" || svc.user != 1 || svc.profile != "p-owner" || svc.device != "device-one" || svc.id != "monitor" || svc.limit != 1 {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), svc)
	}
	rec = do(t, h, "POST", path+"?cursor="+got.Page.NextCursor, string(body), device)
	if rec.Code != 200 || svc.after == nil || svc.after.ContentID != "special" {
		t.Fatalf("cursor %d %+v", rec.Code, svc)
	}
	altered, _ := json.Marshal(map[string]string{"subscription_id": "other", "etag": downloadSubscriptionOf(row).ETag})
	rec = do(t, h, "POST", path+"?cursor="+got.Page.NextCursor, string(altered), device)
	if rec.Code != 400 {
		t.Fatalf("foreign monitor cursor %d", rec.Code)
	}
	row.Active = !row.Active
	rec = do(t, h, "POST", path, string(body), device)
	if rec.Code != 409 {
		t.Fatalf("stale monitor %d %s", rec.Code, rec.Body.String())
	}
}
