package jellycompat

import (
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestListMappingDoesNotInventDetailIDs(t *testing.T) {
	mapper := newMapper(NewResourceIDCodec(), &config.Config{})
	dto := mapper.itemFromList(upstreamListItem{ContentID: "movie", Title: "Movie", Type: "movie"}, false, nil, map[string]bool{"people": true, "mediasources": true, "mediastreams": true, "chapters": true})
	if len(dto.People) != 0 || len(dto.MediaSources) != 0 || len(dto.MediaStreams) != 0 || len(dto.Chapters) != 0 {
		t.Fatalf("list mapping invented detail: %+v", dto)
	}
}
