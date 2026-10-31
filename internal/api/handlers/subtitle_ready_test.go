package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type subtitleReadyCall struct {
	fileID, subtitleID int
	language, label    string
}

type recordingSubtitleReadyAnnouncer struct{ calls chan subtitleReadyCall }

func (a *recordingSubtitleReadyAnnouncer) SubtitleReady(_ context.Context, fileID, subtitleID int, language, label string) {
	a.calls <- subtitleReadyCall{fileID, subtitleID, language, label}
}

func (a *recordingSubtitleReadyAnnouncer) next(t *testing.T) subtitleReadyCall {
	t.Helper()
	select {
	case call := <-a.calls:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("no subtitle ready announcement")
		return subtitleReadyCall{}
	}
}

// Viewers already playing the file learn about a downloaded or uploaded
// subtitle at once; a refused upload announces nothing.
func TestAddedSubtitlesAreAnnouncedToPlayingSessions(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	manager.RegisterProvider(&adminSubtitleTestProvider{name: "os"})
	h := NewSubtitleSearchHandler(manager, repo, nil)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{}}
	announcer := &recordingSubtitleReadyAnnouncer{calls: make(chan subtitleReadyCall, 4)}
	h.SetSubtitleReadyAnnouncer(announcer)
	access := catalog.AccessFilter{UserID: 1}

	downloaded, err := h.DownloadStoredSubtitle(t.Context(), access, subtitles.DownloadRequest{MediaFileID: 42, ProviderName: "os", SubtitleID: "result", Language: "fr", ReleaseName: "Movie.FR"})
	if err != nil {
		t.Fatal(err)
	}
	if call := announcer.next(t); call != (subtitleReadyCall{42, downloaded.ID, "fr", "Movie.FR"}) {
		t.Fatalf("download announcement = %+v", call)
	}

	uploaded, err := h.UploadStoredSubtitle(t.Context(), access, subtitles.UploadRequest{MediaFileID: 42, Filename: "movie.srt", Language: "de", PreferUserLanguage: true, Data: []byte("1\n00:00:01,000 --> 00:00:02,000\nHallo\n")})
	if err != nil {
		t.Fatal(err)
	}
	if call := announcer.next(t); call != (subtitleReadyCall{42, uploaded.ID, "de", "movie.srt"}) {
		t.Fatalf("upload announcement = %+v", call)
	}

	if _, err := h.UploadStoredSubtitle(t.Context(), access, subtitles.UploadRequest{MediaFileID: 42, Filename: "movie.srt", Language: "de", PreferUserLanguage: true}); err == nil {
		t.Fatal("empty upload was accepted")
	}
	// The refused upload announced nothing: the next announcement is the
	// following accepted upload.
	next, err := h.UploadStoredSubtitle(t.Context(), access, subtitles.UploadRequest{MediaFileID: 42, Filename: "movie.it.srt", Language: "it", PreferUserLanguage: true, Data: []byte("1\n00:00:01,000 --> 00:00:02,000\nCiao\n")})
	if err != nil {
		t.Fatal(err)
	}
	if call := announcer.next(t); call.subtitleID != next.ID {
		t.Fatalf("announcement after refused upload = %+v, want subtitle %d", call, next.ID)
	}
}
