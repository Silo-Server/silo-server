package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Silo-Server/silo-server/internal/ebookconvert"
	"github.com/Silo-Server/silo-server/internal/models"
)

// ConversionHeader tells the client how a kindle-family read was served.
// "converted" => body is EPUB; "failed" => body is the raw original and the
// client should fall back to opening it externally. Absent => no conversion
// was attempted (feature off or non-kindle format), serve/behave as before.
const ConversionHeader = "X-Silo-Ebook-Conversion"

// EbookConverter produces (and caches) a converted EPUB for a source file.
// Implemented by *ebookconvert.Cache; an interface here for testability.
type EbookConverter interface {
	GetOrConvert(ctx context.Context, srcPath string, key ebookconvert.SourceKey) (string, error)
	// Lookup reports a cached conversion WITHOUT converting (used by HEAD so a
	// metadata probe never kicks off a full conversion): a path on hit, a
	// non-nil error for a negatively-cached source, else a miss.
	Lookup(key ebookconvert.SourceKey) (string, error, bool)
}

// EbookConversion bundles the converter with the admin-flag predicate. A nil
// *EbookConversion (or nil Converter) means the feature is off.
type EbookConversion struct {
	Converter EbookConverter
	// Enabled reports whether the admin flag is on for this request.
	Enabled func(ctx context.Context) bool
}

func (c *EbookConversion) active(ctx context.Context) bool {
	return c != nil && c.Converter != nil && c.Enabled != nil && c.Enabled(ctx)
}

// kindleReaderFormats are the Kindle-family formats the converter handles.
var kindleReaderFormats = map[string]bool{"mobi": true, "azw": true, "azw3": true}

func isKindleReaderFile(file *models.MediaFile) bool {
	return file != nil && kindleReaderFormats[ebookReaderFormat(file.FilePath, file.Container)]
}

// serveEbook serves an ebook file, converting Kindle-family formats to EPUB
// when the feature is enabled. On conversion failure it serves the raw original
// with the ConversionHeader=failed contract so the client opens it externally.
func (h *EbookReaderHandler) serveEbook(w http.ResponseWriter, r *http.Request, file *models.MediaFile) error {
	if h.Conversion.active(r.Context()) && isKindleReaderFile(file) {
		// Strong identity for both the conversion cache and the response ETag:
		// id + size + mtime (stat) + content hash + module version.
		key, statErr := ebookconvert.SourceKeyFromStat(file.ID, file.FilePath)
		if statErr != nil {
			return h.serveRawKindleFallback(w, r, file)
		}
		key.Checksum = file.FileHash

		// HEAD must not trigger a (possibly minute-long, 1 GiB) conversion. Answer
		// from cache only; on a miss, advertise the converted representation
		// cheaply and let the GET produce the body + authoritative verdict.
		if r.Method == http.MethodHead {
			return h.serveKindleHead(w, r, file, key)
		}

		epubPath, err := h.Conversion.Converter.GetOrConvert(r.Context(), file.FilePath, key)
		switch {
		case err == nil:
			if serveErr := serveConvertedEpub(w, r, file, epubPath, key); serveErr != nil {
				// Produced but unservable (open/stat) — honor the contract, not a 500.
				return h.serveRawKindleFallback(w, r, file)
			}
			return nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			// Caller went away / request cancelled — not a conversion verdict.
			return err
		default:
			// DRM, corrupt, oversize, timed out, unavailable: fall back to the raw
			// original and tell the client so it can open externally.
			return h.serveRawKindleFallback(w, r, file)
		}
	}
	// Feature off or non-kindle format: serve the raw original with its native
	// MIME and no conversion header. This path deliberately omits the failed
	// contract — when conversion is disabled the capability endpoint already
	// reports `enabled:false`, so clients keep their external-open path and never
	// expect an EPUB here. (No representation flip to guard against.)
	return serveEbookInline(w, r, file)
}

