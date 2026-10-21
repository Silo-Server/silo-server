package apiv2

import (
	"context"
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
