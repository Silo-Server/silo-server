package userdb

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/storetest"
)

func TestSQLiteSettingsWriterSerialization(t *testing.T) {
	storetest.RunSettingsWriterSerialization(t, newConformanceStore)
}
