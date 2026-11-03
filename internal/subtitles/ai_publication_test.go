package subtitles

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func aiPublicationDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := subtitleStorageDatabase(t)
	_, err := pool.Exec(t.Context(), `CREATE TABLE subtitle_ai_jobs (
 id BIGINT PRIMARY KEY,media_file_id BIGINT NOT NULL,requested_by BIGINT,status TEXT NOT NULL,
 progress DOUBLE PRECISION NOT NULL DEFAULT 0,result_subtitle_id BIGINT REFERENCES downloaded_subtitles(id),
 error_message TEXT NOT NULL DEFAULT '',updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now());
 INSERT INTO subtitle_ai_jobs(id,media_file_id,requested_by,status) VALUES(1,42,7,'running'),(2,42,7,'running')`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
func aiPublicationRequest() StoreSubtitleRequest {
	return StoreSubtitleRequest{Publication: &AIJobPublication{JobID: 1, Complete: true}, MediaFileID: 42, UserID: new(7), Provider: "ai-translated", Language: "en", Format: FormatSRT, Data: []byte("synthetic AI subtitle")}
}
func assertAIPublicationState(t *testing.T, pool *pgxpool.Pool, status string, count int) {
	t.Helper()
	var got string
	var tracks int
	if err := pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM downloaded_subtitles) FROM subtitle_ai_jobs WHERE id=1`).Scan(&got, &tracks); err != nil {
		t.Fatal(err)
	}
	if got != status || tracks != count {
		t.Fatalf("state=(%s,%d), want=(%s,%d)", got, tracks, status, count)
	}
}

type lostAIPublicationReply struct{ *PgRepository }

func (r lostAIPublicationReply) PublishAISubtitle(ctx context.Context, sub *DownloadedSubtitle, fence AIJobPublication, legacy *DownloadedSubtitle) (*DownloadedSubtitle, error) {
	if _, err := r.PgRepository.PublishAISubtitle(ctx, sub, fence, legacy); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: synthetic lost commit reply", ErrAIPublicationUncertain)
}
func TestAIPublicationPostgresLostReplyRetainsBytes(t *testing.T) {
	pool := aiPublicationDatabase(t)
	objects := newMockBlobStore()
	manager := NewManager(lostAIPublicationReply{NewPgRepository(pool, nil)}, objects)
	if _, err := manager.StoreSubtitle(t.Context(), aiPublicationRequest()); !errors.Is(err, ErrAIPublicationUncertain) {
		t.Fatalf("lost reply did not preserve uncertainty: %v", err)
	}
	assertAIPublicationState(t, pool, "completed", 1)
	var id int
	if err := pool.QueryRow(t.Context(), `SELECT result_subtitle_id FROM subtitle_ai_jobs WHERE id=1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.GetSubtitleContent(t.Context(), id); err != nil {
		t.Fatal("lost reply removed committed bytes", err)
	}
	if objects.deletes != 0 {
		t.Fatal("uncertain candidate deleted")
	}
}

func TestAIPublicationRequiresAtomicRepository(t *testing.T) {
	repo := newMockSubtitleRepo()
	objects := newMockBlobStore()
	manager := NewManager(repo, objects)
	if _, err := manager.StoreSubtitle(t.Context(), aiPublicationRequest()); err == nil {
		t.Fatal("missing atomic repository silently accepted publication")
	}
	if repo.inserts != 0 || objects.puts != 0 {
		t.Fatal("unsupported publication performed storage effects")
	}
}
