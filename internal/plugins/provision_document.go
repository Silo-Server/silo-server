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
	"github.com/Silo-Server/silo-server/internal/config"
)

// ProvisionDocument holds the deployment's declaration and a snapshot of its
// local secret inputs. Only resolved fields of a verified manifest are passed
// to a plugin. A section the document leaves out is nil: what it would
// declare is left as stored, where an empty section releases it.
type ProvisionDocument struct {
	APIVersion  provisionInt                   `json:"apiVersion"`
	Plugins     map[string]ProvisionPlugin     `json:"plugins"`
	AIProviders map[string]ProvisionAIProvider `json:"ai_providers"`
	secrets     map[provisionReference]string
	variable    string
}

// ProvisionAIProvider is one AI provider the deployment declares. APIKey is a
// fromFile or fromEnv reference; the schema refuses a literal key.
type ProvisionAIProvider struct {
	Type         string         `json:"type"`
	Name         string         `json:"name"`
	BaseURL      string         `json:"base_url"`
	Capabilities []string       `json:"capabilities"`
	APIKey       map[string]any `json:"api_key"`
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

// provisionDocumentVars name the deployment document: inline JSON, or a JSON
// or YAML file. SILO_CONFIG and SILO_CONFIG_FILE are its names for
// everything it declares; SILO_PLUGINS and SILO_PLUGINS_FILE, the names it
// had when it declared plugins alone, still work.
var provisionDocumentVars = []struct {
	name string
	file bool
}{
	{"SILO_CONFIG", false}, {"SILO_CONFIG_FILE", true}, {"SILO_PLUGINS", false}, {"SILO_PLUGINS_FILE", true},
}

// ReadProvisionDocument is called by API hosts only. A nil document means
// that this instance reads the committed deployment state.
func ReadProvisionDocument() (*ProvisionDocument, error) {
	var set []string
	var source struct {
		name string
		file bool
	}
	for _, v := range provisionDocumentVars {
		if os.Getenv(v.name) != "" {
			set = append(set, v.name)
			source = v
		}
	}
	switch {
	case len(set) == 0:
		return nil, nil
	case len(set) > 1:
		return nil, fmt.Errorf("set only one of %s and %s", strings.Join(set[:len(set)-1], ", "), set[len(set)-1])
	}
	data := []byte(os.Getenv(source.name))
	if source.file {
		var err error
		if data, err = readProvisionFile(string(data)); err != nil {
			return nil, fmt.Errorf("%s: %w", source.name, err)
		}
	}
	document, err := parseProvisionDocument(data, source.file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source.name, err)
	}
	document.variable = source.name
	return document, nil
}

// Variable names the environment variable the document came from.
func (d *ProvisionDocument) Variable() string {
	if d == nil {
		return ""
	}
	return d.variable
}

// DeclaresPlugins reports whether the document has a plugins section. A
// document without one leaves plugin installations as they are.
func (d *ProvisionDocument) DeclaresPlugins() bool {
	return d != nil && d.Plugins != nil
}

// DeclaredAIProviders returns the AI providers the document declares, in id
// order with their keys resolved, and whether it has an ai_providers section.
// An empty section declares none, which releases every provider an earlier
// document declared.
func (d *ProvisionDocument) DeclaredAIProviders() ([]config.AIProvider, bool, error) {
	if d == nil || d.AIProviders == nil {
		return nil, false, nil
	}
	providers := make([]config.AIProvider, 0, len(d.AIProviders))
	var keyBytes int
	for _, id := range slices.Sorted(maps.Keys(d.AIProviders)) {
		declared := d.AIProviders[id]
		apiKey, err := d.resolveAIProviderKey(declared.APIKey, &keyBytes)
		if err != nil {
			return nil, true, fmt.Errorf("ai_providers.%s.api_key: %w", id, err)
		}
		capabilities := make([]config.AICapability, len(declared.Capabilities))
		for i, capability := range declared.Capabilities {
			capabilities[i] = config.AICapability(capability)
		}
		provider, err := config.NewDeclaredAIProvider(id, config.AIProviderType(declared.Type), declared.Name, declared.BaseURL, apiKey, capabilities)
		if err != nil {
			return nil, true, fmt.Errorf("ai_providers.%s: %w", id, err)
		}
		providers = append(providers, provider)
	}
	return providers, true, nil
}

// resolveAIProviderKey reads a declared provider's key, adding a file or
// variable it names to the document's secret budget. The key is read here
// rather than with the document, so a server that applies no providers, such
// as a frontend, needs no AI keys.
func (d *ProvisionDocument) resolveAIProviderKey(value map[string]any, keyBytes *int) (string, error) {
	if value == nil {
		return "", nil
	}
	if ref, ok := referenceFrom(value); ok {
		if err := d.captureSecret(ref, keyBytes); err != nil {
			return "", err
		}
	}
	key, err := d.resolveField(value, nil, true)
	if err != nil {
		return "", err
	}
	apiKey, _ := key.(string)
	return apiKey, nil
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
		return nil, errors.New("the deployment document must be UTF-8 input up to 1 MiB")
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
		return nil, errors.New("invalid deployment document JSON: check syntax and duplicate keys")
	}
	schema, err := provisionSchema()
	if err != nil {
		return nil, fmt.Errorf("load plugin provisioning schema: %w", err)
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("invalid deployment declaration")
	}
	if err := schema.Validate(value); err != nil {
		if e, ok := errors.AsType[*jsonschema.ValidationError](err); ok {
			for len(e.Causes) == 1 {
				e = e.Causes[0]
			}
			return nil, fmt.Errorf("invalid deployment declaration at /%s", strings.Join(e.InstanceLocation, "/"))
		}
		return nil, errors.New("declaration does not match the deployment document schema")
	}
	var document ProvisionDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber() // Keep plugin config integers exact through the encrypted store.
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("invalid deployment declaration")
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
		return nil, errors.New("invalid deployment document YAML: check syntax and duplicate keys")
	}
	if err := decoder.Decode(new(yaml.Node)); !errors.Is(err, io.EOF) {
		return nil, errors.New("the deployment document must be one YAML document")
	}
	// yaml.v3 decodes floating point scalars into float64. Check the spelling
	// before that conversion so a lossy value cannot pass the JSON checks.
	if _, err := provisionYAMLSize(&root, make(map[*yaml.Node]int), limit); err != nil {
		return nil, err
	}
	var document any
	if err := root.Decode(&document); err != nil {
		return nil, errors.New("invalid deployment document YAML: check syntax and duplicate keys")
	}
	if !provisionJSONValue(document) {
		return nil, errors.New("the deployment document requires JSON-compatible YAML with string mapping keys")
	}
	encoded := provisionJSONBuffer{limit: limit}
	if err := jsonv2.MarshalWrite(&encoded, document, jsonv2.WithMarshalers(provisionYAMLFloat)); err != nil {
		if errors.Is(err, errProvisionExpandedSize) {
			return nil, errProvisionExpandedSize
		}
		return nil, errors.New("the deployment document requires finite JSON numbers and UTF-8 strings")
	}
	return encoded.Bytes(), nil
}

var errProvisionExpandedSize = errors.New("the expanded deployment document exceeds 1 MiB")

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
				return 0, errors.New("the deployment document requires string mapping keys")
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
