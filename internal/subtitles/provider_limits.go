package subtitles

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
)

const (
	// MaxProviderResponseSize caps how many bytes Silo reads from one subtitle
	// provider response, after any transport decompression. It leaves room for
	// a ZIP holding a few subtitles; extracted entries are held to MaxUploadSize.
	MaxProviderResponseSize = 16 << 20 // 16 MiB

	// maxProviderErrorBodySize caps the response body kept for a provider
	// error message.
	maxProviderErrorBodySize = 4 << 10 // 4 KiB
)

// ErrSubtitleTooLarge reports provider data over a size limit. Oversized data
// is rejected, never truncated.
var ErrSubtitleTooLarge = errors.New("subtitle data exceeds size limit")

// readLimited reads all of r, failing with ErrSubtitleTooLarge instead of
// buffering more than limit bytes.
func readLimited(r io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: more than %d bytes", ErrSubtitleTooLarge, limit)
	}
	return data, nil
}

// ReadProviderResponse reads a provider response body of at most
// MaxProviderResponseSize bytes.
func ReadProviderResponse(body io.Reader) ([]byte, error) {
	return readLimited(body, MaxProviderResponseSize)
}

// LimitProviderResponse bounds a provider response body that is decoded as a
// stream. A body cut off at MaxProviderResponseSize fails to decode.
func LimitProviderResponse(body io.Reader) io.Reader {
	return io.LimitReader(body, MaxProviderResponseSize)
}

// ProviderErrorBody returns the start of a failed provider response for an
// error message, reading no more than a few KiB.
func ProviderErrorBody(body io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(body, maxProviderErrorBodySize))
	return string(data)
}

// ExtractSubtitleFromZip returns the first subtitle entry in a ZIP archive.
// An entry larger than MaxUploadSize fails with ErrSubtitleTooLarge, and
// decompression stops once the limit is passed.
func ExtractSubtitleFromZip(data []byte) ([]byte, SubtitleFormat, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, "", fmt.Errorf("open zip: %w", err)
	}

	for _, f := range reader.File {
		format, err := FormatFromFilename(f.Name)
		if err != nil {
			continue
		}
		if f.UncompressedSize64 > MaxUploadSize {
			return nil, "", fmt.Errorf("zip entry %q: %w: more than %d bytes", f.Name, ErrSubtitleTooLarge, MaxUploadSize)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, "", fmt.Errorf("open zip entry: %w", err)
		}
		// archive/zip already fails reads past the declared size; this keeps
		// the limit from depending on that.
		content, err := readLimited(rc, MaxUploadSize)
		_ = rc.Close()
		if err != nil {
			return nil, "", fmt.Errorf("read zip entry %q: %w", f.Name, err)
		}
		return content, format, nil
	}

	return nil, "", fmt.Errorf("no subtitle file found in zip archive")
}
