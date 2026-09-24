package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/sections"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
)

// nextUpReadTracer records the statements that read a next-up mode setting:
// the canonical ui.next_up_mode row or the legacy next_up_mode account key.
// Everything else a section fetch issues is its own work and is not counted.
type nextUpReadTracer struct {
	mu    sync.Mutex
	armed bool
	reads []string
}

func (c *nextUpReadTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.armed {
		return ctx
	}
	mentions := strings.Contains(data.SQL, "next_up_mode")
	for _, arg := range data.Args {
		if strings.Contains(fmt.Sprint(arg), "next_up_mode") {
			mentions = true
		}
	}
	if mentions {
		c.reads = append(c.reads, strings.Join(strings.Fields(data.SQL), " "))
	}
	return ctx
}

func (c *nextUpReadTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *nextUpReadTracer) arm() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = true
	c.reads = nil
}

func (c *nextUpReadTracer) disarm() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.armed = false
	return append([]string(nil), c.reads...)
}

// TestHomeSectionItemsNextUpReadsDB counts the next-up mode reads one Home
// section-items request makes after the production viewer resolver has put
// the scope in its context, as RequireViewerAccess does. The request consults
// the mode twice (maybeInjectNextUp, then the section's fetcher), and both
// answer from the scope, so the budget is zero whatever the profile's mode.
// The response checks confirm the scope's mode is the one applied: only the
// "separate" profile gets the synthetic Next Up row.
func TestHomeSectionItemsNextUpReadsDB(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	tracer := &nextUpReadTracer{}
	config.ConnConfig.Tracer = tracer
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)

	var userID int
	name := fmt.Sprintf("home-next-up-reads-%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (username, email, password_hash, role) VALUES ($1, $2, '', 'user') RETURNING id`,
		name, name+"@example.test",
	).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID) })

	provider := pgstore.NewPostgresProvider(pool)
	store, err := provider.ForUser(ctx, userID)
	if err != nil {
		t.Fatalf("ForUser: %v", err)
	}
	for _, profileID := range []string{"profile-default", "profile-separate"} {
		if err := store.CreateProfile(ctx, userstore.Profile{ID: profileID, Name: profileID}); err != nil {
			t.Fatalf("CreateProfile: %v", err)
		}
	}
	if _, err := store.UpsertSettingValue(ctx, userstore.SettingIdentity{
		Key: settingskeys.UiNextUpMode, Scope: settingscontract.ScopeProfile, ProfileID: "profile-separate",
	}, json.RawMessage(`"separate"`)); err != nil {
		t.Fatalf("store next-up mode: %v", err)
	}

	engine, err := policy.NewEngine(ctx)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	resolver := policy.NewViewerResolver(auth.NewUserRepository(pool), provider,
		access.NewProfileTokenService("home-next-up-reads-profile-secret", 0), policy.NewPDP(engine), access.NewGroupStore(pool))

	fetcher := sections.NewFetcher(pool)
	fetcher.StoreProvider = provider
	fetcher.NextUpRepo = catalog.NewNextUpRepository(pool, provider)
	h := NewSectionHandler(sections.NewRepository(pool), fetcher)
	h.StoreProvider = provider

	tests := []struct {
		name      string
		profileID string
		// nextUpRow reports whether the Home layout gains the synthetic
		// Next Up row, which only a "separate" mode injects.
		nextUpRow bool
	}{
		{name: "default profile", profileID: "profile-default"},
		{name: "separate profile", profileID: "profile-separate", nextUpRow: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scope, err := resolver.Resolve(ctx, access.ResolveInput{
				UserID: userID, ProfileID: tt.profileID, SkipPINVerification: true,
			})
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			reqCtx := apimw.SetClaims(ctx, &auth.Claims{UserID: userID})
			reqCtx = apimw.SetProfileID(reqCtx, tt.profileID)
			reqCtx = access.SetScope(reqCtx, scope)

			resolved, _, _, _, err := h.loadResolvedHomeSections(reqCtx)
			if err != nil {
				t.Fatalf("loadResolvedHomeSections: %v", err)
			}
			continueWatchingID := ""
			for _, s := range resolved {
				if s.SectionType == sections.SectionContinueWatching &&
					sections.ContinueTypeFromConfig(s.Config) == sections.ContinueTypeWatching {
					continueWatchingID = s.ID
					break
				}
			}
			if continueWatchingID == "" {
				t.Fatal("the Home layout has no Continue Watching row to request")
			}

			for _, sectionID := range []string{continueWatchingID, "system-next-up"} {
				tracer.arm()
				_, err := h.HomeSectionItems(reqCtx, sectionID, SectionViewer{Access: catalog.AccessFilter{}})
				reads := tracer.disarm()

				wantFound := sectionID == continueWatchingID || tt.nextUpRow
				var apiErr *APIError
				switch {
				case wantFound && err != nil:
					t.Fatalf("HomeSectionItems(%s): %v", sectionID, err)
				case !wantFound && (!errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound):
					t.Fatalf("HomeSectionItems(%s) = %v, want not found: no Next Up row without the separate mode", sectionID, err)
				}
				if len(reads) != 0 {
					t.Fatalf("HomeSectionItems(%s) issued %d next-up mode reads, want 0:\n%s",
						sectionID, len(reads), strings.Join(reads, "\n"))
				}
				t.Logf("HomeSectionItems(%s) next-up mode reads: %d", sectionID, len(reads))
			}
		})
	}
}
