package apiv2

import (
	"context"

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
