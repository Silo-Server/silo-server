package plugins

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/big"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	provisioningv1 "github.com/Silo-Server/silo-server/contracts/plugins/provisioning/v1"
)

// ProvisionDocument holds the declaration and a snapshot of its local secret
// inputs. Only resolved fields of a verified manifest are passed to a plugin.
type ProvisionDocument struct {
	APIVersion provisionInt               `json:"apiVersion"`
	Plugins    map[string]ProvisionPlugin `json:"plugins"`
	secrets    map[provisionReference]string
}

type ProvisionPlugin struct {
	Catalog     string                    `json:"catalog"`
	Version     string                    `json:"version"`
	Config      map[string]map[string]any `json:"config"`
	AuthBinding *ProvisionAuthBinding     `json:"auth_binding"`
}

type ProvisionAuthBinding struct {
	CapabilityID  string        `json:"capability_id"`
	Enabled       *bool         `json:"enabled"`
	DisplayOrder  *provisionInt `json:"display_order"`
	AutoProvision *bool         `json:"auto_provision"`
	DefaultLogin  *bool         `json:"default_login"`
	Callback      string        `json:"callback"`
}

// JSON Schema treats integral numbers such as 3.0 and 3e0 as integers.
type provisionInt int

func (v *provisionInt) UnmarshalJSON(data []byte) error {
	n, ok := new(big.Rat).SetString(string(data))
	if !ok || !n.IsInt() || !n.Num().IsInt64() {
		return errors.New("expected an integer")
	}
	*v = provisionInt(n.Num().Int64())
	return nil
}

const maxProvisionBytes = 1 << 20

// ReadProvisionDocument is called by API hosts only. A nil document means
// that this instance reads the committed deployment state.
func ReadProvisionDocument() (*ProvisionDocument, error) {
	inline := os.Getenv("SILO_PLUGINS")
	path := os.Getenv("SILO_PLUGINS_FILE")
	switch {
	case inline != "" && path != "":
		return nil, errors.New("set only one of SILO_PLUGINS and SILO_PLUGINS_FILE")
	case path != "":
		data, err := readProvisionFile(path)
		if err != nil {
			return nil, fmt.Errorf("SILO_PLUGINS_FILE: %w", err)
		}
		return parseProvisionDocument(data, true)
	case inline != "":
		return parseProvisionDocument([]byte(inline), false)
	default:
		return nil, nil
	}
}

func readProvisionFile(path string) ([]byte, error) {
	info, err := os.Stat(path) //nolint:gosec // The operator selects the declaration or credential mount.
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("expected a readable regular file")
	}
	f, err := os.Open(path) //nolint:gosec // The deployment selects its declaration and mounted credential paths.
	if err != nil {
		return nil, errors.New("could not open file")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxProvisionBytes+1))
	if err != nil || len(data) > maxProvisionBytes {
		return nil, errors.New("could not read file within the 1 MiB limit")
	}
	return data, nil
}

var provisionSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	const resource = "urn:silo:plugin-provisioning:v1"
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(provisioningv1.Schema))
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(resource, doc); err != nil {
		return nil, err
	}
	return compiler.Compile(resource)
})

func parseProvisionDocument(data []byte, allowYAML bool) (*ProvisionDocument, error) {
	if len(data) > maxProvisionBytes || !utf8.Valid(data) {
		return nil, errors.New("plugin provisioning expects UTF-8 input up to 1 MiB")
	}
	if allowYAML && !jsontext.Value(data).IsValid() {
		// A malformed JSON document beginning with an object can still be a
		// YAML flow mapping. The YAML reader checks duplicates independently.
		var err error
		data, err = provisionYAMLToJSON(data, maxProvisionBytes)
		if err != nil {
			return nil, err
		}
	}
	if !jsontext.Value(data).IsValid() {
		return nil, errors.New("invalid plugin JSON: check syntax and duplicate keys")
	}
	schema, err := provisionSchema()
	if err != nil {
		return nil, fmt.Errorf("load plugin provisioning schema: %w", err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid plugin declaration")
	}
	if err := schema.Validate(value); err != nil {
		if e, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
			for len(e.Causes) == 1 {
				e = e.Causes[0]
			}
			return nil, fmt.Errorf("invalid plugin declaration at /%s", strings.Join(e.InstanceLocation, "/"))
		}
		return nil, errors.New("declaration does not match the plugin provisioning schema")
	}
	var document ProvisionDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber() // Keep plugin config integers exact through the encrypted store.
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("invalid plugin declaration")
	}
	var secretBytes int
	document.secrets = make(map[provisionReference]string)
	for _, id := range slices.Sorted(maps.Keys(document.Plugins)) {
		plugin := document.Plugins[id]
		if _, err := validateOfficialReleaseRequest(id, plugin.Version); err != nil {
			return nil, fmt.Errorf("plugins.%s: %w", id, err)
		}
		if plugin.Catalog == "" {
			plugin.Catalog = "silo"
			document.Plugins[id] = plugin
		}
		for entry, fields := range plugin.Config {
			for field, value := range fields {
				if err := document.captureReferences(value, &secretBytes); err != nil {
					return nil, fmt.Errorf("plugins.%s.config.%s.%s: %w", id, entry, field, err)
				}
			}
		}
	}
	return &document, nil
}

