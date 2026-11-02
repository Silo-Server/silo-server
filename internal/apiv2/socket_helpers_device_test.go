package apiv2

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A ticket keeps the device id sign-in would record, and nothing longer.
func TestSocketDeviceIDKeepsOnlyARecordableID(t *testing.T) {
	for _, tc := range []struct {
		name, header, want string
	}{
		{"uuid", "8d2f6a4e-3c1b-4e5f-9a7d-0b1c2d3e4f50", "8d2f6a4e-3c1b-4e5f-9a7d-0b1c2d3e4f50"},
		{"at the limit", strings.Repeat("a", 128), strings.Repeat("a", 128)},
		{"over the limit", strings.Repeat("a", 512<<10), ""},
		{"absent", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/v2/events/ws-ticket", nil)
			if tc.header != "" {
				r.Header.Set(deviceIDHeader, tc.header)
			}
			if got := socketDeviceID(r); got != tc.want {
				t.Fatalf("socketDeviceID = %.40q (len %d), want %.40q", got, len(got), tc.want)
			}
		})
	}
}
