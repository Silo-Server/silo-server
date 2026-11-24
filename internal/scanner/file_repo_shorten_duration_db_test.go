package scanner

import (
	"context"
	"testing"
	"time"
)

// A transcode's stream end lowers a stored duration only for the file it
// read: one probed again after the run started may be a replacement, even
// with the same stored duration, and keeps its duration.
func TestShortenDurationOnlyForTheFileTheRunReadDB(t *testing.T) {
	pool := newScannerTestPool(t)
	ctx := context.Background()
	folderID := seedDeadRootTestFolder(t, pool, "movies", "Shorten duration")
	probedAt := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)

	seed := func(name string, probed *time.Time, audioTracks string) int {
		t.Helper()
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO media_files (media_folder_id, file_path, file_size, duration, probe_updated_at, audio_tracks)
			VALUES ($1, '/media/shorten/' || $2 || '.mkv', 1024, 154, $3, $4::jsonb)
			RETURNING id`, folderID, name, probed, audioTracks).Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return id
	}
	oneTrack := `[{"index": 1}]`
	repo := NewFileRepository(pool)
	for _, tt := range []struct {
		name        string
		probed      *time.Time
		audioTracks string
		from        int
		readSince   time.Time
		want        int
	}{
		{"read after the probe", &probedAt, oneTrack, 154, probedAt.Add(time.Minute), 150},
		{"probed again after the run started", &probedAt, oneTrack, 154, probedAt.Add(-time.Minute), 154},
		{"never probed", nil, oneTrack, 154, probedAt.Add(time.Minute), 154},
		{"planned with another duration", &probedAt, oneTrack, 160, probedAt.Add(time.Minute), 154},
		{"two audio tracks", &probedAt, `[{"index": 1}, {"index": 2}]`, 154, probedAt.Add(time.Minute), 154},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id := seed(tt.name, tt.probed, tt.audioTracks)
			shortened, err := repo.ShortenDuration(ctx, id, tt.from, 150, tt.readSince)
			if err != nil {
				t.Fatalf("ShortenDuration: %v", err)
			}
			var got int
			if err := pool.QueryRow(ctx, `SELECT duration FROM media_files WHERE id = $1`, id).Scan(&got); err != nil {
				t.Fatalf("read duration: %v", err)
			}
			if got != tt.want || shortened != (tt.want != 154) {
				t.Fatalf("duration %d (shortened %v), want %d", got, shortened, tt.want)
			}
		})
	}
}
