package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/remotestream"
)

func TestVirtualPlaybackAlternativesAreIdentityProfileAndCountBounded(t *testing.T) {
	file := &models.MediaFile{
		FilePath: "virtual://movie/tt1234?profile=1080p",
	}
	streams := []VirtualPlaybackStream{
		{URI: "virtual://movie/tt9999?profile=1080p&result=wrong-title"},
		{URI: "virtual://movie/tt1234?profile=2160p&result=wrong-profile"},
	}
	for index := 0; index < maxVirtualPlaybackStreams+10; index++ {
		streams = append(streams, VirtualPlaybackStream{
			URI: "virtual://movie/tt1234?profile=1080p&result=" + strconv.Itoa(index),
		})
	}
	got := virtualPlaybackAlternatives(
		context.Background(),
		VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return streams, nil
		}),
		file, 1, "profile-1",
	)
	if len(got) != maxVirtualPlaybackStreams-2 {
		t.Fatalf("alternatives = %d, want %d", len(got), maxVirtualPlaybackStreams-2)
	}
	for _, stream := range got {
		if strings.Contains(stream.URI, "tt9999") || strings.Contains(stream.URI, "2160p") {
			t.Fatalf("cross-identity/profile candidate was retained: %q", stream.URI)
		}
	}
}

func TestVirtualTranscodeStartupRefreshesExactSessionSelection(t *testing.T) {
	file := &models.MediaFile{
		ID: 42, FilePath: "virtual://movie/tt1234?profile=1080p",
		VirtualOwnerInstallationID: 7,
	}
	sessionManager := playback.NewSessionManager(0, 0)
	session, err := sessionManager.StartSession(9, "profile-1", file.ID, playback.PlayTranscode, false)
	if err != nil {
		t.Fatal(err)
	}
	selectedURI := "virtual://movie/tt1234?profile=1080p&result=second"
	if err := sessionManager.SetVirtualSource(session.ID, selectedURI, 8); err != nil {
		t.Fatal(err)
	}
	handler := NewPlaybackHandler(sessionManager, testPlaybackFileResolver{file: file})
	handler.RemoteStreamRelay = remotestream.NewRelay()
	t.Cleanup(func() { _ = handler.RemoteStreamRelay.Close(context.Background()) })

	var mu sync.Mutex
	var resolved []string
	handler.VirtualMediaResolver = VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, user int, profile string) (string, error) {
		mu.Lock()
		resolved = append(resolved, uri)
		mu.Unlock()
		if uri == selectedURI {
			// Registration rejects the primary before FFmpeg startup.
			return "http://127.0.0.1/private", nil
		}
		return "", errors.New("unexpected selection")
	})
	handler.VirtualMediaRefreshResolver = VirtualMediaRefreshResolverFunc(func(_ context.Context, uri string, owner, user int, profile string) (string, error) {
		mu.Lock()
		resolved = append(resolved, uri)
		mu.Unlock()
		if uri != selectedURI || owner != 8 || user != 9 || profile != "profile-1" {
			t.Errorf("refresh routing = uri %q owner %d user %d profile %q", uri, owner, user, profile)
		}
		return "https://1.1.1.1/refreshed", nil
	})

	_, err = handler.startLocalPlaybackTransport(context.Background(), playback.TranscodeOpts{
		InputPath:   file.FilePath,
		MediaFileID: file.ID,
		SessionID:   session.ID,
		OutputDir:   t.TempDir(),
		FFmpegPath:  filepath.Join(t.TempDir(), "missing-ffmpeg"),
	})
	if err == nil {
		t.Fatal("startup succeeded with a missing FFmpeg binary")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resolved) != 2 || resolved[0] != selectedURI || resolved[1] != selectedURI {
		t.Fatalf("resolved attempts = %v", resolved)
	}
}

