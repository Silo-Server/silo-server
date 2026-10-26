package trickplay

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/catalog"
)

// identityStore is a store at an identity whose objects hold their own key.
type identityStore string

func (s identityStore) Identity() string { return string(s) }

func (s identityStore) Get(_ context.Context, key string) (io.ReadCloser, blobstore.ObjectInfo, error) {
	return io.NopCloser(strings.NewReader(key)), blobstore.ObjectInfo{Key: key}, nil
}

// fakeURLs signs every key, each expiring an hour apart.
type fakeURLs struct{}

func (fakeURLs) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := map[string]catalog.ResolvedImageURL{}
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for i, key := range keys {
		expiry := base.Add(time.Duration(len(keys)-i) * time.Hour)
		out[key] = catalog.ResolvedImageURL{URL: "https://cdn.example/" + key, ExpiresAt: &expiry}
	}
	return out
}
