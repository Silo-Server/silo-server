package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// Exercise both discovery pipelines with an instance-local reader: no global
// filesystem hooks or permission assumptions (the Docker tests run as root).
func TestWalkDirectoryReadRetries(t *testing.T) {
	for _, kind := range []string{"ebooks", "audiobooks", "movies"} {
		for _, outcome := range []string{"recovers", "exhausted", "read timeout", "canceled"} {
			t.Run(kind+"/"+outcome, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				child := filepath.Join(root, "Book")
				if err := os.Mkdir(child, 0o755); err != nil {
					t.Fatal(err)
				}
				ext := map[string]string{"ebooks": ".epub", "audiobooks": ".mp3", "movies": ".mkv"}[kind]
				file := filepath.Join(child, "media"+ext)
				if err := os.WriteFile(file, []byte("media"), 0o644); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				attempts := 0
				readDir := func(path string) ([]os.DirEntry, error) {
					if path == child {
						attempts++
						if outcome == "canceled" {
							cancel()
						}
						if outcome == "read timeout" {
							return nil, context.DeadlineExceeded
						}
						if outcome != "recovers" || attempts == 1 {
							return nil, errors.New("transient directory listing failure")
						}
					}
					return os.ReadDir(path)
				}
				var files, failures []string
				var err error
				if kind == "audiobooks" {
					scan := audiobookRootScan{root: root, seenPaths: make(map[string]bool)}
					err = walkAudiobookDirectories(ctx, root, &scan, make(map[string]bool), nil, true, readDir)
					for path := range scan.seenPaths {
						files = append(files, path)
					}
					failures = scan.walkFailures
				} else {
					err = walkLogicalTree(ctx, root, root, walkModeFor(kind), make(map[string]struct{}), nil, &files, &failures, readDir)
				}
				if outcome == "canceled" {
					if !errors.Is(err, context.Canceled) || attempts != 1 || len(failures) != 0 {
						t.Fatalf("canceled walk: error=%v attempts=%d failures=%v; want cancellation, one attempt, no exhausted failure", err, attempts, failures)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "recovers" {
					if !reflect.DeepEqual(files, []string{file}) || len(failures) != 0 || attempts != 2 {
						t.Fatalf("recovered walk: files=%v failures=%v attempts=%d; want the file, no failures, two attempts", files, failures, attempts)
					}
				} else if attempts != 3 || !reflect.DeepEqual(failures, []string{child}) || len(files) != 0 {
					t.Fatalf("exhausted walk: attempts=%d failures=%v files=%v; want three attempts, one failed path, no files", attempts, failures, files)
				}
			})
		}
	}
}

type retryWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *retryWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestDirectoryReadCancellationInterruptsBackoff(t *testing.T) {
	t.Parallel()
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &retryWaitContext{Context: base, waiting: make(chan struct{})}
	result := make(chan error, 1)
	attempts := 0
	go func() {
		_, err := readDirectoryWithRetry(ctx, "/library", func(string) ([]os.DirEntry, error) {
			attempts++
			return nil, errors.New("listing failed")
		})
		result <- err
	}()
	select {
	case <-ctx.waiting:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("reader did not enter cancellable backoff")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Fatalf("error=%v attempts=%d; want cancellation before another read", err, attempts)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt backoff")
	}
}
