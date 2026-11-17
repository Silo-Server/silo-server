package extrastills

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"slices"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/mediaartifact"
	"github.com/Silo-Server/silo-server/internal/models"
)

type fakePosterStore struct {
	pending []catalog.PendingFramePoster
	won     map[string]bool // content ID -> whether the save finds the slot as read
	saved   []string
	// cleanups counts ClearStaleFramePosters calls, which fail
	// with cleanupErr.
	cleanups   int
	cleanupErr error
}

func (s *fakePosterStore) ClearStaleFramePosters(context.Context) (int64, error) {
	s.cleanups++
	return 0, s.cleanupErr
}

func (s *fakePosterStore) ListPendingFramePosters(context.Context, mediaartifact.Key, int) ([]catalog.PendingFramePoster, error) {
	return s.pending, nil
}

func (s *fakePosterStore) SetFramePosterIfSource(_ context.Context, contentID, _, _, _, _ string) (bool, error) {
	if !s.won[contentID] {
		return false, nil
	}
	s.saved = append(s.saved, contentID)
	return true, nil
}

type fakeFiles struct{}

func (fakeFiles) GetByID(_ context.Context, id int) (*models.MediaFile, error) {
	return &models.MediaFile{ID: id}, nil
}

// fakeFrames returns a frame for the file IDs in ok and fails the rest.
type fakeFrames struct {
	ok    map[int]bool
	frame []byte
	tried []int
}

func (f *fakeFrames) ExtractStill(_ context.Context, file *models.MediaFile, _ float64) ([]byte, error) {
	f.tried = append(f.tried, file.ID)
	if f.ok[file.ID] {
		return f.frame, nil
	}
	return nil, errors.New("timed out")
}

// fakePosterCacher stores every poster but those of the content IDs in fail.
type fakePosterCacher struct{ fail map[string]bool }

func (c fakePosterCacher) CacheFramePoster(_ context.Context, _ []byte, contentID string) (string, string, error) {
	if c.fail[contentID] {
		return "", "", errors.New("encoder rejected the image")
	}
	return "local/movies/x/poster/original.webp", "thumb", nil
}

type fakeFailures struct{ failed []int }

func (f *fakeFailures) Upsert(context.Context, mediaartifact.Artifact) error { return nil }

func (f *fakeFailures) RecordFailure(_ context.Context, failure mediaartifact.Failure) error {
	f.failed = append(f.failed, failure.MediaFileID)
	return nil
}

// A poster that cannot be stored is recorded on its file, so the next run
// moves past the movie, and the pass goes on to the next one. A frame that
// loses its slot to another image is not counted as saved, but it shows the
// files are reachable. Failures that may pass, in a row, end the pass before
// it spends more time on a share that is offline.
func TestPosterPassStopsAfterFailuresInARow(t *testing.T) {
	var frame bytes.Buffer
	if err := jpeg.Encode(&frame, image.NewRGBA(image.Rect(0, 0, 64, 36)), nil); err != nil {
		t.Fatal(err)
	}
	store := &fakePosterStore{won: map[string]bool{"a": true}}
	for i, id := range []string{"s", "a", "b", "c", "d", "e", "f", "g"} {
		store.pending = append(store.pending, catalog.PendingFramePoster{ContentID: id, FileID: i + 1, Duration: 600})
	}
	// s cannot be stored, a saves, b fails, c loses its slot, then d, e and f
	// fail in a row.
	frames := &fakeFrames{ok: map[int]bool{1: true, 2: true, 4: true, 8: true}, frame: frame.Bytes()}
	failures := &fakeFailures{}
	cacher := fakePosterCacher{fail: map[string]bool{"s": true}}
	pass := &posterPass{items: store, files: fakeFiles{}, frames: frames, cacher: cacher, failures: failures}

	saved, err := pass.run(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if saved != 1 || !slices.Equal(store.saved, []string{"a"}) {
		t.Errorf("saved %d %v, want only a", saved, store.saved)
	}
	if want := []int{1, 2, 3, 4, 5, 6, 7}; !slices.Equal(frames.tried, want) {
		t.Errorf("tried files %v, want %v: the pass stops after three failures in a row", frames.tried, want)
	}
	if want := []int{1, 3, 5, 6, 7}; !slices.Equal(failures.failed, want) {
		t.Errorf("recorded failures %v, want %v", failures.failed, want)
	}
	if store.cleanups != 1 {
		t.Errorf("cleanups = %d, want one before listing", store.cleanups)
	}

	// A pass whose cleanup fails takes no new frames.
	store.cleanupErr, frames.tried = errors.New("connection lost"), nil
	if _, err := pass.run(context.Background(), 10); err == nil || len(frames.tried) != 0 {
		t.Errorf("after a failed cleanup: err %v, tried %v; want the error and no frames", err, frames.tried)
	}
}
