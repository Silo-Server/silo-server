package subtitles

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// unsupportedTimingLogged remembers subtitle ids already warned about, so a
// row whose timing cannot be applied logs once per process, not per request.
var unsupportedTimingLogged sync.Map

// DeliveryBytes returns the bytes a client receives for a stored subtitle:
// data (the stored, immutable bytes) with the row's timing correction applied.
// Identity timing returns data unchanged. A format Retime cannot rewrite is
// delivered as stored; that row should never carry timing, so it is logged.
func DeliveryBytes(sub *DownloadedSubtitle, data []byte) ([]byte, error) {
	if sub == nil || sub.Timing.IsIdentity() {
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

// GetDeliveryContent reads sub's stored bytes and applies its timing
// correction. Admin downloads and content identity use the raw bytes instead.
func (m *Manager) GetDeliveryContent(ctx context.Context, sub *DownloadedSubtitle) ([]byte, error) {
	data, err := m.blobs.Get(ctx, sub.S3Key)
	if err != nil {
		return nil, fmt.Errorf("fetch subtitle content: %w", err)
	}
	return DeliveryBytes(sub, data)
}
