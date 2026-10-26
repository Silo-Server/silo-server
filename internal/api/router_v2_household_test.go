package api

import (
	"context"
	"errors"
	"testing"

	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

type unusedUserStores struct{}

func (unusedUserStores) ForUser(context.Context, int) (userstore.UserStore, error) {
	return nil, errors.New("not opened by this test")
}

func (unusedUserStores) Close() error { return nil }

// TestV2DependenciesWiresHouseholdProfileGate: the v2 listener gets the
// household profile gate wherever viewer access is wired, so the operations
// declaring it are served; without a user store neither is wired and those
// operations fail closed.
func TestV2DependenciesWiresHouseholdProfileGate(t *testing.T) {
	viewer := apimw.NewViewerAccessMiddleware(nil)
	if got := v2Dependencies(Dependencies{UserStoreProvider: unusedUserStores{}}, nil, viewer, nil, nil, nil, nil); got.HouseholdProfile == nil {
		t.Fatal("household profile gate not wired next to viewer access")
	}
	if got := v2Dependencies(Dependencies{}, nil, nil, nil, nil, nil, nil); got.HouseholdProfile != nil {
		t.Fatal("household profile gate wired without a user store")
	}
}
