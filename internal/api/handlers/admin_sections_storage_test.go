package handlers

import (
	"context"
	"testing"

	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A mixed provider must not inherit the PostgreSQL-only reset capability even
// when one of its account stores happens to use PostgreSQL.
type mixedSectionProvider struct{ userstore.UserStoreProvider }

func (mixedSectionProvider) ForUser(context.Context, int) (userstore.UserStore, error) {
	panic("unsupported reset must reject before consulting account stores")
}

func TestSectionProfileResetCapabilitySurvivesProductionDecorator(t *testing.T) {
	pool := &pgxpool.Pool{}
	pg := pgstore.NewPostgresProvider(pool)
	sqlite := userdb.NewSQLiteProvider(nil)
	wrap := func(p userstore.UserStoreProvider) userstore.UserStoreProvider {
		return notifications.WrapUserStoreProvider(p, &notifications.System{})
	}
	for _, tc := range []struct {
		name     string
		provider userstore.UserStoreProvider
		want     bool
	}{
		{"postgres", pg, true},
		{"wrapped postgres", wrap(pg), true},
		{"nested notification wrappers", wrap(wrap(pg)), true},
		{"sqlite", sqlite, false},
		{"wrapped sqlite", wrap(sqlite), false},
		{"mixed", mixedSectionProvider{pg}, false},
		{"wrapped mixed", wrap(mixedSectionProvider{pg}), false},
		{"different pool", wrap(pgstore.NewPostgresProvider(&pgxpool.Pool{})), false},
		{"wrapped typed nil", wrap((*pgstore.PostgresProvider)(nil)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &SectionHandler{repo: sections.NewRepository(pool), StoreProvider: tc.provider}
			if got := h.canResetAllSectionProfileOverrides(); got != tc.want {
				t.Fatalf("reset capability=%v, want %v", got, tc.want)
			}
		})
	}
}
