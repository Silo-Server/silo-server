package notifications

import (
	"cmp"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"

	publicconfig "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/config"
	publicmanifest "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/manifest"

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
// present. An integer beyond maxSafePluginInteger is refused too.
func checkPluginSettings(channel PluginChannel, merged map[string]any) error {
	var fields []PluginSettingError
	if err := publicconfig.ValidateValue(channel.Schema, "destination", channel.CapabilityID, merged); err != nil {
		var invalid *jsonschema.ValidationError
		if !errors.As(err, &invalid) {
			// The schema itself doesn't compile. Install refuses such a form
			// and the channel listing skips a stored one, so waiting won't
			// fix it: the channel counts as not provided.
			return fmt.Errorf("%w: %w: %w", ErrPluginChannelUnavailable, ErrPluginChannelNotProvided, err)
		}
		collectPluginSettingErrors(invalid, &fields)
		if len(fields) == 0 {
			fields = append(fields, PluginSettingError{Code: pluginSettingInvalid, Message: "The settings don't fit this destination's form."})
		}
	}
	// An integer too large to carry is out of range whatever else the schema
	// says about it, such as not being one of a select's options.
	for _, key := range unsafePluginIntegers(channel, merged) {
		fields = slices.DeleteFunc(fields, func(f PluginSettingError) bool { return f.Field == key })
		fields = append(fields, PluginSettingError{Field: key, Code: "out_of_range", Message: "Out of the allowed range."})
	}
	if len(fields) == 0 {
		return nil
	}
	slices.SortFunc(fields, func(a, b PluginSettingError) int { return cmp.Compare(a.Field, b.Field) })
	return &PluginDestinationSettingsError{Fields: fields}
}

// maxSafePluginInteger is the largest integer a JSON or protobuf number
// carries exactly, 2^53-1. Settings reach a plugin as a protobuf Struct,
// whose numbers are doubles, so a larger integer would arrive rounded. It is
// the SDK's bound for manifest options and defaults.
const maxSafePluginInteger = publicmanifest.MaxSafeInteger

// unsafePluginIntegers returns the keys of integer fields whose value is
// beyond maxSafePluginInteger in magnitude.
func unsafePluginIntegers(channel PluginChannel, values map[string]any) []string {
	var schema struct {
		Properties map[string]pluginSchemaProperty `json:"properties"`
	}
	if json.Unmarshal([]byte(channel.Schema.GetJsonSchema()), &schema) != nil {
		return nil
	}
	var out []string
	for key, value := range values {
		var valueType string
		if json.Unmarshal(schema.Properties[key].Type, &valueType) != nil || valueType != "integer" {
			continue
		}
		var unsafe bool
		switch v := value.(type) {
		case float64:
			unsafe = v > maxSafePluginInteger || v < -maxSafePluginInteger
		case int64:
			unsafe = v > maxSafePluginInteger || v < -maxSafePluginInteger
		case json.Number:
			f, err := v.Float64()
			unsafe = err == nil && (f > maxSafePluginInteger || f < -maxSafePluginInteger)
		}
		if unsafe {
			out = append(out, key)
		}
	}
	return out
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
	// saved credential.
	WriteOnly []string `json:"write_only"`
	// unrecorded marks settings stored without a write_only record (a bare
	// settings object, or an envelope from a build that named the record
	// differently). Nothing tells which of their values were secret, so every
	// one is write-only. A save writes the record: what it replaced is
	// classified by the schema then current, and every other value stays
	// write-only.
	unrecorded bool
}

// isSecret reports whether key is write-only: the channel marks it secret,
// it was saved as secret, or the settings carry no record of which were.
func (p pluginSettings) isSecret(channel PluginChannel, key string) bool {
	return p.unrecorded || slices.Contains(channel.SecretFields, key) || slices.Contains(p.WriteOnly, key)
}

// isPublic reports whether key may be returned: the channel declares it
// public and it wasn't saved as secret. A nil channel has no public keys,
// and neither do settings without a write_only record.
func (p pluginSettings) isPublic(channel *PluginChannel, key string) bool {
	return channel != nil && !p.unrecorded && slices.Contains(channel.PublicFields, key) && !slices.Contains(p.WriteOnly, key)
}

// decodePluginSettings reads a decrypted settings envelope. One written
// before the secret record is the bare settings object; its fields are flat
// scalars, so a top-level "settings" object marks the envelope. Settings
// without a write_only array are unrecorded (pluginSettings.unrecorded).
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
			out.unrecorded = true
			out.WriteOnly = []string{}
		}
		return out, nil
	}
	values := map[string]any{}
	if err := json.Unmarshal(raw, &values); err != nil {
		return pluginSettings{}, err
	}
	return pluginSettings{Values: values, WriteOnly: []string{}, unrecorded: true}, nil
}

