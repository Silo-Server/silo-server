package ai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"

	"github.com/jackc/pgx/v5/pgxpool"
)

func newReuseTestService(t *testing.T, repo *recordingRepo, file *models.MediaFile) *Service {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := Config{Configured: true, SpeechConfigured: true, TranslateEnabled: true, TranscribeEnabled: true, ChatModel: "chat", ASRModel: "asr"}
	return NewService(ctx, cfg, repo, nil, stubTranscriber{}, nil, nil, singleFileResolver{file: file}, nil, "", nil, nil)
}

// embeddedSource is the identity of reuseTestFile's embedded track.
const embeddedSource = "embedded:" + reuseTestHash + "-0-0:3"

const reuseTestHash = "8e245d9679d31e12"

// A file with one embedded English text track (container stream 3).
func reuseTestFile(sidecars ...string) *models.MediaFile {
	file := &models.MediaFile{ID: 1, FileHash: reuseTestHash, SubtitleTracks: []models.SubtitleTrack{{Index: 3, Language: "eng", Codec: "subrip"}}}
	for _, path := range sidecars {
		file.ExternalSubtitles = append(file.ExternalSubtitles, models.ExternalSubtitle{Path: path, Language: "eng", Format: "srt"})
	}
	return file
}

func TestEnqueueReusesFinishedTranslation(t *testing.T) {
	result := 31
	repo := &recordingRepo{reusable: &Job{
		ID: 7, MediaFileID: 1, Kind: JobKindTranslate, TargetLanguage: "de", Status: JobStatusCompleted,
		ResultSubtitleID: &result, IdempotencyKey: idempotencyKey(1, JobKindTranslate, embeddedSource, "de", "chat"),
	}}
	svc := newReuseTestService(t, repo, reuseTestFile())

	job, err := svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != 7 || job.Status != JobStatusCompleted || job.ResultSubtitleID == nil || *job.ResultSubtitleID != result {
		t.Fatalf("Enqueue = %+v, want the finished job 7 with subtitle %d", job, result)
	}
	if repo.inserts != 0 {
		t.Fatalf("inserted %d jobs; a finished translation must not call the provider again", repo.inserts)
	}

	// Another target language is a different translation.
	if _, err := svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "fr"}); err != nil {
		t.Fatal(err)
	}
	if repo.inserts != 1 {
		t.Fatalf("inserts = %d, want a new job for another language", repo.inserts)
	}
}

// A rescan that adds a sidecar shifts the embedded track to index 1; index 0
// now names the sidecar, whose translation is a different job.
func TestEnqueueDoesNotReuseAnotherTrackAfterTheIndexShifts(t *testing.T) {
	result := 31
	repo := &recordingRepo{reusable: &Job{
		ID: 7, MediaFileID: 1, Kind: JobKindTranslate, TargetLanguage: "de", Status: JobStatusCompleted,
		ResultSubtitleID: &result, IdempotencyKey: idempotencyKey(1, JobKindTranslate, embeddedSource, "de", "chat"),
	}}
	svc := newReuseTestService(t, repo, reuseTestFile("/media/movie.eng.srt"))

	job, err := svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == 7 || repo.inserts != 1 {
		t.Fatalf("Enqueue = job %d with %d inserts, want a new job for the sidecar", job.ID, repo.inserts)
	}
	if job, err = svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 1, TargetLanguage: "de"}); err != nil {
		t.Fatal(err)
	}
	if job.ID != 7 {
		t.Fatalf("Enqueue(embedded track at its new index) = job %d, want the finished job 7", job.ID)
	}
	// The response names the index the caller asked for, so the client can
	// match it to its request.
	if job.SourceIndex != 1 {
		t.Fatalf("reused job source_index = %d, want the requested index 1", job.SourceIndex)
	}
}

// Replacing the media file at the same path changes its content hash, so the
// embedded track is a new source.
func TestEnqueueDoesNotReuseAfterTheMediaFileIsReplaced(t *testing.T) {
	result := 31
	repo := &recordingRepo{reusable: &Job{
		ID: 7, MediaFileID: 1, Kind: JobKindTranslate, TargetLanguage: "de", Status: JobStatusCompleted,
		ResultSubtitleID: &result, IdempotencyKey: idempotencyKey(1, JobKindTranslate, embeddedSource, "de", "chat"),
	}}
	file := reuseTestFile()
	file.FileHash = "0000000000000001"
	svc := newReuseTestService(t, repo, file)

	job, err := svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == 7 || repo.inserts != 1 {
		t.Fatalf("Enqueue = job %d with %d inserts, want a new job for the replaced file", job.ID, repo.inserts)
	}
}

