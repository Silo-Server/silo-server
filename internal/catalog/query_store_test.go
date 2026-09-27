package catalog

import (
	"errors"
	"testing"

	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

func TestQueryStoreUsesSelectedProvider(t *testing.T) {
	access := AccessFilter{UserID: 7, ProfileID: "profile-1"}
	resolver := NewCatalogResolver(nil, nil).WithUserStoreProvider(testDetailUserStoreProvider{store: newDetailTestStore(t)})
	for _, def := range []QueryDefinition{
		{Sort: QuerySort{Field: "progress"}},
		{Groups: []QueryGroup{{Rules: []QueryRule{{Field: "watched", Op: "is", Value: true}}}}},
	} {
		if err := resolver.requireQueryStore(t.Context(), def, access); !errors.Is(err, ErrCatalogStorageUnsupported) {
			t.Fatalf("SQLite viewer state must not fall through to PostgreSQL: %v", err)
		}
	}
	if err := resolver.requireQueryStore(t.Context(), QueryDefinition{Sort: QuerySort{Field: "title"}}, access); err != nil {
		t.Fatalf("nonpersonal catalog query needs no viewer SQL: %v", err)
	}
	resolver.WithUserStoreProvider(testDetailUserStoreProvider{store: new(pgstore.PostgresUserStore)})
	if err := resolver.requireQueryStore(t.Context(), QueryDefinition{Sort: QuerySort{Field: "progress"}}, access); err != nil {
		t.Fatalf("selected PostgreSQL provider refused: %v", err)
	}
}
