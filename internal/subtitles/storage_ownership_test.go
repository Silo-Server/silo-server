package subtitles

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func subtitleStorageDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL must name a disposable PostgreSQL test database")
	}
	control, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "subtitle_storage_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ident := pgx.Identifier{schema}.Sanitize()
	if _, err := control.Exec(t.Context(), "CREATE SCHEMA "+ident); err != nil {
		control.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := control.Exec(ctx, "DROP SCHEMA "+ident+" CASCADE"); err != nil {
			t.Error(err)
		}
		control.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err = pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `CREATE TABLE downloaded_subtitles (
 id BIGSERIAL PRIMARY KEY,media_file_id BIGINT NOT NULL,provider TEXT NOT NULL,language TEXT NOT NULL,
 format TEXT NOT NULL,release_name TEXT NOT NULL,s3_key TEXT NOT NULL UNIQUE,score DOUBLE PRECISION NOT NULL,
 hearing_impaired BOOLEAN NOT NULL,downloaded_by BIGINT,created_at TIMESTAMPTZ NOT NULL DEFAULT now())`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../migrations/sql/20260906031359_subtitle_content_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, ok := strings.Cut(string(migration), "-- +goose Down")
	if !ok {
		t.Fatal("missing migration boundary")
	}
	if _, err := pool.Exec(t.Context(), up); err != nil {
		t.Fatal(err)
	}
	return pool
}

type gatedSubtitleObjectStore struct {
	BlobStore
	putReady      chan struct{}
	putRelease    chan struct{}
	deleteReady   chan struct{}
	deleteRelease chan struct{}
}

