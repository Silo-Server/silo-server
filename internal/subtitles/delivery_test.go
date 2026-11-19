package subtitles

import (
	"bytes"
	"testing"
)

const deliverySRT = "1\r\n00:00:01,000 --> 00:00:02,500\r\nHello\r\n\r\n2\r\n00:00:03,000 --> 00:00:04,000\r\nWorld\r\n"

func TestDeliveryBytesUnsupportedFormatServesStoredBytes(t *testing.T) {
	data := []byte("{1}{2}microdvd")
	got, err := DeliveryBytes(&DownloadedSubtitle{ID: 8, Format: FormatSUB, Timing: Timing{OffsetMS: 100}}, data)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("unsupported format: %q %v", got, err)
	}
}
