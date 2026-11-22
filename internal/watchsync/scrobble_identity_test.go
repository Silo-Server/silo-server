package watchsync

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/userstore"
)

type fixedScrobbleIdentityResolver struct {
	identity userstore.WatchIdentity
}

func (r fixedScrobbleIdentityResolver) ResolveHistoryIdentity(context.Context, string) userstore.WatchIdentity {
	return r.identity
}
