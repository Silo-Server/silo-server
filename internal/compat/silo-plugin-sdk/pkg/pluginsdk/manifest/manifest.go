package manifest

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"

	"google.golang.org/protobuf/encoding/protojson"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/capability"
)

// knownCapabilityTypes is the set of capability type strings recognised by
// the manifest validator. Add new capability types here as they are defined.
//
// Note: capability types are recorded purely for discovery — the host exposes
// them via the admin plugins API so SPAs can filter by them client-side.
// They do not drive any dispatch behavior on the server side.
var knownCapabilityTypes = knownCapabilityTypeSet()

func knownCapabilityTypeSet() map[string]struct{} {
	out := make(map[string]struct{}, len(capability.KnownTypes))
	for _, typ := range capability.KnownTypes {
		out[typ] = struct{}{}
	}
	return out
}

func Load(data []byte) (*pluginv1.PluginManifest, error) {
	var manifest pluginv1.PluginManifest
	// Use DiscardUnknown so that fields defined outside the proto schema do
	// not cause a decode error. The proto-schema fields are still fully
	// decoded.
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode plugin manifest: %w", err)
	}
	if err := Validate(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func MustLoad(data []byte) *pluginv1.PluginManifest {
	manifest, err := Load(data)
	if err != nil {
		panic(err)
	}
	return manifest
}

func LoadFromDisk(path string) (*pluginv1.PluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read plugin manifest %q: %w", path, err)
	}
	manifest, err := Load(data)
	if err != nil {
		return nil, fmt.Errorf("load plugin manifest %q: %w", path, err)
	}
	return manifest, nil
}

// Validate validates the proto plugin manifest.
func Validate(manifest *pluginv1.PluginManifest) error {
	if manifest == nil {
		return fmt.Errorf("plugin manifest is required")
	}
	if manifest.PluginId == "" {
		return fmt.Errorf("plugin manifest plugin_id is required")
	}
	if manifest.Version == "" {
		return fmt.Errorf("plugin manifest version is required")
	}
	for _, capability := range manifest.Capabilities {
		if capability.Type == "" {
			return fmt.Errorf("plugin capability type is required")
		}
		if capability.Id == "" {
			return fmt.Errorf("plugin capability id is required")
		}
		if _, ok := knownCapabilityTypes[capability.Type]; !ok {
			return fmt.Errorf("plugin capability %q: unknown type %q", capability.Id, capability.Type)
		}
	}
	for _, schema := range manifest.GlobalConfigSchema {
		if err := validateConfigSchema(schema); err != nil {
			return err
		}
	}
	for _, schema := range manifest.UserConfigSchema {
		if err := validateConfigSchema(schema); err != nil {
			return err
		}
	}
	for _, c := range manifest.GetCapabilities() {
		for _, cs := range c.GetConfigSchema() {
			if err := validateConfigSchema(cs); err != nil {
				return fmt.Errorf("capability %q config schema: %w", c.GetId(), err)
			}
		}
	}
	return nil
}

