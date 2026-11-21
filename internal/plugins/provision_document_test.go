package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"

	provisioningv1 "github.com/Silo-Server/silo-server/contracts/plugins/provisioning/v1"
	"github.com/Silo-Server/silo-server/internal/config"
)

func TestReadProvisionDocumentEmptyEnvironment(t *testing.T) {
	const release = `{"apiVersion":1,"plugins":{}}`
	path := filepath.Join(t.TempDir(), "plugins.json")
	if err := os.WriteFile(path, []byte(release), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		env          map[string]string
		wantVariable string
		wantError    bool
	}{
		{"empty variables use committed state", nil, "", false},
		{"inline release", map[string]string{"SILO_PLUGINS": release}, "SILO_PLUGINS", false},
		{"file release", map[string]string{"SILO_PLUGINS_FILE": path}, "SILO_PLUGINS_FILE", false},
		{"deployment document", map[string]string{"SILO_CONFIG": release}, "SILO_CONFIG", false},
		{"deployment document file", map[string]string{"SILO_CONFIG_FILE": path}, "SILO_CONFIG_FILE", false},
		{"two declarations", map[string]string{"SILO_PLUGINS": release, "SILO_PLUGINS_FILE": path}, "", true},
		{"old and new names", map[string]string{"SILO_CONFIG_FILE": path, "SILO_PLUGINS": release}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range []string{"SILO_CONFIG", "SILO_CONFIG_FILE", "SILO_PLUGINS", "SILO_PLUGINS_FILE"} {
				t.Setenv(name, tc.env[name])
			}
			doc, err := ReadProvisionDocument()
			if (err != nil) != tc.wantError || (doc != nil) != (tc.wantVariable != "") {
				t.Fatalf("document present = %t, error = %v", doc != nil, err)
			}
			if doc != nil && doc.Variable() != tc.wantVariable {
				t.Fatalf("variable = %q, want %q", doc.Variable(), tc.wantVariable)
			}
		})
	}
}

