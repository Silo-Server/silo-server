package blobgc_test

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/blobgc"
	"github.com/Silo-Server/silo-server/internal/blobstore"
	"github.com/Silo-Server/silo-server/internal/chapterthumbs"
	"github.com/Silo-Server/silo-server/internal/s3client"
)

func chapterS3Store(t *testing.T) *blobstore.S3 {
	t.Helper()
	var mu sync.Mutex
	objects := map[string][]byte{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := strings.TrimPrefix(r.URL.Path, "/images/")
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("delete"):
			var request struct {
				Objects []struct{ Key string } `xml:"Object"`
			}
			if err := xml.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			response := struct {
				XMLName xml.Name               `xml:"DeleteResult"`
				Deleted []struct{ Key string } `xml:"Deleted"`
			}{}
			for _, object := range request.Objects {
				delete(objects, object.Key)
				response.Deleted = append(response.Deleted, object)
			}
			_ = xml.NewEncoder(w).Encode(response)
		case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
			response := struct {
				XMLName  xml.Name `xml:"ListBucketResult"`
				Contents []struct{ Key string }
			}{}
			for _, stored := range slices.Sorted(maps.Keys(objects)) {
				if strings.HasPrefix(stored, r.URL.Query().Get("prefix")) {
					response.Contents = append(response.Contents, struct{ Key string }{stored})
				}
			}
			_ = xml.NewEncoder(w).Encode(response)
		case r.Method == http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			objects[key] = data
		case r.Method == http.MethodHead:
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		default:
			t.Errorf("unexpected S3 operation: %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(server.Close)
	return blobstore.NewS3(s3client.NewClient(s3client.BucketConfig{
		Endpoint: server.URL, Bucket: "images", PathStyle: true, AccessKey: "test", SecretKey: "test",
	}))
}

func TestCollectorDeletesSingleImagesS3DB(t *testing.T) {
	id := time.Now().UnixNano()
	keys := []string{
		fmt.Sprintf("chapter-images/%d/0/w300.webp", id),
		fmt.Sprintf("chapter-images/%d/0-%s/w300.webp", id+1, strings.Repeat("a", 64)),
	}
	pool := singleConnectionPool(t, keys)
	store := chapterS3Store(t)
	for _, key := range keys {
		if err := store.Put(t.Context(), key, []byte("image")); err != nil {
			t.Fatal(err)
		}
	}
	if err := blobgc.NewQueue(pool).Schedule(t.Context(), keys, 0); err != nil {
		t.Fatal(err)
	}
	stats, err := blobgc.NewCollector(pool, store, chapterthumbs.ImageBlobNamespace()).Collect(t.Context(), len(keys))
	if err != nil || stats.Deleted != len(keys) || stats.Objects != len(keys) {
		t.Fatalf("single-image collection: stats=%+v err=%v", stats, err)
	}
	for _, key := range keys {
		if _, err := store.Stat(t.Context(), key); !errors.Is(err, blobstore.ErrNotFound) {
			t.Errorf("chapter image remains after collection: %v", err)
		}
	}
}