func provisionYAMLToJSON(data []byte, limit int) ([]byte, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return nil, errors.New("invalid provisioning YAML: check syntax and duplicate keys")
	}
	if err := decoder.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return nil, errors.New("plugin provisioning expects one YAML document")
	}
	// yaml.v3 decodes floating point scalars into float64. Check the spelling
	// before that conversion so a lossy value cannot pass the JSON checks.
	if _, err := provisionYAMLSize(&root, make(map[*yaml.Node]int), limit); err != nil {
		return nil, err
	}
	var document any
	if err := root.Decode(&document); err != nil {
		return nil, errors.New("invalid provisioning YAML: check syntax and duplicate keys")
	}
	if !provisionJSONValue(document) {
		return nil, errors.New("plugin provisioning requires JSON-compatible YAML with string mapping keys")
	}
	encoded := provisionJSONBuffer{limit: limit}
	if err := jsonv2.MarshalWrite(&encoded, document, jsonv2.WithMarshalers(provisionYAMLFloat)); err != nil {
		if errors.Is(err, errProvisionExpandedSize) {
			return nil, errProvisionExpandedSize
		}
		return nil, errors.New("plugin provisioning requires finite JSON numbers and UTF-8 strings")
	}
	return encoded.Bytes(), nil
}

var errProvisionExpandedSize = errors.New("expanded plugin declaration exceeds 1 MiB")

type provisionJSONBuffer struct {
	bytes.Buffer
	limit int
}

func (b *provisionJSONBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errProvisionExpandedSize
	}
	return b.Buffer.Write(p)
}

// The shortest decimal for an integral float can name a different integer
// that rounds to the same float. Preserve its exact integer through JSON.
var provisionYAMLFloat = jsonv2.MarshalFunc(func(value float64) ([]byte, error) {
	if value == math.Trunc(value) && !math.IsInf(value, 0) {
		return strconv.AppendFloat(nil, value, 'f', 0, 64), nil
	}
	return json.Marshal(value)
})

// Charge each alias for its target before yaml.v3 expands it. Only completed
// anchors can be referenced, so an alias back to a parent is invalid.
func provisionYAMLSize(node *yaml.Node, anchors map[*yaml.Node]int, limit int) (int, error) {
	if node.Kind == yaml.AliasNode {
		size, ok := anchors[node.Alias]
		if !ok {
			return 0, errors.New("recursive or unresolved YAML alias")
		}
		return size, nil
	}
	if node.Kind == yaml.ScalarNode && node.ShortTag() == "!!float" &&
		!provisionNumberFitsRuntime(strings.ReplaceAll(node.Value, "_", "")) {
		return 0, fmt.Errorf("number cannot be represented by the plugin runtime at YAML line %d, column %d", node.Line, node.Column)
	}
	size := max(1, len(node.Value))
	var keys map[string]struct{}
	if node.Kind == yaml.MappingNode {
		keys = make(map[string]struct{}, len(node.Content)/2)
	}
	for i, child := range node.Content {
		n, err := provisionYAMLSize(child, anchors, limit)
		if err != nil {
			return 0, err
		}
		size += n
		if size > limit {
			return 0, errProvisionExpandedSize
		}
		if keys != nil && i%2 == 0 {
			key := child
			if key.Kind == yaml.AliasNode {
				key = key.Alias
			}
			if key.Kind != yaml.ScalarNode || (key.ShortTag() != "!!str" && key.ShortTag() != "!!merge") {
				return 0, errors.New("plugin provisioning requires string mapping keys")
			}
			if _, duplicate := keys[key.Value]; duplicate {
				return 0, fmt.Errorf("duplicate YAML mapping key at line %d, column %d", child.Line, child.Column)
			}
			keys[key.Value] = struct{}{}
		}
	}
	if node.Anchor != "" {
		anchors[node] = size
	}
	return size, nil
}

