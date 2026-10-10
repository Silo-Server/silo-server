package userdb

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestSQLiteManualCollectionsHolding(t *testing.T) {
	storetest.RunManualCollectionsHolding(t, newConformanceStore(t))
}