// serveRawKindleFallback serves the raw original with the failed-conversion
// contract. no-store because the same URL flips raw/converted with the flag.
func (h *EbookReaderHandler) serveRawKindleFallback(w http.ResponseWriter, r *http.Request, file *models.MediaFile) error {
	w.Header().Set(ConversionHeader, "failed")
	w.Header().Set("Cache-Control", "no-store")
	return serveEbookInline(w, r, file)
}

// serveKindleHead answers a HEAD for a kindle-family file without converting:
//   - cache hit   → real converted headers (and ServeContent's Content-Length);
//   - neg-cached  → the failed contract (raw original headers);
//   - miss        → converted headers advertised optimistically (no body, no
//     Content-Length, no conversion). A HEAD is only a hint; the GET delivers
//     the body and the authoritative DRM/failure verdict, which then populates
//     the (positive or negative) cache that subsequent HEADs read.
func (h *EbookReaderHandler) serveKindleHead(w http.ResponseWriter, r *http.Request, file *models.MediaFile, key ebookconvert.SourceKey) error {
	path, lookupErr, ok := h.Conversion.Converter.Lookup(key)
	switch {
	case ok:
		if serveErr := serveConvertedEpub(w, r, file, path, key); serveErr != nil {
			return h.serveRawKindleFallback(w, r, file)
		}
		return nil
	case lookupErr != nil:
		return h.serveRawKindleFallback(w, r, file)
	default:
		setConvertedEpubHeaders(w, file, key)
		w.WriteHeader(http.StatusOK)
		return nil
	}
}

// setConvertedEpubHeaders writes the headers for a converted-EPUB response.
// Because the same URL can return raw or converted bytes depending on the
// flag/outcome, the ETag is derived from the exact conversion cache key (so a
// validator can never disagree with the cache) with a revalidate policy that
// keeps clients/proxies from serving a stale representation.
func setConvertedEpubHeaders(w http.ResponseWriter, file *models.MediaFile, key ebookconvert.SourceKey) {
	name := strings.TrimSuffix(filepath.Base(file.FilePath), filepath.Ext(file.FilePath)) + ".epub"
	w.Header().Set("Content-Type", "application/epub+zip")
	w.Header().Set("Content-Disposition", inlineContentDisposition(name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set(ConversionHeader, "converted")
	w.Header().Set("ETag", fmt.Sprintf("%q", "epubconv-"+key.CacheKey()))
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
}

// serveConvertedEpub serves the cached EPUB with EPUB headers (see
// setConvertedEpubHeaders). For a HEAD, ServeContent emits the headers and
// Content-Length without a body.
func serveConvertedEpub(w http.ResponseWriter, r *http.Request, file *models.MediaFile, epubPath string, key ebookconvert.SourceKey) error {
	f, err := os.Open(epubPath)
	if err != nil {
		return fmt.Errorf("opening converted epub: %w", err)
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat converted epub: %w", err)
	}

	name := strings.TrimSuffix(filepath.Base(file.FilePath), filepath.Ext(file.FilePath)) + ".epub"
	setConvertedEpubHeaders(w, file, key)
	http.ServeContent(w, r, name, stat.ModTime(), f)
	return nil
}

// ebookConversionCapability is the client-facing advertisement for the
// Kindle->EPUB feature. Clients flip mobi/azw/azw3 to in-app reading only when
// Enabled is true; otherwise they keep the external-open path.
type ebookConversionCapability struct {
	Enabled        bool     `json:"enabled"`
	SourceFormats  []string `json:"source_formats"`
	ServedFormat   string   `json:"served_format"`
	Header         string   `json:"header"`
	HeaderOnFailed string   `json:"header_failed_value"`
}

// HandleConversionCapability reports whether Kindle->EPUB conversion is active
// (admin flag on AND the converter initialized). GET /api/v1/ebooks/capability.
func (h *EbookReaderHandler) HandleConversionCapability(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, ebookConversionCapability{
		Enabled:        h.Conversion.active(r.Context()),
		SourceFormats:  []string{"mobi", "azw", "azw3"},
		ServedFormat:   "epub",
		Header:         ConversionHeader,
		HeaderOnFailed: "failed",
	})
}
