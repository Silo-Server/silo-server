package artworkadopt

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/artworkkey"
	"github.com/Silo-Server/silo-server/internal/artworkstore"
)

type memoryStore map[string][]byte

func (s memoryStore) WriteImmutable(_ context.Context, key string, data []byte, _ artworkstore.ObjectMetadata) error {
	if current, ok := s[key]; ok && !bytes.Equal(current, data) {
		return artworkstore.ErrContentMismatch
	}
	s[key] = bytes.Clone(data)
	return nil
}

func (s memoryStore) Open(_ context.Context, key string) (*artworkstore.Object, error) {
	data, ok := s[key]
	if !ok {
		return nil, artworkstore.ErrNotFound
	}
	return &artworkstore.Object{Info: artworkstore.ObjectInfo{Key: key, SizeBytes: int64(len(data))}, Body: io.NopCloser(bytes.NewReader(data))}, nil
}

func (s memoryStore) Stat(_ context.Context, key string) (artworkstore.ObjectInfo, error) {
	data, ok := s[key]
	if !ok {
		return artworkstore.ObjectInfo{}, artworkstore.ErrNotFound
	}
	return artworkstore.ObjectInfo{Key: key, SizeBytes: int64(len(data)), MediaType: artworkstore.MediaTypeForKey(key), ModTime: time.Unix(1, 0)}, nil
}

func (s memoryStore) DeleteObjects(_ context.Context, keys []string) (int, error) {
	for _, key := range keys {
		delete(s, key)
	}
	return len(keys), nil
}

func TestTryValidatesAndAdoptsCompleteRevision(t *testing.T) {
	revision, err := artworkkey.BuildPortableRevision(artworkkey.RevisionInput{
		ImageType: "poster", MediaType: "image/webp", Ext: ".webp",
		Variants: []artworkkey.VariantBytes{{Name: "original", Data: []byte("original")}, {Name: "w300", Data: []byte("small")}, {Name: "w500", Data: []byte("medium")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := memoryStore{}
	for name, key := range revision.VariantKeys {
		for _, variant := range revision.Manifest.Variants {
			if variant.Name == name {
				var data []byte
				switch name {
				case "original":
					data = []byte("original")
				case "w300":
					data = []byte("small")
				case "w500":
					data = []byte("medium")
				}
				store[key] = data
			}
		}
	}
	store[revision.ManifestKey] = revision.ManifestJSON
	fingerprint, _ := artworkkey.SourceFingerprint("provider", "tmdb://poster/42")
	if err := WriteIndex(context.Background(), store, fingerprint, revision.Manifest, revision.ManifestJSON); err != nil {
		t.Fatal(err)
	}
	got, ok := Try(context.Background(), store, fingerprint, "poster")
	if !ok || got.OriginalKey != revision.OriginalKey || got.Manifest.Revision != revision.Revision {
		t.Fatalf("adoption = %#v, %v", got, ok)
	}
	store[revision.OriginalKey] = []byte("tampered")
	if _, ok := Try(context.Background(), store, fingerprint, "poster"); ok {
		t.Fatal("adopted a revision whose object digest did not match")
	}
}
