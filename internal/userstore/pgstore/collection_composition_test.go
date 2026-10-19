package pgstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

func compositionStoreFixture(t *testing.T) (*PostgresUserStore, *userstore.Collection) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var uid int
	if err := pool.QueryRow(t.Context(), `INSERT INTO users(username,role) VALUES($1,'user') RETURNING id`, fmt.Sprintf("composition-%d", time.Now().UnixNano())).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		for _, query := range []string{`DELETE FROM users WHERE id=$1`, `DELETE FROM user_collection_revisions WHERE user_id=$1`, `DELETE FROM user_collection_order_revisions WHERE user_id=$1`} {
			if _, err := pool.Exec(ctx, query, uid); err != nil {
				t.Errorf("composition fixture cleanup: %v", err)
			}
		}
	})
	s := newStore(pool, uid)
	c, err := s.CreateCollection(t.Context(), userstore.CreateCollectionInput{CreatorProfileID: "owner", Name: "Synthetic composition", CollectionType: "smart"})
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

func TestPostgresCollectionCompositionWriteLockDB(t *testing.T) {
	for _, guarded := range []bool{false, true} {
		t.Run(fmt.Sprintf("wildcard=%t", guarded), func(t *testing.T) {
			s, c := compositionStoreFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			firstDone := make(chan error, 1)
			go func() {
				firstDone <- s.UpdateCollection(ctx, userstore.UpdateCollectionInput{
					ID: c.ID, RequestProfileID: "owner", QueryDefinition: new(`{"library_ids":[1]}`),
					ValidateQueryComposition: func(source, display string) error {
						close(entered)
						select {
						case <-release:
							return catalog.ValidatePersonalCollectionQueryBudget(source, display)
						case <-ctx.Done():
							return ctx.Err()
						}
					},
				})
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatalf("first writer did not validate: %v", ctx.Err())
			}
			cfg, err := pgxpool.ParseConfig(os.Getenv("SILO_TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			name := fmt.Sprintf("composition-second-%d", s.userID)
			cfg.ConnConfig.RuntimeParams["application_name"] = name
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			var expected *int64
			if guarded {
				expected = new(int64(-1))
			}
			secondDone := make(chan error, 1)
			go func() {
				secondDone <- newStore(pool, s.userID).UpdateCollection(ctx, userstore.UpdateCollectionInput{
					ID: c.ID, RequestProfileID: "owner", ExpectedRevision: expected, DisplayQueryDefinition: new(`{}`),
					ValidateQueryComposition: func(source, display string) error {
						if source != `{"library_ids": [1]}` {
							return fmt.Errorf("read stale source: %s", source)
						}
						return catalog.ValidatePersonalCollectionQueryBudget(source, display)
					},
				})
			}()
			waitCollectionBlocked(t, ctx, s.pool, name, "transactionid")
			close(release)
			if err := <-firstDone; err != nil {
				t.Fatal(err)
			}
			if err := <-secondDone; err != nil {
				t.Fatalf("waiting writer did not validate the committed companion: %v", err)
			}
		})
	}
}

func TestPostgresCollectionCompositionRecoveryDB(t *testing.T) {
	s, c := compositionStoreFixture(t)
	ctx := t.Context()
	// Synthetic legacy JSON need not be countable to permit a metadata edit.
	if _, err := s.pool.Exec(ctx, `UPDATE user_personal_collections SET query_definition=$3::jsonb WHERE user_id=$1 AND id=$2`, s.userID, c.ID, `{"groups":[`+repeatEmptyGroups(17)+`]}`); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCollection(ctx, userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", Name: new("Synthetic renamed")}); err != nil {
		t.Fatalf("legacy metadata edit: %v", err)
	}
	revision, err := s.CollectionRevision(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := userstore.UpdateCollectionInput{ID: c.ID, RequestProfileID: "owner", QueryDefinition: new(`{}`), ExpectedRevision: &revision, ValidateQueryComposition: catalog.ValidatePersonalCollectionQueryBudget}
	if err := s.UpdateCollection(ctx, input); err != nil {
		t.Fatalf("revision-guarded legacy repair: %v", err)
	}
	if err := s.UpdateCollection(ctx, input); !errors.Is(err, userstore.ErrCollectionRevisionMismatch) {
		t.Fatalf("stale v2 revision: %v", err)
	}
	revision, err = s.CollectionRevision(ctx, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.QueryDefinition = new(`{"groups":[` + repeatEmptyGroups(17) + `]}`)
	if err := s.UpdateCollection(ctx, input); !errors.Is(err, catalog.ErrQueryComplexity) {
		t.Fatalf("invalid replacement accepted: %v", err)
	}
	after, err := s.CollectionRevision(ctx, c.ID)
	if err != nil || after != revision {
		t.Fatalf("rejected edit consumed revision: %d -> %d, %v", revision, after, err)
	}
}

func repeatEmptyGroups(n int) string {
	return strings.Repeat("{},", n-1) + "{}"
}
