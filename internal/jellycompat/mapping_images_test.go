package jellycompat

import (
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
)

func TestItemImageTagsUseStableCanonicalSeed(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	updatedAt := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	item := upstreamListItem{
		ContentID:       "movie-1",
		Type:            "movie",
		Title:           "Movie",
		PosterURL:       "https://cdn.example.test/poster.jpg?sig=one",
		PosterPath:      "metadb://poster/movie-1",
		PosterThumbhash: "thumbhash",
		UpdatedAt:       updatedAt,
	}

	first := m.itemFromList(item, false, nil, nil)
	item.PosterURL = "https://cdn.example.test/poster.jpg?sig=two"
	second := m.itemFromList(item, false, nil, nil)

	if first.ImageTags["Primary"] == "" {
		t.Fatal("primary image tag is empty")
	}
	if first.ImageTags["Primary"] != second.ImageTags["Primary"] {
		t.Fatalf("image tag changed when only signed URL changed: %q vs %q", first.ImageTags["Primary"], second.ImageTags["Primary"])
	}

	item.UpdatedAt = updatedAt.Add(time.Second)
	third := m.itemFromList(item, false, nil, nil)
	if third.ImageTags["Primary"] == first.ImageTags["Primary"] {
		t.Fatalf("image tag did not change after UpdatedAt changed: %q", third.ImageTags["Primary"])
	}
}

func TestItemImageTagsFallbackToURLWhenCanonicalSeedMissing(t *testing.T) {
	m := newMapper(NewResourceIDCodec(), &config.Config{})
	item := upstreamListItem{
		ContentID: "movie-1",
		Type:      "movie",
		Title:     "Movie",
		PosterURL: "https://cdn.example.test/poster.jpg?sig=one",
	}

	first := m.itemFromList(item, false, nil, nil)
	item.PosterURL = "https://cdn.example.test/poster.jpg?sig=two"
	second := m.itemFromList(item, false, nil, nil)

	if first.ImageTags["Primary"] == second.ImageTags["Primary"] {
		t.Fatalf("fallback image tag did not change with URL: %q", first.ImageTags["Primary"])
	}
}

func TestItemImageTagsUseConfiguredSecret(t *testing.T) {
	item := upstreamListItem{
		ContentID:       "movie-1",
		Type:            "movie",
		Title:           "Movie",
		PosterURL:       "https://cdn.example.test/poster.jpg?sig=one",
		PosterPath:      "metadb://poster/movie-1",
		PosterThumbhash: "thumbhash",
		UpdatedAt:       time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
	}

	first := newMapper(NewResourceIDCodec(), &config.Config{
		Auth: config.AuthConfig{JWTSecret: "secret-one"},
	}).itemFromList(item, false, nil, nil)
	second := newMapper(NewResourceIDCodec(), &config.Config{
		Auth: config.AuthConfig{JWTSecret: "secret-two"},
	}).itemFromList(item, false, nil, nil)

	if first.ImageTags["Primary"] == second.ImageTags["Primary"] {
		t.Fatalf("signed image tag did not change with configured secret: %q", first.ImageTags["Primary"])
	}
}

func TestEpisodeListImageTagsUseStillThumbhash(t *testing.T) {
	secret := "image-secret"
	updatedAt := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	item := upstreamListItem{
		ContentID:       "episode-1",
		Type:            "episode",
		Title:           "Episode",
		PosterURL:       "https://cdn.example.test/still.jpg?sig=one",
		PosterPath:      "metadb://still/episode-1",
		PosterThumbhash: "poster-thumbhash",
		StillPath:       "metadb://still/episode-1",
		StillThumbhash:  "still-thumbhash",
		UpdatedAt:       updatedAt,
	}

	dto := newMapper(NewResourceIDCodec(), &config.Config{
		Auth: config.AuthConfig{JWTSecret: secret},
	}).itemFromList(item, false, nil, nil)
	expected := newImageTagSigner(secret).Tag(
		imageTagSeed(item.ContentID, "Primary", compatCardImageSize, item.StillPath, item.StillThumbhash, updatedAt),
		item.PosterURL,
	)

	if dto.ImageTags["Primary"] != expected {
		t.Fatalf("primary tag = %q, want still-thumbhash seed %q", dto.ImageTags["Primary"], expected)
	}
}

