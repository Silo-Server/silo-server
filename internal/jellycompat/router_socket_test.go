package jellycompat

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
)

const compatSocketToken = "compat-socket-token"

func TestMountedCompatRouterPreservesMediaHTTPAndCompression(t *testing.T) {
	server, client, mediaURL, _ := newCompatSocketServer(t)

	full := compatSocketRequest(t, client, http.MethodGet, mediaURL, nil)
	etag := full.Header.Get("ETag")
	assertCompatSocketResponse(t, full, http.StatusOK, "0123456789abcdefghijklmnopqrstuvwxyz")
	_ = full.Body.Close()
	if etag == "" {
		t.Fatal("media response omitted ETag")
	}
	head := compatSocketRequest(t, client, http.MethodHead, mediaURL, nil)
	assertCompatSocketResponse(t, head, http.StatusOK, "")
	_ = head.Body.Close()
	singleRange := compatSocketRequest(t, client, http.MethodGet, mediaURL, map[string]string{"Range": "bytes=2-5"})
	assertCompatSocketResponse(t, singleRange, http.StatusPartialContent, "2345")
	_ = singleRange.Body.Close()
	multiRange := compatSocketRequest(t, client, http.MethodGet, mediaURL, map[string]string{"Range": "bytes=0-1,4-6"})
	assertCompatMultiRange(t, multiRange)
	_ = multiRange.Body.Close()
	notModified := compatSocketRequest(t, client, http.MethodGet, mediaURL, map[string]string{"If-None-Match": etag})
	assertCompatSocketResponse(t, notModified, http.StatusNotModified, "")
	_ = notModified.Body.Close()
	ifRangeHit := compatSocketRequest(t, client, http.MethodGet, mediaURL, map[string]string{"Range": "bytes=2-5", "If-Range": etag})
	assertCompatSocketResponse(t, ifRangeHit, http.StatusPartialContent, "2345")
	_ = ifRangeHit.Body.Close()
	ifRangeMiss := compatSocketRequest(t, client, http.MethodGet, mediaURL, map[string]string{"Range": "bytes=2-5", "If-Range": `"stale"`})
	assertCompatSocketResponse(t, ifRangeMiss, http.StatusOK, "0123456789abcdefghijklmnopqrstuvwxyz")
	_ = ifRangeMiss.Body.Close()

	gzipMedia := compatSocketRequest(t, client, http.MethodGet, mediaURL, map[string]string{"Accept-Encoding": "gzip"})
	if encoding := gzipMedia.Header.Get("Content-Encoding"); encoding != "" {
		t.Fatalf("media Content-Encoding = %q, want empty", encoding)
	}
	assertCompatSocketResponse(t, gzipMedia, http.StatusOK, "0123456789abcdefghijklmnopqrstuvwxyz")
	_ = gzipMedia.Body.Close()

	jsonResp := compatSocketRequest(t, client, http.MethodGet, server.URL+"/System/Info/Public", map[string]string{"Accept-Encoding": "gzip"})
	defer func() { _ = jsonResp.Body.Close() }()
	if encoding := jsonResp.Header.Get("Content-Encoding"); encoding != "gzip" {
		t.Fatalf("JSON Content-Encoding = %q, want gzip", encoding)
	}
	if vary := jsonResp.Header.Values("Vary"); !compatHeaderContains(vary, "Accept-Encoding") {
		t.Fatalf("JSON Vary = %q, want Accept-Encoding", vary)
	}
	zr, err := gzip.NewReader(jsonResp.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = zr.Close() }()
	body, err := io.ReadAll(zr)
	if err != nil || !bytes.Contains(body, []byte(`"ProductName":"Jellyfin Server"`)) {
		t.Fatalf("compressed JSON body invalid: body=%q err=%v", body, err)
	}
}