// A sidecar is named by its bytes: the same file at another index is reused,
// and editing it in place makes a new source.
func TestEnqueueNamesSidecarsByContent(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "movie.eng.srt")
	other := filepath.Join(dir, "movie.eng.forced.srt")
	if err := os.WriteFile(sidecar, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := &recordingRepo{}
	svc := newReuseTestService(t, repo, reuseTestFile(sidecar))
	source, stable := svc.translationSource(context.Background(), 1, 0)
	if !stable {
		t.Fatal("readable sidecar has no stable identity")
	}
	result := 31
	repo.reusable = &Job{
		ID: 7, MediaFileID: 1, Kind: JobKindTranslate, TargetLanguage: "de", Status: JobStatusCompleted,
		ResultSubtitleID: &result, IdempotencyKey: idempotencyKey(1, JobKindTranslate, source, "de", "chat"),
	}

	shifted := newReuseTestService(t, repo, reuseTestFile(other, sidecar))
	job, err := shifted.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 1, TargetLanguage: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != 7 || job.SourceIndex != 1 {
		t.Fatalf("Enqueue(same sidecar at index 1) = job %d index %d, want job 7 at index 1", job.ID, job.SourceIndex)
	}

	if err := os.WriteFile(sidecar, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello there\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if job, err = svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "de"}); err != nil {
		t.Fatal(err)
	}
	if job.ID == 7 || repo.inserts != 1 {
		t.Fatalf("Enqueue(edited sidecar) = job %d with %d inserts, want a new job", job.ID, repo.inserts)
	}
}

// A job queued under the index key before identity keys existed still
// deduplicates a request for the same index.
func TestEnqueueJoinsAnInFlightJobQueuedUnderTheIndexKey(t *testing.T) {
	repo := &recordingRepo{active: &Job{
		ID: 5, MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "de", Status: JobStatusRunning,
		IdempotencyKey: idempotencyKey(1, JobKindTranslate, "0", "de", "chat"),
	}}
	svc := newReuseTestService(t, repo, reuseTestFile())

	job, err := svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != 5 || repo.inserts != 0 {
		t.Fatalf("Enqueue = job %d with %d inserts, want the in-flight job 5", job.ID, repo.inserts)
	}
}

// A request that loses the insert race to an identical one, made before a
// rescan shifted the track's index, gets the winner under its own index.
func TestEnqueueReturnsARaceWinnerUnderTheRequestedIndex(t *testing.T) {
	repo := &recordingRepo{raceWin: &Job{
		ID: 5, MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "de", Status: JobStatusPending,
		IdempotencyKey: idempotencyKey(1, JobKindTranslate, embeddedSource, "de", "chat"),
	}}
	svc := newReuseTestService(t, repo, reuseTestFile("/media/movie.eng.srt"))

	job, err := svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 1, TargetLanguage: "de"})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != 5 || job.SourceIndex != 1 {
		t.Fatalf("Enqueue = job %d at source_index %d, want the winning job 5 at the requested index 1", job.ID, job.SourceIndex)
	}
}

// A rescan that adds a sidecar while a job waits moves another track to its
// index; the job must not translate that track under the original key.
func TestQueuedTranslationDetectsAChangedSource(t *testing.T) {
	queued := &Job{
		MediaFileID: 1, Kind: JobKindTranslate, SourceIndex: 0, TargetLanguage: "de", Model: "chat",
		IdempotencyKey: idempotencyKey(1, JobKindTranslate, embeddedSource, "de", "chat"),
	}
	if newReuseTestService(t, &recordingRepo{}, reuseTestFile()).sourceChanged(context.Background(), queued) {
		t.Fatal("unchanged source reported as changed")
	}
	sidecar := filepath.Join(t.TempDir(), "movie.eng.srt")
	if err := os.WriteFile(sidecar, []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rescanned := newReuseTestService(t, &recordingRepo{}, reuseTestFile(sidecar))
	if !rescanned.sourceChanged(context.Background(), queued) {
		t.Fatal("index 0 now names the sidecar; want a changed source")
	}
	legacy := *queued
	legacy.IdempotencyKey = idempotencyKey(1, JobKindTranslate, "0", "de", "chat")
	if rescanned.sourceChanged(context.Background(), &legacy) {
		t.Fatal("a job queued under the index key keeps its existing behavior")
	}
}

func TestEnqueueDoesNotReuseFinishedTranscription(t *testing.T) {
	result := 32
	repo := &recordingRepo{reusable: &Job{
		ID: 8, MediaFileID: 1, Kind: JobKindTranscribe, Status: JobStatusCompleted,
		ResultSubtitleID: &result, IdempotencyKey: idempotencyKey(1, JobKindTranscribe, "-1", "", "asr"),
	}}
	svc := newReuseTestService(t, repo, reuseTestFile())

	job, err := svc.Enqueue(context.Background(), JobRequest{MediaFileID: 1, Kind: JobKindTranscribe, SourceIndex: -1})
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == 8 || repo.inserts != 1 {
		t.Fatalf("Enqueue = job %d with %d inserts, want a new transcription job", job.ID, repo.inserts)
	}
}

// Index-named keys hash as they did when the source was an integer, so
// in-flight jobs keep deduplicating across the upgrade.
func TestIdempotencyKeyKeepsTheIndexHash(t *testing.T) {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%d|%s|%s", 1, JobKindTranscribe, -1, "", "asr")))
	if got, want := idempotencyKey(1, JobKindTranscribe, "-1", "", "asr"), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("idempotencyKey = %s, want %s", got, want)
	}
}

