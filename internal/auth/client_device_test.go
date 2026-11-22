package auth

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNewClientDeviceClampsAndDropsMalformedIDs(t *testing.T) {
	longName := strings.Repeat("é", 200)
	for _, tc := range []struct {
		name               string
		id, dev, platform  string
		wantID, wantPlatfm string
		wantNameRunes      int
	}{
		{name: "uuid id", id: " 8d2f6a4e-3c1b-4e5f-9a7d-0b1c2d3e4f50 ", dev: " Pixel 8 Pro ", platform: "android", wantID: "8d2f6a4e-3c1b-4e5f-9a7d-0b1c2d3e4f50", wantPlatfm: "android", wantNameRunes: 11},
		{name: "joined repeated header", id: "abc,abc", wantID: ""},
		{name: "interior space", id: "abc def", wantID: ""},
		{name: "too long id is dropped, not cut", id: strings.Repeat("a", maxClientDeviceIDLen+1), wantID: ""},
		{name: "longest id kept", id: strings.Repeat("a", maxClientDeviceIDLen), wantID: strings.Repeat("a", maxClientDeviceIDLen)},
		{name: "invalid UTF-8 name and platform dropped", dev: "TV\xff\xfe", platform: "\xc3(", wantNameRunes: 0},
		{name: "long name and platform clamped by runes", dev: longName, platform: strings.Repeat("p", 60), wantPlatfm: strings.Repeat("p", maxClientDevicePlatformLen), wantNameRunes: maxClientDeviceNameLen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := NewClientDevice(tc.id, tc.dev, tc.platform)
			if got.ID != tc.wantID || got.Platform != tc.wantPlatfm || utf8.RuneCountInString(got.Name) != tc.wantNameRunes || !utf8.ValidString(got.Name) {
				t.Fatalf("NewClientDevice = %+v", got)
			}
		})
	}
}
