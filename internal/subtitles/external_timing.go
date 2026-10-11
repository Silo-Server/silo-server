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
// with the correction stored for exactly these bytes applied, and a revision
// that changes whenever they do. It hashes the delivered bytes: a correction
// row recreated after a media file replacement restarts its revision, so the
// revision number alone could repeat for different bytes. Without a lookup
// or a format Retime can rewrite, data is returned unchanged.
func ExternalDelivery(ctx context.Context, timings ExternalTimingLookup, mediaFileID int, format SubtitleFormat, data []byte) ([]byte, string, error) {
	sha := ContentSHA256(data)
	if timings == nil || !SupportsRetime(format) {
		return data, externalRevision(sha, nil), nil
	}
	row, err := timings.ExternalTiming(ctx, mediaFileID, sha)
	if err != nil {
		return nil, "", fmt.Errorf("look up sidecar subtitle timing: %w", err)
	}
	if row == nil || row.Timing.IsIdentity() {
		return data, externalRevision(sha, row), nil
	}
	out, err := Retime(format, data, row.Timing)
	if err != nil {
		return nil, "", fmt.Errorf("retime sidecar subtitle: %w", err)
	}
	return out, externalRevision(ContentSHA256(out), row), nil
}

// ExternalDeliveryBytes is ExternalDelivery without the revision.
func ExternalDeliveryBytes(ctx context.Context, timings ExternalTimingLookup, mediaFileID int, format SubtitleFormat, data []byte) ([]byte, error) {
	out, _, err := ExternalDelivery(ctx, timings, mediaFileID, format, data)
	return out, err
}

func externalRevision(sha string, row *ExternalTiming) string {
	var revision int64
	if row != nil {
		revision = row.Revision
	}
	return sha[:16] + "-" + strconv.FormatInt(revision, 10)
}
