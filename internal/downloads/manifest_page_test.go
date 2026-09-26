package downloads

import (
	"errors"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

func TestDownloadManifestPagesPostgres(t *testing.T) {
	repo := statusEventTestRepo(t)
	at := time.Now().UTC().Truncate(time.Millisecond)
	for _, id := range []string{"a", "b", "c", "d", "e", "foreign"} {
		row := &Download{ID: id, UserID: 1, ProfileID: "one", DeviceID: "device", ContentID: "series", EpisodeID: "ep-" + id, MediaFileID: 42, BatchID: "batch", Kind: KindQueued, Status: StatusReady, CreatedAt: at, UpdatedAt: at}
		if id == "e" {
			row.Status = StatusRevoked
		}
		if id == "foreign" {
			row.ProfileID = "two"
		}
		if err := repo.Create(t.Context(), row); err != nil {
			t.Fatal(err)
		}
	}
	source := &mapManifestSource{details: map[string]*catalog.ItemDetail{
		"ep-a":   {Type: "episode", Title: "A", SeriesID: "series"},
		"ep-b":   {Type: "episode", Title: "B", SeriesID: "series"},
		"ep-c":   {Type: "episode", Title: "C", SeriesID: "series"},
		"series": {Type: "series", Title: "Series"},
	}, calls: map[string]int{}}
	service := NewService(repo, nil, nil, nil, nil, nil, nil, nil, nil, &config.DownloadConfig{Enabled: true})
	service.SetOfflineDeps(source, nil, nil)
	first, err := service.PageBatchManifests(t.Context(), 1, "one", "device", "batch", nil, 1, catalog.AccessFilter{})
	if err != nil || len(first.Items) != 0 || len(first.Skipped) != 1 || first.Skipped[0].Reason != "revoked" || first.Next == nil || first.Next.ID != "e" {
		t.Fatalf("%+v %v", first, err)
	}
	if len(source.calls) != 0 {
		t.Fatalf("built beyond bounded first page: %+v", source.calls)
	}
	second, err := service.PageBatchManifests(t.Context(), 1, "one", "device", "batch", first.Next, 2, catalog.AccessFilter{})
	if err != nil || len(second.Items) != 1 || second.Items[0].DownloadID != "c" || len(second.Skipped) != 1 || second.Skipped[0].DownloadID != "d" || second.Next == nil || second.Next.ID != "c" {
		t.Fatalf("%+v %v", second, err)
	}
	source.calls = map[string]int{}
	last, err := service.PageBatchManifests(t.Context(), 1, "one", "device", "batch", second.Next, 2, catalog.AccessFilter{})
	if err != nil || len(last.Items) != 2 || len(last.Skipped) != 0 || last.Next != nil {
		t.Fatalf("%+v %v", last, err)
	}
	if source.calls["series"] != 1 || source.calls["ep-foreign"] != 0 {
		t.Fatalf("scope/cache: %+v", source.calls)
	}
	if _, err := service.PageBatchManifests(t.Context(), 1, "one", "device", "batch", nil, 11, catalog.AccessFilter{}); err == nil {
		t.Fatal("unbounded page accepted")
	}
	if _, err := service.PageBatchManifests(t.Context(), 1, "one", "other", "batch", nil, 1, catalog.AccessFilter{}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}
