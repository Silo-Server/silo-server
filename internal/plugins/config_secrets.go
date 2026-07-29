package plugins

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
)

// GlobalConfigFieldSets returns the top-level fields that are safe to expose
// and those that must be redacted. When an annotation appears below the top
// level, the current flat Admin form redacts the containing object as a unit.
func GlobalConfigFieldSets(
	manifest *pluginv1.PluginManifest,
	configKey string,
) (publicFields, secretFields []string) {
	declared := make(map[string]struct{})
	secrets := make(map[string]struct{})
	schema := globalConfigSchema(manifest, configKey)
	if schema != nil {
		if form := schema.GetAdminForm(); form != nil {
			for _, field := range form.GetFields() {
				if field == nil {
					continue
				}
				key := strings.TrimSpace(field.GetKey())
				if key == "" {
					continue
				}
				declared[key] = struct{}{}
				if field.GetSecret() ||
					field.GetControl() == pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD {
					secrets[key] = struct{}{}
				}
			}
		}
		var document any
		if json.Unmarshal([]byte(schema.GetJsonSchema()), &document) == nil {
			properties := make(map[string][]any)
			collectTopLevelSchemaProperties(document, document, make(map[string]bool), properties)
			for key, candidates := range properties {
				declared[key] = struct{}{}
				for _, candidate := range candidates {
					if jsonSchemaContainsSecret(candidate, document, make(map[string]bool)) {
						secrets[key] = struct{}{}
						break
					}
				}
			}
		}
	}
	publicFields = make([]string, 0, len(declared))
	secretFields = make([]string, 0, len(secrets))
	for key := range declared {
		if _, secret := secrets[key]; secret {
			continue
		}
		publicFields = append(publicFields, key)
	}
	for key := range secrets {
		secretFields = append(secretFields, key)
	}
	sort.Strings(publicFields)
	sort.Strings(secretFields)
	return publicFields, secretFields
}

// GlobalConfigSecretFields returns top-level fields the plugin manifest marks
// as credentials, including objects that contain nested credentials.
func GlobalConfigSecretFields(manifest *pluginv1.PluginManifest, configKey string) []string {
	_, secrets := GlobalConfigFieldSets(manifest, configKey)
	return secrets
}

// GlobalConfigSecretPaths returns schema-relative paths to actual credential
// values. Top-level redaction remains intentionally broader: an object that
// contains one of these paths is withheld as a unit.
func GlobalConfigSecretPaths(manifest *pluginv1.PluginManifest, configKey string) [][]string {
	schema := globalConfigSchema(manifest, configKey)
	if schema == nil {
		return nil
	}

	pathsByKey := make(map[string][]string)
	addPath := func(path []string) {
		if len(path) == 0 {
			return
		}
		cloned := append([]string(nil), path...)
		pathsByKey[strings.Join(cloned, "\x00")] = cloned
	}
	if form := schema.GetAdminForm(); form != nil {
		for _, field := range form.GetFields() {
			if field == nil ||
				(!field.GetSecret() &&
					field.GetControl() != pluginv1.AdminFormControl_ADMIN_FORM_CONTROL_PASSWORD) {
				continue
			}
			if key := strings.TrimSpace(field.GetKey()); key != "" {
				addPath([]string{key})
			}
		}
	}

	var document any
	if json.Unmarshal([]byte(schema.GetJsonSchema()), &document) == nil {
		properties := make(map[string][]any)
		collectTopLevelSchemaProperties(document, document, make(map[string]bool), properties)
		for key, candidates := range properties {
			for _, candidate := range candidates {
				foundAddressablePath := false
				collectJSONSchemaSecretPaths(
					candidate,
					document,
					make(map[string]bool),
					[]string{key},
					func(path []string) {
						foundAddressablePath = true
						addPath(path)
					},
				)
				if !foundAddressablePath &&
					jsonSchemaContainsSecret(candidate, document, make(map[string]bool)) {
					// A valid schema keyword that this path walker cannot
					// address must remain protected as an opaque top-level
					// credential rather than silently losing preservation.
					addPath([]string{key})
				}
			}
		}
	}

	keys := make([]string, 0, len(pathsByKey))
	for key := range pathsByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	paths := make([][]string, 0, len(keys))
	for _, key := range keys {
		paths = append(paths, pathsByKey[key])
	}
	return paths
}

