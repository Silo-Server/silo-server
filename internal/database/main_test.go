package database

import (
	"fmt"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/database/queryattr"
)

func TestMain(m *testing.M) {
	if err := os.Setenv("SILO_QUERY_ATTRIBUTION_ENABLED", "true"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func TestPostgresAttributionInitializedBeforePool(t *testing.T) {
	if !queryattr.Initialize() {
		t.Fatal("attribution disabled before the first pool, want enabled fixture")
	}
}
