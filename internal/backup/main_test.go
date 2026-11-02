package backup

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
)

// TestMain drops the template database the dump and restore tests copy.
func TestMain(m *testing.M) { backuptest.Main(m) }
