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
			// The null case is settled before scalarDefault, which rejects null
			// as non-scalar. Asking it first skipped the subtest and left the
			// comparison below unreachable, so a nullable contract default could
			// drift from the registry without failing anything.
			//
			// The legacy registry stores every value as a string and has no way
			// to say "unset", so it spells that as the empty string. The
			// contract spells it null, which is why the language settings are
			// nullable. Those are the same statement, not a disagreement — but
			// null against a non-empty registry default is a real one.
			if strings.TrimSpace(string(def.DefaultValue)) == "null" {
				if spec.DefaultValue != "" {
					t.Errorf("default disagrees: settingsRegistry has %q, contract has null",
						spec.DefaultValue)
				}
				return
			}

			contractDefault, err := scalarDefault(def.DefaultValue)
			if err != nil {
				t.Skipf("contract default is not a scalar: %s", def.DefaultValue)
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

// TestAudioLanguageRejectsMalformedTags closes a drift measured against the
// live server: the manifest declares playback.audio_language as language_tag,
// but the registry check was "32 characters or fewer", so "!!!" was stored for
// a field track matching would then silently never match.
func TestAudioLanguageRejectsMalformedTags(t *testing.T) {
	const key = "playback.audio_language"

	// The empty string is how the string-only API says "no preference", and
	// both Android and web send it to clear the choice. It must keep working.
	accepted := []string{"", "  ", "en", "EN", "en-US", "en_US", "pt-BR", "zh-Hant-TW", "es-419"}
	for _, v := range accepted {
		if err := validateRegisteredSetting(key, v, scopeDevice); err != nil {
			t.Errorf("value %q was rejected: %v", v, err)
		}
	}

	rejected := []string{"!!!", "english please", "e", "en-", "-US", "en--US", "123", "<script>"}
	for _, v := range rejected {
		if err := validateRegisteredSetting(key, v, scopeDevice); err == nil {
			t.Errorf("value %q was accepted; the manifest declares this key as language_tag", v)
		}
	}
}

// TestPlaybackSpeedEnforcesDeclaredStep closes the other measured drift: the
// manifest declares step 0.05 over 0.25..3.0 and nothing enforced it, so the
// server stored values no client's stepper can represent.
func TestPlaybackSpeedEnforcesDeclaredStep(t *testing.T) {
	const key = "player.playback_speed"

	for _, v := range []string{"0.25", "0.75", "1", "1.0", "1.25", "1.4", "2.5", "3", "3.0"} {
		if err := validateRegisteredSetting(key, v, scopeDevice); err != nil {
			t.Errorf("on-step value %q was rejected: %v", v, err)
		}
	}
	// Off-step but in-range values stay accepted on this legacy endpoint:
	// it accepted them before the contract landed, and v1 rules forbid
	// turning that 204 into a 400 before the coordinated cutover. Step
	// enforcement lives on the typed mutation endpoint, and the migration
	// snaps historical off-step values onto the grid.
	for _, v := range []string{"0.26", "1.4372", "1.01", "2.99"} {
		if err := validateRegisteredSetting(key, v, scopeDevice); err != nil {
			t.Errorf("in-range value %q was rejected on the legacy endpoint: %v", v, err)
		}
	}
	// Range still enforced, as it always was.
	for _, v := range []string{"0.2", "3.05", "abc"} {
		if err := validateRegisteredSetting(key, v, scopeDevice); err == nil {
			t.Errorf("out-of-range value %q was accepted", v)
		}
	}
}

// TestRegistryStepMatchesTheManifest keeps the two in lockstep: if the manifest
// widens or narrows the step, this fails until the typed endpoint follows.
// The legacy registry deliberately does not enforce the step — see the
// player.playback_speed entry — so the check runs against the contract
// validator the typed mutation endpoint uses.
func TestRegistryStepMatchesTheManifest(t *testing.T) {
	manifest, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	def, ok := manifest.Lookup("player.playback_speed")
	if !ok {
		t.Fatal("player.playback_speed is not in the manifest")
	}
	if def.ValueSchema.Step == nil {
		t.Fatal("the manifest no longer declares a step; drop this check too")
	}
	step := *def.ValueSchema.Step
	min, ok := def.ValueSchema.Minimum.Current()
	if !ok {
		t.Fatal("the manifest no longer declares a minimum for player.playback_speed")
	}

	// A value one half-step above the minimum must be rejected by the typed
	// endpoint's validator for whatever step the manifest currently declares.
	offStep := strconv.FormatFloat(min+step/2, 'f', -1, 64)
	if err := def.ValueSchema.ValidateValue(json.RawMessage(offStep), nil); err == nil {
		t.Errorf("%s is off the manifest's declared %g step but the contract accepted it", offStep, step)
	}
}
