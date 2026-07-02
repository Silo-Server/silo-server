// Package logsink provides a routing slog.Handler that persists logs to
// rotating files in addition to the console (stderr).
//
// Silo's application logging otherwise only ever reaches os.Stderr, so under
// Docker the sole record of a run lives in the daemon's json-file log, which
// is discarded when the container is recreated. logsink lets an operator opt
// into durable, self-rotating log files via SILO_LOG_FILE without touching the
// container entrypoint.
//
// Records are classified into a coarse Category using the same "component"
// signal opslog persists to the database (an explicit "component" attribute, or
// the "subsystem:" message prefix via opslog.InferComponent). When split output
// is enabled every record is written to the combined file AND to a per-category
// file (e.g. silo-jellycompat.log, silo-playback.log), so an operator chasing a
// transcode or Jellyfin-compat issue can tail one focused file instead of
// grepping the firehose.
//
// The Router is installed as the process's base handler, beneath the log_quiet
// filter (see cmd/silo/main.go). Consequently server.log_quiet suppresses
// records from the files exactly as it does from the console: quieting a noisy
// subsystem also empties its per-category file. This keeps file output an exact
// mirror of the console stream; operators who need a quieted subsystem captured
// durably should narrow log_quiet rather than rely on the split file.
package logsink

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/opslog"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Category is a coarse routing bucket for a log record. The set is intentionally
// small and stable: per-category files are only created for these buckets, so
// an unbounded component space (e.g. "notifications.webhooks.retry") can never
// spawn unbounded files. Anything unmapped falls back to CategoryApp, which has
// no dedicated file and lands only in the combined log + stderr.
type Category string

const (
	CategoryApp           Category = "app"
	CategoryJellycompat   Category = "jellycompat"
	CategoryPlayback      Category = "playback"
	CategoryAPI           Category = "api"
	CategoryScanner       Category = "scanner"
	CategoryMetadata      Category = "metadata"
	CategoryNotifications Category = "notifications"
)

// splitCategories are the categories that get their own file when Split is on.
// CategoryApp is deliberately excluded: it is the default bucket and would just
// duplicate the combined log.
var splitCategories = []Category{
	CategoryJellycompat,
	CategoryPlayback,
	CategoryAPI,
	CategoryScanner,
	CategoryMetadata,
	CategoryNotifications,
}

// componentPrefixCategory maps the leading dotted/underscored segment of a
// component name to a Category. Matching is longest-prefix-wins on "."-segments
// so "notifications.webhooks.retry" resolves via "notifications". Values come
// from two sources that already exist in the codebase: explicit
// slog.With("component", …) attributes and opslog.InferComponent's "subsystem:"
// message prefixes (see the log_quiet convention in internal/logfilter).
var componentPrefixCategory = map[string]Category{
	"jellycompat":   CategoryJellycompat,
	"ffmpeg":        CategoryPlayback,
	"play":          CategoryPlayback,
	"playback":      CategoryPlayback,
	"transcode":     CategoryPlayback,
	"api":           CategoryAPI,
	"admin":         CategoryAPI,
	"requests":      CategoryAPI,
	"webhook_sync":  CategoryAPI,
	"scanner":       CategoryScanner,
	"scan":          CategoryScanner,
	"autoscan":      CategoryScanner,
	"metadata":      CategoryMetadata,
	"collage":       CategoryMetadata,
	"people":        CategoryMetadata,
	"catalog":       CategoryMetadata,
	"library":       CategoryMetadata,
	"notifications": CategoryNotifications,
}

// CategoryForComponent maps a component name to its routing Category. It walks
// the "."-separated prefixes from longest to shortest so the most specific
// registered mapping wins, then falls back to CategoryApp.
func CategoryForComponent(component string) Category {
	component = strings.TrimSpace(strings.ToLower(component))
	if component == "" {
		return CategoryApp
	}
	if cat, ok := lookupCategory(component); ok {
		return cat
	}
	// opslog.InferComponent yields the whole message prefix before ':', which is
	// frequently a multi-word "subsystem detail" string (e.g. "jellycompat
	// autoscan", "person refresh"). Fall back to the leading whitespace token so
	// those still route to their subsystem file instead of the catch-all app log.
	if first := strings.Fields(component)[0]; first != component {
		if cat, ok := lookupCategory(first); ok {
			return cat
		}
	}
	return CategoryApp
}

