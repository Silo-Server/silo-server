package watchsync

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// ConnectionSettingType is the value type of a provider-declared connection
// setting.
type ConnectionSettingType string

// ConnectionSettingTypeBoolean is an on/off switch.
const ConnectionSettingTypeBoolean ConnectionSettingType = "boolean"

// ConnectionSetting is a preference a provider declares that a profile can
// change on its connection at any time, such as whether Simkl logs rewatches.
// Unlike the connection config an API-key provider collects when a profile
// connects, its value is stored with the connection and sent to the provider
// on every authenticated call.
type ConnectionSetting struct {
	Key         string
	Label       string
	Description string
	Type        ConnectionSettingType
	// Default is the value of a connection that has not changed the setting:
	// a bool for a boolean setting.
	Default any
}

type connectionSettingsProvider interface {
	ConnectionSettings() []ConnectionSetting
}

func providerConnectionSettings(provider Provider) []ConnectionSetting {
	if declared, ok := provider.(connectionSettingsProvider); ok {
		return declared.ConnectionSettings()
	}
	return nil
}

// InvalidConnectionSettingError reports an update to a setting the provider
// does not declare, or with a value of the wrong type.
type InvalidConnectionSettingError struct {
	Key    string
	Reason string
}

func (e InvalidConnectionSettingError) Error() string {
	return fmt.Sprintf("connection setting %q %s", e.Key, e.Reason)
}

// validateConnectionSettingsUpdate checks that every key in values is a
// setting the provider declares and that its value has the declared type.
func validateConnectionSettingsUpdate(declared []ConnectionSetting, values map[string]any) error {
	for key, value := range values {
		setting, ok := findConnectionSetting(declared, key)
		if !ok {
			return InvalidConnectionSettingError{Key: key, Reason: "is not a setting of this provider"}
		}
		switch setting.Type {
		case ConnectionSettingTypeBoolean:
			if _, ok := value.(bool); !ok {
				return InvalidConnectionSettingError{Key: key, Reason: "must be true or false"}
			}
		default:
			return InvalidConnectionSettingError{Key: key, Reason: "has a type this server does not support"}
		}
	}
	return nil
}

func findConnectionSetting(declared []ConnectionSetting, key string) (ConnectionSetting, bool) {
	for _, setting := range declared {
		if setting.Key == key {
			return setting, true
		}
	}
	return ConnectionSetting{}, false
}

// encodeConnectionSettingValue converts a validated value to the plugin
// contract's string encoding: strings as they are, other values as JSON, so a
// boolean is "true" or "false".
func encodeConnectionSettingValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// connectionSettingValues returns the value of every declared setting, as
// stored for the connection or the setting's default, typed for the API: a
// bool for a boolean setting. A stored value that no longer parses as the
// declared type falls back to the default.
func connectionSettingValues(declared []ConnectionSetting, stored map[string]string) map[string]any {
	values := make(map[string]any, len(declared))
	for _, setting := range declared {
		switch setting.Type {
		case ConnectionSettingTypeBoolean:
			value, _ := setting.Default.(bool)
			if raw, ok := stored[setting.Key]; ok {
				if parsed, err := strconv.ParseBool(raw); err == nil {
					value = parsed
				}
			}
			values[setting.Key] = value
		}
	}
	return values
}

// encodedConnectionSettings returns connectionSettingValues in the plugin
// contract's string encoding.
func encodedConnectionSettings(declared []ConnectionSetting, stored map[string]string) map[string]string {
	values := connectionSettingValues(declared, stored)
	encoded := make(map[string]string, len(values))
	for key, value := range values {
		encoded[key] = encodeConnectionSettingValue(value)
	}
	return encoded
}

func encodeStoredConnectionSettings(settings map[string]string) []byte {
	return encodeSyncCursors(settings)
}

func decodeStoredConnectionSettings(data []byte) map[string]string {
	return decodeSyncCursors(data)
}

// encodeConnectionSettingsPatch returns the JSON object an update merges into
// the stored settings.
func encodeConnectionSettingsPatch(values map[string]any) []byte {
	patch := make(map[string]string, len(values))
	for key, value := range values {
		patch[key] = encodeConnectionSettingValue(value)
	}
	return encodeSyncCursors(patch)
}