func TestReusableJobRequiresAStoredResultPostgres(t *testing.T) {
	dsn := os.Getenv("SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("SILO_SUBTITLE_STORAGE_TEST_DATABASE_URL must name a disposable PostgreSQL test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// Temporary tables shadow the real ones for this connection only.
	if _, err := pool.Exec(t.Context(), `
CREATE TEMP TABLE subtitle_ai_jobs (
	id bigint PRIMARY KEY, media_file_id int, kind text, source_index int, source_language text,
	target_language text, engine text, model text, status text, progress float8, progress_message text,
	result_subtitle_id int, error_message text, idempotency_key text, requested_by int,
	created_at timestamptz DEFAULT now(), updated_at timestamptz, heartbeat_at timestamptz DEFAULT now());
CREATE TEMP TABLE downloaded_subtitles (id int PRIMARY KEY, media_file_id int);
INSERT INTO downloaded_subtitles VALUES (10, 1), (11, 2);
INSERT INTO subtitle_ai_jobs (id, media_file_id, kind, source_index, source_language, target_language, engine, model,
	status, progress, progress_message, result_subtitle_id, error_message, idempotency_key, updated_at) VALUES
	(1, 1, 'translate', 0, '', 'de', 'openai', 'chat', 'completed', 1, '', 10, '', 'k', '2026-01-01'),
	(2, 1, 'translate', 0, '', 'de', 'openai', 'chat', 'completed', 1, '', 99, '', 'k', '2026-01-02'),
	(3, 1, 'translate', 0, '', 'de', 'openai', 'chat', 'failed', 0, 'x', NULL, 'x', 'k', '2026-01-03'),
	(4, 1, 'translate', 0, '', 'de', 'openai', 'chat', 'completed', 1, '', 11, '', 'other-file', '2026-01-04');`); err != nil {
		t.Fatal(err)
	}
	repo := NewPgJobRepository(pool)

	job, err := repo.GetReusableJobByIdempotencyKey(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	if job == nil || job.ID != 1 {
		t.Fatalf("reusable job = %+v, want job 1 (job 2's track was deleted, job 3 failed)", job)
	}
	if job, err := repo.GetReusableJobByIdempotencyKey(t.Context(), "other-file"); err != nil || job != nil {
		t.Fatalf("job whose track belongs to another file = %+v, %v; want none", job, err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM downloaded_subtitles WHERE id = 10`); err != nil {
		t.Fatal(err)
	}
	if job, err := repo.GetReusableJobByIdempotencyKey(t.Context(), "k"); err != nil || job != nil {
		t.Fatalf("after deleting the track: %+v, %v; want none so a fresh translation runs", job, err)
	}
}

// A remux can keep the size and the bytes the OpenSubtitles hash reads, so
// the modification time also names the embedded source.
func TestEmbeddedSourceChangesWithTheModificationTime(t *testing.T) {
	file := reuseTestFile()
	before, _ := newReuseTestService(t, &recordingRepo{}, file).translationSource(context.Background(), 1, 0)
	modified := time.Unix(1_800_000_000, 0)
	remuxed := reuseTestFile()
	remuxed.FileModifiedAt = &modified
	after, stable := newReuseTestService(t, &recordingRepo{}, remuxed).translationSource(context.Background(), 1, 0)
	if !stable || before == after {
		t.Fatalf("identity %q unchanged after the file was rewritten", after)
	}
}

// A stored subtitle is named by the bytes it is translated from, so a saved
// timing correction makes a new source.
func TestDownloadedSourceIncludesItsTimingCorrection(t *testing.T) {
	data := []byte("1\n00:00:01,000 --> 00:00:02,000\nHello\n")
	identity := func(offset int) string {
		svc := &Service{
			files:  singleFileResolver{file: &models.MediaFile{ID: 1}},
			lister: timedSourceLister{{ID: 5, MediaFileID: 1, Language: "en", Format: subtitles.FormatSRT}},
			store: &timedSourceStore{sub: subtitles.DownloadedSubtitle{ID: 5, MediaFileID: 1, Language: "en",
				Format: subtitles.FormatSRT, Timing: subtitles.Timing{OffsetMS: offset, Scale: 1}}, data: data},
		}
		source, stable := svc.translationSource(context.Background(), 1, 0)
		if !stable {
			t.Fatal("stored subtitle has no stable identity")
		}
		return source
	}
	if identity(0) == identity(1500) {
		t.Fatal("timing correction did not change the source identity")
	}
}
