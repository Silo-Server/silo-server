package artworkvariant

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

type selectorStore struct {
	objects   map[string][]byte
	statCalls map[string]int
	statErr   error
}

func (s *selectorStore) Open(_ context.Context, key string) (*artworkstore.Object, error) {
	data, ok := s.objects[key]
	if !ok {
		return nil, artworkstore.ErrNotFound
	}
	return &artworkstore.Object{
		Info: artworkstore.ObjectInfo{Key: key, SizeBytes: int64(len(data))},
		Body: io.NopCloser(bytes.NewReader(data)),
	}, nil
}

func (s *selectorStore) Stat(_ context.Context, key string) (artworkstore.ObjectInfo, error) {
	s.statCalls[key]++
	if s.statErr != nil {
		return artworkstore.ObjectInfo{}, s.statErr
	}
	data, ok := s.objects[key]
	if !ok {
		return artworkstore.ObjectInfo{}, artworkstore.ErrNotFound
	}
	return artworkstore.ObjectInfo{Key: key, SizeBytes: int64(len(data))}, nil
}

func portableFixture(t *testing.T, names ...string) (*selectorStore, string) {
	t.Helper()
	variants := make([]artworkkey.VariantBytes, 0, len(names))
	for _, name := range names {
		variants = append(variants, artworkkey.VariantBytes{Name: name, Data: []byte("bytes-" + name)})
	}
	built, err := artworkkey.BuildPortableRevision(artworkkey.RevisionInput{
		ImageType: artworkkey.ImageTypePoster,
		MediaType: "image/webp",
		Ext:       ".webp",
		Variants:  variants,
	})
	if err != nil {
		t.Fatalf("BuildPortableRevision: %v", err)
	}
	objects := map[string][]byte{built.ManifestKey: built.ManifestJSON}
	for _, variant := range variants {
		objects[built.VariantKeys[variant.Name]] = variant.Data
	}
	return &selectorStore{objects: objects, statCalls: map[string]int{}}, built.OriginalKey
}

func TestPortablePreRungUsesManifestWithoutExistenceProbes(t *testing.T) {
	store, original := portableFixture(t, artworkkey.OriginalVariant, artworkkey.VariantW300, artworkkey.VariantW500)
	selector := New(store)

	got, err := selector.Select(context.Background(), original, artworkkey.ImageTypePoster, artworkkey.VariantW780)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if want := artworkkey.Variant(original, artworkkey.VariantW500); got != want {
		t.Fatalf("selected = %q, want %q", got, want)
	}
	if len(store.statCalls) != 0 {
		t.Fatalf("portable selection performed existence probes: %v", store.statCalls)
	}
}

func TestLegacyPreRungWalksDownAndCachesExistence(t *testing.T) {
	original := "tmdb/movies/550/poster/original.rev.webp"
	w500 := artworkkey.Variant(original, artworkkey.VariantW500)
	store := &selectorStore{objects: map[string][]byte{w500: []byte("poster")}, statCalls: map[string]int{}}
	selector := New(store)
	selector.now = func() time.Time { return time.Unix(100, 0) }

	for range 2 {
		got, err := selector.Select(context.Background(), original, artworkkey.ImageTypePoster, artworkkey.VariantW780)
		if err != nil {
			t.Fatalf("Select: %v", err)
		}
		if got != w500 {
			t.Fatalf("selected = %q, want %q", got, w500)
		}
	}
	if got := store.statCalls[artworkkey.Variant(original, artworkkey.VariantW780)]; got != 1 {
		t.Fatalf("w780 probes = %d, want 1", got)
	}
	if got := store.statCalls[w500]; got != 1 {
		t.Fatalf("w500 probes = %d, want 1", got)
	}
}

func TestLegacyProbeFailureDoesNotDowngrade(t *testing.T) {
	wantErr := io.ErrUnexpectedEOF
	store := &selectorStore{objects: map[string][]byte{}, statCalls: map[string]int{}, statErr: wantErr}
	selector := New(store)
	if _, err := selector.Select(context.Background(), "tmdb/movies/550/poster/original.rev.webp", artworkkey.ImageTypePoster, artworkkey.VariantW780); !errors.Is(err, wantErr) {
		t.Fatalf("Select error = %v, want %v", err, wantErr)
	}
}
