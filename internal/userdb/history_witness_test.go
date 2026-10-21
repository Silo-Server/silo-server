package userdb

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestSQLiteHistoryWitness(t *testing.T) { storetest.RunHistoryWitness(t, newConformanceStore) }
