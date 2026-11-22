package apiv2

import (
	"context"
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
