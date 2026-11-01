package main

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/backup/backuptest"
)

// TestMain drops the template database the backup command tests copy.
func TestMain(m *testing.M) { backuptest.Main(m) }
