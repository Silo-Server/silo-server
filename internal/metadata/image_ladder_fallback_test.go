package metadata

import (
	"context"

	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
)

type noProbeStore struct{ t *testing.T }

func (s noProbeStore) ResolveURLs(_ context.Context, keys []string) map[string]catalog.ResolvedImageURL {
	out := make(map[string]catalog.ResolvedImageURL, len(keys))
	for _, key := range keys {
		out[key] = catalog.ResolvedImageURL{URL: "https://images.example/" + key}
	}
	return out
}
func (s noProbeStore) ObjectExists(context.Context, string, string) (bool, error) {
	s.t.Fatal("catalog read checked storage")
	return false, nil
}
func (s noProbeStore) ObjectAvailable(context.Context, string, string) (bool, error) {
	s.t.Fatal("catalog read checked delivery")
	return false, nil
}
func (s noProbeStore) UsesExternalDelivery() bool { return true }
