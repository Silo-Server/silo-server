package ebookconvert

import (
	"archive/zip"
	"bytes"
	"context"
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// mobitoolWasm is libmobi's mobitool compiled to wasm32-wasi.
// Built by tools/mobitool-wasm/Dockerfile; provenance in mobitool.wasm.sha256.
//
//go:embed mobitool.wasm
var mobitoolWasm []byte

// Guest layout. Input is mounted READ-ONLY (confined fs.FS, no `..` escape);
// output is a dedicated writable dir. NOTE: a writable wazero dir mount is not
// a hard jail (`..` traversal is possible within the server user's reach), so
// the WASM sandbox is a *memory-safety* boundary — filesystem isolation relies
// on running the server as a constrained, non-root user/container. See the
// design doc's "Filesystem sandboxing" section.
const (
	guestInDir     = "/in"
	guestOutDir    = "/out"
	guestInputName = "in.mobi" // mobitool sniffs content; extension is cosmetic
	guestEpubName  = "in.epub" // mobitool derives this from the input basename
)

// mobitool diagnostic lines that mean the source is encrypted/DRM'd. Matched as
// substrings against individual output lines (not the whole blob) so book
// metadata/text containing "DRM" cannot false-positive. "Document is encrypted"
// is distinct from mobitool's "Document is not encrypted, ignoring PID/serial".
var drmMarkers = []string{
	"Document is encrypted",
	"DRM key not found",
	"Invalid DRM pid",
	"DRM expired",
	"DRM support not included",
}

// Print Replica = fixed-layout (image) Kindle book; mobitool ignores -e and
// produces no EPUB. Detected for a clear failure message.
const printReplicaMarker = "Print Replica"

// Options configures a Converter. Zero values fall back to sane defaults.
type Options struct {
	MaxSourceBytes int64         // reject larger sources before invoking the converter
	MaxOutputBytes int64         // reject a produced EPUB larger than this
	Timeout        time.Duration // bounds a single conversion
	MaxConcurrent  int           // caps simultaneous conversions
	MaxMemoryPages uint32        // wasm linear-memory cap (64 KiB/page)
	MaxLogBytes    int           // cap on captured stdout+stderr each
	WorkRoot       string        // parent for per-conversion scratch dirs ("" = os.TempDir)
}

const (
	DefaultMaxSourceBytes = 256 << 20 // 256 MiB
	DefaultMaxOutputBytes = 512 << 20 // 512 MiB
	DefaultTimeout        = 60 * time.Second
	DefaultConcurrency    = 2
	DefaultMaxMemoryPages = 16384 // 1 GiB
	DefaultMaxLogBytes    = 64 << 10
)

func (o *Options) applyDefaults() {
	if o.MaxSourceBytes <= 0 {
		o.MaxSourceBytes = DefaultMaxSourceBytes
	}
	if o.MaxOutputBytes <= 0 {
		o.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxConcurrent <= 0 {
		o.MaxConcurrent = DefaultConcurrency
	}
	if o.MaxMemoryPages == 0 {
		o.MaxMemoryPages = DefaultMaxMemoryPages
	}
	if o.MaxLogBytes <= 0 {
		o.MaxLogBytes = DefaultMaxLogBytes
	}
}

// Converter converts Kindle-family ebooks to EPUB in-process via wazero.
// Safe for concurrent use. The WASM module is compiled once; each conversion
// instantiates a fresh, isolated module.
type Converter struct {
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	opts     Options
	sem      chan struct{}
	closed   atomic.Bool
}

// NewConverter compiles the embedded module and returns a ready Converter.
// On failure it returns ErrUnavailable; callers must not advertise the
// conversion capability when this fails.
func NewConverter(ctx context.Context, opts Options) (*Converter, error) {
	opts.applyDefaults()

	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(opts.MaxMemoryPages)
	r := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("%w: instantiate wasi: %v", ErrUnavailable, err)
	}
	compiled, err := r.CompileModule(ctx, mobitoolWasm)
	if err != nil {
		_ = r.Close(ctx)
		return nil, fmt.Errorf("%w: compile module: %v", ErrUnavailable, err)
	}
	return &Converter{
		runtime:  r,
		compiled: compiled,
		opts:     opts,
		sem:      make(chan struct{}, opts.MaxConcurrent),
	}, nil
}

// Close releases the runtime. Convert returns ErrUnavailable afterward.
func (c *Converter) Close(ctx context.Context) error {
	if c == nil || c.runtime == nil {
		return nil
	}
	c.closed.Store(true)
	return c.runtime.Close(ctx)
}

// Convert reads the MOBI/AZW/AZW3 at srcPath and writes a validated EPUB to
// dstPath. Returns ErrDRMProtected, ErrConversionFailed (deterministic),
// ErrConversionTimedOut (transient — the per-call timeout fired),
// ErrSourceTooLarge, ErrUnavailable, or the caller's context error on
// cancellation/deadline.
func (c *Converter) Convert(ctx context.Context, srcPath, dstPath string) error {
	if c.closed.Load() {
		return ErrUnavailable
	}
	info, statErr := os.Stat(srcPath)
	if statErr != nil {
		return fmt.Errorf("%w: stat source: %v", ErrConversionFailed, statErr)
	}
	if info.Size() > c.opts.MaxSourceBytes {
		return ErrSourceTooLarge
	}

	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	// Per-conversion scratch: in/ (read-only mount) and out/ (writable mount).
	scratch, err := os.MkdirTemp(c.opts.WorkRoot, "ebookconvert-")
	if err != nil {
		return fmt.Errorf("%w: scratch: %v", ErrConversionFailed, err)
	}
	defer os.RemoveAll(scratch)

	inDir := filepath.Join(scratch, "in")
	outDir := filepath.Join(scratch, "out")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		return fmt.Errorf("%w: in dir: %v", ErrConversionFailed, err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("%w: out dir: %v", ErrConversionFailed, err)
	}
	if err := copyFile(srcPath, filepath.Join(inDir, guestInputName)); err != nil {
		return fmt.Errorf("%w: stage input: %v", ErrConversionFailed, err)
	}

	runCtx, cancel := context.WithTimeout(ctx, c.opts.Timeout)
	defer cancel()

	stdout := &cappedWriter{max: c.opts.MaxLogBytes}
	stderr := &cappedWriter{max: c.opts.MaxLogBytes}
	fsConfig := wazero.NewFSConfig().
		WithFSMount(os.DirFS(inDir), guestInDir). // read-only, confined
		WithDirMount(outDir, guestOutDir)         // writable
	modConfig := wazero.NewModuleConfig().
		WithArgs("mobitool", "-e", "-o", guestOutDir, guestInDir+"/"+guestInputName).
		WithFSConfig(fsConfig).
		WithStdout(stdout).
		WithStderr(stderr).
		WithSysWalltime().
		WithName("")

	mod, instErr := c.runtime.InstantiateModule(runCtx, c.compiled, modConfig)
	if mod != nil {
		_ = mod.Close(runCtx)
	}
	combined := stdout.String() + "\n" + stderr.String()

	if err := classifyRunError(ctx, runCtx, instErr, c.opts.Timeout, combined); err != nil {
		return err
	}
	// DRM and unconvertible layouts are reported on stdout with exit 0.
	if matchesAnyLine(combined, drmMarkers) {
		return ErrDRMProtected
	}
	if lineContains(combined, printReplicaMarker) {
		return fmt.Errorf("%w: print-replica (fixed-layout) book is not convertible", ErrConversionFailed)
	}

	producedEpub := filepath.Join(outDir, guestEpubName)
	if fi, err := os.Stat(producedEpub); err != nil {
		return fmt.Errorf("%w: no EPUB produced: %s", ErrConversionFailed, trim(combined))
	} else if fi.Size() > c.opts.MaxOutputBytes {
		return fmt.Errorf("%w: output EPUB exceeds %d bytes", ErrConversionFailed, c.opts.MaxOutputBytes)
	}
	if err := validateEpub(producedEpub); err != nil {
		return fmt.Errorf("%w: %v", ErrConversionFailed, err)
	}
	if err := moveFile(producedEpub, dstPath); err != nil {
		return fmt.Errorf("%w: deliver output: %v", ErrConversionFailed, err)
	}
	return nil
}

// classifyRunError maps a wazero run error to our error taxonomy. Context
// errors (timeout/cancel) MUST be checked before a generic nonzero exit code,
// because WithCloseOnContextDone surfaces them as *sys.ExitError with special
// codes that would otherwise read as an absurd "exit code".
//
// Ordering matters: the *caller's* context is checked first so a request-level
// cancel OR deadline is propagated verbatim (never reclassified as a conversion
// verdict — that would both mislead upstream cancellation and poison the
// negative cache). Only once the caller's context is healthy do we attribute a
// deadline to our own per-call timeout, which is a transient ErrConversionTimedOut.
func classifyRunError(parent, run context.Context, instErr error, timeout time.Duration, combined string) error {
	if instErr == nil {
		return nil
	}
	// Caller's context ended (cancel or deadline) → propagate it verbatim.
	if parent.Err() != nil {
		return parent.Err()
	}
	// Our per-call timeout fired (caller's context is still healthy).
	if errors.Is(run.Err(), context.DeadlineExceeded) ||
		errors.Is(instErr, context.DeadlineExceeded) {
		return fmt.Errorf("%w: after %s", ErrConversionTimedOut, timeout)
	}
	if errors.Is(instErr, context.Canceled) {
		return context.Canceled
	}
	var exitErr *sys.ExitError
	if errors.As(instErr, &exitErr) {
		switch exitErr.ExitCode() {
		case sys.ExitCodeDeadlineExceeded:
			return fmt.Errorf("%w: after %s", ErrConversionTimedOut, timeout)
		case sys.ExitCodeContextCanceled:
			return context.Canceled
		case 0:
			return nil // normal exit; output checks follow
		default:
			return fmt.Errorf("%w: mobitool exit %d: %s", ErrConversionFailed, exitErr.ExitCode(), trim(combined))
		}
	}
	return fmt.Errorf("%w: run: %v", ErrConversionFailed, instErr)
}

// validateEpub confirms a well-formed EPUB OCF container: a zip whose first
// entry is a STORED (uncompressed) "mimetype" holding exactly
// application/epub+zip, with a META-INF/container.xml that names an OPF
// rootfile present in the archive.
func validateEpub(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open epub: %w", err)
	}
	defer zr.Close()

	if len(zr.File) == 0 {
		return errors.New("empty epub")
	}
	first := zr.File[0]
	if first.Name != "mimetype" {
		return fmt.Errorf("first entry is %q, want mimetype", first.Name)
	}
	if first.Method != zip.Store {
		return errors.New("mimetype is not stored (uncompressed)")
	}
	rc, err := first.Open()
	if err != nil {
		return fmt.Errorf("read mimetype: %w", err)
	}
	mt, err := io.ReadAll(io.LimitReader(rc, 64))
	rc.Close()
	if err != nil {
		return fmt.Errorf("read mimetype: %w", err)
	}
	if string(mt) != "application/epub+zip" {
		return fmt.Errorf("mimetype is %q", string(mt))
	}

	names := make(map[string]bool, len(zr.File))
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["META-INF/container.xml"] {
		return errors.New("missing META-INF/container.xml")
	}
	rootfile, err := opfRootfile(zr)
	if err != nil {
		return err
	}
	if !names[rootfile] {
		return fmt.Errorf("OPF rootfile %q missing from archive", rootfile)
	}
	return nil
}