func TestResolveVirtualInputUsesRefreshResolverWhenForced(t *testing.T) {
	file := &models.MediaFile{
		ID: 42, FilePath: "virtual://movie/tt1234?profile=1080p",
		VirtualOwnerInstallationID: 7,
	}
	handler := &PlaybackHandler{RemoteStreamRelay: remotestream.NewRelay()}
	t.Cleanup(func() { _ = handler.RemoteStreamRelay.Close(context.Background()) })
	var normalCalls, refreshCalls int
	handler.VirtualMediaResolver = VirtualMediaResolverFunc(func(_ context.Context, uri string, owner, user int, profile string) (string, error) {
		normalCalls++
		return "https://1.1.1.1/normal", nil
	})
	handler.VirtualMediaRefreshResolver = VirtualMediaRefreshResolverFunc(func(_ context.Context, uri string, owner, user int, profile string) (string, error) {
		refreshCalls++
		if uri != file.FilePath || owner != 7 || user != 9 || profile != "profile-1" {
			t.Errorf("refresh routing = uri %q owner %d user %d profile %q", uri, owner, user, profile)
		}
		return "https://1.1.1.1/refreshed", nil
	})

	_, releaseNormal, err := handler.resolveVirtualInput(context.Background(), file, 9, "profile-1", false)
	if err != nil {
		t.Fatal(err)
	}
	releaseNormal()
	_, releaseRefreshed, err := handler.resolveVirtualInput(context.Background(), file, 9, "profile-1", true)
	if err != nil {
		t.Fatal(err)
	}
	releaseRefreshed()
	if normalCalls != 1 || refreshCalls != 1 {
		t.Fatalf("resolver calls = normal %d refresh %d", normalCalls, refreshCalls)
	}
}

func TestVirtualRemuxRetriesBeforeCommittingResponse(t *testing.T) {
	tempDir := t.TempDir()
	counterPath := filepath.Join(tempDir, "count")
	ffmpegPath := filepath.Join(tempDir, "fake-ffmpeg")
	script := "#!/bin/sh\n" +
		"count=0\n" +
		"if [ -f '" + counterPath + "' ]; then count=$(cat '" + counterPath + "'); fi\n" +
		"count=$((count + 1))\n" +
		"printf '%s' \"$count\" > '" + counterPath + "'\n" +
		"if [ \"$count\" -eq 1 ]; then exit 1; fi\n" +
		"printf 'remuxed-media'\n"
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	file := &models.MediaFile{
		ID: 42, FilePath: "virtual://movie/tt1234?profile=1080p",
		VirtualOwnerInstallationID: 7,
	}
	session := &playback.Session{
		UserID: 9, ProfileID: "profile-1", AudioTrackIndex: -1,
	}
	handler := &StreamHandler{
		VirtualMediaResolver: VirtualMediaResolverFunc(func(_ context.Context, uri string, _, _ int, _ string) (string, error) {
			return "https://1.1.1.1/" + uri[strings.LastIndex(uri, "=")+1:], nil
		}),
		VirtualStreamLister: VirtualPlaybackStreamListerFunc(func(context.Context, string, int, string, int) ([]VirtualPlaybackStream, error) {
			return []VirtualPlaybackStream{{
				URI:                 "virtual://movie/tt1234?profile=1080p&result=second",
				OwnerInstallationID: 8,
			}}, nil
		}),
		RemoteStreamRelay: remotestream.NewRelay(),
		PlaybackConfig: func() config.PlaybackConfig {
			return config.PlaybackConfig{FFmpegPath: ffmpegPath}
		},
	}
	t.Cleanup(func() { _ = handler.RemoteStreamRelay.Close(context.Background()) })

	request := httptest.NewRequest(http.MethodGet, "/stream", nil)
	recorder := httptest.NewRecorder()
	err := handler.serveVirtualRemux(
		recorder, request, file, session, "https://1.1.1.1/primary", 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || recorder.Body.String() != "remuxed-media" {
		t.Fatalf("response = %d %q", recorder.Code, recorder.Body.String())
	}
	count, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(count) != "2" {
		t.Fatalf("FFmpeg starts = %q, want 2", count)
	}
}
