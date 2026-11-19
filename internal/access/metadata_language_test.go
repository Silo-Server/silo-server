package access

import (
	"context"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// settingStoreStub only answers the one method resolution reaches; the
// embedded nil interface panics on anything else, which is the point — this
// path must not touch the rest of the store.
type settingStoreStub struct {
	userstore.UserStore
	rows []userstore.SettingValue
	err  error
}

func (s settingStoreStub) ListSettingValuesForResolution(
	context.Context, userstore.SettingResolutionQuery,
) ([]userstore.SettingValue, error) {
	return s.rows, s.err
}
