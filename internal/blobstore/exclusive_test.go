package blobstore

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Silo-Server/silo-server/internal/s3client"
)

// interleavingReader lets another writer publish the same key while the
// exclusive create is still reading its body: the window a check-then-write
// would lose.
type interleavingReader struct {
	r      io.Reader
	before func()
	once   sync.Once
}

func (r *interleavingReader) Read(p []byte) (int, error) {
	r.once.Do(r.before)
	return r.r.Read(p)
}

func readObject(t *testing.T, s Store, key string) string {
	t.Helper()
	body, _, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// checkExclusiveCreate: an exclusive create writes an absent key, refuses an
// existing one, and never replaces an object another writer published while
// it was writing.
func checkExclusiveCreate(t *testing.T, s Store) {
	t.Helper()
	ctx := context.Background()
	if err := CreateExclusive(ctx, s, "local/a/original.r1.jpg", strings.NewReader("restored"), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if got := readObject(t, s, "local/a/original.r1.jpg"); got != "restored" {
		t.Fatalf("created object = %q", got)
	}
	if err := CreateExclusive(ctx, s, "local/a/original.r1.jpg", strings.NewReader("again"), "image/jpeg"); !errors.Is(err, ErrExists) {
		t.Fatalf("create over an existing object = %v, want ErrExists", err)
	}
	const key = "local/b/original.r1.jpg"
	body := &interleavingReader{r: strings.NewReader("restored"), before: func() {
		if err := s.Put(ctx, key, []byte("uploaded meanwhile")); err != nil {
			t.Error(err)
		}
	}}
	if err := CreateExclusive(ctx, s, key, body, "image/jpeg"); !errors.Is(err, ErrExists) {
		t.Fatalf("create racing another writer = %v, want ErrExists", err)
	}
	if got := readObject(t, s, key); got != "uploaded meanwhile" {
		t.Fatalf("the concurrent writer's object became %q", got)
	}
}

func TestFilesystemCreateExclusiveNeverReplaces(t *testing.T) {
	store, err := NewFilesystem(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkExclusiveCreate(t, store)
}

// conditionalS3 is an in-memory bucket that honors If-None-Match: *, or,
// with rejectConditional, refuses the header as some backends do.
func conditionalS3(t *testing.T, rejectConditional bool) *S3 {
	t.Helper()
	var mu sync.Mutex
	objects := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/artwork/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "" {
				if rejectConditional {
					w.WriteHeader(http.StatusNotImplemented)
					_, _ = io.WriteString(w, `<Error><Code>NotImplemented</Code><Message>If-None-Match is not supported</Message></Error>`)
					return
				}
				if _, ok := objects[key]; ok {
					w.WriteHeader(http.StatusPreconditionFailed)
					_, _ = io.WriteString(w, `<Error><Code>PreconditionFailed</Code></Error>`)
					return
				}
			}
			data, _ := io.ReadAll(r.Body)
			objects[key] = string(data)
			w.WriteHeader(http.StatusOK)
		case http.MethodHead, http.MethodGet:
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, data)
			}
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(server.Close)
	return NewS3(s3client.NewClient(s3client.BucketConfig{Endpoint: server.URL, Region: "us-east-1", Bucket: "artwork", PathStyle: true, AccessKey: "test", SecretKey: "test"}))
}

func TestS3CreateExclusiveNeverReplaces(t *testing.T) {
	checkExclusiveCreate(t, conditionalS3(t, false))
}

// TestCreateExclusiveRefusesWithoutAtomicCreate: a bucket that rejects
// If-None-Match, and a store with no exclusive create at all, refuse with
// ErrExclusiveUnsupported and write nothing, rather than check and then
// write, which could replace an object published between the two.
func TestCreateExclusiveRefusesWithoutAtomicCreate(t *testing.T) {
	ctx := context.Background()
	s := conditionalS3(t, true)
	const key = "local/a/original.r1.jpg"
	if err := CreateExclusive(ctx, s, key, strings.NewReader("restored"), "image/jpeg"); !errors.Is(err, ErrExclusiveUnsupported) {
		t.Fatalf("create on a bucket without conditional writes = %v, want ErrExclusiveUnsupported", err)
	}
	if _, err := s.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused create left an object: %v", err)
	}

	plain := struct{ Store }{s}
	if err := CreateExclusive(ctx, plain, "local/b/original.r1.jpg", strings.NewReader("restored"), "image/jpeg"); !errors.Is(err, ErrExclusiveUnsupported) {
		t.Fatalf("create on a store without ExclusiveCreator = %v, want ErrExclusiveUnsupported", err)
	}
	if _, err := s.Stat(ctx, "local/b/original.r1.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("refused create left an object: %v", err)
	}
}