// lookupCategory walks the "."-separated prefixes of component from longest to
// shortest and returns the first registered Category, so the most specific
// mapping wins (e.g. "notifications.webhooks" resolves via "notifications").
func lookupCategory(component string) (Category, bool) {
	segments := strings.Split(component, ".")
	for i := len(segments); i > 0; i-- {
		key := strings.Join(segments[:i], ".")
		if cat, ok := componentPrefixCategory[key]; ok {
			return cat, true
		}
	}
	return "", false
}

// Rotation controls lumberjack file rotation. A zero MaxSizeMB falls back to
// DefaultRotation.MaxSizeMB in New; MaxBackups and MaxAgeDays are passed through
// verbatim, so an explicit 0 keeps its lumberjack meaning (retain all backups /
// never expire by age). Callers that want defaults should start from
// DefaultRotation rather than the zero value.
type Rotation struct {
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
}

// DefaultRotation mirrors the settings already used by the Jellyfin-compat
// debug logger (internal/jellycompat/router.go) for consistency.
var DefaultRotation = Rotation{
	MaxSizeMB:  50,
	MaxBackups: 100,
	MaxAgeDays: 30,
	Compress:   true,
}

// Options configures New.
type Options struct {
	// Stderr is the console sink. It always receives every record and must be
	// non-nil; file sinks are layered on top of it.
	Stderr io.Writer
	// Format is "json" or "text"; it applies to both the console and files.
	Format string
	// Level gates every sink.
	Level slog.Leveler
	// File is the combined log file path. When empty, New returns a plain
	// console-only handler and a no-op closer — behaviour identical to today.
	File string
	// Split, when true, additionally writes a per-category file alongside the
	// combined file (silo-<category>.log in the combined file's directory).
	Split bool
	// Rotation tunes file rotation; zero fields fall back to DefaultRotation.
	Rotation Rotation
}

// New builds the base slog.Handler for the process. When opts.File is empty it
// returns a console-only handler (matching the pre-file-logging behaviour). The
// returned closer flushes and closes any file writers; callers should defer it.
func New(opts Options) (slog.Handler, io.Closer, error) {
	console := newFormatHandler(opts.Format, opts.Stderr, opts.Level)
	if strings.TrimSpace(opts.File) == "" {
		return console, noopCloser{}, nil
	}

	rot := opts.Rotation
	// Only MaxSizeMB is coalesced: a zero size is never meaningful. MaxBackups
	// and MaxAgeDays are passed through verbatim so an explicit 0 keeps its
	// lumberjack meaning (retain all backups / never expire by age); the env
	// layer already starts from DefaultRotation, so "unset" arrives here as the
	// defaults rather than as zero.
	if rot.MaxSizeMB == 0 {
		rot.MaxSizeMB = DefaultRotation.MaxSizeMB
	}

	// Collect every target path up front so we can fail fast (and let the caller
	// fall back to stderr-only) if the log directory is unwritable. lumberjack
	// otherwise opens files lazily on first write, which would surface a bad
	// path only at runtime, where slog discards Handle errors.
	paths := []string{opts.File}
	if opts.Split {
		dir := filepath.Dir(opts.File)
		stem := stem(opts.File)
		ext := filepath.Ext(opts.File)
		if ext == "" {
			ext = ".log"
		}
		for _, cat := range splitCategories {
			paths = append(paths, filepath.Join(dir, stem+"-"+string(cat)+ext))
		}
	}
	for _, p := range paths {
		if err := ensureWritable(p); err != nil {
			return nil, noopCloser{}, err
		}
	}

	var closers []io.Closer
	newFile := func(path string, only Category) dest {
		lj := &lumberjack.Logger{
			Filename:   path,
			MaxSize:    rot.MaxSizeMB,
			MaxBackups: rot.MaxBackups,
			MaxAge:     rot.MaxAgeDays,
			Compress:   rot.Compress,
		}
		closers = append(closers, lj)
		return dest{h: newFormatHandler(opts.Format, lj, opts.Level), only: only}
	}

	// The console is the only destination whose write error may propagate (see
	// Router.Handle); the combined file and split files are best-effort.
	dests := []dest{
		{h: console, console: true},
		newFile(opts.File, ""),
	}
	if opts.Split {
		for i, cat := range splitCategories {
			dests = append(dests, newFile(paths[i+1], cat))
		}
	}

	return &Router{dests: dests}, multiCloser(closers), nil
}