func collectJSONSchemaSecretPaths(
	node any,
	root any,
	visitingRefs map[string]bool,
	path []string,
	addPath func([]string),
) {
	object, ok := node.(map[string]any)
	if !ok {
		return
	}
	if writeOnly, ok := object["writeOnly"].(bool); ok && writeOnly {
		addPath(path)
		return
	}
	if format, ok := object["format"].(string); ok && strings.EqualFold(format, "password") {
		addPath(path)
		return
	}
	if ref, ok := object["$ref"].(string); ok {
		if visitingRefs[ref] {
			return
		}
		resolved, found := resolveLocalJSONSchemaRef(root, ref)
		if !found {
			// The containing field is sensitive when its referenced shape
			// cannot be audited locally.
			addPath(path)
			return
		}
		visitingRefs[ref] = true
		collectJSONSchemaSecretPaths(resolved, root, visitingRefs, path, addPath)
		delete(visitingRefs, ref)
	}
	if properties, ok := object["properties"].(map[string]any); ok {
		for key, child := range properties {
			collectJSONSchemaSecretPaths(
				child,
				root,
				visitingRefs,
				appendPath(path, key),
				addPath,
			)
		}
	}
	for _, keyword := range []string{
		"allOf", "anyOf", "oneOf", "if", "then", "else", "dependentSchemas",
	} {
		switch child := object[keyword].(type) {
		case map[string]any:
			if keyword == "dependentSchemas" {
				for _, dependent := range child {
					collectJSONSchemaSecretPaths(
						dependent,
						root,
						visitingRefs,
						path,
						addPath,
					)
				}
				continue
			}
			collectJSONSchemaSecretPaths(child, root, visitingRefs, path, addPath)
		case []any:
			for _, candidate := range child {
				collectJSONSchemaSecretPaths(candidate, root, visitingRefs, path, addPath)
			}
		}
	}
	for _, keyword := range []string{
		"items", "prefixItems", "contains", "unevaluatedItems",
		"additionalProperties", "patternProperties", "unevaluatedProperties",
	} {
		child, present := object[keyword]
		if present && jsonSchemaContainsSecret(child, root, make(map[string]bool)) {
			// Array indexes and dynamic property names cannot be represented by
			// the flat clear_secrets contract, so protect their containing value.
			addPath(path)
		}
	}
}

func appendPath(path []string, segment string) []string {
	result := make([]string, len(path)+1)
	copy(result, path)
	result[len(path)] = segment
	return result
}

func collectTopLevelSchemaProperties(
	node any,
	root any,
	visitingRefs map[string]bool,
	properties map[string][]any,
) {
	object, ok := node.(map[string]any)
	if !ok {
		return
	}
	if values, ok := object["properties"].(map[string]any); ok {
		for key, value := range values {
			properties[key] = append(properties[key], value)
		}
	}
	if ref, ok := object["$ref"].(string); ok && !visitingRefs[ref] {
		if resolved, found := resolveLocalJSONSchemaRef(root, ref); found {
			visitingRefs[ref] = true
			collectTopLevelSchemaProperties(resolved, root, visitingRefs, properties)
			delete(visitingRefs, ref)
		}
	}
	for _, keyword := range []string{"allOf", "anyOf", "oneOf", "if", "then", "else"} {
		switch child := object[keyword].(type) {
		case map[string]any:
			collectTopLevelSchemaProperties(child, root, visitingRefs, properties)
		case []any:
			for _, candidate := range child {
				collectTopLevelSchemaProperties(candidate, root, visitingRefs, properties)
			}
		}
	}
	if dependentSchemas, ok := object["dependentSchemas"].(map[string]any); ok {
		for _, schema := range dependentSchemas {
			collectTopLevelSchemaProperties(schema, root, visitingRefs, properties)
		}
	}
}

func jsonSchemaContainsSecret(node any, root any, visitingRefs map[string]bool) bool {
	switch value := node.(type) {
	case map[string]any:
		if writeOnly, ok := value["writeOnly"].(bool); ok && writeOnly {
			return true
		}
		if format, ok := value["format"].(string); ok && strings.EqualFold(format, "password") {
			return true
		}
		if ref, ok := value["$ref"].(string); ok {
			if visitingRefs[ref] {
				return false
			}
			resolved, found := resolveLocalJSONSchemaRef(root, ref)
			if !found {
				// An external or malformed reference cannot be audited locally.
				return true
			}
			visitingRefs[ref] = true
			secret := jsonSchemaContainsSecret(resolved, root, visitingRefs)
			delete(visitingRefs, ref)
			if secret {
				return true
			}
		}
		for key, child := range value {
			if key == "$ref" {
				continue
			}
			if jsonSchemaContainsSecret(child, root, visitingRefs) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if jsonSchemaContainsSecret(child, root, visitingRefs) {
				return true
			}
		}
	}
	return false
}

func resolveLocalJSONSchemaRef(root any, ref string) (any, bool) {
	if ref == "#" {
		return root, true
	}
	if !strings.HasPrefix(ref, "#/") {
		return nil, false
	}
	current := root
	for _, encoded := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		segment := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			var ok bool
			current, ok = value[segment]
			if !ok {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(value) {
				return nil, false
			}
			current = value[index]
		default:
			return nil, false
		}
	}
	return current, true
}

// HasGlobalConfigSchema reports whether the manifest still declares the
// persisted config key. Callers that expose decrypted values must fail closed
// when a row has outlived its schema after a plugin upgrade.
func HasGlobalConfigSchema(manifest *pluginv1.PluginManifest, configKey string) bool {
	return globalConfigSchema(manifest, configKey) != nil
}

func globalConfigSchema(manifest *pluginv1.PluginManifest, configKey string) *pluginv1.ConfigSchema {
	for _, schema := range manifest.GetGlobalConfigSchema() {
		if schema != nil && schema.GetKey() == configKey {
			return schema
		}
	}
	return nil
}

func secretFieldSet(fields []string) map[string]struct{} {
	result := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		result[field] = struct{}{}
	}
	return result
}