func validateConfigSchema(schema *pluginv1.ConfigSchema) error {
	if schema == nil {
		return nil
	}
	form := schema.GetAdminForm()
	if form == nil {
		return nil
	}

	var parsed struct {
		Type       string `json:"type"`
		Properties map[string]struct {
			Type string `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(schema.GetJsonSchema()), &parsed); err != nil {
		return fmt.Errorf("plugin config schema %q has invalid json_schema: %w", schema.GetKey(), err)
	}
	if parsed.Type != "object" {
		return fmt.Errorf("plugin config schema %q admin_form requires an object json_schema", schema.GetKey())
	}

	seen := make(map[string]struct{}, len(form.GetFields()))
	for _, field := range form.GetFields() {
		if field == nil {
			continue
		}
		key := field.GetKey()
		if key == "" {
			return fmt.Errorf("plugin config schema %q admin_form field key is required", schema.GetKey())
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("plugin config schema %q admin_form field %q is duplicated", schema.GetKey(), key)
		}
		seen[key] = struct{}{}
		property, ok := parsed.Properties[key]
		if !ok {
			return fmt.Errorf("plugin config schema %q admin_form field %q is not declared in json_schema", schema.GetKey(), key)
		}
		switch field.GetControl() {
		case pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_MULTI_SELECT:
			if property.Type != "array" {
				return fmt.Errorf("plugin config schema %q admin_form field %q multi_select control requires an array json_schema property", schema.GetKey(), key)
			}
			// A static multi_select must enumerate its options. A field declaring
			// dynamic_options is exempt: the plugin supplies options at runtime
			// via ListConfigOptions, so it legitimately carries no static set.
			if !field.GetDynamicOptions() && len(field.GetOptions()) == 0 {
				return fmt.Errorf("plugin config schema %q admin_form field %q select control requires options", schema.GetKey(), key)
			}
		case pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_SELECT:
			// A static select must enumerate its options. A field declaring
			// dynamic_options is exempt: the plugin supplies options at runtime
			// via ListConfigOptions, so it legitimately carries no static set.
			// No type constraint: SELECT value may be string or numeric.
			if !field.GetDynamicOptions() && len(field.GetOptions()) == 0 {
				return fmt.Errorf("plugin config schema %q admin_form field %q select control requires options", schema.GetKey(), key)
			}
		}
		if defaultValue := field.GetDefaultValue(); defaultValue != nil {
			switch property.Type {
			case "boolean":
				if _, ok := defaultValue.AsInterface().(bool); !ok {
					return fmt.Errorf("plugin config schema %q admin_form field %q default_value must be boolean", schema.GetKey(), key)
				}
			case "integer", "number":
				switch defaultValue.AsInterface().(type) {
				case float64:
				default:
					return fmt.Errorf("plugin config schema %q admin_form field %q default_value must be numeric", schema.GetKey(), key)
				}
			case "string":
				if _, ok := defaultValue.AsInterface().(string); !ok {
					return fmt.Errorf("plugin config schema %q admin_form field %q default_value must be string", schema.GetKey(), key)
				}
			}
		}
	}

	// Cross-field references are validated in a second pass so a field may
	// reference another declared later (e.g. show_when pointing at a field below).
	for _, field := range form.GetFields() {
		if field == nil {
			continue
		}
		key := field.GetKey()
		for _, cond := range field.GetShowWhen() {
			if cond.GetField() == "" {
				return fmt.Errorf("plugin config schema %q admin_form field %q show_when.field is required", schema.GetKey(), key)
			}
			if _, ok := seen[cond.GetField()]; !ok {
				return fmt.Errorf("plugin config schema %q admin_form field %q show_when references unknown field %q", schema.GetKey(), key, cond.GetField())
			}
		}
		if eg := field.GetExclusiveGroupField(); eg != "" {
			if _, ok := seen[eg]; !ok {
				return fmt.Errorf("plugin config schema %q admin_form field %q exclusive_group_field references unknown field %q", schema.GetKey(), key, eg)
			}
		}
	}
	for _, section := range form.GetSections() {
		if section == nil {
			continue
		}
		for _, fk := range section.GetFieldKeys() {
			if _, ok := seen[fk]; !ok {
				return fmt.Errorf("plugin config schema %q admin_form section %q references unknown field %q", schema.GetKey(), section.GetKey(), fk)
			}
		}
		for _, cond := range section.GetShowWhen() {
			if cond.GetField() != "" {
				if _, ok := seen[cond.GetField()]; !ok {
					return fmt.Errorf("plugin config schema %q admin_form section %q show_when references unknown field %q", schema.GetKey(), section.GetKey(), cond.GetField())
				}
			}
		}
	}
	return nil
}

func RegisterHTTPRoutes(manifest *pluginv1.PluginManifest, routes ...*pluginv1.HttpRouteDescriptor) error {
	if err := Validate(manifest); err != nil {
		return err
	}
	manifest.HttpRoutes = append(manifest.HttpRoutes, routes...)
	return nil
}

func RegisterAssets(manifest *pluginv1.PluginManifest, assets ...*pluginv1.PackagedAsset) error {
	if err := Validate(manifest); err != nil {
		return err
	}
	manifest.Assets = append(manifest.Assets, assets...)
	return nil
}

func Asset(path string, filesystem fs.FS) (*pluginv1.PackagedAsset, error) {
	if _, err := fs.Stat(filesystem, path); err != nil {
		return nil, fmt.Errorf("plugin asset %q: %w", path, err)
	}
	return &pluginv1.PackagedAsset{Path: path}, nil
}