func provisionJSONValue(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		for _, item := range value {
			if !provisionJSONValue(item) {
				return false
			}
		}
	case []any:
		for _, item := range value {
			if !provisionJSONValue(item) {
				return false
			}
		}
	case nil, string, bool, int, int64, uint64, float64:
	default:
		return false
	}
	return true
}

type provisionReference struct {
	source string
	name   string
}

func referenceFrom(value map[string]any) (provisionReference, bool) {
	if len(value) == 1 {
		for _, source := range []string{"fromFile", "fromEnv"} {
			if name, ok := value[source].(string); ok {
				return provisionReference{source: source, name: name}, true
			}
		}
	}
	return provisionReference{}, false
}

func (d *ProvisionDocument) captureReferences(value any, total *int) error {
	switch value := value.(type) {
	case map[string]any:
		if ref, ok := referenceFrom(value); ok {
			return d.captureSecret(ref, total)
		}
		for _, child := range value {
			if err := d.captureReferences(child, total); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := d.captureReferences(child, total); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *ProvisionDocument) captureSecret(ref provisionReference, total *int) error {
	if _, captured := d.secrets[ref]; captured {
		return nil
	}
	var secret string
	if ref.source == "fromFile" {
		data, err := readProvisionFile(ref.name)
		if err != nil {
			return fmt.Errorf("secret file: %w", err)
		}
		secret = string(data)
		if line, ended := strings.CutSuffix(secret, "\n"); ended {
			secret = strings.TrimSuffix(line, "\r")
		}
	} else {
		secret = os.Getenv(ref.name)
	}
	if secret == "" || !utf8.ValidString(secret) {
		return errors.New("secret reference requires a nonempty UTF-8 value")
	}
	if len(secret) > maxProvisionBytes-*total {
		return errors.New("combined secret inputs exceed 1 MiB")
	}
	*total += len(secret)
	d.secrets[ref] = secret
	return nil
}

// ResolveConfig resolves references only at declared credential paths. The
// writer validates the merged result against the verified manifest on save.
func (d *ProvisionDocument) ResolveConfig(manifest *pluginv1.PluginManifest, pluginID string) (map[string]map[string]any, error) {
	plugin, ok := d.Plugins[pluginID]
	if !ok || manifest.GetPluginId() != pluginID {
		return nil, errors.New("plugin manifest identity does not match the declaration")
	}
	resolved := make(map[string]map[string]any, len(plugin.Config))
	for entry, fields := range plugin.Config {
		if !HasGlobalConfigSchema(manifest, entry) {
			return nil, fmt.Errorf("undeclared plugin config entry %q", entry)
		}
		public, secret := GlobalConfigFieldSets(manifest, entry)
		secretPaths := GlobalConfigSecretPaths(manifest, entry)
		resolved[entry] = make(map[string]any, len(fields))
		for field, value := range fields {
			if !slices.Contains(public, field) && !slices.Contains(secret, field) {
				return nil, fmt.Errorf("undeclared plugin config field %s.%s", entry, field)
			}
			nested, sensitive := childSecretPaths(secretPaths, field)
			out, err := d.resolveField(value, nested, sensitive)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: %w", entry, field, err)
			}
			resolved[entry][field] = out
		}
	}
	return resolved, nil
}

func (d *ProvisionDocument) resolveField(value any, paths [][]string, sensitive bool) (any, error) {
	object, _ := value.(map[string]any)
	ref, isRef := referenceFrom(object)
	if sensitive {
		if !isRef {
			return nil, errors.New("secret field requires a fromFile or fromEnv reference")
		}
		secret, ok := d.secrets[ref]
		if !ok {
			return nil, errors.New("secret input unavailable")
		}
		return secret, nil
	}
	if isRef {
		return nil, errors.New("secret reference on a public field")
	}
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			nested, secret := childSecretPaths(paths, key)
			v, err := d.resolveField(child, nested, secret)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
			result[key] = v
		}
		return result, nil
	case []any:
		result := make([]any, len(value))
		for i, item := range value {
			v, err := d.resolveField(item, nil, false)
			if err != nil {
				return nil, err
			}
			result[i] = v
		}
		return result, nil
	default:
		return value, nil
	}
}
