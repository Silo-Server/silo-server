package convert

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

type CapabilityRecord struct {
	Type     string
	ID       string
	Metadata map[string]any
}

func CapabilityRecordsFromManifest(manifest *pluginv1.PluginManifest) ([]CapabilityRecord, error) {
	records := make([]CapabilityRecord, 0, len(manifest.GetCapabilities()))
	for _, descriptor := range manifest.GetCapabilities() {
		metadata, err := capabilityMetadata(descriptor)
		if err != nil {
			return nil, err
		}
		records = append(records, CapabilityRecord{
			Type:     descriptor.GetType(),
			ID:       descriptor.GetId(),
			Metadata: metadata,
		})
	}
	return records, nil
}

func DecodeCapability(record CapabilityRecord) (*pluginv1.CapabilityDescriptor, error) {
	descriptor := &pluginv1.CapabilityDescriptor{
		Type: record.Type,
		Id:   record.ID,
	}
	if record.Metadata == nil {
		return descriptor, nil
	}

	if displayName, ok := record.Metadata["display_name"].(string); ok {
		descriptor.DisplayName = displayName
	}
	if description, ok := record.Metadata["description"].(string); ok {
		descriptor.Description = description
	}
	if subscriptions, ok := record.Metadata["subscriptions"]; ok {
		list, err := toStringSlice(subscriptions)
		if err != nil {
			return nil, err
		}
		descriptor.Subscriptions = list
	}
	if authModes, ok := record.Metadata["auth_modes"]; ok {
		list, err := toStringSlice(authModes)
		if err != nil {
			return nil, err
		}
		descriptor.AuthModes = list
	}
	if iconURL, ok := record.Metadata["icon_url"].(string); ok {
		descriptor.IconUrl = iconURL
	}
	if configSchema, ok := record.Metadata["config_schema"]; ok {
		schemas, err := decodeConfigSchemas(configSchema)
		if err != nil {
			return nil, err
		}
		descriptor.ConfigSchema = schemas
	}
	if metadataValue, ok := record.Metadata["metadata"]; ok {
		structValue, err := structpb.NewStruct(toStringAnyMap(metadataValue))
		if err != nil {
			return nil, fmt.Errorf("decode capability metadata struct: %w", err)
		}
		descriptor.Metadata = structValue
	}

	return descriptor, nil
}

func capabilityMetadata(descriptor *pluginv1.CapabilityDescriptor) (map[string]any, error) {
	if descriptor == nil {
		return nil, fmt.Errorf("capability descriptor is required")
	}

	metadata := map[string]any{
		"display_name": descriptor.GetDisplayName(),
		"description":  descriptor.GetDescription(),
	}
	if len(descriptor.GetSubscriptions()) > 0 {
		metadata["subscriptions"] = append([]string(nil), descriptor.GetSubscriptions()...)
	}
	if len(descriptor.GetAuthModes()) > 0 {
		metadata["auth_modes"] = append([]string(nil), descriptor.GetAuthModes()...)
	}
	if descriptor.GetIconUrl() != "" {
		metadata["icon_url"] = descriptor.GetIconUrl()
	}
	if len(descriptor.GetConfigSchema()) > 0 {
		schemas := make([]map[string]any, 0, len(descriptor.GetConfigSchema()))
		for _, schema := range descriptor.GetConfigSchema() {
			schemas = append(schemas, map[string]any{
				"key":         schema.GetKey(),
				"title":       schema.GetTitle(),
				"description": schema.GetDescription(),
				"json_schema": schema.GetJsonSchema(),
				"required":    schema.GetRequired(),
			})
		}
		metadata["config_schema"] = schemas
	}
	if descriptor.GetMetadata() != nil {
		metadata["metadata"] = descriptor.GetMetadata().AsMap()
	}
	return metadata, nil
}

func decodeConfigSchemas(value any) ([]*pluginv1.ConfigSchema, error) {
	entries, ok := value.([]any)
	if !ok {
		if typedEntries, ok := value.([]map[string]any); ok {
			entries = make([]any, 0, len(typedEntries))
			for _, entry := range typedEntries {
				entries = append(entries, entry)
			}
		} else {
			return nil, fmt.Errorf("config_schema must be a list")
		}
	}

	schemas := make([]*pluginv1.ConfigSchema, 0, len(entries))
	for _, entry := range entries {
		rawSchema := toStringAnyMap(entry)
		jsonData, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(&pluginv1.ConfigSchema{
			Key:         stringValue(rawSchema["key"]),
			Title:       stringValue(rawSchema["title"]),
			Description: stringValue(rawSchema["description"]),
			JsonSchema:  stringValue(rawSchema["json_schema"]),
			Required:    boolValue(rawSchema["required"]),
		})
		if err != nil {
			return nil, fmt.Errorf("encode config schema: %w", err)
		}
		var schema pluginv1.ConfigSchema
		if err := protojson.Unmarshal(jsonData, &schema); err != nil {
			return nil, fmt.Errorf("decode config schema: %w", err)
		}
		schemas = append(schemas, &schema)
	}

	return schemas, nil
}

func toStringSlice(value any) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), nil
	case []any:
		result := make([]string, 0, len(typed))
		for _, entry := range typed {
			text, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("subscription value must be a string")
			}
			result = append(result, text)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("subscriptions must be a list")
	}
}

func toStringAnyMap(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case nil:
		return map[string]any{}
	default:
		return map[string]any{}
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func boolValue(value any) bool {
	flag, _ := value.(bool)
	return flag
}