func TestMountedCompatRouterImageProxyUARewritesJSONButStreamsVideo(t *testing.T) {
	_, client, mediaURL, itemURL := newCompatSocketServer(t)
	const userAgent = "Infuse-Direct/8.4.6"

	itemResp := compatSocketRequest(t, client, http.MethodGet, itemURL, map[string]string{
		"User-Agent":   userAgent,
		"X-Emby-Token": compatSocketToken,
	})
	defer func() { _ = itemResp.Body.Close() }()
	if itemResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(itemResp.Body)
		t.Fatalf("item status = %d, body=%s", itemResp.StatusCode, body)
	}
	var item baseItemDTO
	if err := json.NewDecoder(itemResp.Body).Decode(&item); err != nil {
		t.Fatalf("decode item: %v", err)
	}
	if primary := item.ImageTags["Primary"]; primary == "" || !strings.HasSuffix(primary, compatImageProxyTagSuffix) {
		t.Fatalf("rewritten primary image tag = %q, want %q suffix", primary, compatImageProxyTagSuffix)
	}

	videoResp := compatSocketRequest(t, client, http.MethodGet, mediaURL, map[string]string{
		"User-Agent": userAgent,
		"Range":      "bytes=3-7",
	})
	if got := videoResp.Header.Get("Content-Range"); got != "bytes 3-7/36" {
		t.Fatalf("video Content-Range = %q, want bytes 3-7/36", got)
	}
	assertCompatSocketResponse(t, videoResp, http.StatusPartialContent, "34567")
	_ = videoResp.Body.Close()
}

func newCompatSocketServer(t *testing.T) (*httptest.Server, *http.Client, string, string) {
	t.Helper()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "movie.mp4")
	if err := os.WriteFile(filePath, []byte("0123456789abcdefghijklmnopqrstuvwxyz"), 0o644); err != nil {
		t.Fatalf("write media: %v", err)
	}
	cfg, err := config.LoadFromDB(map[string]string{})
	if err != nil {
		t.Fatalf("LoadFromDB: %v", err)
	}
	store := NewSessionStore(time.Hour, nil)
	if err := store.Put(Session{Token: compatSocketToken, StreamAppUserID: 1, ProfileID: "profile-1"}); err != nil {
		t.Fatalf("put compat session: %v", err)
	}
	codec := NewResourceIDCodec()
	contentID := "socket-movie"
	detail := &upstreamItemDetail{
		ContentID: contentID,
		Type:      "movie",
		Title:     "Socket Movie",
		PosterURL: "https://images.invalid/poster.jpg",
		Versions: []catalog.FileVersion{{
			FileID:    42,
			FilePath:  filePath,
			Container: "mp4",
			Duration:  3600,
			FileSize:  36,
			AddedAt:   time.Now(),
		}},
	}
	router := NewRouter(Dependencies{
		Config:         cfg,
		SessionStore:   store,
		IDCodec:        codec,
		ContentService: &stubContentService{detail: detail},
		FileResolver:   testCompatFileResolver{file: &models.MediaFile{ID: 42, FilePath: filePath}},
		SessionMgr:     &testCompatSessionManager{},
	})
	server := httptest.NewUnstartedServer(router)
	server.Start()
	t.Cleanup(server.Close)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	t.Cleanup(client.CloseIdleConnections)
	itemID := codec.EncodeStringID(EncodedIDItem, contentID)
	mediaURL := server.URL + "/Videos/" + itemID + "/stream.mp4?static=true&api_key=" + compatSocketToken
	itemURL := server.URL + "/Items/" + itemID
	return server, client, mediaURL, itemURL
}

func compatSocketRequest(t *testing.T, client *http.Client, method, url string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, url, err)
	}
	return resp
}

func assertCompatSocketResponse(t *testing.T, resp *http.Response, wantStatus int, wantBody string) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != wantStatus || string(body) != wantBody {
		t.Fatalf("status/body = %d, %q; want %d, %q", resp.StatusCode, body, wantStatus, wantBody)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" {
		t.Fatalf("media Content-Encoding = %q, want empty", encoding)
	}
}

func assertCompatMultiRange(t *testing.T, resp *http.Response) {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("multi-range status = %d, want 206", resp.StatusCode)
	}
	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/byteranges" {
		t.Fatalf("multi-range Content-Type = %q: %v", resp.Header.Get("Content-Type"), err)
	}
	mr := multipart.NewReader(resp.Body, params["boundary"])
	var bodies []string
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("read multipart range: %v", err)
		}
		body, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("read multipart body: %v", err)
		}
		bodies = append(bodies, string(body))
	}
	if strings.Join(bodies, ",") != "01,456" {
		t.Fatalf("multi-range bodies = %q, want [01 456]", bodies)
	}
}

func compatHeaderContains(values []string, want string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}
