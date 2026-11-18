package notifications

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	publicconfig "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/config"

	"github.com/Silo-Server/silo-server/internal/logredact"
)

// pluginSettingInvalid is the API field error code for a value that fails a
// rule with no more specific code.
const pluginSettingInvalid = "invalid"

// checkPluginSettings checks merged settings against the channel's JSON
// Schema with the SDK's config validator, the one plugin configuration uses.
// A destination schema is flat (manifest.ValidateDestinationSchema), so this
// covers declared scalar types, required fields, enum, string length and
// pattern, and numeric bounds; anything more is the plugin's
// ValidateDestination. mergePluginSettings has already refused undeclared
// fields, and a stored secret left blank is in merged, so it counts as
// present.
func checkPluginSettings(channel PluginChannel, merged map[string]any) error {
	err := publicconfig.ValidateValue(channel.Schema, "destination", channel.CapabilityID, merged)
	if err == nil {
		return nil
	}
	var invalid *jsonschema.ValidationError
	if !errors.As(err, &invalid) {
		// The schema itself doesn't compile: install refuses such a form,
		// so the channel is broken rather than the settings.
		return fmt.Errorf("%w: %w", ErrPluginChannelUnavailable, err)
	}
	var fields []PluginSettingError
	collectPluginSettingErrors(invalid, &fields)
	if len(fields) == 0 {
		fields = append(fields, PluginSettingError{Code: pluginSettingInvalid, Message: "The settings don't fit this destination's form."})
	}
	slices.SortFunc(fields, func(a, b PluginSettingError) int { return cmp.Compare(a.Field, b.Field) })
	return &PluginDestinationSettingsError{Fields: fields}
}

// collectPluginSettingErrors flattens a validation error into one entry per
// failing field. Only leaves name a failure; their parents group them.
func collectPluginSettingErrors(e *jsonschema.ValidationError, out *[]PluginSettingError) {
	if len(e.Causes) > 0 {
		for _, cause := range e.Causes {
			collectPluginSettingErrors(cause, out)
		}
		return
	}
	field := strings.Join(e.InstanceLocation, ".")
	switch k := e.ErrorKind.(type) {
	case *kind.Required:
		for _, missing := range k.Missing {
			*out = append(*out, PluginSettingError{Field: missing, Code: "required", Message: "This setting is required."})
		}
	case *kind.Type:
		*out = append(*out, PluginSettingError{Field: field, Code: "invalid_type", Message: "Expected a " + strings.Join(k.Want, " or ") + "."})
	case *kind.Enum:
		*out = append(*out, PluginSettingError{Field: field, Code: "invalid_enum", Message: "Not one of the allowed values."})
	case *kind.MinLength, *kind.MaxLength, *kind.Minimum, *kind.Maximum, *kind.ExclusiveMinimum, *kind.ExclusiveMaximum:
		*out = append(*out, PluginSettingError{Field: field, Code: "out_of_range", Message: "Out of the allowed range."})
	case *kind.Pattern:
		*out = append(*out, PluginSettingError{Field: field, Code: pluginSettingInvalid, Message: "Not in the expected format."})
	case *kind.AdditionalProperties:
		for _, extra := range k.Properties {
			*out = append(*out, PluginSettingError{Field: extra, Code: "unknown_field", Message: "Not a setting of this destination."})
		}
	default:
		*out = append(*out, PluginSettingError{Field: field, Code: pluginSettingInvalid, Message: "Not a valid value."})
	}
}

// pluginSettings is a destination's stored settings: the values, and the
// keys that were saved as secret.
type pluginSettings struct {
	Values map[string]any `json:"settings"`
	// WriteOnly names the keys saved as secret. They stay write-only whatever a
	// later manifest says, until the owner replaces or clears them, so a
	// plugin upgrade that drops a field's secret marking can't expose a
	// saved credential. nil for settings stored before the record existed,
	// which follow the current schema alone.
	WriteOnly []string `json:"write_only"`
}

// isSecret reports whether key is write-only: the channel marks it secret,
// or it was saved as secret.
func (p pluginSettings) isSecret(channel PluginChannel, key string) bool {
	return slices.Contains(channel.SecretFields, key) || slices.Contains(p.WriteOnly, key)
}

// isPublic reports whether key may be returned: the channel declares it
// public and it wasn't saved as secret. A nil channel has no public keys.
func (p pluginSettings) isPublic(channel *PluginChannel, key string) bool {
	return channel != nil && slices.Contains(channel.PublicFields, key) && !slices.Contains(p.WriteOnly, key)
}

// decodePluginSettings reads a decrypted settings envelope. One written
// before the secret record is the bare settings object; its fields are flat
// scalars, so a top-level "settings" object marks the current shape.
func decodePluginSettings(raw []byte) (pluginSettings, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return pluginSettings{}, err
	}
	if inner, ok := envelope["settings"]; ok && len(inner) > 0 && inner[0] == '{' {
		var out pluginSettings
		if err := json.Unmarshal(raw, &out); err != nil {
			return pluginSettings{}, err
		}
		if out.Values == nil {
			out.Values = map[string]any{}
		}
		if out.WriteOnly == nil {
			out.WriteOnly = []string{}
		}
		return out, nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return pluginSettings{}, err
	}
	return pluginSettings{Values: values}, nil
}

// redactPluginText masks text a plugin wrote (an error, a refusal, a send
// result) before it is stored, returned or logged. A plugin or its provider
// may quote a rejected credential, so every value of each settings map that
// may not be returned is masked, which covers secret fields, fields saved as
// secret, and stored fields the current schema no longer declares; then
// credential assignments and URL userinfo are masked as everywhere else. A
// nil channel masks every value.
func redactPluginText(text string, channel *PluginChannel, settings ...pluginSettings) string {
	if text == "" {
		return text
	}
	var secrets []string
	for _, stored := range settings {
		for key, value := range stored.Values {
			if stored.isPublic(channel, key) {
				continue
			}
			if secret := pluginSecretText(value); secret != "" {
				secrets = append(secrets, secret)
			}
		}
	}
	// Longest first, so a secret containing another is masked whole.
	slices.SortFunc(secrets, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, logredact.Placeholder)
	}
	return logredact.SanitizeText(text)
}

// pluginSecretText is how a setting value can appear in text. A boolean
// can't be a credential, and masking "true" would only garble messages.
func pluginSecretText(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}
