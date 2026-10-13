package historyimport

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Source servers are user-supplied, so every response an import reads is
// bounded: a hostile or broken server must not be able to make Silo buffer an
// unbounded body or page forever.
const (
	// maxUpstreamResponseBytes caps one decoded (decompressed) response. The
	// heaviest legitimate response is a Plex page of 500 section items or a
	// batch of full movie metadata, a few MiB at most; Jellyfin and Emby pages
	// request only a handful of fields.
	maxUpstreamResponseBytes int64 = 16 << 20
	// maxListingBytes caps the bytes one paged listing reads in total.
	maxListingBytes int64 = 512 << 20
	// maxListingItems and maxListingPages cap one paged listing. A very large
	// library or a decade of Plex plays is well under these; a server that
	// keeps reporting more rows than it ever delivers is not.
	maxListingItems = 500_000
	maxListingPages = 5_000
	// upstreamErrorBodyBytes caps how much of a non-2xx body is kept for
	// diagnostics.
	upstreamErrorBodyBytes = 2048
)

// ErrUpstreamResponseTooLarge reports a source server whose response, or
// paged listing, is larger than an import accepts. The run fails with
// RunErrorSourceTooLarge instead of importing a truncated listing.
var ErrUpstreamResponseTooLarge = errors.New("history import source response exceeds the import size limit")

// listingBudget tracks one paged listing against the listing limits. A nil
// budget applies only the per-response limit.
type listingBudget struct {
	bytes int64
	items int
	pages int
}

// startPage counts a page request, failing once the listing has used its
// page allowance.
func (b *listingBudget) startPage() error {
	if b.pages >= maxListingPages {
		return fmt.Errorf("%w: listing exceeds %d pages", ErrUpstreamResponseTooLarge, maxListingPages)
	}
	b.pages++
	return nil
}

// addItems counts the rows a page delivered.
func (b *listingBudget) addItems(n int) error {
	b.items += n
	if b.items > maxListingItems {
		return fmt.Errorf("%w: listing exceeds %d items", ErrUpstreamResponseTooLarge, maxListingItems)
	}
	return nil
}

func (b *listingBudget) responseLimit() int64 {
	if b == nil {
		return maxUpstreamResponseBytes
	}
	return max(0, min(maxUpstreamResponseBytes, maxListingBytes-b.bytes))
}

// decodeUpstreamJSON decodes one successful source response into out, reading
// at most the response limit (and what remains of budget, when given). A body
// past the limit fails with ErrUpstreamResponseTooLarge rather than decoding a
// truncated value.
func decodeUpstreamJSON(body io.Reader, out any, budget *listingBudget) error {
	return decodeLimitedJSON(body, out, budget, budget.responseLimit())
}

func decodeLimitedJSON(body io.Reader, out any, budget *listingBudget, limit int64) error {
	limited := &io.LimitedReader{R: body, N: limit + 1}
	err := json.NewDecoder(limited).Decode(out)
	if budget != nil {
		budget.bytes += limit + 1 - limited.N
	}
	if limited.N <= 0 {
		if budget != nil && limit < maxUpstreamResponseBytes {
			return fmt.Errorf("%w: listing exceeds %d bytes", ErrUpstreamResponseTooLarge, maxListingBytes)
		}
		return fmt.Errorf("%w: response exceeds %d bytes", ErrUpstreamResponseTooLarge, limit)
	}
	return err
}

// discardUpstreamBody drains a body whose content is not needed, so the
// connection can be reused, without reading past the response limit.
func discardUpstreamBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, maxUpstreamResponseBytes))
}

// readUpstreamErrorBody returns the start of a non-2xx body for diagnostics.
func readUpstreamErrorBody(body io.Reader) []byte {
	data, _ := io.ReadAll(io.LimitReader(body, upstreamErrorBodyBytes))
	return data
}
