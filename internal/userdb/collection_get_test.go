package userdb

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestSQLiteGetMissingCollection(t *testing.T) {
	storetest.RunGetMissingCollection(t, newConformanceStore(t))
}
