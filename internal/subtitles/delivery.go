package subtitles

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
)

// unsupportedTimingLogged remembers subtitle ids already warned about, so a
// row whose timing cannot be applied logs once per process, not per request.
var unsupportedTimingLogged sync.Map

// DeliveryBytes returns the bytes a client receives for a stored subtitle:
// data (the stored, immutable bytes) as UTF-8 (DecodeText, using the row's
// language) with the row's timing correction applied. A format Retime cannot
// rewrite gets no correction; that row should never carry timing, so it is
// logged.
func DeliveryBytes(sub *DownloadedSubtitle, data []byte) ([]byte, error) {
	if sub == nil {
		return data, nil
	}
	data = DecodeText(sub.Format, sub.Language, data)
	if sub.Timing.IsIdentity() {
		return data, nil
	}
	if !SupportsRetime(sub.Format) {
		if _, seen := unsupportedTimingLogged.LoadOrStore(sub.ID, struct{}{}); !seen {
			slog.Warn("subtitle timing ignored for unsupported format", "component", "subtitles",
				"subtitle_id", sub.ID, "format", sub.Format,
				"timing_offset_ms", sub.Timing.OffsetMS, "timing_scale", sub.Timing.Scale)
		}
		return data, nil
	}
	out, err := Retime(sub.Format, data, sub.Timing)
	if err != nil {
		return nil, fmt.Errorf("retime subtitle %d: %w", sub.ID, err)
	}
	return out, nil
}

// GetDeliveryContent reads sub's stored bytes and prepares them for delivery
// (DeliveryBytes). Admin downloads and content identity use the raw bytes instead.
func (m *Manager) GetDeliveryContent(ctx context.Context, sub *DownloadedSubtitle) ([]byte, error) {
	data, err := m.blobs.Get(ctx, sub.S3Key)
	if err != nil {
		return nil, fmt.Errorf("fetch subtitle content: %w", err)
	}
	return DeliveryBytes(sub, data)
}

// DeliveryRevision identifies the bytes DeliveryBytes produces for sub: they
// change with the row's revision and, through the suffix, with the delivery
// rules, so offline copies fetched before stored subtitles were delivered as
// UTF-8 are fetched again.
func DeliveryRevision(sub *DownloadedSubtitle) string {
	return strconv.FormatInt(sub.Revision, 10) + "-utf8"
}
