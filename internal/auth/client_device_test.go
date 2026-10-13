package auth

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/models"
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

func TestWithClientDeviceAppliesReportedDeviceToSession(t *testing.T) {
	ctx := t.Context()
	if WithClientDevice(ctx, http.Header{"User-Agent": {"okhttp/4.12.0"}}) != ctx {
		t.Fatal("a request without device headers should leave the context unchanged")
	}

	h := http.Header{}
	h.Set(ClientDeviceIDHeader, "android-7f3c")
	h.Set(ClientDevicePlatformHeader, "android")
	session := models.AuthSession{DeviceName: "okhttp/4.12.0"}
	applyClientDevice(WithClientDevice(ctx, h), &session)
	if session.DeviceID != "android-7f3c" || session.DevicePlatform != "android" || session.DeviceName != "okhttp/4.12.0" {
		t.Fatalf("without a name header the caller's name stays: %+v", session)
	}

	h.Set(ClientDeviceNameHeader, "Google Pixel 8 Pro")
	session = models.AuthSession{DeviceName: "okhttp/4.12.0", DevicePlatform: "android-tv"}
	applyClientDevice(WithClientDevice(ctx, h), &session)
	if session.DeviceName != "Google Pixel 8 Pro" || session.DevicePlatform != "android" {
		t.Fatalf("reported values win over the caller's: %+v", session)
	}

	session = models.AuthSession{DeviceName: "Living Room TV", DevicePlatform: "tvos"}
	applyClientDevice(ctx, &session)
	if session.DeviceName != "Living Room TV" || session.DevicePlatform != "tvos" || session.DeviceID != "" {
		t.Fatalf("no reported device leaves the session alone: %+v", session)
	}
}
