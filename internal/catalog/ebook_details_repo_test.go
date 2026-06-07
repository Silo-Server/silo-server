package catalog

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestEbookDetailsRepositoryUpsertRequiresContentID(t *testing.T) {
	repo := &EbookDetailsRepository{}
	err := repo.Upsert(context.Background(), models.EbookDetails{})
	if err == nil {
		t.Fatal("expected error for empty content id")
	}
}
