package jellycompat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	maxDeviceProfileRequestBytes = 1 << 20
	maxDeviceProfileBytes        = 256 << 10
	maxDeviceProfileEntries      = 1024
	maxDeviceIDBytes             = 256
	maxDeviceProfilesPerToken    = 64
	deviceProfilePayloadTooLarge = "PayloadTooLarge"
)

func readDeviceProfileRequest(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxDeviceProfileRequestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxDeviceProfileRequestBytes {
		return nil, &HTTPError{StatusCode: http.StatusRequestEntityTooLarge, Code: deviceProfilePayloadTooLarge, Message: "Device profile request is too large"}
	}
	return body, nil
}

// deviceProfileHashedIDPrefix marks a device identity stored as its digest.
const deviceProfileHashedIDPrefix = "sha256:"

// deviceProfileStorageID bounds the device identity a registration is stored
// under. Jellyfin Web derives its DeviceId from the browser's user agent, so a
// long user agent (embedded browsers, some TVs) exceeds the bound; Jellyfin
// accepts such IDs, so longer ones are keyed by their SHA-256 instead of being
// rejected. An ID that already looks like a stored digest is hashed as well,
// so it cannot share a key with a hashed long ID.
func deviceProfileStorageID(deviceID string) string {
	if len(deviceID) <= maxDeviceIDBytes && !strings.HasPrefix(deviceID, deviceProfileHashedIDPrefix) {
		return deviceID
	}
	sum := sha256.Sum256([]byte(deviceID))
	return deviceProfileHashedIDPrefix + hex.EncodeToString(sum[:])
}

func encodeDeviceProfile(profile DeviceProfile) ([]byte, error) {
	entries := len(profile.DirectPlayProfiles) + len(profile.TranscodingProfiles) + len(profile.ContainerProfiles) + len(profile.CodecProfiles) + len(profile.SubtitleProfiles)
	for _, p := range profile.TranscodingProfiles {
		entries += len(p.Conditions)
	}
	for _, p := range profile.ContainerProfiles {
		entries += len(p.Conditions)
	}
	for _, p := range profile.CodecProfiles {
		entries += len(p.Conditions) + len(p.ApplyConditions)
	}
	if entries > maxDeviceProfileEntries {
		return nil, &HTTPError{StatusCode: http.StatusRequestEntityTooLarge, Code: deviceProfilePayloadTooLarge, Message: "Device profile has too many entries"}
	}
	data, err := json.Marshal(profile)
	if err != nil {
		return nil, err
	}
	if len(data) > maxDeviceProfileBytes {
		return nil, &HTTPError{StatusCode: http.StatusRequestEntityTooLarge, Code: deviceProfilePayloadTooLarge, Message: "Device profile is too large"}
	}
	return data, nil
}

func deviceProfileQuotaError() error {
	return &HTTPError{StatusCode: http.StatusTooManyRequests, Code: "TooManyDevices", Message: "Too many device profiles registered for this token"}
}

func writeDeviceProfileRequestError(w http.ResponseWriter, err error, invalidMessage string) {
	if errors.Is(err, errDeviceProfileStore) {
		writeError(w, http.StatusServiceUnavailable, "Unavailable", "Device profile storage unavailable")
	} else if httpErr, ok := errors.AsType[*HTTPError](err); ok {
		writeError(w, httpErr.StatusCode, httpErr.Code, httpErr.Message)
	} else {
		writeError(w, http.StatusBadRequest, "BadRequest", invalidMessage)
	}
}
