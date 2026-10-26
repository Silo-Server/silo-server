package catalog

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestEpisodeRepository_BulkUpsertRejectsMixedSeries(t *testing.T) {
	repo := &EpisodeRepository{}
	err := repo.BulkUpsert(context.Background(), "series-a", []*models.Episode{{SeriesID: "series-b"}})
	if err == nil || !strings.Contains(err.Error(), "belongs to series") {
		t.Fatalf("BulkUpsert error = %v, want mixed-series validation error", err)
	}
}

func TestBulkUpsertsRejectPostgresIntegerOverflow(t *testing.T) {
	overflow64 := int64(1) << 31
	overflow := int(overflow64)
	if int64(overflow) != overflow64 {
		t.Skip("int cannot represent a value outside PostgreSQL integer range")
	}

	seasonRepo := &SeasonRepository{}
	if err := seasonRepo.BulkUpsert(context.Background(), []*models.Season{{SeasonNumber: overflow}}); err == nil ||
		!strings.Contains(err.Error(), "outside PostgreSQL integer range") {
		t.Fatalf("season BulkUpsert error = %v, want PostgreSQL integer range validation error", err)
	}

	for _, testCase := range []struct {
		name    string
		episode models.Episode
	}{
		{name: "season number", episode: models.Episode{SeriesID: "series-a", SeasonNumber: overflow}},
		{name: "episode number", episode: models.Episode{SeriesID: "series-a", EpisodeNumber: overflow}},
		{name: "runtime", episode: models.Episode{SeriesID: "series-a", Runtime: overflow}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			repo := &EpisodeRepository{}
			err := repo.BulkUpsert(context.Background(), "series-a", []*models.Episode{&testCase.episode})
			if err == nil || !strings.Contains(err.Error(), "outside PostgreSQL integer range") {
				t.Fatalf("Episode BulkUpsert error = %v, want PostgreSQL integer range validation error", err)
			}
		})
	}
}
