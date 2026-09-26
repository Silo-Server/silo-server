package apiv2

import (
	"context"
	"strings"
	"testing"
	"time"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/downloads"
)

type fakeSubscriptionMutations struct {
	row                 *downloads.Subscription
	request             downloads.SubscriptionRequest
	patch               downloads.SubscriptionPatch
	user                int
	profile, device, id string
	err                 error
	calls               int
}

func (f *fakeSubscriptionMutations) CreateSubscriptionMonitor(_ context.Context, user int, req downloads.SubscriptionRequest, _ catalogpkg.AccessFilter) (*downloads.Subscription, error) {
	f.calls++
	f.user = user
	f.request = req
	return f.row, f.err
}
func (f *fakeSubscriptionMutations) UpdateSubscriptionMonitor(_ context.Context, user int, profile, device, id string, patch downloads.SubscriptionPatch, _ catalogpkg.AccessFilter, check func(*downloads.Subscription) error) (*downloads.Subscription, error) {
	f.calls++
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	if f.err != nil {
		return nil, f.err
	}
	if err := check(f.row); err != nil {
		return nil, err
	}
	f.patch = patch
	if patch.Active != nil {
		f.row.Active = *patch.Active
	}
	f.row.UpdatedAt = f.row.UpdatedAt.Add(time.Microsecond)
	return f.row, nil
}
func (f *fakeSubscriptionMutations) DeleteSubscriptionMonitor(_ context.Context, user int, profile, device, id string, check func(*downloads.Subscription) error) error {
	f.calls++
	f.user = user
	f.profile = profile
	f.device = device
	f.id = id
	if f.err != nil {
		return f.err
	}
	return check(f.row)
}
func TestSubscriptionMutationTransport(t *testing.T) {
	svc := &fakeSubscriptionMutations{row: syntheticDownloadSubscription()}
	deps := pilotDeps(nil, nil)
	deps.DownloadSubscriptionMutations = svc
	h := newTestHandler(t, deps)
	viewer := with(bearer(memberToken), "X-Profile-Id", "p-owner")
	device := with(viewer, "X-Silo-Device-Id", "device-one")
	path := Prefix + "/downloads/subscriptions"
	rec := do(t, h, "POST", path, `{"series_id":"series","mode":"specific_seasons","season_numbers":[0],"delete_watched":false,"max_storage_bytes":0}`, device)
	if rec.Code != 200 || rec.Header().Get("ETag") == "" || svc.user != 1 || svc.request.ProfileID != "p-owner" || svc.request.DeviceID != "device-one" || svc.request.SeasonNumbers[0] != 0 {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), svc)
	}
	tag := rec.Header().Get("ETag")
	for _, headers := range []map[string]string{device, with(device, "If-Match", `"stale"`)} {
		rec = do(t, h, "PATCH", path+"/monitor", `{"active":true}`, headers)
		if rec.Code != 428 && rec.Code != 412 {
			t.Fatalf("unguarded %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = do(t, h, "PATCH", path+"/monitor", `{"active":true,"delete_watched":false,"max_storage_bytes":0}`, with(device, "If-Match", tag))
	if rec.Code != 200 || rec.Header().Get("ETag") == tag || svc.patch.Active == nil || !*svc.patch.Active || svc.patch.DeleteWatched == nil || *svc.patch.DeleteWatched || svc.patch.MaxStorageBytes == nil || *svc.patch.MaxStorageBytes != 0 {
		t.Fatalf("%d %s %+v", rec.Code, rec.Body.String(), svc.patch)
	}
	current := rec.Header().Get("ETag")
	for _, body := range []string{`{"active":null}`, `{"mode":null}`, `{"season_numbers":null}`, `{"max_storage_bytes":-1}`} {
		rec = do(t, h, "PATCH", path+"/monitor", body, with(device, "If-Match", current))
		if rec.Code != 400 {
			t.Fatalf("invalid %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = do(t, h, "DELETE", path+"/monitor", "", with(device, "If-Match", tag))
	if rec.Code != 412 {
		t.Fatalf("stale delete %d", rec.Code)
	}
	rec = do(t, h, "DELETE", path+"/monitor", "", with(device, "If-Match", current))
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("delete %d %s", rec.Code, rec.Body.String())
	}
	for _, method := range []string{"POST", "PATCH", "DELETE"} {
		suffix := ""
		body := `{}`
		if method != "POST" {
			suffix = "/monitor"
		}
		if method == "DELETE" {
			body = ""
		}
		rec = do(t, h, method, path+suffix, body, viewer)
		if rec.Code != 422 {
			t.Fatalf("missing device %s %d", method, rec.Code)
		}
	}
	svc.err = catalogpkg.ErrItemNotFound
	rec = do(t, h, "PATCH", path+"/monitor", `{"active":true}`, with(device, "If-Match", current))
	if rec.Code != 404 {
		t.Fatalf("hidden %d", rec.Code)
	}
	rec = do(t, h, "POST", path, `{"series_id":"series","mode":"all","delete_watched":false,"max_storage_bytes":0,"extra":"`+strings.Repeat("x", 129<<10)+`"}`, device)
	if rec.Code != 413 {
		t.Fatalf("oversize %d", rec.Code)
	}
}
