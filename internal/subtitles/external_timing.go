package subtitles

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// ExternalTiming is the timing correction of a sidecar subtitle, a subtitle
// file next to the media that Silo never writes. It belongs to the sidecar's
// bytes: a sidecar edited or replaced on disk no longer matches it, while a
// renamed one still does.
type ExternalTiming struct {
	ID            int64
	MediaFileID   int
	ContentSHA256 string
	// Path is where the sidecar was last seen; a sync job reads it there.
	Path     string
	Format   SubtitleFormat
	Timing   Timing
	Revision int64
}

// ExternalTimingLookup finds the correction stored for a sidecar's bytes.
type ExternalTimingLookup interface {
	ExternalTiming(ctx context.Context, mediaFileID int, contentSHA256 string) (*ExternalTiming, error)
}

// ErrExternalTimingChanged reports that a sidecar's correction changed after
// the caller read it; the newer write wins.
var ErrExternalTimingChanged = errors.New("sidecar subtitle timing changed")

// ContentSHA256 is the hex SHA-256 that identifies subtitle bytes.
func ContentSHA256(data []byte) string { return subtitleContentHash(data) }

// ExternalDelivery returns the bytes a client receives for a sidecar, data
// as UTF-8 (DecodeText, using the sidecar's language) with the correction
// stored for exactly these bytes applied, and a revision that changes
// whenever they do. The correction is looked up by the bytes on disk, which
// is what sync jobs record it under. The revision hashes the delivered bytes:
// a correction row recreated after a media file replacement restarts its
// revision, so the revision number alone could repeat for different bytes.
// Without a lookup or a format Retime can rewrite, no correction applies.
func ExternalDelivery(ctx context.Context, timings ExternalTimingLookup, mediaFileID int, format SubtitleFormat, language string, data []byte) ([]byte, string, error) {
	text := DecodeText(format, language, data)
	if timings == nil || !SupportsRetime(format) {
		return text, externalRevision(text, nil), nil
	}
	row, err := timings.ExternalTiming(ctx, mediaFileID, ContentSHA256(data))
	if err != nil {
		return nil, "", fmt.Errorf("look up sidecar subtitle timing: %w", err)
	}
	if row == nil || row.Timing.IsIdentity() {
		return text, externalRevision(text, row), nil
	}
	out, err := Retime(format, text, row.Timing)
	if err != nil {
		return nil, "", fmt.Errorf("retime sidecar subtitle: %w", err)
	}
	return out, externalRevision(out, row), nil
}

// ExternalDeliveryBytes is ExternalDelivery without the revision.
func ExternalDeliveryBytes(ctx context.Context, timings ExternalTimingLookup, mediaFileID int, format SubtitleFormat, language string, data []byte) ([]byte, error) {
	out, _, err := ExternalDelivery(ctx, timings, mediaFileID, format, language, data)
	return out, err
}

func externalRevision(delivered []byte, row *ExternalTiming) string {
	sha := ContentSHA256(delivered)
	var revision int64
	if row != nil {
		revision = row.Revision
	}
	return sha[:16] + "-" + strconv.FormatInt(revision, 10)
}