// The deployment document declares AI providers with keys taken by reference.
// A section it leaves out is left as stored; an empty one releases it.
func TestProvisionDocumentAIProviders(t *testing.T) {
	t.Setenv("TEST_SILO_OPENAI_KEY", "sk-from-env")
	doc, err := parseProvisionDocument([]byte(`{"apiVersion":1,"ai_providers":{
		"openai-main":{"type":"openai","api_key":{"fromEnv":"TEST_SILO_OPENAI_KEY"}},
		"local-embed":{"type":"custom","name":"Local embeddings","base_url":"http://embed:8080/v1","capabilities":["embeddings"]}}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if doc.DeclaresPlugins() {
		t.Error("a document without a plugins section declares plugins")
	}
	providers, declared, err := doc.DeclaredAIProviders()
	if err != nil || !declared || len(providers) != 2 {
		t.Fatalf("providers = %+v, declared = %t, error = %v", providers, declared, err)
	}
	if p := providers[1]; p.ID != "openai-main" || p.APIKey != "sk-from-env" {
		t.Errorf("openai-main = %+v, want the key from the environment", p)
	}
	if p := providers[0]; p.ID != "local-embed" || p.APIKey != "" {
		t.Errorf("local-embed = %+v, want no key", p)
	}

	empty, err := parseProvisionDocument([]byte(`{"apiVersion":1,"ai_providers":{}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if providers, declared, err := empty.DeclaredAIProviders(); err != nil || !declared || len(providers) != 0 {
		t.Errorf("empty section: providers = %v, declared = %t, error = %v; want a release", providers, declared, err)
	}
	plugins, err := parseProvisionDocument([]byte(`{"apiVersion":1,"plugins":{}}`), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, declared, _ := plugins.DeclaredAIProviders(); declared || !plugins.DeclaresPlugins() {
		t.Error("a plugins-only document declares AI providers, or no plugins")
	}

	t.Setenv("TEST_SILO_BLANK_KEY", "  ")
	for name, input := range map[string]string{
		"literal key":        `{"apiVersion":1,"ai_providers":{"openai-main":{"type":"openai","api_key":"sk-private-literal"}}}`,
		"key it requires":    `{"apiVersion":1,"ai_providers":{"openai-main":{"type":"openai"}}}`,
		"blank key":          `{"apiVersion":1,"ai_providers":{"local":{"type":"ollama","api_key":{"fromEnv":"TEST_SILO_BLANK_KEY"}}}}`,
		"empty capabilities": `{"apiVersion":1,"ai_providers":{"local":{"type":"ollama","capabilities":[]}}}`,
	} {
		doc, err := parseProvisionDocument([]byte(input), false)
		if err == nil {
			_, _, err = doc.DeclaredAIProviders()
		}
		if err == nil || strings.Contains(err.Error(), "sk-private-literal") {
			t.Errorf("%s: error = %v, want a refusal that doesn't quote the key", name, err)
		}
	}
}

func TestProvisionDocumentResolvesOnlyVerifiedSecretFields(t *testing.T) {
	const id = "silo.auth.oidc"
	dir := t.TempDir()
	secret := filepath.Join(dir, "mounted")
	if err := os.WriteFile(filepath.Join(dir, "release"), []byte("line one\nline two \r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("release", secret); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVISION_TEST_PASSWORD", "v1")
	file := fmt.Sprintf(`apiVersion: 1
plugins:
  %s:
    config:
      connection:
        display: '$literal'
        counter: 9007199254740993
        tls:
          credential: {fromFile: %q}
        password: {fromEnv: PROVISION_TEST_PASSWORD}
`, id, secret)
	doc, err := parseProvisionDocument([]byte(file), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROVISION_TEST_PASSWORD", "v2")
	manifest := &pluginv1.PluginManifest{
		PluginId: id,
		GlobalConfigSchema: []*pluginv1.ConfigSchema{{
			Key:        "connection",
			JsonSchema: `{"type":"object","properties":{"display":{"type":"string"},"counter":{"type":"integer"},"tls":{"type":"object","properties":{"credential":{"type":"string","writeOnly":true}}},"password":{"type":"string","writeOnly":true}}}`,
		}},
	}
	resolved, err := doc.ResolveConfig(manifest, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved["connection"]; got["display"] != "$literal" || got["password"] != "v1" || got["counter"] != json.Number("9007199254740993") ||
		got["tls"].(map[string]any)["credential"] != "line one\nline two " {
		t.Fatal("resolved values do not match the startup snapshot")
	}
	fields := doc.Plugins[id].Config["connection"]
	fields["display"] = fields["password"]
	if _, err := doc.ResolveConfig(manifest, id); err == nil || !strings.Contains(err.Error(), "public field") {
		t.Fatalf("reference supplied for a public field: %v", err)
	}
	delete(fields, "display")
	fields["password"] = "private-literal"
	if _, err := doc.ResolveConfig(manifest, id); err == nil || strings.Contains(err.Error(), "private-literal") {
		t.Fatal("literal credential must be refused without including its value")
	}
}

func TestProvisionDocumentRejectsAmbiguousInputs(t *testing.T) {
	for name, tc := range map[string]struct {
		input string
		yaml  bool
	}{
		"duplicate JSON release":     {`{"apiVersion":1,"plugins":{"silo.auth.oidc":{"version":"1.0.0","version":"0.1.2"}}}`, false},
		"duplicate YAML release":     {"apiVersion: 1\nplugins:\n  silo.auth.oidc:\n    version: 1.0.0\n    version: 0.1.2\n", true},
		"multi document":             {"apiVersion: 1\nplugins: {}\n---\nplugins: {}\n", true},
		"null releases no ownership": {`{"apiVersion":1,"plugins":null}`, false},
		"misspelled host option":     {`{"apiVersion":1,"plugins":{"silo.auth.oidc":{"authBinding":{}}}}`, false},
		"conflicting secret sources": {`{"apiVersion":1,"plugins":{"silo.auth.oidc":{"config":{"connection":{"password":{"fromFile":"private-literal","fromEnv":"ALSO"}}}}}}`, false},
		"non-string map key":         {"apiVersion: 1\nplugins: {3: {}}", true},
		"yaml in inline variable":    {"apiVersion: 1\nplugins: {}", false},
		"unknown plugin":             {`{"apiVersion":1,"plugins":{"not.official":{}}}`, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseProvisionDocument([]byte(tc.input), tc.yaml)
			if err == nil || strings.Contains(err.Error(), "private-literal") {
				t.Fatalf("expected a redacted declaration error, got %v", err)
			}
		})
	}
	if _, err := parseProvisionDocument([]byte(`{"apiVersion":1,"plugins":{}}`), false); err != nil {
		t.Fatalf("explicit release: %v", err)
	}
}

func TestProvisionVersionMatchesOfficialReleaseRules(t *testing.T) {
	for _, version := range []string{"1.0.0-01", "1.0.0-alpha.01", "1.0.0-alpha..2", "1.0.0-" + strings.Repeat("a", 123), "1.0.0-01a.0", "1.0.0-" + strings.Repeat("a", 122)} {
		input := fmt.Sprintf(`{"apiVersion":1,"plugins":{"silo.auth.oidc":{"version":%q}}}`, version)
		schema, err := provisionSchema()
		if err != nil {
			t.Fatal(err)
		}
		var fields any
		if err := json.Unmarshal([]byte(input), &fields); err != nil {
			t.Fatal(err)
		}
		_, catalogErr := validateOfficialReleaseRequest("silo.auth.oidc", version)
		valid := catalogErr == nil
		if (schema.Validate(fields) == nil) != valid {
			t.Errorf("published schema disagrees with the catalog for %q", version)
		}
		if _, err := parseProvisionDocument([]byte(input), false); (err == nil) != valid {
			t.Errorf("document loader disagrees with the catalog for %q", version)
		}
	}
}

func TestProvisionDocumentHostIntegersAndPluginNumbers(t *testing.T) {
	doc, err := parseProvisionDocument([]byte(`{"apiVersion":1e0,"plugins":{"silo.auth.oidc":{"auth_binding":{"capability_id":"oidc","display_order":3.0}}}}`), false)
	if err != nil || doc.APIVersion != 1 || *doc.Plugins["silo.auth.oidc"].AuthBinding.DisplayOrder != 3 {
		t.Fatalf("schema-valid integral host options: %v", err)
	}
	const key = "connection"
	manifest := &pluginv1.PluginManifest{
		GlobalConfigSchema: []*pluginv1.ConfigSchema{{Key: key, JsonSchema: `{"type":"object","properties":{"limits":{"type":"array","items":{"type":"number"}}}}`}},
	}
	for _, tc := range []struct {
		input string
		ok    bool
	}{
		{"1e400", false},
		{"9007199254740993", false},
		{"1.00000000000000000001", false},
		{"9007199254740992", true},
		{"0.1", true},
	} {
		field := map[string]any{"limits": []any{json.Number(tc.input)}}
		err := validateProvisionConfig(manifest, key, field, field)
		if (err == nil) != tc.ok {
			t.Fatalf("runtime representation of %s: %v", tc.input, err)
		}
		if err != nil {
			refusal := invalidProvisionConfig("silo.auth.oidc", key, err, true).Error()
			if !strings.Contains(refusal, "connection.limits[0]") || strings.Contains(refusal, tc.input) {
				t.Fatalf("expected only the affected path in %q", refusal)
			}
		}
	}
	const exactYAML = "apiVersion: 1\nplugins:\n  silo.auth.oidc:\n    config:\n      connection:\n        seconds: 1000000000000000128.0"
	exact, err := parseProvisionDocument([]byte(exactYAML), true)
	if err != nil || exact.Plugins["silo.auth.oidc"].Config[key]["seconds"] != json.Number("1000000000000000128") {
		t.Fatalf("exact YAML integer was lost during conversion: %v", err)
	}

	if _, err := parseProvisionDocument([]byte("apiVersion: 1\nplugins:\n  silo.auth.oidc:\n    config:\n      connection:\n        seconds: 9007199254740993.0"), true); err == nil {
		t.Fatal("YAML rounded the number before validation")
	}
}

// The schema lists the provider types and capabilities the server knows, so a
// type added in config is declarable.
func TestProvisionSchemaMatchesAIProviderTypes(t *testing.T) {
	var schema struct {
		Defs struct {
			AIProvider struct {
				Properties struct {
					Type         struct{ Enum []string } `json:"type"`
					Capabilities struct {
						Items struct{ Enum []string } `json:"items"`
					} `json:"capabilities"`
				} `json:"properties"`
			} `json:"aiProvider"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(provisioningv1.Schema, &schema); err != nil {
		t.Fatal(err)
	}
	var types, capabilities []string
	for _, spec := range config.AIProviderTypes {
		types = append(types, string(spec.Type))
	}
	for _, capability := range config.AICapabilities {
		capabilities = append(capabilities, string(capability))
	}
	if got := schema.Defs.AIProvider.Properties.Type.Enum; !slices.Equal(got, types) {
		t.Errorf("schema types = %v, want %v", got, types)
	}
	if got := schema.Defs.AIProvider.Properties.Capabilities.Items.Enum; !slices.Equal(got, capabilities) {
		t.Errorf("schema capabilities = %v, want %v", got, capabilities)
	}
}
