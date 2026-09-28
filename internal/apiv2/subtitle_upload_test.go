package apiv2

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	catalogpkg "github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/subtitles"
)

type fakeSubtitleUploads struct {
	calls   int
	access  catalogpkg.AccessFilter
	request subtitles.UploadRequest
}

func (f *fakeSubtitleUploads) UploadStoredSubtitle(_ context.Context, access catalogpkg.AccessFilter, req subtitles.UploadRequest) (*subtitles.DownloadedSubtitle, error) {
	f.calls++
	f.access = access
	f.request = req
	return &subtitles.DownloadedSubtitle{ID: 7, MediaFileID: req.MediaFileID, Provider: subtitles.ProviderUpload, Language: req.Language, Format: subtitles.FormatSRT, CreatedAt: fixedTime(), S3Key: "PRIVATE_OBJECT", DownloadedBy: new(99)}, nil
}
func subtitleMultipart(t *testing.T, h http.Handler, path, filename string, data []byte, fields map[string]string, authenticated bool) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := form.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, Prefix+path, &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	if authenticated {
		for key, value := range viewerHeaders() {
			req.Header.Set(key, value)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
func TestSubtitleUploadV2MultipartAndLimits(t *testing.T) {
	deps, _ := catalogDeps(t)
	f := new(fakeSubtitleUploads)
	deps.SubtitleUploads = f
	h := newTestHandler(t, deps)
	fields := map[string]string{"media_file_id": "42", "language": "fr", "language_override": "true", "hearing_impaired": "true", "release_name": "Synthetic"}
	rec := subtitleMultipart(t, h, "/subtitles/upload", "synthetic.en.srt", []byte("synthetic bytes"), fields, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"id":"7"`) || strings.Contains(rec.Body.String(), "PRIVATE") || strings.Contains(rec.Body.String(), "downloaded_by") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if f.calls != 1 || f.access.UserID != 1 || f.access.ProfileID != "p-owner" || f.request.UserID != nil || !f.request.PreferUserLanguage || !f.request.HearingImpaired || string(f.request.Data) != "synthetic bytes" || f.request.Filename != "synthetic.en.srt" {
		t.Fatalf("multipart lost authority/data: %+v", f)
	}
	requireProblem(t, subtitleMultipart(t, h, "/subtitles/upload", "synthetic.en.srt", []byte("x"), fields, false), TypeAuthenticationRequired)
	requireProblem(t, subtitleMultipart(t, h, "/subtitles/upload", "synthetic.en.srt", bytes.Repeat([]byte("x"), subtitles.MaxUploadSize+1), fields, true), TypePayloadTooLarge)
	fields["media_file_id"] = "0"
	requireProblem(t, subtitleMultipart(t, h, "/subtitles/upload", "synthetic.en.srt", []byte("x"), fields, true), TypeValidationFailed)
	if f.calls != 1 {
		t.Fatal("rejected multipart reached upload service")
	}
}
func TestSubtitleDetectionV2DoesNotRequireStorage(t *testing.T) {
	deps, _ := catalogDeps(t)
	h := newTestHandler(t, deps)
	for _, tc := range []struct{ file, language, want, source string }{{"synthetic.en.srt", "fr", "en", "filename"}, {"synthetic.srt", "fr", "fr", "manual"}} {
		rec := subtitleMultipart(t, h, "/subtitles/detect-language", tc.file, []byte("123"), map[string]string{"language": tc.language}, true)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"language":"`+tc.want+`"`) || !strings.Contains(rec.Body.String(), `"source":"`+tc.source+`"`) {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	requireProblem(t, subtitleMultipart(t, h, "/subtitles/detect-language", "synthetic.exe", []byte("123"), nil, true), TypeMalformedRequest)
}
