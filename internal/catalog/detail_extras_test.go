package catalog

import (
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/models"
)

func TestFetchItemExtrasSignsStillsInOneBatch(t *testing.T) {
	resolver := &batchImageResolver{}
	s := &DetailService{imageResolver: resolver}
	pf := &itemDetailPrefetch{haveExtras: true, extras: []ExtraWithFile{
		{MediaExtra: models.MediaExtra{ContentID: "x1", StillPath: "local/extras/x1/still/original.a.webp", StillThumbhash: "h1"}},
		{MediaExtra: models.MediaExtra{ContentID: "x2", StillThumbhash: "stale"}},
		{MediaExtra: models.MediaExtra{ContentID: "x3", StillPath: "local/extras/x3/still/original.b.webp", StillThumbhash: "h3"}},
	}}

	infos := s.fetchItemExtras(t.Context(), "movie", AccessFilter{}, pf)

	if len(resolver.batches) != 1 || len(resolver.batches[0]) != 2 {
		t.Fatalf("batches = %v, want one batch of the two stills", resolver.batches)
	}
	for i, want := range []struct{ dir, thumbhash string }{{"local/extras/x1/still/", "h1"}, {}, {"local/extras/x3/still/", "h3"}} {
		got := infos[i]
		if want.dir == "" {
			if got.StillURL != "" || got.StillThumbhash != "" {
				t.Errorf("%s: still = %q, %q; want none", got.ContentID, got.StillURL, got.StillThumbhash)
			}
			continue
		}
		if !strings.HasPrefix(got.StillURL, "batch://"+want.dir) || got.StillThumbhash != want.thumbhash {
			t.Errorf("%s: still = %q, %q; want a batch URL under %s and %q", got.ContentID, got.StillURL, got.StillThumbhash, want.dir, want.thumbhash)
		}
	}
}
