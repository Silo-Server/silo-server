package progresssync

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/notifications"
	"github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/userdb"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/userstore/pgstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func serviceFixture(t *testing.T) (*Service, Actor, *pgxpool.Pool, userstore.UserStoreProvider) {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires migrated isolated PostgreSQL")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	users := auth.NewUserRepository(pool)
	suffix := uuid.NewString()
	user, err := users.Create(t.Context(), models.CreateUserInput{Username: "bootstrap-" + suffix, Email: suffix + "@example.test", Password: "synthetic-bootstrap-password", Role: models.RoleUser})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, user.ID) })
	provider := notifications.WrapUserStoreProvider(pgstore.NewPostgresProvider(pool), &notifications.System{})
	store, err := provider.ForUser(t.Context(), user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.CreateProfile(t.Context(), userstore.Profile{ID: suffix, Name: "Synthetic profile"}); err != nil {
		t.Fatal(err)
	}
	engine, err := policy.NewEngine(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	resolver := policy.NewViewerResolver(users, provider, nil, policy.NewPDP(engine), access.NewGroupStore(pool))
	actor := Actor{Input: access.ResolveInput{UserID: user.ID, ProfileID: suffix}}
	actor.Recheck = func(ctx context.Context) (access.Scope, error) { return resolver.Resolve(ctx, actor.Input) }
	service := NewService(pool, provider, catalog.NewServerSettingsRepo(pool), resolver)
	return service, actor, pool, provider
}
func TestProductionWrappedProgressBootstrap(t *testing.T) {
	s, actor, pool, provider := serviceFixture(t)
	selected, err := provider.ForUser(t.Context(), actor.Input.UserID)
	if err != nil {
		t.Fatal(err)
	}
	source, ok := selected.(userstore.ProgressSnapshotSource)
	if !ok {
		t.Fatal("production wrapper lost bootstrap capability")
	}
	db, id := source.ProgressSnapshotDatabase()
	if db != pool || id != actor.Input.UserID {
		t.Fatal("wrong database/account")
	}
	support, err := s.Capabilities(t.Context(), actor)
	if err != nil || support.Generation == "" || support.InstallationID == "" {
		t.Fatalf("support=%+v err=%v", support, err)
	}
	items := []string{uuid.NewString(), uuid.NewString()}
	for _, item := range items {
		exec(t, pool, `INSERT INTO media_items(content_id,type,title) VALUES($1,'movie','Synthetic')`, item)
		exec(t, pool, `INSERT INTO user_watch_progress(user_id,profile_id,media_item_id,position_seconds) VALUES($1,$2,$3,12)`, actor.Input.UserID, actor.Input.ProfileID, item)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM media_items WHERE content_id=ANY($1)`, items)
	})
	first, err := s.CreateSnapshot(t.Context(), actor, uuid.NewString(), 1)
	if err != nil || first.Next == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	last, err := s.ReadSnapshot(t.Context(), actor, *first.Next)
	if err != nil || last.Next != nil || len(last.Items) != 1 {
		t.Fatalf("last=%+v err=%v", last, err)
	}
	if err = s.CheckSnapshotVisibility(t.Context(), actor, first.Snapshot.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.CheckSnapshotVisibility(t.Context(), actor, uuid.NewString()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown snapshot error=%v", err)
	}
	// Rechecking current account authority is required even when middleware was
	// satisfied before the long snapshot operation began.
	exec(t, pool, `UPDATE users SET enabled=false WHERE id=$1`, actor.Input.UserID)
	if _, err = s.ReadSnapshot(t.Context(), actor, *first.Next); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled account error=%v", err)
	}
}
func TestProductionWrappedSQLiteUnsupported(t *testing.T) {
	s, actor, _, _ := serviceFixture(t)
	provider := notifications.WrapUserStoreProvider(userdb.NewSQLiteProvider(userdb.NewUserDBPool(userdb.PoolConfig{DataDir: t.TempDir()})), &notifications.System{})
	t.Cleanup(func() { _ = provider.Close() })
	s.provider = provider
	if _, err := s.Capabilities(t.Context(), actor); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SQLite support error=%v", err)
	}
	if _, err := s.CreateSnapshot(t.Context(), actor, uuid.NewString(), 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("SQLite create error=%v", err)
	}
}
func TestBootstrapFinalAuthorityRevocation(t *testing.T) {
	s, actor, _, _ := serviceFixture(t)
	original := actor.Recheck
	calls := 0
	actor.Recheck = func(ctx context.Context) (access.Scope, error) {
		calls++
		if calls > 1 {
			return access.Scope{}, access.ErrProfileUnverified
		}
		return original(ctx)
	}
	if _, err := s.CreateSnapshot(t.Context(), actor, uuid.NewString(), 1); !errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("revoked PIN error=%v", err)
	}
}
func TestBootstrapRejectsDifferentSelectedDatabase(t *testing.T) {
	s, actor, pool, _ := serviceFixture(t)
	config := pool.Config()
	other, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	s.provider = pgstore.NewPostgresProvider(other)
	if _, err = s.Capabilities(t.Context(), actor); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("mismatched source error=%v", err)
	}
}

func TestBootstrapUsesCustomPDPAndCurrentPIN(t *testing.T) {
	s, actor, pool, provider := serviceFixture(t)
	engine, err := policy.NewEngineWithCustom(t.Context(), map[string]policy.ActiveSource{"scope": {Source: `package silo_custom.scope
import rego.v1
override(_, _) := {"profile_verified": false}
`}})
	if err != nil {
		t.Fatal(err)
	}
	s.resolver = policy.NewViewerResolver(auth.NewUserRepository(pool), provider, nil, policy.NewPDP(engine), access.NewGroupStore(pool))
	if _, err = s.CreateSnapshot(t.Context(), actor, uuid.NewString(), 1); !errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("custom PDP bypass: %v", err)
	}
	s, actor, pool, _ = serviceFixture(t)
	if _, err = s.Capabilities(t.Context(), actor); err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `UPDATE user_profiles SET pin_hash='synthetic-locked-profile' WHERE user_id=$1 AND id=$2`, actor.Input.UserID, actor.Input.ProfileID)
	if _, err = s.CreateSnapshot(t.Context(), actor, uuid.NewString(), 1); !errors.Is(err, access.ErrProfileUnverified) {
		t.Fatalf("new PIN bypass: %v", err)
	}
}