// maxPluginTextRunes bounds text a plugin wrote once it is stored or shown.
const maxPluginTextRunes = 256

// minEncodedSecretLen is the shortest secret whose encoded forms are masked
// too. An encoding of a short value is short enough to turn up in ordinary
// text, and masking it would garble messages for nothing.
const minEncodedSecretLen = 8

// redactPluginText makes text a plugin wrote (an error, a refusal, a send
// result) safe to store, return or log. Every such text goes through here.
//
// It is first made storable: valid UTF-8 without NUL or other control
// characters but newline and tab, at most maxPluginTextRunes runes, so
// PostgreSQL can't refuse it and a finalize can't fail over it. Then a
// plugin or its provider may quote a rejected credential, so every value of
// each settings map that may not be returned is masked, as written and, when
// long enough, base64 (standard and URL alphabets, padded or not), URL-escaped
// and hex encoded. That covers secret fields, fields saved as secret, and
// stored fields the current schema no longer declares. Last, credential
// assignments and URL userinfo are masked as everywhere else. A nil channel
// masks every value.
func redactPluginText(text string, channel *PluginChannel, settings ...pluginSettings) string {
	text = storablePluginText(text)
	if text == "" {
		return text
	}
	var secrets []string
	for _, stored := range settings {
		for key, value := range stored.Values {
			if stored.isPublic(channel, key) {
				continue
			}
			secrets = append(secrets, pluginSecretForms(pluginSecretText(value))...)
		}
	}
	// Longest first, so a secret containing another is masked whole.
	slices.SortFunc(secrets, func(a, b string) int { return cmp.Compare(len(b), len(a)) })
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, logredact.Placeholder)
	}
	return truncateRunes(logredact.SanitizeText(text), maxPluginTextRunes)
}

// storablePluginText drops what PostgreSQL text can't hold or a reader
// shouldn't see: invalid UTF-8, NUL and other control characters.
func storablePluginText(text string) string {
	text = strings.ToValidUTF8(text, "")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			return r
		}
		return -1
	}, text)
}

func truncateRunes(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}

// pluginSecretForms is how a secret can appear in text: as stored and
// without its surrounding whitespace and, for each of those that is at least
// minEncodedSecretLen bytes, in the encodings a provider or plugin is likely to
// quote them in. A credential pasted with a stray space is sent with it, and
// a plugin may trim it first.
func pluginSecretForms(secret string) []string {
	trimmed := strings.TrimSpace(secret)
	if trimmed == "" {
		return nil
	}
	plain := []string{secret}
	if trimmed != secret {
		plain = append(plain, trimmed)
	}
	forms := slices.Clone(plain)
	for _, value := range plain {
		// Each spelling is judged on its own length: the stored one can
		// reach the minimum when the trimmed one doesn't.
		if len(value) < minEncodedSecretLen {
			continue
		}
		raw := []byte(value)
		for _, form := range []string{
			base64.StdEncoding.EncodeToString(raw),
			base64.RawStdEncoding.EncodeToString(raw),
			base64.URLEncoding.EncodeToString(raw),
			base64.RawURLEncoding.EncodeToString(raw),
			url.QueryEscape(value),
			url.PathEscape(value),
			hex.EncodeToString(raw),
			strings.ToUpper(hex.EncodeToString(raw)),
		} {
			if !slices.Contains(forms, form) {
				forms = append(forms, form)
			}
		}
	}
	return forms
}

// pluginSecretText is how a setting value can appear in text. A boolean
// can't be a credential, and masking "true" would only garble messages.
func pluginSecretText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	}
	return ""
}
