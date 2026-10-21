package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRestoreSectionDefaultsRejectsUnsupportedProfileResetBeforeWrites(t *testing.T) {
	for name, provider := range map[string]userstore.UserStoreProvider{
		"unavailable":    nil,
		"sqlite":         userdb.NewSQLiteProvider(nil),
		"mixed":          mixedSectionProvider{pgstore.NewPostgresProvider(nil)},
		"wrapped sqlite": notifications.WrapUserStoreProvider(userdb.NewSQLiteProvider(nil), &notifications.System{}),
		"wrapped mixed":  notifications.WrapUserStoreProvider(mixedSectionProvider{pgstore.NewPostgresProvider(nil)}, &notifications.System{}),
		"nil postgres":   (*pgstore.PostgresProvider)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			// No repository is wired: an attempted definition read or write would panic.
			h := &SectionHandler{StoreProvider: provider}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/sections/restore-defaults", strings.NewReader(`{"scope":"home","reset_profiles":true}`))
			rec := httptest.NewRecorder()
			h.HandleRestoreDefaults(rec, req)
			if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "capability_unsupported") {
				t.Fatalf("unsupported reset response: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
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
