package historyimport

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

func editorRepository(t *testing.T) *Repository {
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
	schema := fmt.Sprintf("history_editor_%d", time.Now().UnixNano())
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
	_, err = pool.Exec(t.Context(), `CREATE TABLE users(id integer PRIMARY KEY,username text); INSERT INTO users VALUES(1,'one'),(2,'two');
 CREATE TABLE user_profiles(user_id integer REFERENCES users(id),id text,name text,PRIMARY KEY(user_id,id));
 INSERT INTO user_profiles VALUES(1,'p1','One'),(1,'p2','Two'),(2,'foreign','Foreign');
 CREATE TABLE history_import_sources(LIKE public.history_import_sources INCLUDING ALL);
 CREATE TABLE history_import_user_mappings(LIKE public.history_import_user_mappings INCLUDING ALL);
 ALTER TABLE history_import_user_mappings ADD FOREIGN KEY(source_id) REFERENCES history_import_sources(id) ON DELETE RESTRICT;
 ALTER TABLE history_import_user_mappings ADD FOREIGN KEY(silo_user_id,silo_profile_id) REFERENCES user_profiles(user_id,id) ON DELETE CASCADE;
 CREATE TRIGGER source_revision BEFORE INSERT OR UPDATE ON history_import_sources FOR EACH ROW EXECUTE FUNCTION public.advance_history_import_source_revision();
 CREATE TRIGGER mapping_revision BEFORE INSERT OR UPDATE ON history_import_user_mappings FOR EACH ROW EXECUTE FUNCTION public.advance_history_import_mapping_revision()`)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := secret.New([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(pool, cipher)
}
func editorSource(t *testing.T, repo *Repository) *Source {
	t.Helper()
	source, err := repo.CreateSource(t.Context(), CreateSourceInput{Name: "Source", SourceType: SourceTypeEmby, BaseURL: "https://example.invalid", Enabled: true, AdminToken: "stored-credential"})
	if err != nil {
		t.Fatal(err)
	}
	return source
}
func concurrentEditorWrites(t *testing.T, write func(int) error, want int) {
	t.Helper()
	var wg sync.WaitGroup
	start := make(chan struct{})
	var won atomic.Int32
	for i := range 12 {
		wg.Go(func() {
			<-start
			err := write(i)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrStaleRevision) {
				t.Errorf("write: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if int(won.Load()) != want {
		t.Fatalf("successful writes %d, want %d", won.Load(), want)
	}
}
func TestSourceEditorDatabase(t *testing.T) {
	repo := editorRepository(t)
	ctx := t.Context()
	source := editorSource(t, repo)
	current, token, err := repo.GetSourceWithAdminToken(ctx, source.ID)
	if err != nil || source.Revision != current.Revision || source.Revision == 0 || token != "stored-credential" {
		t.Fatalf("created source final revision: %v", err)
	}
	if _, err = repo.UpdateSourceConditional(ctx, source.ID, UpdateSourceInput{BaseURL: new("https://other.invalid")}, source.Revision); !errors.Is(err, ErrCredentialScopeChanged) {
		t.Fatalf("retarget without token: %v", err)
	}
	if _, err = repo.UpdateSource(ctx, source.ID, UpdateSourceInput{SystemID: new("other")}); !errors.Is(err, ErrCredentialScopeChanged) {
		t.Fatalf("legacy retarget bypass: %v", err)
	}
	renamed, err := repo.UpdateSource(ctx, source.ID, UpdateSourceInput{Name: new("Renamed")})
	if err != nil || renamed.Revision <= source.Revision {
		t.Fatalf("legacy revision: %v", err)
	}
	if _, err = repo.ClearSourceAdminTokenConditional(ctx, source.ID, source.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale clear: %v", err)
	}
	replaced, err := repo.UpdateSourceConditional(ctx, source.ID, UpdateSourceInput{BaseURL: new("https://other.invalid"), AdminToken: new("replacement")}, renamed.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, token, err = repo.GetSourceWithAdminToken(ctx, source.ID)
	if err != nil || token != "replacement" {
		t.Fatalf("replacement credential: %v", err)
	}
	var stored string
	if err = repo.pool.QueryRow(ctx, `SELECT admin_token FROM history_import_sources WHERE id=$1`, source.ID).Scan(&stored); err != nil || stored == token {
		t.Fatalf("credential encryption: %v", err)
	}
	cleared, err := repo.UpdateSourceConditional(ctx, source.ID, UpdateSourceInput{SystemID: new("new-server"), AdminToken: new("")}, replaced.Revision)
	if err != nil || cleared.HasAdminToken {
		t.Fatalf("explicit clear: %v", err)
	}
	concurrentEditorWrites(t, func(i int) error {
		_, err := repo.UpdateSourceConditional(ctx, source.ID, UpdateSourceInput{Name: new(fmt.Sprintf("race-%d", i))}, cleared.Revision)
		return err
	}, 1)
	concurrentEditorWrites(t, func(i int) error {
		_, err := repo.SetSourceAdminTokenConditional(ctx, source.ID, fmt.Sprintf("token-%d", i), -1)
		return err
	}, 12)
	current, err = repo.GetSourceByID(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE history_import_sources SET sort_order=sort_order+1 WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteSourceConditional(ctx, source.ID, current.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("background invalidates delete: %v", err)
	}
	current, err = repo.GetSourceByID(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteSourceConditional(ctx, source.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `INSERT INTO history_import_sources(id,name,source_type,base_url) VALUES($1,'Recreated','emby','https://example.invalid')`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateSourceConditional(ctx, source.ID, UpdateSourceInput{Name: new("stale")}, current.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("recreated generation: %v", err)
	}
	if err = repo.DeleteSourceConditional(ctx, source.ID, -1); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteSourceConditional(ctx, source.ID, -1); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("missing source: %v", err)
	}
}
func TestMappingEditorDatabase(t *testing.T) {
	repo := editorRepository(t)
	ctx := t.Context()
	source := editorSource(t, repo)
	input := CreateMappingInput{SourceID: source.ID, ExternalUserID: "external", ExternalUserName: "External", SiloUserID: 1, SiloProfileID: "p1"}
	mapping, err := repo.CreateMapping(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.CreateMapping(ctx, input); !errors.Is(err, ErrMappingDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	bad := input
	bad.ExternalUserID = "bad"
	bad.SiloProfileID = "foreign"
	if _, err = repo.CreateMapping(ctx, bad); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("bad pair: %v", err)
	}
	bad = input
	bad.SourceID = -1
	if _, err = repo.CreateMapping(ctx, bad); !errors.Is(err, ErrSourceNotFound) {
		t.Fatalf("bad source: %v", err)
	}
	if _, err = repo.UpdateMappingConditional(ctx, mapping.ID, UpdateMappingInput{SiloUserID: new(2)}, mapping.Revision); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("merged user-only pair: %v", err)
	}
	if _, err = repo.UpdateMapping(ctx, mapping.ID, UpdateMappingInput{SiloProfileID: new("foreign")}); !errors.Is(err, ErrProfileNotFound) {
		t.Fatalf("legacy profile-only pair: %v", err)
	}
	if err = repo.TouchMappingLastImported(ctx, mapping.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `UPDATE user_profiles SET name='New label' WHERE user_id=1 AND id='p1'`); err != nil {
		t.Fatal(err)
	}
	touched, err := repo.GetMappingByID(ctx, mapping.ID)
	if err != nil || touched.Revision != mapping.Revision || touched.LastImportedAt == nil || touched.SiloProfileName != "New label" {
		t.Fatalf("bookkeeping changed editor: %v", err)
	}
	concurrentEditorWrites(t, func(int) error {
		_, err := repo.UpdateMappingConditional(ctx, mapping.ID, UpdateMappingInput{SiloProfileID: new("p2")}, mapping.Revision)
		return err
	}, 1)
	current, err := repo.GetMappingByID(ctx, mapping.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteSourceConditional(ctx, source.ID, source.Revision); !errors.Is(err, ErrSourceInUse) {
		t.Fatalf("mapped source delete: %v", err)
	}
	if err = repo.DeleteMappingConditional(ctx, mapping.ID, mapping.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("stale mapping delete: %v", err)
	}
	concurrentEditorWrites(t, func(int) error {
		_, err := repo.UpdateMappingConditional(ctx, mapping.ID, UpdateMappingInput{SiloUserID: new(2), SiloProfileID: new("foreign")}, -1)
		return err
	}, 12)
	if err = repo.DeleteMappingConditional(ctx, mapping.ID, -1); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.pool.Exec(ctx, `INSERT INTO history_import_user_mappings(id,source_id,external_user_id,silo_user_id,silo_profile_id) VALUES($1,$2,'external',1,'p1')`, mapping.ID, source.ID); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteMappingConditional(ctx, mapping.ID, current.Revision); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("recreated mapping: %v", err)
	}
	if err = repo.DeleteMappingConditional(ctx, mapping.ID, -1); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteMappingConditional(ctx, mapping.ID, -1); !errors.Is(err, ErrMappingNotFound) {
		t.Fatalf("missing mapping: %v", err)
	}
}
