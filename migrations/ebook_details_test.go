package migrations

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEbookDetailsMigrationFilesExist(t *testing.T) {
	for _, suffix := range []string{"up.sql", "down.sql"} {
		path := filepath.Join("..", "migrations", "181_ebook_details."+suffix)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected migration file %s: %v", path, err)
		}
	}
}
