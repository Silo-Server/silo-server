package plugins

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestLegacyReplacementPreservesCredentialProtectionDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	cipher := instanceStateTestCipher(t)
	store := NewInstallationStore(pool, cipher)
	configs := NewRuntimeConfigStore(pool, cipher)
	const credential = "synthetic-credential-value"
	const privateSchema = `{"type":"object","properties":{"token":{"type":"string","writeOnly":true}}}`
	const publicSchema = `{"type":"object","properties":{"token":{"type":"string"}}}`
	for _, tc := range []struct {
		name, refusal string
		localManifest bool
		saved         map[string]any
		nextSchema    string
	}{
		{name: "old credential loses protection", localManifest: true, saved: map[string]any{"token": credential}, nextSchema: publicSchema, refusal: `saved credential at "token"`},
		{name: "old credential keeps protection", localManifest: true, saved: map[string]any{"token": credential}, nextSchema: privateSchema},
		{name: "old manifest missing", saved: map[string]any{"token": credential}, nextSchema: publicSchema, refusal: "could not verify how the saved settings"},
		{name: "old manifest missing after settings cleared", saved: map[string]any{}, nextSchema: publicSchema},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := seedResidentQueryInstallation(t, pool, true)
			installation, err := store.GetByID(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.localManifest {
				path := filepath.Join(t.TempDir(), "plugin")
				previous := packageErrorTestManifest(installation.PluginID, installation.Version)
				previous.GlobalConfigSchema = []*pluginv1.ConfigSchema{{Key: "app", JsonSchema: privateSchema}}
				data, err := protojson.Marshal(previous)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(InstalledManifestPath(path), data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := store.Update(ctx, id, UpdateInstallationInput{InstallPath: &path}); err != nil {
					t.Fatal(err)
				}
			}
			if err := configs.PutGlobalConfig(ctx, id, "app", tc.saved); err != nil {
				t.Fatal(err)
			}
			candidate := packageErrorTestManifest(installation.PluginID, "1.0.0")
			candidate.GlobalConfigSchema = []*pluginv1.ConfigSchema{{Key: "app", JsonSchema: tc.nextSchema}}
			binary := []byte("replacement fixture")
			candidate.Checksum = sha256Hex(binary)
			data, err := protojson.Marshal(candidate)
			if err != nil {
				t.Fatal(err)
			}
			archive, err := buildBinaryPluginArchive(data, binary)
			if err != nil {
				t.Fatal(err)
			}
			err = store.Update(ctx, id, UpdateInstallationInput{
				Version: &candidate.Version,
				Archive: &ArchiveInput{ManifestJSON: data, Checksum: candidate.Checksum, Bytes: archive},
			})
			wantVersion := candidate.Version
			if tc.refusal != "" {
				requirePackageError(t, err, tc.refusal)
				if strings.Contains(err.Error(), credential) {
					t.Fatal("refusal includes the saved credential")
				}
				wantVersion = installation.Version
			} else if err != nil {
				t.Fatal(err)
			}
			current, err := store.GetByID(ctx, id)
			if err != nil || current.Version != wantVersion {
				t.Fatalf("expected installation version %s, got %+v, %v", wantVersion, current, err)
			}
		})
	}
}

func TestProvisionedReplacementProtectsNestedCredentialDB(t *testing.T) {
	pool := builtinGuardTestPool(t)
	ctx := t.Context()
	id := seedResidentQueryInstallation(t, pool, true)
	cipher := instanceStateTestCipher(t)
	configs := NewRuntimeConfigStore(pool, cipher)
	const credential = "synthetic-credential-value"
	if err := configs.PutGlobalConfig(ctx, id, "app", map[string]any{
		"account": map[string]any{"token": credential},
	}); err != nil {
		t.Fatal(err)
	}
	before := &pluginv1.PluginManifest{GlobalConfigSchema: []*pluginv1.ConfigSchema{{Key: "app",
		JsonSchema: `{"type":"object","properties":{"account":{"type":"object","properties":{"token":{"type":"string","writeOnly":true}}}}}`}}}
	writer := &ProvisionWriter{configs: configs}
	for _, tc := range []struct {
		name, schema string
		submitted    map[string]any
		rejected     bool
	}{
		{name: "drops whole entry", rejected: true},
		{name: "another nested credential",
			schema:   `{"type":"object","properties":{"account":{"type":"object","properties":{"new_token":{"writeOnly":true}}}}}`,
			rejected: true},
		{name: "moves credential into public property",
			schema:    `{"type":"object","properties":{"account":{"type":"object","properties":{"label":{"type":"string"}}}}}`,
			submitted: map[string]any{"account": map[string]any{"label": credential}},
			rejected:  true},
		{name: "moves credential to a different public field",
			schema:    `{"type":"object","properties":{"account":{"type":"object"},"label":{"type":"string"}}}`,
			submitted: map[string]any{"account": map[string]any{}, "label": credential},
			rejected:  true},
		{name: "moves credential to another protected field",
			schema:    `{"type":"object","properties":{"account":{"type":"object"},"new_token":{"type":"string","writeOnly":true}}}`,
			submitted: map[string]any{"account": map[string]any{}, "new_token": credential}},
		{name: "new safe value in public property",
			schema:    `{"type":"object","properties":{"account":{"type":"object","properties":{"label":{"type":"string"}}}}}`,
			submitted: map[string]any{"account": map[string]any{"label": "new label"}}},
		{name: "parent stays protected", schema: `{"type":"object","properties":{"account":{"type":"object","writeOnly":true}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := &pluginv1.PluginManifest{}
			if tc.schema != "" {
				candidate.GlobalConfigSchema = []*pluginv1.ConfigSchema{{Key: "app", JsonSchema: tc.schema}}
			}
			item := preparedProvision{pluginID: "silo.test", pkg: &PreparedProvisionPackage{
				Manifest: candidate, previousManifest: before, existing: &Installation{ID: id}, stage: &provisionPackageStage{},
			}}
			if tc.submitted != nil {
				item.config = map[string]map[string]any{"app": tc.submitted}
			}
			_, err := writer.planConfigValues(ctx, pool, item, id)
			if tc.rejected {
				if err == nil || !strings.Contains(err.Error(), "account.token") || strings.Contains(err.Error(), credential) {
					t.Fatalf("wanted credential refusal naming only its path, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("compatible replacement refused: %v", err)
			}
		})
	}
}

func TestMovedCredentialComparesExactJSONNumbers(t *testing.T) {
	const saved = json.Number("9007199254740992")
	before := map[string]any{"token": saved}
	removed := [][]string{{"token"}}
	for _, tc := range []struct {
		public json.Number
		refuse bool
	}{
		{"9007199254740992.0", true},
		{"9007199254740993", false},
	} {
		err := protectRetainedCredentials("app", removed, nil, before, map[string]any{"value": tc.public})
		if (err != nil) != tc.refuse {
			t.Errorf("replacement with %s: got %v, want refusal %v", tc.public, err, tc.refuse)
		}
	}
}
