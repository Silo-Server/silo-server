package auth

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Silo-Server/silo-server/internal/models"
)

// The device headers a client sends to identify itself. They match the
// headers the settings routes read (internal/api/handlers/settings.go).
const (
	ClientDeviceIDHeader       = "X-Silo-Device-Id"
	ClientDeviceNameHeader     = "X-Silo-Device-Name"
	ClientDevicePlatformHeader = "X-Silo-Device-Platform"
)

// The bounds the settings device headers are clamped to.
const (
	maxClientDeviceIDLen       = 128
	maxClientDeviceNameLen     = 120
	maxClientDevicePlatformLen = 40
)

// clientDeviceIDShape is the identifier v2 accepts in X-Silo-Device-Id
// (internal/apiv2/device_header.go): a UUID or an opaque token of letters,
// digits, dot, underscore, colon and hyphen.
var clientDeviceIDShape = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// ClientDevice is the device a request says it comes from. Every field is
// client-reported: it is audit and display data and must never authorize
// anything.
type ClientDevice struct {
	ID       string
	Name     string
	Platform string
}

// NewClientDevice normalizes client-reported device values. The name and
// platform are trimmed and clamped like the settings device headers. An id
// that is too long or not shaped like a device identifier is dropped rather
// than truncated, since a shortened id would name a different device.
func NewClientDevice(id, name, platform string) ClientDevice {
	id = strings.TrimSpace(id)
	if len(id) > maxClientDeviceIDLen || !clientDeviceIDShape.MatchString(id) {
		id = ""
	}
	return ClientDevice{
		ID:       id,
		Name:     clampClientDeviceValue(name, maxClientDeviceNameLen),
		Platform: clampClientDeviceValue(platform, maxClientDevicePlatformLen),
	}
}

// ClientDeviceFromHeaders reads the X-Silo-Device-* headers of a request.
func ClientDeviceFromHeaders(h http.Header) ClientDevice {
	return NewClientDevice(h.Get(ClientDeviceIDHeader), h.Get(ClientDeviceNameHeader), h.Get(ClientDevicePlatformHeader))
}

// clampClientDeviceValue trims and clamps a reported name or platform. A value
// that is not valid UTF-8 is dropped: header values may carry arbitrary
// high bytes, and Postgres rejects them in a text column, which would fail
// the sign-in that records them.
func clampClientDeviceValue(value string, maxLen int) string {
	if !utf8.ValidString(value) {
		return ""
	}
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maxLen {
		return value
	}
	return strings.TrimSpace(string(runes[:maxLen]))
}

type clientDeviceKey struct{}

// WithClientDevice returns ctx carrying the device the request reported in
// its X-Silo-Device-* headers. A login session opened under ctx records that
// device (see SessionRepository.Create). Sign-in transports call it; a
// request without device headers leaves ctx unchanged.
func WithClientDevice(ctx context.Context, h http.Header) context.Context {
	device := ClientDeviceFromHeaders(h)
	if device == (ClientDevice{}) {
		return ctx
	}
	return context.WithValue(ctx, clientDeviceKey{}, device)
}

// ClientDeviceFromContext returns the device WithClientDevice attached, or
// the zero value.
func ClientDeviceFromContext(ctx context.Context) ClientDevice {
	device, _ := ctx.Value(clientDeviceKey{}).(ClientDevice)
	return device
}

// applyClientDevice fills a new session's device from the request that opens
// it. The device's own report wins over what the caller derived, which is the
// User-Agent or, for a device sign-in, the name the device gave when it
// started; a value the device did not send leaves the caller's in place.
func applyClientDevice(ctx context.Context, session *models.AuthSession) {
	device := ClientDeviceFromContext(ctx)
	if device.ID != "" {
		session.DeviceID = device.ID
	}
	if device.Name != "" {
		session.DeviceName = device.Name
	}
	if device.Platform != "" {
		session.DevicePlatform = device.Platform
	}
}
