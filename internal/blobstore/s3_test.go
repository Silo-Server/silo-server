package blobstore

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

func fakeArtworkS3(t *testing.T) *S3 {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := strings.TrimPrefix(r.URL.Path, "/artwork/")
		w.Header().Set("Content-Type", "application/xml")
		if r.Method == http.MethodPost && r.URL.Query().Has("delete") {
			var request struct {
				Objects []struct{ Key string } `xml:"Object"`
			}
			if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode delete: %v", err)
				w.WriteHeader(400)
				return
			}
			type deleted struct {
				Key string `xml:"Key"`
			}
			response := struct {
				XMLName xml.Name  `xml:"DeleteResult"`
				Deleted []deleted `xml:"Deleted"`
			}{}
			for _, obj := range request.Objects {
				delete(objects, obj.Key)
				response.Deleted = append(response.Deleted, deleted{obj.Key})
			}
			_ = xml.NewEncoder(w).Encode(response)
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			q := r.URL.Query()
			after := q.Get("start-after")
			if q.Get("continuation-token") != "" {
				after = q.Get("continuation-token")
			}
			keys := []string{}
			for k := range objects {
				if strings.HasPrefix(k, q.Get("prefix")) && k > after {
					keys = append(keys, k)
				}
			}
			sort.Strings(keys)
			limit, _ := strconv.Atoi(q.Get("max-keys"))
			if limit <= 0 {
				limit = 1000
			}
			type entry struct {
				Key          string
				Size         int
				LastModified string
				ETag         string
			}
			response := struct {
				XMLName               xml.Name `xml:"ListBucketResult"`
				IsTruncated           bool
				NextContinuationToken string `xml:",omitempty"`
				Contents              []entry
			}{}
			if len(keys) > limit {
				response.IsTruncated = true
				keys = keys[:limit]
				response.NextContinuationToken = keys[len(keys)-1]
			}
			for _, k := range keys {
				response.Contents = append(response.Contents, entry{k, len(objects[k]), modified.Format(time.RFC3339), `"etag"`})
			}
			_ = xml.NewEncoder(w).Encode(response)
			return
		}
		switch r.Method {
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			objects[key] = data
			w.Header().Set("ETag", `"etag"`)
		case http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(204)
		case http.MethodGet, http.MethodHead:
			if r.URL.Path == "/artwork" || r.URL.Path == "/artwork/" {
				return
			}
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code></Error>")
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Last-Modified", modified.Format(http.TimeFormat))
			w.Header().Set("ETag", `"etag"`)
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
		default:
			t.Errorf("unexpected S3 request %s %s", r.Method, r.URL)
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	return NewS3(s3client.NewClient(s3client.BucketConfig{Endpoint: server.URL, Bucket: "artwork", PathStyle: true, AccessKey: "test", SecretKey: "test"}))
}

func TestS3ArtworkRoundTrip(t *testing.T) {
	s := fakeArtworkS3(t)
	ctx := context.Background()
	for _, payload := range []string{"first", "replacement"} {
		if err := s.Put(ctx, "posters/a.webp", []byte(payload)); err != nil {
			t.Fatal(err)
		}
		r, info, err := s.Get(ctx, "posters/a.webp")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != payload || info.Key != "posters/a.webp" || info.Size != int64(len(payload)) || info.ModTime.IsZero() || info.ETag == "" {
			t.Fatalf("roundtrip: %q %#v", data, info)
		}
	}
	if _, err := s.Stat(ctx, "missing.webp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat missing: %v", err)
	}
	if _, _, err := s.Get(ctx, "missing.webp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if _, err := s.Delete(ctx, []string{"missing.webp", "posters/a.webp"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, "posters/a.webp"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted stat: %v", err)
	}
}

func TestS3ArtworkPrefixAndCursor(t *testing.T) {
	s := fakeArtworkS3(t)
	ctx := context.Background()
	for _, k := range []string{"items/a/1.webp", "items/a/2.webp", "items/a/3.webp", "items/ab/1.webp"} {
		if err := s.Put(ctx, k, []byte(k)); err != nil {
			t.Fatal(err)
		}
	}
	cursor := ""
	got := []string{}
	for i := 0; i < 4; i++ {
		page, next, err := s.List(ctx, "items/a", cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, obj := range page {
			got = append(got, obj.Key)
		}
		if next == "" {
			break
		}
		if next <= cursor {
			t.Fatalf("cursor did not advance: %q -> %q", cursor, next)
		}
		cursor = next
	}
	if fmt.Sprint(got) != "[items/a/1.webp items/a/2.webp items/a/3.webp]" {
		t.Fatalf("pages: %v", got)
	}
	if _, err := s.DeletePrefix(ctx, "items/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Stat(ctx, "items/ab/1.webp"); err != nil {
		t.Fatalf("sibling removed: %v", err)
	}
	page, _, err := s.List(ctx, "items/a", "", 10)
	if err != nil || len(page) != 0 {
		t.Fatalf("prefix remains: %v %v", page, err)
	}
}

func TestS3IdentityCoversStorageLocationOnly(t *testing.T) {
	build := func(endpoint, bucket, prefix, readEndpoint string) string {
		return NewS3(s3client.NewClient(s3client.BucketConfig{
			Endpoint: endpoint, Bucket: bucket, KeyPrefix: prefix, PublicEndpoint: readEndpoint,
			PathStyle: true, AccessKey: "test", SecretKey: "test",
		})).Identity()
	}
	base := build("https://s3.example", "artwork", "silo/prod", "")
	if base != build(" https://S3.Example ", "Artwork", " /silo/prod/ ", "https://images.example") {
		t.Fatal("case, whitespace, slashes, and the read endpoint must not change the identity")
	}
	for name, other := range map[string]string{
		"endpoint": build("https://other.example", "artwork", "silo/prod", ""),
		"bucket":   build("https://s3.example", "other", "silo/prod", ""),
		"prefix":   build("https://s3.example", "artwork", "silo/Prod", ""),
	} {
		if other == base {
			t.Fatalf("%s change did not change the identity", name)
		}
	}
	if !strings.HasPrefix(base, BackendS3+"|") {
		t.Fatalf("identity = %q", base)
	}
	// Only the scheme and host of an endpoint are case-insensitive; a path
	// names case-sensitive upstream storage on gateways.
	if build("https://gateway.example/TenantA", "artwork", "", "") == build("https://gateway.example/tenanta", "artwork", "", "") {
		t.Fatal("endpoint path case must change the identity")
	}
	if build("HTTPS://Gateway.Example/TenantA", "artwork", "", "") != build("https://gateway.example/TenantA", "artwork", "", "") {
		t.Fatal("scheme and host case must not change the identity")
	}
}
