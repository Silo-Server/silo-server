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
