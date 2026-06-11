package config

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

func ValidateManifestGlobalValue(manifest *pluginv1.PluginManifest, key string, value map[string]any) error {
	if manifest == nil {
		return fmt.Errorf("plugin manifest is required")
	}
	return ValidateValue(FindSchema(manifest.GetGlobalConfigSchema(), key), "plugin global config", key, value)
}

func ValidateManifestUserValue(manifest *pluginv1.PluginManifest, key string, value map[string]any) error {
	if manifest == nil {
		return fmt.Errorf("plugin manifest is required")
	}
	return ValidateValue(FindSchema(manifest.GetUserConfigSchema(), key), "plugin user config", key, value)
}

func FindSchema(schemas []*pluginv1.ConfigSchema, key string) *pluginv1.ConfigSchema {
	for _, schema := range schemas {
		if schema != nil && schema.GetKey() == key {
			return schema
		}
	}
	return nil
}

func ValidateValue(schema *pluginv1.ConfigSchema, kind string, key string, value map[string]any) error {
	if schema == nil {
		return fmt.Errorf("%s key %q is not declared in the manifest schema", kind, key)
	}

	schemaJSON := strings.TrimSpace(schema.GetJsonSchema())
	if schemaJSON == "" {
		schemaJSON = `{}`
	}

	var schemaDoc any
	if err := json.Unmarshal([]byte(schemaJSON), &schemaDoc); err != nil {
		return fmt.Errorf("parse %s schema %q: %w", kind, key, err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	resourceName := "memory://config/" + key + ".schema.json"
	if err := compiler.AddResource(resourceName, schemaDoc); err != nil {
		return fmt.Errorf("load %s schema %q: %w", kind, key, err)
	}

	compiled, err := compiler.Compile(resourceName)
	if err != nil {
		return fmt.Errorf("compile %s schema %q: %w", kind, key, err)
	}

	configValue := value
	if configValue == nil {
		configValue = map[string]any{}
	}
	if err := compiled.Validate(configValue); err != nil {
		return fmt.Errorf("validate %s %q: %w", kind, key, err)
	}

	return nil
}