func opfRootfile(zr *zip.ReadCloser) (string, error) {
	for _, f := range zr.File {
		if f.Name != "META-INF/container.xml" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("open container.xml: %w", err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, 64<<10))
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("read container.xml: %w", err)
		}
		var c struct {
			Rootfiles []struct {
				FullPath string `xml:"full-path,attr"`
			} `xml:"rootfiles>rootfile"`
		}
		if err := xml.Unmarshal(data, &c); err != nil {
			return "", fmt.Errorf("parse container.xml: %w", err)
		}
		if len(c.Rootfiles) == 0 || c.Rootfiles[0].FullPath == "" {
			return "", errors.New("container.xml has no rootfile")
		}
		return c.Rootfiles[0].FullPath, nil
	}
	return "", errors.New("missing META-INF/container.xml")
}

// matchesAnyLine reports whether any line of s contains any of the markers.
func matchesAnyLine(s string, markers []string) bool {
	for _, line := range strings.Split(s, "\n") {
		for _, m := range markers {
			if strings.Contains(line, m) {
				return true
			}
		}
	}
	return false
}

func lineContains(s, marker string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// cappedWriter buffers up to max bytes and silently drops the rest, always
// reporting a full write so the guest is never blocked on a full pipe.
type cappedWriter struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); room > 0 {
		if room >= len(p) {
			w.buf.Write(p)
		} else {
			w.buf.Write(p[:room])
			w.truncated = true
		}
	} else if len(p) > 0 {
		w.truncated = true
	}
	return len(p), nil
}

func (w *cappedWriter) String() string { return w.buf.String() }

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// moveFile delivers src to dst atomically: rename when possible, else copy to a
// temp file in dst's directory, fsync, and rename — never leaving a partial dst.
func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".move-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	in, err := os.Open(src)
	if err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	_, copyErr := io.Copy(tmp, in)
	in.Close()
	if copyErr == nil {
		copyErr = tmp.Sync()
	}
	if cerr := tmp.Close(); copyErr == nil {
		copyErr = cerr
	}
	if copyErr != nil {
		os.Remove(tmpName)
		return copyErr
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Remove(src)
}

func trim(s string) string {
	const max = 500
	s = strings.TrimSpace(s)
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
