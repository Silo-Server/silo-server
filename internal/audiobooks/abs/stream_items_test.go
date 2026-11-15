package abs

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

type streamStubMediaStore struct {
	noopMediaStore
	batches [][]*models.MediaItem
}

func (s streamStubMediaStore) StreamAudiobooks(_ context.Context, _ int64, _ catalog.AccessFilter, _ ItemsQuery, onTotal func(int) error, emit func([]*models.MediaItem) error) error {
	n := 0
	for _, b := range s.batches {
		n += len(b)
	}
	if n == 0 {
		return nil
	}
	if err := onTotal(n); err != nil {
		return err
	}
	for _, b := range s.batches {
		if err := emit(b); err != nil {
			return err
		}
	}
	return nil
}

// A limit=0 response is written batch by batch by hand; it must still be one
// valid ABS envelope with every item in order.
func TestStreamLibraryItemsWritesOneEnvelope(t *testing.T) {
	book := func(id string) *models.MediaItem { return &models.MediaItem{ContentID: id, Title: id} }
	for name, batches := range map[string][][]*models.MediaItem{
		"two batches": {{book("a"), book("b")}, {book("c")}},
		"empty":       nil,
	} {
		h := New(Dependencies{MediaStore: streamStubMediaStore{batches: batches}})
		rec := httptest.NewRecorder()
		envelope := pagedEnvelope(nil, 0, 0, 0, "", false, "", true, "")
		h.streamLibraryItems(rec, httptest.NewRequest("GET", "/api/libraries/1/items?limit=0", nil), AudiobookLibrary{ID: 1}, catalog.AccessFilter{}, ItemsQuery{}, true, envelope)

		var got struct {
			Results  []struct{ ID string } `json:"results"`
			Total    int                   `json:"total"`
			Minified bool                  `json:"minified"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: body is not JSON: %v\n%s", name, err, rec.Body.String())
		}
		var ids []string
		for _, r := range got.Results {
			ids = append(ids, r.ID)
		}
		want := []string{}
		for _, b := range batches {
			for _, it := range b {
				want = append(want, it.ContentID)
			}
		}
		if got.Total != len(want) || len(ids) != len(want) || !got.Minified || got.Results == nil {
			t.Errorf("%s: total %d, ids %v, minified %v; want %d items %v", name, got.Total, ids, got.Minified, len(want), want)
		}
		for i := range want {
			if ids[i] != want[i] {
				t.Errorf("%s: ids %v, want %v", name, ids, want)
				break
			}
		}
	}
}
