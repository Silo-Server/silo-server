package subdl

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Silo-Server/silo-server/internal/subtitles"
)

func subtitleZip(t *testing.T, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, err := w.Create("synthetic.en.srt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// offHost stands in for any host other than the download host; a test fails
// if a download ever reaches it.
func offHost(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write(subtitleZip(t, "off host"))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestDownloadFetchesWellFormedIDFromDownloadHost(t *testing.T) {
	var gotPath, gotQuery string
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write(subtitleZip(t, "1\n00:00:01,000 --> 00:00:02,000\nhello\n"))
	}))
	defer dl.Close()

	p := New(Config{DLBaseURL: dl.URL})
	data, format, err := p.Download(t.Context(), "/subtitle/3197651-3213944.zip?api_key=synthetic")
	if err != nil {
		t.Fatal(err)
	}
	if format != subtitles.FormatSRT || !strings.Contains(string(data), "hello") {
		t.Fatalf("got %q (%s)", data, format)
	}
	if gotPath != "/subtitle/3197651-3213944.zip" || gotQuery != "api_key=synthetic" {
		t.Fatalf("requested %s?%s", gotPath, gotQuery)
	}
}

func TestDownloadRefusesIDsThatLeaveTheDownloadHost(t *testing.T) {
	var dlHits atomic.Int32
	dl := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dlHits.Add(1) }))
	defer dl.Close()
	other, otherHits := offHost(t)
	otherHost := strings.TrimPrefix(other.URL, "http://")

	ids := []string{
		"@" + otherHost + "/subtitle/1-2.zip",
		"." + otherHost + "/subtitle/1-2.zip",
		"//" + otherHost + "/subtitle/1-2.zip",
		other.URL + "/subtitle/1-2.zip",
		"http://user@" + strings.TrimPrefix(dl.URL, "http://") + "/subtitle/1-2.zip",
		"/subtitle/1-2.zip#@" + otherHost,
		"/subtitle/1-2.zip/../../admin",
		"/subtitle/../1-2.zip",
		"/subtitle/%2e%2e/1-2.zip",
		"subtitle/1-2.zip",
		"///subtitle/1-2.zip",
		`\\` + otherHost + "/subtitle/1-2.zip",
		"/subtitle/1-2.zip\n",
		"/subtitle/%252e%252e/1-2.zip",
		"/subtitle/1-2.srt",
		"/subtitle/1-2.zip?a=%zz",
		"",
	}
	p := New(Config{DLBaseURL: dl.URL})
	for _, id := range ids {
		_, _, err := p.Download(t.Context(), id)
		if !errors.Is(err, subtitles.ErrInvalidSubtitleID) {
			t.Errorf("Download(%q) error = %v, want ErrInvalidSubtitleID", id, err)
		}
	}
	if n := otherHits.Load(); n != 0 {
		t.Fatalf("off-host server received %d requests", n)
	}
	if n := dlHits.Load(); n != 0 {
		t.Fatalf("download host received %d requests for malformed IDs", n)
	}
}

func TestDownloadRefusesRedirectOffTheDownloadHost(t *testing.T) {
	other, otherHits := offHost(t)
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/subtitle/1-2.zip", http.StatusFound)
	}))
	defer dl.Close()

	if _, _, err := New(Config{DLBaseURL: dl.URL}).Download(t.Context(), "/subtitle/1-2.zip"); err == nil {
		t.Fatal("download followed an off-host redirect")
	}
	if n := otherHits.Load(); n != 0 {
		t.Fatalf("off-host server received %d requests", n)
	}
}

func TestDownloadFollowsRedirectOnTheDownloadHost(t *testing.T) {
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/subtitle/1-2.zip" {
			http.Redirect(w, r, "/files/1-2.zip", http.StatusFound)
			return
		}
		_, _ = w.Write(subtitleZip(t, "redirected"))
	}))
	defer dl.Close()

	data, _, err := New(Config{DLBaseURL: dl.URL}).Download(t.Context(), "/subtitle/1-2.zip")
	if err != nil || string(data) != "redirected" {
		t.Fatalf("got %q, %v", data, err)
	}
}

func TestSearchRefusesRedirectOffTheAPIHost(t *testing.T) {
	other, otherHits := offHost(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusFound)
	}))
	defer api.Close()

	if _, err := New(Config{BaseURL: api.URL}).Search(t.Context(), subtitles.SearchRequest{IMDbID: "tt0000001"}); err == nil {
		t.Fatal("search followed an off-host redirect")
	}
	if n := otherHits.Load(); n != 0 {
		t.Fatalf("off-host server received %d requests", n)
	}
}
