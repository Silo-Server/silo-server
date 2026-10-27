package handlers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type downloadRecordingProvider struct {
	calls int
	err   error
}

func (*downloadRecordingProvider) Name() string { return "example" }
func (*downloadRecordingProvider) Search(context.Context, subtitles.SearchRequest) ([]subtitles.SubtitleResult, error) {
	return nil, nil
}
func (p *downloadRecordingProvider) Download(context.Context, string) ([]byte, subtitles.SubtitleFormat, error) {
	p.calls++
	return []byte("synthetic subtitle"), subtitles.FormatSRT, p.err
}
func TestSubtitleUploadServiceAuthorizationAndAttribution(t *testing.T) {
	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	h := NewSubtitleSearchHandler(manager, repo, nil)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{err: catalog.ErrItemNotFound}}
	request := subtitles.UploadRequest{MediaFileID: 42, UserID: new(999), Filename: "synthetic.en.srt", Language: "fr", PreferUserLanguage: true, Data: []byte("1\n00:00:01,000 --> 00:00:02,000\nSynthetic\n")}
	_, err := h.UploadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, request)
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Status != 404 {
		t.Fatalf("authorization: %v", err)
	}
	h.FileAuthorizer.ItemAccess = stubItemAccessChecker{}
	row, err := h.UploadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, request)
	if err != nil || row.DownloadedBy == nil || *row.DownloadedBy != 1 || row.Language != "fr" {
		t.Fatalf("upload result: %+v %v", row, err)
	}
	for _, invalid := range []struct {
		name, filename, data, message string
	}{
		{name: "empty", filename: "synthetic.en.srt"},
		{name: "unsupported extension", filename: "notes.txt", data: "hello"},
		{name: "no cues", filename: "notes.srt", data: "Notes for the movie night.\n", message: unreadableSubtitleMessage},
	} {
		bad := request
		bad.Filename, bad.Data = invalid.filename, []byte(invalid.data)
		_, err = h.UploadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, bad)
		failure, ok := errors.AsType[*APIError](err)
		if !ok || failure.Status != 400 || failure.Code != "bad_request" || (invalid.message != "" && failure.Message != invalid.message) {
			t.Fatalf("%s upload: %v", invalid.name, err)
		}
	}
}

// DownloadStoredSubtitle authorizes the file before contacting the provider,
// attributes the row to the authenticated user rather than the request, and
// maps provider failures to client errors where the client is at fault.
func TestSubtitleDownloadServiceOutcomes(t *testing.T) {
	cases := []struct {
		name        string
		denied      bool
		provider    string
		providerErr error
		wantStatus  int
		wantCode    string
		wantField   string
		wantCause   error
		wantCalls   int
	}{
		{name: "hidden file never reaches the provider", denied: true, provider: "example", wantStatus: 404},
		{name: "attributed to the caller", provider: "example", wantCalls: 1},
		{name: "unknown provider", provider: "not-registered", wantStatus: 404, wantCode: "provider_not_found", wantCause: subtitles.ErrUnknownProvider},
		{name: "malformed result id", provider: "example", providerErr: subtitles.ErrInvalidSubtitleID, wantStatus: 400, wantCode: "bad_request", wantField: "subtitle_id", wantCause: subtitles.ErrInvalidSubtitleID, wantCalls: 1},
		{name: "upstream failure stays opaque", provider: "example", providerErr: errors.New("PRIVATE_UPSTREAM"), wantStatus: 500, wantCode: "download_error", wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newMockSubtitleRepoForHandler()
			manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
			provider := &downloadRecordingProvider{err: tc.providerErr}
			manager.RegisterProvider(provider)
			h := NewSubtitleSearchHandler(manager, repo, nil)
			access := stubItemAccessChecker{}
			if tc.denied {
				access.err = catalog.ErrItemNotFound
			}
			h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: access}

			row, err := h.DownloadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1},
				subtitles.DownloadRequest{MediaFileID: 42, ProviderName: tc.provider, SubtitleID: "result", Language: "en", UserID: new(999)})
			if provider.calls != tc.wantCalls {
				t.Fatalf("provider calls = %d, want %d", provider.calls, tc.wantCalls)
			}
			if tc.wantStatus == 0 {
				if err != nil || row.DownloadedBy == nil || *row.DownloadedBy != 1 {
					t.Fatalf("download = %+v, %v; want a row attributed to user 1", row, err)
				}
				return
			}
			failure, ok := errors.AsType[*APIError](err)
			if !ok || failure.Status != tc.wantStatus || (tc.wantCode != "" && failure.Code != tc.wantCode) || failure.Field != tc.wantField {
				t.Fatalf("err = %v, want %d %s field %q", err, tc.wantStatus, tc.wantCode, tc.wantField)
			}
			if strings.Contains(failure.Message, "PRIVATE") {
				t.Fatalf("upstream detail leaked: %q", failure.Message)
			}
			if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
				t.Fatalf("err %v lost its cause %v", err, tc.wantCause)
			}
		})
	}
}

// A client can resend a SubDL result ID issued while IDs still carried the
// signed api_key query; neither that query nor a credential quoted in the
// provider's error reaches the failure log.
func TestSubtitleDownloadFailureLogOmitsCredentials(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	repo := newMockSubtitleRepoForHandler()
	manager := subtitles.NewManager(repo, newMockBlobStoreForHandler())
	manager.RegisterProvider(&downloadRecordingProvider{err: errors.New("upstream said api_key=synthetic-error-key")})
	h := NewSubtitleSearchHandler(manager, repo, nil)
	h.FileAuthorizer = &MediaFileAuthorizer{FileResolver: stubMediaFileResolver{file: &models.MediaFile{ID: 42, ContentID: "movie"}}, ItemAccess: stubItemAccessChecker{}}

	_, err := h.DownloadStoredSubtitle(t.Context(), catalog.AccessFilter{UserID: 1}, subtitles.DownloadRequest{MediaFileID: 42, ProviderName: "example", SubtitleID: "/subtitle/1-2.zip?api_key=synthetic-id-key", Language: "en"})
	if failure, ok := errors.AsType[*APIError](err); !ok || failure.Code != "download_error" {
		t.Fatalf("service error = %v", err)
	}
	got := logs.String()
	if !strings.Contains(got, "subtitle_id=/subtitle/1-2.zip ") || strings.Contains(got, "synthetic-id-key") || strings.Contains(got, "synthetic-error-key") {
		t.Fatalf("failure log = %q", got)
	}
}
