package handlers

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

// contractKeyRenames maps a legacy registry key to the canonical contract key
// where the two deliberately differ.
//
// This is the only handwritten part of the cross-check, and it encodes a
// decision rather than an inventory: every entry is a rename the manifest notes
// justify. The registry itself is iterated, never transcribed — a hand-copied
// key list cannot detect a key added to one side and not the other, which is
// the drift this whole contract exists to prevent.
var contractKeyRenames = map[string]string{
	"subtitle_appearance": "playback.subtitle_appearance",
}

func canonicalContractKey(registryKey string) string {
	if canonical, ok := contractKeyRenames[registryKey]; ok {
		return canonical
	}
	return registryKey
}

// TestEverySettingsRegistryKeyIsRegisteredInTheContract is the gate that makes
// the manifest authoritative rather than descriptive. Adding a key to
// settingsRegistry without a manifest definition fails here.
func TestEverySettingsRegistryKeyIsRegisteredInTheContract(t *testing.T) {
	manifest, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading settings contract: %v", err)
	}

	for registryKey := range settingsRegistry {
		canonical := canonicalContractKey(registryKey)
		if _, ok := manifest.Lookup(canonical); !ok {
			t.Errorf("settingsRegistry key %q has no definition in "+
				"contracts/settings/v1/manifest.json (looked up %q). Add one, or add a "+
				"rename to contractKeyRenames if the canonical name differs.",
				registryKey, canonical)
		}
	}
}

// TestContractRenamesStayLive keeps the rename table honest: an entry for a key
// the registry no longer has is dead weight that hides the next real rename.
func TestContractRenamesStayLive(t *testing.T) {
	for registryKey := range contractKeyRenames {
		if _, ok := settingsRegistry[registryKey]; !ok {
			t.Errorf("contractKeyRenames maps %q, which settingsRegistry no longer defines",
				registryKey)
		}
	}
}

// TestRegistryDefaultsMatchTheContract catches the failure mode that is silent
// in production: the two sides agree a setting exists and disagree on what it
// resolves to when nobody has set it. A user who never touched the toggle gets
// one answer from the server today and a different one from a manifest-driven
// client tomorrow.
func TestRegistryDefaultsMatchTheContract(t *testing.T) {
	manifest, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading settings contract: %v", err)
	}

	for registryKey, spec := range settingsRegistry {
		canonical := canonicalContractKey(registryKey)
		def, ok := manifest.Lookup(canonical)
		if !ok {
			continue // reported by the coverage test above
		}

		t.Run(registryKey, func(t *testing.T) {
			contractDefault, err := scalarDefault(def.DefaultValue)
			if err != nil {
				t.Skipf("contract default is not a scalar: %s", def.DefaultValue)
			}
			// The legacy registry stores every value as a string and has no way
			// to say "unset", so it spells that as the empty string. The
			// contract spells it null, which is why the language settings are
			// nullable. Those are the same statement, not a disagreement.
			if spec.DefaultValue == "" && string(def.DefaultValue) == "null" {
				return
			}
			if spec.DefaultValue != contractDefault {
				t.Errorf("default disagrees: settingsRegistry has %q, contract has %q",
					spec.DefaultValue, contractDefault)
			}
		})
	}
}

// scalarDefault renders a contract default the way the legacy registry would
// have stored it, so the two can be compared.
func scalarDefault(raw json.RawMessage) (string, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	default:
		return "", errNotScalar
	}
}

var errNotScalar = &notScalarError{}

type notScalarError struct{}

func (*notScalarError) Error() string { return "not a scalar default" }

// TestContractLoadsUnderTheServerBuild is a cheap canary: the handlers package
// is linked into cmd/silo, so if the embedded manifest is self-inconsistent the
// failure shows up here rather than at a customer's startup.
func TestContractLoadsUnderTheServerBuild(t *testing.T) {
	manifest, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("embedded settings contract is invalid: %v", err)
	}
	if len(manifest.Keys()) == 0 {
		t.Fatal("settings contract declares no keys")
	}
	for _, key := range manifest.Keys() {
		if strings.TrimSpace(key) == "" {
			t.Error("contract declares an empty key")
		}
	}
}
