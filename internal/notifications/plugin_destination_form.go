package notifications

import (
	"encoding/json"
	"slices"
	"strconv"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// PluginFormField is one field of a destination settings form as clients
// render it: the form field the plugin declares, with the value type and the
// checks its JSON Schema property sets, which the host enforces on save
// (checkPluginSettings). A destination form is one flat list of these
// (manifest.ValidateDestinationSchema).
type PluginFormField struct {
	Key         string
	Label       string
	Description string
	Placeholder string
	// Control is text, password, number, switch or select.
	Control string
	// Type is the value's JSON type: string, integer, number or boolean.
	Type     string
	Required bool
	// Secret marks a write-only field: never returned, and left blank on an
	// edit to keep the stored value.
	Secret bool
	// Default is the field's default value, nil when it has none.
	Default   any
	Options   []PluginFormOption
	Minimum   *float64
	Maximum   *float64
	MinLength *int
	MaxLength *int
	Pattern   string
}

// PluginFormOption is one static choice of a select field. Value is typed
// as the field's Type, like Default.
type PluginFormOption struct {
	Value any
	Label string
}

var pluginFormControls = map[pluginv1.AdminFormControl]string{
	pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_TEXT:     "text",
	pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD: "password",
	pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_NUMBER:   "number",
	pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SWITCH:   "switch",
	pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SELECT:   "select",
}

// pluginSchemaProperty is what a flat destination schema may say about one
// field.
type pluginSchemaProperty struct {
	Type      json.RawMessage `json:"type"`
	Default   any             `json:"default"`
	Minimum   *float64        `json:"minimum"`
	Maximum   *float64        `json:"maximum"`
	MinLength *int            `json:"minLength"`
	MaxLength *int            `json:"maxLength"`
	Pattern   string          `json:"pattern"`
}

// FormFields describes the channel's destination form, in the plugin's
// field order. A schema that doesn't parse yields the form fields alone:
// install refuses such a schema, and saving checks it again.
func (c PluginChannel) FormFields() []PluginFormField {
	var schema struct {
		Required   []string                        `json:"required"`
		Properties map[string]pluginSchemaProperty `json:"properties"`
	}
	_ = json.Unmarshal([]byte(c.Schema.GetJsonSchema()), &schema)
	fields := c.Schema.GetAdminForm().GetFields()
	out := make([]PluginFormField, 0, len(fields))
	for _, field := range fields {
		property := schema.Properties[field.GetKey()]
		control, ok := pluginFormControls[field.GetControl()]
		if !ok {
			control = "text"
		}
		var valueType string
		if json.Unmarshal(property.Type, &valueType) != nil || valueType == "" {
			valueType = "string"
		}
		item := PluginFormField{
			Key:         field.GetKey(),
			Label:       field.GetLabel(),
			Description: field.GetDescription(),
			Placeholder: field.GetPlaceholder(),
			Control:     control,
			Type:        valueType,
			Required:    field.GetRequired() || slices.Contains(schema.Required, field.GetKey()),
			Secret:      slices.Contains(c.SecretFields, field.GetKey()),
			Default:     property.Default,
			Minimum:     property.Minimum,
			Maximum:     property.Maximum,
			MinLength:   property.MinLength,
			MaxLength:   property.MaxLength,
			Pattern:     property.Pattern,
		}
		if field.GetDefaultValue() != nil {
			item.Default = field.GetDefaultValue().AsInterface()
		}
		for _, option := range field.GetOptions() {
			item.Options = append(item.Options, PluginFormOption{Value: typedOptionValue(option.GetValue(), valueType), Label: option.GetLabel()})
		}
		out = append(out, item)
	}
	return out
}

// typedOptionValue reads a select option, which a manifest carries as a
// string, as the field's JSON type. A value that doesn't read as that type
// stays a string; saving checks it against the schema either way.
func typedOptionValue(value, valueType string) any {
	switch valueType {
	case "integer":
		if i, err := strconv.ParseInt(value, 10, 64); err == nil {
			return i
		}
	case "number":
		if f, err := strconv.ParseFloat(value, 64); err == nil {
			return f
		}
	case "boolean":
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return value
}