// ensureWritable makes the parent directory and confirms the log file can be
// opened for appending, so a misconfigured SILO_LOG_FILE fails at startup
// instead of silently no-opping at runtime.
func ensureWritable(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create log directory for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", path, err)
	}
	return f.Close()
}

// dest is one output destination. only == "" accepts every record; otherwise it
// accepts only records classified into that Category. console marks the single
// stderr destination whose write error may propagate out of Router.Handle.
type dest struct {
	h       slog.Handler
	only    Category
	console bool
}

// Router fans each record out to the console, the combined file, and (when
// split output is enabled) the file for the record's Category.
//
// Component classification mirrors opslog: an explicit top-level "component"
// attribute wins, otherwise the message prefix via opslog.InferComponent. Only
// top-level attributes count — a "component" nested inside a WithGroup is
// namespaced ("group.component") and intentionally ignored, matching how opslog
// keys its column.
type Router struct {
	dests []dest
	// staticComponent is the component carried by With("component", …) applied
	// to this handler, resolved only while at the top level (no active group).
	staticComponent string
	inGroup         bool
}

func (r *Router) Enabled(ctx context.Context, level slog.Level) bool {
	for i := range r.dests {
		if r.dests[i].h.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (r *Router) Handle(ctx context.Context, rec slog.Record) error {
	cat := r.classify(rec)
	var consoleErr error
	for i := range r.dests {
		d := r.dests[i]
		if d.only != "" && d.only != cat {
			continue
		}
		if !d.h.Enabled(ctx, rec.Level) {
			continue
		}
		err := d.h.Handle(ctx, rec)
		// File destinations are best-effort. Their write errors (ENOSPC,
		// read-only mount, rotation failure) must NOT propagate: this handler is
		// wrapped by opslog.Handler, which skips its DB/admin-stream capture
		// whenever its inner handler returns an error. Only the console error may
		// bubble up, so a file problem can never disable the other log paths —
		// preserving the pre-file-logging contract that logging never fails.
		if d.console && err != nil {
			consoleErr = err
		}
	}
	return consoleErr
}

func (r *Router) classify(rec slog.Record) Category {
	component := r.staticComponent
	// A top-level inline "component" attribute overrides one set via
	// With("component", …), matching opslog.Handler's last-write-wins precedence
	// so file and DB classification agree. Attributes inside a group are
	// namespaced ("group.component") and intentionally ignored, as opslog does.
	if !r.inGroup {
		rec.Attrs(func(a slog.Attr) bool {
			if a.Key == "component" {
				if s := a.Value.Resolve().String(); s != "" {
					component = s
					return false
				}
			}
			return true
		})
	}
	if component == "" {
		component = opslog.InferComponent(rec.Message)
	}
	return CategoryForComponent(component)
}

func (r *Router) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := &Router{
		dests:           make([]dest, len(r.dests)),
		staticComponent: r.staticComponent,
		inGroup:         r.inGroup,
	}
	for i := range r.dests {
		next.dests[i] = dest{h: r.dests[i].h.WithAttrs(attrs), only: r.dests[i].only, console: r.dests[i].console}
	}
	// A top-level component attribute pins the category for every subsequent
	// record from this logger; inside a group it would be namespaced, so skip.
	if !r.inGroup {
		for _, a := range attrs {
			if a.Key == "component" {
				if s := a.Value.Resolve().String(); s != "" {
					next.staticComponent = s
				}
			}
		}
	}
	return next
}

func (r *Router) WithGroup(name string) slog.Handler {
	if name == "" {
		return r
	}
	next := &Router{
		dests:           make([]dest, len(r.dests)),
		staticComponent: r.staticComponent,
		inGroup:         true,
	}
	for i := range r.dests {
		next.dests[i] = dest{h: r.dests[i].h.WithGroup(name), only: r.dests[i].only, console: r.dests[i].console}
	}
	return next
}

func newFormatHandler(format string, w io.Writer, level slog.Leveler) slog.Handler {
	opts := &slog.HandlerOptions{Level: level}
	if strings.EqualFold(format, "json") {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// stem returns the filename of path without its directory or final extension:
// "/var/log/silo/silo.log" -> "silo".
func stem(path string) string {
	base := filepath.Base(path)
	if ext := filepath.Ext(base); ext != "" {
		base = strings.TrimSuffix(base, ext)
	}
	if base == "" {
		return "silo"
	}
	return base
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

type multiCloser []io.Closer

func (m multiCloser) Close() error {
	var firstErr error
	for _, c := range m {
		if c == nil {
			continue
		}
		if err := c.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