func (s *gatedSubtitleObjectStore) Put(ctx context.Context, key string, data []byte) error {
	if err := s.BlobStore.Put(ctx, key, data); err != nil {
		return err
	}
	if s.putReady != nil {
		s.putReady <- struct{}{}
		select {
		case <-s.putRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
func (s *gatedSubtitleObjectStore) Delete(ctx context.Context, key string) error {
	if s.deleteReady != nil {
		s.deleteReady <- struct{}{}
		select {
		case <-s.deleteRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.BlobStore.Delete(ctx, key)
}

type lostSubtitleInsertReply struct{ Repository }

func (r lostSubtitleInsertReply) InsertDownloadedSubtitle(ctx context.Context, sub *DownloadedSubtitle) error {
	if err := r.Repository.InsertDownloadedSubtitle(ctx, sub); err != nil {
		return err
	}
	return errors.New("synthetic lost commit reply")
}

func TestSubtitleStoragePostgresOwnership(t *testing.T) {
	pool := subtitleStorageDatabase(t)
	repo := NewPgRepository(pool, nil)
	objects := newMockBlobStore()
	request := StoreSubtitleRequest{MediaFileID: 42, Provider: ProviderUpload, Language: "en", Format: FormatSRT, Data: []byte("synthetic content")}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	gate := &gatedSubtitleObjectStore{BlobStore: objects, putReady: make(chan struct{}, 2), putRelease: make(chan struct{})}
	type result struct {
		sub *DownloadedSubtitle
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		manager := NewManager(repo, gate)
		go func() { sub, err := manager.StoreSubtitle(ctx, request); results <- result{sub, err} }()
	}
	for range 2 {
		select {
		case <-gate.putReady:
		case <-ctx.Done():
			t.Fatal("concurrent uploads did not reach barrier")
		}
	}
	close(gate.putRelease)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.sub.ID != second.sub.ID {
		t.Fatalf("duplicate publication: %+v %+v", first, second)
	}
	winner := first.sub
	manager := NewManager(repo, objects)
	_, data, err := manager.GetSubtitleContent(ctx, winner.ID)
	if err != nil || !bytes.Equal(data, request.Data) {
		t.Fatalf("loser removed winner bytes: %v %q", err, data)
	}
	if objects.puts != 2 || objects.deletes != 1 {
		t.Fatalf("candidate cleanup: puts=%d deletes=%d", objects.puts, objects.deletes)
	}

	// A later publication of the same content must survive the old row's
	// delayed object deletion, even when handled by a separate manager.
	deletionGate := &gatedSubtitleObjectStore{BlobStore: objects, deleteReady: make(chan struct{}, 1), deleteRelease: make(chan struct{})}
	deleted := make(chan error, 1)
	go func() { deleted <- NewManager(repo, deletionGate).DeleteSubtitle(ctx, winner.ID) }()
	select {
	case <-deletionGate.deleteReady:
	case <-ctx.Done():
		t.Fatal("delete did not reach barrier")
	}
	replacement, err := manager.StoreSubtitle(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == winner.ID || replacement.S3Key == winner.S3Key {
		t.Fatal("physical object identity reused")
	}
	close(deletionGate.deleteRelease)
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
	_, data, err = manager.GetSubtitleContent(ctx, replacement.ID)
	if err != nil || !bytes.Equal(data, request.Data) {
		t.Fatalf("old deletion removed replacement: %v", err)
	}

	// A bridge metadata write invalidates the v2 revision while retaining the
	// same object. Unspecified fields are merged by the database, not a stale read.
	changed, err := manager.UpdateDownloadedSubtitle(ctx, replacement.ID, SubtitleMetadataPatch{ReleaseName: new("edited")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.UpdateDownloadedSubtitleWithRevision(ctx, replacement.ID, SubtitleMetadataPatch{Language: new("fr")}, new(replacement.Revision))
	if conflict, ok := errors.AsType[*SubtitleRevisionConflict](err); !ok || conflict.Current.Revision != changed.Revision {
		t.Fatalf("stale guard: %v", err)
	}
	changed, err = manager.UpdateDownloadedSubtitle(ctx, replacement.ID, SubtitleMetadataPatch{Language: new("fr")})
	if err != nil {
		t.Fatal(err)
	}
	if changed.S3Key != replacement.S3Key || changed.ReleaseName != "edited" {
		t.Fatal("metadata merge moved content or lost another field")
	}
	// Direct SQL writers cannot preserve a captured revision accidentally.
	if _, err := pool.Exec(ctx, "UPDATE downloaded_subtitles SET hearing_impaired=true WHERE id=$1", changed.ID); err != nil {
		t.Fatal(err)
	}
	_, err = manager.UpdateDownloadedSubtitleWithRevision(ctx, changed.ID, SubtitleMetadataPatch{ReleaseName: new("stale")}, new(changed.Revision))
	if conflict, ok := errors.AsType[*SubtitleRevisionConflict](err); !ok || conflict.Current.Revision != changed.Revision+1 || !conflict.Current.HearingImpaired {
		t.Fatalf("direct SQL did not invalidate revision: %v", err)
	}
	french := request
	french.Language = "fr"
	duplicate, err := manager.StoreSubtitle(ctx, french)
	if err != nil || duplicate.ID != changed.ID {
		t.Fatalf("edited identity not deduplicated: %v", err)
	}

	// Treat an insert error as uncertain: its transaction may have committed.
	uncertainRequest := request
	uncertainRequest.MediaFileID = 43
	uncertainManager := NewManager(lostSubtitleInsertReply{repo}, objects)
	if _, err := uncertainManager.StoreSubtitle(ctx, uncertainRequest); err == nil {
		t.Fatal("missing synthetic lost reply")
	}
	recovered, err := manager.StoreSubtitle(ctx, uncertainRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, data, err = manager.GetSubtitleContent(ctx, recovered.ID)
	if err != nil || !bytes.Equal(data, uncertainRequest.Data) {
		t.Fatalf("uncertain commit destroyed published bytes: %v", err)
	}
}

func TestSubtitleStorageLegacyHashCollision(t *testing.T) {
	repo := NewPgRepository(subtitleStorageDatabase(t), nil)
	objects := newMockBlobStore()
	a, b := []byte("synthetic-subtitle-66155"), []byte("synthetic-subtitle-73709")
	legacyKey := buildSubtitleS3Key(42, "en", ProviderUpload, FormatSRT, a)
	if legacyKey != buildSubtitleS3Key(42, "en", ProviderUpload, FormatSRT, b) || bytes.Equal(a, b) {
		t.Fatal("invalid collision fixture")
	}
	legacy := &DownloadedSubtitle{MediaFileID: 42, Provider: ProviderUpload, Language: "en", Format: FormatSRT, S3Key: legacyKey}
	if err := repo.InsertDownloadedSubtitle(t.Context(), legacy); err != nil {
		t.Fatal(err)
	}
	if err := objects.Put(t.Context(), legacy.S3Key, a); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(repo, objects)
	exact, err := manager.StoreSubtitle(t.Context(), StoreSubtitleRequest{MediaFileID: 42, Provider: ProviderUpload, Language: "en", Format: FormatSRT, Data: a})
	if err != nil || exact.ID != legacy.ID {
		t.Fatalf("legacy identical content not reused: %v", err)
	}
	different, err := manager.StoreSubtitle(t.Context(), StoreSubtitleRequest{MediaFileID: 42, Provider: ProviderUpload, Language: "en", Format: FormatSRT, Data: b})
	if err != nil || different.ID == legacy.ID {
		t.Fatalf("truncated hash treated as identity: %v", err)
	}
	_, data, err := manager.GetSubtitleContent(t.Context(), legacy.ID)
	if err != nil || !bytes.Equal(data, a) {
		t.Fatalf("legacy bytes overwritten: %v", err)
	}
}
