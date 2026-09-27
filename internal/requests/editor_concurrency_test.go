package requests

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func editorTestRepository(t *testing.T) *Repository {
	t.Helper()
	dsn := os.Getenv("SILO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_TEST_DATABASE_URL is not set")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("request_editor_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(t.Context(), `CREATE SCHEMA `+quoted); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+quoted+` CASCADE`) })
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	for _, table := range []string{"request_settings", "request_user_limits", "request_integrations"} {
		var count int
		if err = admin.QueryRow(t.Context(), `SELECT count(*) FROM pg_trigger WHERE tgrelid=$1::regclass AND tgname=$2`, "public."+table, table+"_revision").Scan(&count); err != nil || count != 1 {
			t.Fatalf("production revision trigger %s: %d %v", table, count, err)
		}
		if _, err = pool.Exec(t.Context(), `CREATE TABLE `+table+` (LIKE public.`+table+` INCLUDING ALL); CREATE TRIGGER editor_revision BEFORE INSERT OR UPDATE ON `+table+` FOR EACH ROW EXECUTE FUNCTION public.advance_request_editor_revision()`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY); INSERT INTO users VALUES(1),(2); CREATE TABLE media_request_targets(integration_id text,status text)`); err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(pool, cipher)
}

func raceEditorWrites(t *testing.T, write func() error, wantSuccess int) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	var won atomic.Int32
	for range 12 {
		wg.Go(func() {
			<-start
			err := write()
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrStaleRevision) {
				t.Errorf("write: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if int(won.Load()) != wantSuccess {
		t.Fatalf("successful writes %d, want %d", won.Load(), wantSuccess)
	}
}

func TestEditorSettingsAndLimitsDatabase(t *testing.T) {
	repo := editorTestRepository(t)
	ctx := t.Context()
	viewer := Viewer{IsAdmin: true}
	service := NewService(repo, nil, nil)
	settings, err := service.GetSettings(ctx, viewer)
	if err != nil || settings.Revision != 0 || settings.GlobalMaxRequests != 5 || settings.GlobalWindowDays != 7 {
		t.Fatalf("missing settings: %+v %v", settings, err)
	}
	raceEditorWrites(t, func() error { _, err := repo.UpdateSettingsConditional(ctx, settings, 0); return err }, 1)
	settings, err = repo.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raceEditorWrites(t, func() error { _, err := repo.UpdateSettingsConditional(ctx, settings, settings.Revision); return err }, 1)
	raceEditorWrites(t, func() error { _, err := repo.UpdateSettingsConditional(ctx, settings, -1); return err }, 12)
	current, err := repo.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := repo.UpdateSettings(ctx, current)
	if err != nil || legacy.Revision <= current.Revision {
		t.Fatalf("legacy revision: %+v %v", legacy, err)
	}
	if _, err = repo.UpdateSettingsConditional(ctx, current, current.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale legacy: %v", err)
	}
	limit, err := service.GetUserLimit(ctx, viewer, 1)
	if err != nil || limit.Revision != 0 || limit.LimitMode != LimitModeInherit {
		t.Fatalf("default limit: %+v %v", limit, err)
	}
	if _, err = service.GetUserLimit(ctx, viewer, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account: %v", err)
	}
	raceEditorWrites(t, func() error { _, err := repo.UpsertUserLimitConditional(ctx, *limit, 0); return err }, 1)
	limit, err = repo.GetUserLimit(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	raceEditorWrites(t, func() error { _, err := repo.UpsertUserLimitConditional(ctx, *limit, limit.Revision); return err }, 1)
	raceEditorWrites(t, func() error { _, err := repo.UpsertUserLimitConditional(ctx, *limit, -1); return err }, 12)
	currentLimit, err := repo.GetUserLimit(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	legacyLimit, err := repo.UpsertUserLimit(ctx, *currentLimit)
	if err != nil || legacyLimit.Revision <= currentLimit.Revision {
		t.Fatalf("legacy limit: %+v %v", legacyLimit, err)
	}
	if _, err = repo.UpsertUserLimitConditional(ctx, UserLimit{UserID: 999}, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown account write: %v", err)
	}
}

func TestEditorIntegrationsDatabase(t *testing.T) {
	repo := editorTestRepository(t)
	ctx := t.Context()
	in := Integration{ID: "editor", Name: "Editor", Enabled: true, BaseURL: "https://example.invalid", APIKeyRef: "secret-key", CapabilityID: "arr", InstallationID: new(1), PluginConfig: map[string]any{"is_default": true}}
	row, err := repo.SaveIntegrationWithDefaults(ctx, in, true)
	if err != nil {
		t.Fatal(err)
	}
	sibling := in
	sibling.ID = "sibling"
	other, err := repo.SaveIntegrationWithDefaults(ctx, sibling, true)
	if err != nil {
		t.Fatal(err)
	}
	raceEditorWrites(t, func() error {
		edit := *row
		edit.APIKeyRef = ""
		_, err := repo.UpdateIntegrationConditional(ctx, edit, row.Revision)
		return err
	}, 1)
	current, err := repo.GetIntegration(ctx, in.ID)
	if err != nil || current.APIKeyRef != in.APIKeyRef {
		t.Fatalf("preserved key: %+v %v", current, err)
	}
	unchanged, err := repo.GetIntegration(ctx, other.ID)
	if err != nil || unchanged.Revision != other.Revision || unchanged.PluginConfig["is_default"] != true {
		t.Fatalf("sibling default changed: %+v %v", unchanged, err)
	}
	var stored string
	if err = repo.pool.QueryRow(ctx, `SELECT api_key_ref FROM request_integrations WHERE id=$1`, in.ID).Scan(&stored); err != nil || stored == in.APIKeyRef {
		t.Fatalf("key not encrypted: %v", err)
	}
	router := &fakeRouterProvider{}
	service := NewService(repo, nil, nil)
	service.SetRouterProvider(router)
	v := Viewer{IsAdmin: true}
	if _, err = service.UpdateIntegrationConditional(ctx, v, in, row.Revision); !errors.Is(err, ErrStaleRevision) || router.validateCalls != 0 {
		t.Fatalf("stale validation: %v calls=%d", err, router.validateCalls)
	}
	edit := *current
	edit.APIKeyRef = ""
	saved, err := service.UpdateIntegrationConditional(ctx, v, edit, current.Revision)
	if err != nil || router.validateCalls != 1 || saved.APIKeyRef != in.APIKeyRef {
		t.Fatalf("validated blank-key edit: %v calls=%d", err, router.validateCalls)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE request_integrations SET last_check_status='ok' WHERE id=$1`, in.ID); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteIntegrationConditional(ctx, in.ID, saved.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("check invalidates delete: %v", err)
	}
	current, err = repo.GetIntegration(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `INSERT INTO media_request_targets VALUES($1,'queued')`, in.ID); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteIntegrationConditional(ctx, in.ID, current.Revision); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("live target protection: %v", err)
	}
	if _, err = repo.pool.Exec(ctx, `DELETE FROM media_request_targets`); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteIntegrationConditional(ctx, in.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	recreated, err := repo.CreateIntegration(ctx, in)
	if err != nil || recreated.Revision <= current.Revision {
		t.Fatalf("recreated generation: %+v %v", recreated, err)
	}
	if _, err = repo.UpdateIntegrationConditional(ctx, in, current.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("recreate stale update: %v", err)
	}
	raceEditorWrites(t, func() error { _, err := repo.UpdateIntegrationConditional(ctx, in, -1); return err }, 12)
	if err = repo.DeleteIntegrationConditional(ctx, in.ID, -1); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteIntegrationConditional(ctx, in.ID, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing wildcard: %v", err)
	}
}

// Validation is external work: a concurrent edit must still win the final CAS.
type editingValidationRouter struct {
	*fakeRouterProvider
	edit func() error
}

func (r *editingValidationRouter) Validate(context.Context, int, string, ResolvedRouterConnection, []ResolvedRouterConnection) (map[string]string, string, error) {
	return nil, "", r.edit()
}
func TestEditorValidationRaceDatabase(t *testing.T) {
	repo := editorTestRepository(t)
	ctx := t.Context()
	in := Integration{ID: "validation-race", Name: "Before", CapabilityID: "arr", InstallationID: new(1)}
	row, err := repo.CreateIntegration(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repo, nil, nil)
	service.SetRouterProvider(&editingValidationRouter{fakeRouterProvider: &fakeRouterProvider{}, edit: func() error {
		concurrent := in
		concurrent.Name = "Concurrent winner"
		_, err := repo.UpdateIntegration(ctx, concurrent)
		return err
	}})
	in.Name = "Losing edit"
	if _, err = service.UpdateIntegrationConditional(ctx, Viewer{IsAdmin: true}, in, row.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("validation race: %v", err)
	}
	current, err := repo.GetIntegration(ctx, in.ID)
	if err != nil || current.Name != "Concurrent winner" {
		t.Fatalf("concurrent edit lost: %+v %v", current, err)
	}
}
