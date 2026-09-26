package userdb

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestSQLiteCollectionAddIfAbsentOffPage(t *testing.T) {
	storetest.RunCollectionAddIfAbsent(t, newConformanceStore(t))
}
