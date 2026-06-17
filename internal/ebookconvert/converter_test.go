package ebookconvert

import (
	"archive/zip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestConverter(t *testing.T) *Converter {
	t.Helper()
	c, err := NewConverter(context.Background(), Options{})
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func TestConvert_DRMFreeProducesValidEpub(t *testing.T) {
	for _, fixture := range []string{"sample-ncx.mobi", "sample-cp1252.mobi"} {
		t.Run(fixture, func(t *testing.T) {
			c := newTestConverter(t)
			dst := filepath.Join(t.TempDir(), "out.epub")
			if err := c.Convert(context.Background(), filepath.Join("testdata", fixture), dst); err != nil {
				t.Fatalf("Convert: %v", err)
			}
			// Independently re-validate the delivered file is a real EPUB.
			assertValidEpub(t, dst)
		})
	}
}

func TestConvert_DRMProtectedRejected(t *testing.T) {
	c := newTestConverter(t)
	dst := filepath.Join(t.TempDir(), "out.epub")
	err := c.Convert(context.Background(), filepath.Join("testdata", "sample-drm-v1.mobi"), dst)
	if !errors.Is(err, ErrDRMProtected) {
		t.Fatalf("got %v, want ErrDRMProtected", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("DRM source must not produce an output file, but %s exists", dst)
	}
}

func TestConvert_OversizeRejected(t *testing.T) {
	c, err := NewConverter(context.Background(), Options{MaxSourceBytes: 1024})
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	dst := filepath.Join(t.TempDir(), "out.epub")
	if err := c.Convert(context.Background(), filepath.Join("testdata", "sample-ncx.mobi"), dst); !errors.Is(err, ErrSourceTooLarge) {
		t.Fatalf("got %v, want ErrSourceTooLarge", err)
	}
}

func TestConvert_CorruptSourceFails(t *testing.T) {
	c := newTestConverter(t)
	bad := filepath.Join(t.TempDir(), "bad.mobi")
	if err := os.WriteFile(bad, []byte("not a mobi file at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "out.epub")
	if err := c.Convert(context.Background(), bad, dst); !errors.Is(err, ErrConversionFailed) {
		t.Fatalf("got %v, want ErrConversionFailed", err)
	}
}

func TestConvert_MissingSourceFails(t *testing.T) {
	c := newTestConverter(t)
	dst := filepath.Join(t.TempDir(), "out.epub")
	if err := c.Convert(context.Background(), "testdata/does-not-exist.mobi", dst); !errors.Is(err, ErrConversionFailed) {
		t.Fatalf("got %v, want ErrConversionFailed", err)
	}
}

func TestConvert_Concurrent(t *testing.T) {
	c := newTestConverter(t)
	const n = 6
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			dst := filepath.Join(t.TempDir(), "c.epub")
			errs <- c.Convert(context.Background(), filepath.Join("testdata", "sample-ncx.mobi"), dst)
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent Convert: %v", err)
		}
	}
}

func TestConvert_ContextCancelled(t *testing.T) {
	c := newTestConverter(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before the conversion starts
	dst := filepath.Join(t.TempDir(), "out.epub")
	if err := c.Convert(ctx, filepath.Join("testdata", "sample-ncx.mobi"), dst); err == nil {
		t.Fatal("expected error on canceled context")
	}
}

func TestValidateEpub_RejectsNonEpubZip(t *testing.T) {
	// A zip that is not an EPUB (no mimetype-first) must be rejected.
	p := filepath.Join(t.TempDir(), "plain.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.Create("hello.txt")
	_, _ = w.Write([]byte("hi"))
	_ = zw.Close()
	_ = f.Close()
	if err := validateEpub(p); err == nil {
		t.Fatal("expected validateEpub to reject a non-EPUB zip")
	}
}

func TestValidateEpub_RejectsDeflatedMimetype(t *testing.T) {
	// mimetype present + correct content but DEFLATED (not stored) -> reject.
	p := filepath.Join(t.TempDir(), "bad.epub")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Deflate})
	_, _ = w.Write([]byte("application/epub+zip"))
	_ = zw.Close()
	_ = f.Close()
	if err := validateEpub(p); err == nil {
		t.Fatal("expected rejection of deflated mimetype")
	}
}

func TestConvert_AfterCloseReturnsUnavailable(t *testing.T) {
	c, err := NewConverter(context.Background(), Options{})
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	_ = c.Close(context.Background())
	dst := filepath.Join(t.TempDir(), "out.epub")
	if err := c.Convert(context.Background(), filepath.Join("testdata", "sample-ncx.mobi"), dst); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
}

func TestConvert_TimeoutClassifiedNotRawExit(t *testing.T) {
	// A 1ns timeout must surface as ErrConversionTimedOut (a transient verdict,
	// distinct from a deterministic failure), never as a bogus "mobitool exit
	// <huge>" code.
	c, err := NewConverter(context.Background(), Options{Timeout: time.Nanosecond})
	if err != nil {
		t.Fatalf("NewConverter: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	dst := filepath.Join(t.TempDir(), "out.epub")
	err = c.Convert(context.Background(), filepath.Join("testdata", "sample-ncx.mobi"), dst)
	if !errors.Is(err, ErrConversionTimedOut) {
		t.Fatalf("got %v, want ErrConversionTimedOut", err)
	}
	if errors.Is(err, ErrConversionFailed) {
		t.Fatalf("a timeout must not also satisfy ErrConversionFailed (it would be negatively cached): %v", err)
	}
	if strings.Contains(err.Error(), "exit ") {
		t.Fatalf("timeout leaked a raw exit code: %v", err)
	}
}

// classifyRunError must propagate a caller's deadline as context.DeadlineExceeded
// (CR review): reclassifying it as a conversion failure breaks upstream
// cancellation handling and would poison the negative cache.
func TestClassifyRunError_ParentDeadlinePropagates(t *testing.T) {
	parent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	run := parent // run derives from parent, so it is also past-deadline
	err := classifyRunError(parent, run, context.DeadlineExceeded, time.Minute, "mobitool output")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v, want context.DeadlineExceeded propagated", err)
	}
	if errors.Is(err, ErrConversionFailed) || errors.Is(err, ErrConversionTimedOut) {
		t.Fatalf("a caller deadline must not be reclassified as a conversion verdict: %v", err)
	}
}

// When the caller's context is healthy but our own per-call timeout fired, the
// result is the transient ErrConversionTimedOut sentinel (not ErrConversionFailed).
func TestClassifyRunError_OwnTimeoutIsTimedOutSentinel(t *testing.T) {
	parent := context.Background() // caller healthy
	run, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()
	err := classifyRunError(parent, run, context.DeadlineExceeded, time.Minute, "mobitool output")
	if !errors.Is(err, ErrConversionTimedOut) {
		t.Fatalf("got %v, want ErrConversionTimedOut", err)
	}
	if errors.Is(err, ErrConversionFailed) {
		t.Fatalf("own timeout must not satisfy ErrConversionFailed: %v", err)
	}
}

func assertValidEpub(t *testing.T, path string) {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open epub: %v", err)
	}
	defer zr.Close()
	if len(zr.File) == 0 || zr.File[0].Name != "mimetype" {
		t.Fatalf("first entry not mimetype")
	}
	rc, _ := zr.File[0].Open()
	mt, _ := io.ReadAll(rc)
	rc.Close()
	if strings.TrimSpace(string(mt)) != "application/epub+zip" {
		t.Fatalf("bad mimetype %q", mt)
	}
	var hasContainer, hasContent bool
	for _, fl := range zr.File {
		if fl.Name == "META-INF/container.xml" {
			hasContainer = true
		}
		if strings.HasPrefix(fl.Name, "OEBPS/") {
			hasContent = true
		}
	}
	if !hasContainer {
		t.Fatal("missing META-INF/container.xml")
	}
	if !hasContent {
		t.Fatal("no OEBPS content")
	}
}

// Guard so the default timeout constant stays sane if edited.
func TestDefaults(t *testing.T) {
	if DefaultTimeout < time.Second {
		t.Fatal("DefaultTimeout too small")
	}
}