func TestLibraryImageTagsUseStablePosterPath(t *testing.T) {
	secret := "image-secret"
	codec := NewResourceIDCodec()
	library := upstreamUserLibrary{
		ID:         1,
		Name:       "Movies",
		Type:       "movies",
		PosterURL:  "https://cdn.example.test/library.jpg?sig=one",
		PosterPath: "library-posters/1/original.jpg",
	}

	first := newMapper(codec, &config.Config{
		Auth: config.AuthConfig{JWTSecret: secret},
	}).viewFromLibrary(library)
	library.PosterURL = "https://cdn.example.test/library.jpg?sig=two"
	second := newMapper(codec, &config.Config{
		Auth: config.AuthConfig{JWTSecret: secret},
	}).viewFromLibrary(library)

	routeID := codec.EncodeIntID(EncodedIDLibrary, int64(library.ID))
	expected := newImageTagSigner(secret).Tag(
		imageTagSeed(routeID, "Primary", compatCardImageSize, library.PosterPath, "", time.Time{}),
		library.PosterURL,
	)

	if first.ImageTags["Primary"] == "" {
		t.Fatal("library primary image tag is empty")
	}
	if first.ImageTags["Primary"] != second.ImageTags["Primary"] {
		t.Fatalf("library tag changed when only signed URL changed: %q vs %q", first.ImageTags["Primary"], second.ImageTags["Primary"])
	}
	if second.ImageTags["Primary"] != expected {
		t.Fatalf("library tag = %q, want %q", second.ImageTags["Primary"], expected)
	}
}

func TestApplySeriesImagesUsesCanonicalSeriesSeeds(t *testing.T) {
	secret := "image-secret"
	codec := NewResourceIDCodec()
	seriesContentID := "series-1"
	seriesRouteID := codec.EncodeStringID(EncodedIDItem, seriesContentID)
	updatedAt := time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC)
	dto := baseItemDTO{SeriesID: seriesRouteID}
	series := seriesImageSet{
		ContentID:         seriesContentID,
		PosterURL:         "https://cdn.example.test/poster.jpg?sig=one",
		PosterPath:        "metadb://poster/series-1",
		PosterThumbhash:   "poster-thumbhash",
		BackdropURL:       "https://cdn.example.test/backdrop.jpg?sig=one",
		BackdropPath:      "metadb://backdrop/series-1",
		BackdropThumbhash: "backdrop-thumbhash",
		UpdatedAt:         updatedAt,
	}

	newMapper(codec, &config.Config{
		Auth: config.AuthConfig{JWTSecret: secret},
	}).applySeriesImages(&dto, series)

	signer := newImageTagSigner(secret)
	expectedPrimary := signer.Tag(
		imageTagSeed(series.ContentID, "Primary", compatCardImageSize, series.PosterPath, series.PosterThumbhash, updatedAt),
		series.PosterURL,
	)
	expectedBackdrop := signer.Tag(
		imageTagSeed(series.ContentID, "Backdrop", compatCardImageSize, series.BackdropPath, series.BackdropThumbhash, updatedAt),
		series.BackdropURL,
	)

	if dto.SeriesPrimaryImageTag != expectedPrimary {
		t.Fatalf("SeriesPrimaryImageTag = %q, want %q", dto.SeriesPrimaryImageTag, expectedPrimary)
	}
	if len(dto.ParentBackdropImageTags) != 1 || dto.ParentBackdropImageTags[0] != expectedBackdrop {
		t.Fatalf("ParentBackdropImageTags = %#v, want [%q]", dto.ParentBackdropImageTags, expectedBackdrop)
	}
	if dto.ParentThumbImageTag != expectedBackdrop {
		t.Fatalf("ParentThumbImageTag = %q, want %q", dto.ParentThumbImageTag, expectedBackdrop)
	}
}
