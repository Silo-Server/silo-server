package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
)

// A local extra with an image advertises a Primary tag; one without keeps an
// empty tag map.
func TestExtraToBaseItemImageTags(t *testing.T) {
	codec := NewResourceIDCodec()
	h := &ItemsHandler{codec: codec, mapper: newMapper(codec, &config.Config{})}

	plain := h.extraToBaseItem(catalog.ItemExtraInfo{ContentID: "local-a", Kind: "featurette"}, "parent")
	if plain.ImageTags == nil || len(plain.ImageTags) != 0 {
		t.Fatalf("extra without image: ImageTags = %v, want empty map", plain.ImageTags)
	}

	withImage := h.extraToBaseItem(catalog.ItemExtraInfo{
		ContentID: "local-b", Kind: "featurette",
		StillPath: "local/extras/local-b/still/original.r.webp", StillURL: "https://img/still", StillThumbhash: "hash",
	}, "parent")
	if withImage.ImageTags["Primary"] == "" {
		t.Fatalf("extra with image: ImageTags = %v, want a Primary tag", withImage.ImageTags)
	}
}
