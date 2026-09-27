package jellycompat

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

// Jellyfin 12 sets an episode's ParentPrimaryImage to its season poster, or to
// the series poster when the season has none.
func TestEpisodeParentPrimaryImagePrefersSeasonPoster(t *testing.T) {
	secret := "image-secret"
	codec := NewResourceIDCodec()
	m := newMapper(codec, &config.Config{Auth: config.AuthConfig{JWTSecret: secret}})
	signer := newImageTagSigner(secret)
	updatedAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	series := seriesImageSet{ContentID: "series-1", PosterURL: "https://cdn.example.test/series.jpg", PosterPath: "metadb://poster/series-1", UpdatedAt: updatedAt}
	season := seriesImageSet{ContentID: "season-1", PosterURL: "https://cdn.example.test/season.jpg", PosterPath: "metadb://poster/season-1", PosterThumbhash: "th", UpdatedAt: updatedAt}
	seriesRouteID := codec.EncodeStringID(EncodedIDItem, "series-1")

	dto := baseItemDTO{SeriesID: seriesRouteID}
	m.applySeriesImages(&dto, series)
	if dto.ParentPrimaryImageItemID != seriesRouteID || dto.ParentPrimaryImageTag == "" || dto.ParentPrimaryImageTag != dto.SeriesPrimaryImageTag {
		t.Fatalf("without a season poster the parent poster is the series poster: %+v", dto)
	}

	m.applySeasonPrimaryImage(&dto, season)
	wantTag := signer.Tag(imageTagSeed("season-1", "Primary", compatCardImageSize, season.PosterPath, season.PosterThumbhash, updatedAt), season.PosterURL)
	if dto.ParentPrimaryImageItemID != codec.EncodeStringID(EncodedIDSeason, "season-1") || dto.ParentPrimaryImageTag != wantTag {
		t.Fatalf("season poster not applied: id=%q tag=%q want tag %q", dto.ParentPrimaryImageItemID, dto.ParentPrimaryImageTag, wantTag)
	}

	unchanged := dto
	m.applySeasonPrimaryImage(&dto, seriesImageSet{ContentID: "season-2"})
	if dto.ParentPrimaryImageItemID != unchanged.ParentPrimaryImageItemID {
		t.Fatal("a season without a poster must not replace the parent poster")
	}

	items := []baseItemDTO{dto}
	applyItemsResponseOptions(items, itemsQuery{enableImageTypes: map[string]bool{"thumb": true}})
	if items[0].ParentPrimaryImageItemID != "" || items[0].ParentPrimaryImageTag != "" {
		t.Fatalf("EnableImageTypes without Primary must strip the parent poster: %+v", items[0])
	}
}

func TestOriginalLanguageMapsFromListAndDetail(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	list := m.itemFromList(upstreamListItem{ContentID: "m-1", Type: "movie", Title: "Ran", OriginalLanguage: "ja"}, false, nil, nil)
	if list.OriginalLanguage != "ja" {
		t.Fatalf("list OriginalLanguage = %q", list.OriginalLanguage)
	}
	detail := m.itemFromDetailWithFields(upstreamItemDetail{ContentID: "s-1", Type: "series", Title: "Dark", OriginalLanguage: "de"}, false, nil, nil)
	if detail.OriginalLanguage != "de" {
		t.Fatalf("detail OriginalLanguage = %q", detail.OriginalLanguage)
	}
}
