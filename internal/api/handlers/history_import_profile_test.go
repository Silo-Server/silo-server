package handlers

import (
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The household rule for history imports (#1336): the verified primary
// profile acts for every profile on the account, any other profile only for
// itself, and another profile's run reads as not found.
func TestHistoryImportProfileAuthorizationDB(t *testing.T) { //nolint:funlen,gocyclo // ported unchanged from the retired v1 handler test
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
	if err := store.UpdateProfile(t.Context(), "primary", userstore.UpdateProfileInput{PINHash: storedPIN(t, "1234")}); err != nil {
		t.Fatal(err)
	}
	stores := mappedTestUserStoreProvider{stores: map[int]userstore.UserStore{1: store}}
	// No credential cipher: an authorized create reaches the 503 admission
	// guard without exchanging external credentials or writing a run.
	handler := NewHistoryImportHandler(historyimport.NewService(t.Context(), historyimport.NewRepository(pool, nil), stores))

	verified := func(string) error { return nil }
	unverified := func(string) error { return access.ErrProfileUnverified }
	actor := func(profile string, verify func(string) error) HistoryImportActor {
		return HistoryImportActor{UserID: 1, ProfileID: profile, VerifyProfile: verify}
	}
	status := func(err error) int {
		if err == nil {
			return 200
		}
		if e, ok := errors.AsType[*APIError](err); ok {
			return e.Status
		}
		t.Fatalf("unexpected error type %T: %v", err, err)
		return 0
	}

	t.Run("create", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			actor  HistoryImportActor
			target string
			want   int
		}{
			{"guest cannot target primary", actor("guest", verified), "primary", 403},
			{"guest own import reaches admission", actor("guest", unverified), "guest", 503},
			{"no acting profile", actor("", verified), "guest", 403},
			{"locked primary without proof", actor("primary", unverified), "guest", 403},
			{"locked primary without a verifier", actor("primary", nil), "guest", 403},
			{"verified primary reaches admission", actor("primary", verified), "guest", 503},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := handler.CreateImportRunAs(t.Context(), tc.actor, historyimport.CreateRunInput{ProfileID: tc.target, Source: "plex"})
				if got := status(err); got != tc.want {
					t.Fatalf("status = %d (%v), want %d", got, err, tc.want)
				}
			})
		}
	})

	t.Run("list", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			actor       HistoryImportActor
			limit       int
			count       int
			profileOnly string
		}{
			{"guest sees only own runs", actor("guest", verified), 2, 2, "guest"},
			{"no acting profile sees no runs", actor("", verified), 50, 0, ""},
			{"verified primary sees household", actor("primary", verified), 50, 50, ""},
			{"unverified primary sees only its own runs", actor("primary", unverified), 100, 60, "primary"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				runs, _, err := handler.ListImportRunsPageAs(t.Context(), tc.actor, nil, tc.limit)
				if err != nil || len(runs) != tc.count {
					t.Fatalf("count=%d err=%v, want %d runs", len(runs), err, tc.count)
				}
				for _, run := range runs {
					if run.UserID != 1 || (tc.profileOnly != "" && run.ProfileID != tc.profileOnly) {
						t.Fatalf("unexpected run: %+v", run)
					}
				}
				if tc.count > 0 && tc.profileOnly == "" && runs[0].ID != "guest-new" {
					t.Fatalf("household list omitted guest run: %+v", runs[0])
				}
			})
		}
	})

	t.Run("get", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			actor HistoryImportActor
			run   string
			want  int
		}{
			{"guest reads own run", actor("guest", unverified), "guest-new", 200},
			{"guest cannot read primary run", actor("guest", verified), "primary-1", 404},
			{"no profile cannot read a run", actor("", verified), "guest-new", 404},
			{"verified primary reads guest run", actor("primary", verified), "guest-new", 200},
			{"unverified primary cannot read guest run", actor("primary", unverified), "guest-new", 404},
			{"verified primary cannot read another account run", actor("primary", verified), "foreign-run", 404},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := handler.GetImportRunAs(t.Context(), tc.actor, tc.run)
				if got := status(err); got != tc.want {
					t.Fatalf("status = %d (%v), want %d", got, err, tc.want)
				}
			})
		}
	})

	t.Run("profile cursor filters every page", func(t *testing.T) {
		guest := actor("guest", unverified)
		first, more, err := handler.ListImportRunsPageAs(t.Context(), guest, nil, 1)
		if err != nil || len(first) != 1 || first[0].ID != "guest-new" || !more {
			t.Fatalf("first page = %+v, more=%v, err=%v", first, more, err)
		}
		after := &historyimport.RunKey{CreatedAt: first[0].CreatedAt, ID: first[0].ID}
		last, more, err := handler.ListImportRunsPageAs(t.Context(), guest, after, 1)
		if err != nil || len(last) != 1 || last[0].ID != "guest-old" || more {
			t.Fatalf("last page = %+v, more=%v, err=%v", last, more, err)
		}
	})

	t.Run("primary without PIN sees every household run", func(t *testing.T) {
		if err := store.UpdateProfile(t.Context(), "primary", userstore.UpdateProfileInput{PINHash: storedPIN(t, "")}); err != nil {
			t.Fatal(err)
		}
		runs, more, err := handler.ListImportRunsPageAs(t.Context(), actor("primary", nil), nil, 100)
		if err != nil || more || len(runs) != 62 || !slices.ContainsFunc(runs, func(run historyimport.Run) bool { return run.ProfileID == "guest" }) {
			t.Fatalf("household page count=%d, more=%v, err=%v", len(runs), more, err)
		}
	})
}
