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

	p := New(Config{DLBaseURL: dl.URL, APIKey: "synthetic-server-key"})
	// An ID issued before result IDs dropped SubDL's signed query: the
	// client's query is not forwarded, the server signs with its own key.
	data, format, err := p.Download(t.Context(), "/subtitle/3197651-3213944.zip?api_key=client-supplied&x=1")
	if err != nil {
		t.Fatal(err)
	}
	if format != subtitles.FormatSRT || !strings.Contains(string(data), "hello") {
		t.Fatalf("got %q (%s)", data, format)
	}
	if gotPath != "/subtitle/3197651-3213944.zip" || gotQuery != "api_key=synthetic-server-key" {
		t.Fatalf("requested %s?%s", gotPath, gotQuery)
	}
}

func TestSignedSearchRowYieldsKeylessIDThatDownloads(t *testing.T) {
	const key = "synthetic-subdl-key"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":true,"subtitles":[{"language":"EN","release_name":"Example.2024.srt","url":"/subtitle/3520378-8443030.zip?api_key=` + key + `"}]}`))
	}))
	defer api.Close()
	var gotQuery string
	dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write(subtitleZip(t, "1\n00:00:01,000 --> 00:00:02,000\nhello\n"))
	}))
	defer dl.Close()

	p := New(Config{BaseURL: api.URL, DLBaseURL: dl.URL, APIKey: key})
	results, err := p.Search(t.Context(), subtitles.SearchRequest{IMDbID: "tt0000001"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "/subtitle/3520378-8443030.zip" {
		t.Fatalf("results = %+v", results)
	}
	if _, _, err := p.Download(t.Context(), results[0].ID); err != nil {
		t.Fatal(err)
	}
	if gotQuery != "api_key="+key {
		t.Fatalf("download query = %q", gotQuery)
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

	_, _, err := New(Config{DLBaseURL: dl.URL, APIKey: "synthetic-subdl-key"}).Download(t.Context(), "/subtitle/1-2.zip")
	if err == nil {
		t.Fatal("download followed an off-host redirect")
	}
	if strings.Contains(err.Error(), "synthetic-subdl-key") {
		t.Fatalf("download error carries the key: %v", err)
	}
	if n := otherHits.Load(); n != 0 {
		t.Fatalf("off-host server received %d requests", n)
	}
}

func TestDownloadTransportErrorOmitsAPIKey(t *testing.T) {
	dl := httptest.NewServer(http.NotFoundHandler())
	dl.Close()
	_, _, err := New(Config{DLBaseURL: dl.URL, APIKey: "synthetic-subdl-key"}).Download(t.Context(), "/subtitle/1-2.zip")
	if err == nil {
		t.Fatal("download from a closed server succeeded")
	}
	if strings.Contains(err.Error(), "synthetic-subdl-key") {
		t.Fatalf("download error carries the key: %v", err)
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
