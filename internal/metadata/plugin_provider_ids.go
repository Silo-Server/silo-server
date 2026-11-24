package metadata

import (
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/bookid"
)

func mergePluginProviderIDs(capabilityID, providerID string, ids *structpb.Struct) map[string]string {
	merged := stringMapFromStruct(ids)
	if providerID != "" {
		merged[capabilityID] = providerID
	}
	for key, value := range merged {
		merged[key] = bookid.CanonicalProviderID(key, value)
	}
	return merged
}

func stringMapFromStruct(value *structpb.Struct) map[string]string {
	result := make(map[string]string)
	if value == nil {
		return result
	}
	for key, raw := range value.AsMap() {
		text, ok := raw.(string)
		if ok && text != "" {
			result[key] = text
		}
	}
	return result
}

func structFromStringMap(value map[string]string) (*structpb.Struct, error) {
	if len(value) == 0 {
		return nil, nil
	}

	converted := make(map[string]any, len(value))
	for key, entry := range value {
		if entry == "" {
			continue
		}
		converted[key] = bookid.CanonicalProviderID(key, entry)
	}
	if len(converted) == 0 {
		return nil, nil
	}
	return structpb.NewStruct(converted)
}
