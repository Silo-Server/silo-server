package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func TestHistoryImportV1ProfileAuthorization(t *testing.T) {
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1 // Keep the temporary tables on the same connection.
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, statement := range []string{
		`CREATE TEMP TABLE user_profiles (id text PRIMARY KEY, user_id integer NOT NULL)`,
		`INSERT INTO user_profiles VALUES ('primary',1), ('guest',1), ('foreign',2)`,
		`CREATE TEMP TABLE history_import_runs (
			id text PRIMARY KEY, user_id integer NOT NULL, profile_id text NOT NULL,
			source_type text NOT NULL DEFAULT 'plex', connection_mode text NOT NULL DEFAULT 'custom',
			status text NOT NULL DEFAULT 'completed', mapping_id integer,
			fetched integer NOT NULL DEFAULT 0, matched integer NOT NULL DEFAULT 0,
			unmatched integer NOT NULL DEFAULT 0, progress_updated integer NOT NULL DEFAULT 0,
			history_created integer NOT NULL DEFAULT 0, watchlist_added integer NOT NULL DEFAULT 0,
			favorites_imported integer NOT NULL DEFAULT 0, skipped integer NOT NULL DEFAULT 0,
			warnings jsonb NOT NULL DEFAULT '[]', unmatched_samples jsonb NOT NULL DEFAULT '[]',
			error_message text, created_at timestamptz NOT NULL DEFAULT now(),
			started_at timestamptz, completed_at timestamptz, cancel_requested_at timestamptz
		)`,
		`INSERT INTO history_import_runs(id,user_id,profile_id,created_at)
		 VALUES ('guest-old',1,'guest','2026-01-01'), ('guest-new',1,'guest','2026-01-04'),
		        ('foreign-run',2,'foreign','2026-01-04')`,
		`INSERT INTO history_import_runs(id,user_id,profile_id,created_at)
		 SELECT 'primary-' || n, 1, 'primary', '2026-01-03'::timestamptz FROM generate_series(1,60) n`,
	} {
		if _, err := pool.Exec(t.Context(), statement); err != nil {
			t.Fatal(err)
		}
	}

	store := newHouseholdTestStore(t)
	for _, profile := range []userstore.Profile{
		{ID: "primary", Name: "Primary", IsPrimary: true},
		{ID: "guest", Name: "Guest"},
	} {
		if err := store.CreateProfile(t.Context(), profile); err != nil {
			t.Fatal(err)
		}
	}
	pin := "1234"
	if err := store.UpdateProfile(t.Context(), "primary", userstore.UpdateProfileInput{PIN: &pin}); err != nil {
		t.Fatal(err)
	}
	stores := mappedTestUserStoreProvider{stores: map[int]userstore.UserStore{1: store}}
	tokens := access.NewProfileTokenService("history-import-profile-test-secret", 0)
	profileToken, _, err := tokens.Mint(access.ProfileTokenClaims{UserID: 1, SessionID: "session-1", ProfileID: "primary"})
	if err != nil {
		t.Fatal(err)
	}
	viewer := apimw.NewViewerAccessMiddleware(access.NewResolver(
		stubUserRepo{user: &models.User{ID: 1}}, stores, tokens,
	))
	// No credential cipher: an authorized create reaches the existing 503
	// admission guard, without exchanging external credentials or writing a run.
	handler := NewHistoryImportHandler(historyimport.NewService(t.Context(), historyimport.NewRepository(pool, nil), stores))
	router := chi.NewRouter()
	router.Use(viewer.RequireViewerAccess)
	router.Post("/api/v1/history-imports/runs", handler.HandleCreateRun)
	router.Get("/api/v1/history-imports/runs", handler.HandleListRuns)
	router.Get("/api/v1/history-imports/runs/{id}", handler.HandleGetRun)

	request := func(method, path, body, profile, tokenType, role, proof string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1/history-imports/runs"+path, strings.NewReader(body))
		r.Header.Set("X-Profile-Id", profile)
		r.Header.Set("X-Profile-Token", proof)
		r = r.WithContext(apimw.SetClaims(r.Context(), &auth.Claims{UserID: 1, SessionID: "session-1", Role: role, TokenType: tokenType}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, r)
		return rec
	}

	t.Run("create", func(t *testing.T) {
		for _, tc := range []struct {
			name, profile, target, tokenType, role, proof string
			status                                        int
		}{
			{"guest cannot target primary", "guest", "primary", auth.TokenTypeAccess, "user", "", 403},
			{"guest own import reaches admission", "guest", "guest", auth.TokenTypeAccess, "user", "", 503},
			{"no acting profile", "", "guest", auth.TokenTypeAccess, "user", "", 403},
			{"locked primary without proof", "primary", "guest", auth.TokenTypeAccess, "user", "", 403},
			{"locked primary with invalid proof", "primary", "guest", auth.TokenTypeAccess, "user", "invalid-token", 403},
			{"verified primary reaches admission", "primary", "guest", auth.TokenTypeAccess, "user", profileToken, 503},
			{"foreign acting profile rejected", "foreign", "guest", auth.TokenTypeAccess, "user", "", 404},
			{"API key PIN exemption is not household authority", "primary", "guest", auth.TokenTypeAPIKey, "user", "", 403},
			{"admin without profile reaches admission", "", "guest", auth.TokenTypeAccess, "admin", "", 503},
			{"admin cannot target another account", "", "foreign", auth.TokenTypeAccess, "admin", "", 404},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rec := request(http.MethodPost, "", fmt.Sprintf(`{"profile_id":%q,"source":"plex"}`, tc.target), tc.profile, tc.tokenType, tc.role, tc.proof)
				if rec.Code != tc.status {
					t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
				}
				var failure struct {
					Error string `json:"error"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil || failure.Error == "" {
					t.Fatalf("missing v1 error envelope: %s (%v)", rec.Body.String(), err)
				}
			})
		}
	})

	t.Run("list", func(t *testing.T) {
		for _, tc := range []struct {
			name, profile, tokenType, role, proof, query string
			count                                        int
			profileOnly                                  string
		}{
			{"guest sees only own runs before limit", "guest", auth.TokenTypeAccess, "user", "", "?limit=2", 2, "guest"},
			{"no acting profile sees no runs", "", auth.TokenTypeAccess, "user", "", "", 0, ""},
			{"verified primary sees household", "primary", auth.TokenTypeAccess, "user", profileToken, "?limit=200", 50, ""},
			{"API key sees only locked primary runs", "primary", auth.TokenTypeAPIKey, "user", "", "?limit=200", 50, "primary"},
			{"admin retains v1 limit", "", auth.TokenTypeAccess, "admin", "", "?limit=200", 50, ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rec := request(http.MethodGet, tc.query, "", tc.profile, tc.tokenType, tc.role, tc.proof)
				var runs []historyimport.Run
				if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil || rec.Code != http.StatusOK || len(runs) != tc.count {
					t.Fatalf("list status=%d count=%d err=%v, want %d runs", rec.Code, len(runs), err, tc.count)
				}
				for _, run := range runs {
					if run.UserID != 1 || (tc.profileOnly != "" && run.ProfileID != tc.profileOnly) {
						t.Fatalf("unexpected run: %+v", run)
					}
				}
				if tc.count > 0 && tc.profileOnly == "" && runs[0].ID != "guest-new" {
					t.Fatalf("household list omitted guest run: %+v", runs[0])
				}
				if tc.profile == "guest" && (runs[0].ID != "guest-new" || runs[1].ID != "guest-old") {
					t.Fatalf("guest run order = %+v", runs)
				}
			})
		}
	})

	t.Run("get", func(t *testing.T) {
		for _, tc := range []struct {
			name, profile, run, tokenType, role, proof string
			status                                     int
		}{
			{"guest reads own run", "guest", "guest-new", auth.TokenTypeAccess, "user", "", 200},
			{"guest cannot read primary run", "guest", "primary-1", auth.TokenTypeAccess, "user", "", 404},
			{"no profile cannot read a run", "", "guest-new", auth.TokenTypeAccess, "user", "", 404},
			{"verified primary reads guest run", "primary", "guest-new", auth.TokenTypeAccess, "user", profileToken, 200},
			{"API key cannot read guest run as locked primary", "primary", "guest-new", auth.TokenTypeAPIKey, "user", "", 404},
			{"admin reads guest run", "", "guest-new", auth.TokenTypeAccess, "admin", "", 200},
			{"admin cannot read another account run", "", "foreign-run", auth.TokenTypeAccess, "admin", "", 404},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rec := request(http.MethodGet, "/"+tc.run, "", tc.profile, tc.tokenType, tc.role, tc.proof)
				if rec.Code != tc.status {
					t.Fatalf("get = %d %s, want %d", rec.Code, rec.Body.String(), tc.status)
				}
			})
		}
	})

	t.Run("profile cursor filters every page", func(t *testing.T) {
		actor := HistoryImportActor{UserID: 1, ProfileID: "guest"}
		first, more, err := handler.ListImportRunsPageAs(t.Context(), actor, nil, 1)
		if err != nil || len(first) != 1 || first[0].ID != "guest-new" || !more {
			t.Fatalf("first page = %+v, more=%v, err=%v", first, more, err)
		}
		after := &historyimport.RunKey{CreatedAt: first[0].CreatedAt, ID: first[0].ID}
		last, more, err := handler.ListImportRunsPageAs(t.Context(), actor, after, 1)
		if err != nil || len(last) != 1 || last[0].ID != "guest-old" || more {
			t.Fatalf("last page = %+v, more=%v, err=%v", last, more, err)
		}
	})

	t.Run("primary without PIN sees every household run", func(t *testing.T) {
		emptyPIN := ""
		if err := store.UpdateProfile(t.Context(), "primary", userstore.UpdateProfileInput{PIN: &emptyPIN}); err != nil {
			t.Fatal(err)
		}
		runs, more, err := handler.ListImportRunsPageAs(t.Context(), HistoryImportActor{UserID: 1, ProfileID: "primary"}, nil, 100)
		if err != nil || more || len(runs) != 62 || !slices.ContainsFunc(runs, func(run historyimport.Run) bool { return run.ProfileID == "guest" }) {
			t.Fatalf("household page count=%d, more=%v, err=%v", len(runs), more, err)
		}
	})
}
