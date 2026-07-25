package settingscontract

import (
	"encoding/json"
	"strings"
	"testing"
)

// Keys referenced by more than one test. These are deliberately test-scoped:
// production code reads keys through generated bindings, not handwritten Go
// constants, so a parallel set of literals here would be a second source of
// truth for the very thing the manifest exists to own.
const (
	keyAudioLanguage      = "playback.audio_language"
	keyAutoPlayNext       = "playback.auto_play_next"
	keyNextUpPrompt       = "playback.next_up_prompt_seconds"
	keyPlaybackSpeed      = "player.playback_speed"
	keyPreferredQuality   = "playback.preferred_quality"
	keySubtitleAppearance = "playback.subtitle_appearance"
	keySubtitleMode       = "playback.subtitle_mode"

	// fixtureKey names definitions built inside tests, never the manifest.
	fixtureKey = "playback.example"

	jsonNull = "null"
)

// TestEmbeddedManifestLoads is the gate the whole contract rests on: the
// checked-in manifest parses, satisfies its own JSON Schema, and passes every
// structural invariant.
func TestEmbeddedManifestLoads(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("embedded manifest failed to load: %v", err)
	}
	if manifest.APIVersion != 1 {
		t.Errorf("api_version = %d, want 1", manifest.APIVersion)
	}
	if manifest.Revision < 1 {
		t.Errorf("revision = %d, want at least 1", manifest.Revision)
	}
	if len(manifest.Definitions) == 0 {
		t.Fatal("manifest declares no definitions")
	}
}

// TestEveryCurrentServerKeyIsRegistered pins the inventory. The design makes an
// unregistered official key a release blocker, so a key that exists in the
// legacy registry but not in the manifest has to fail here rather than be
// discovered during migration.
func TestEveryCurrentServerKeyIsRegistered(t *testing.T) {
	// Keys the legacy settingsRegistry in internal/api/handlers/settings.go
	// accepts today, mapped to their canonical names. A legacy key whose
	// canonical name differs is renamed deliberately; see the manifest notes.
	legacyToCanonical := map[string]string{
		"playback.preferred_quality":        keyPreferredQuality,
		"playback.audio_language":           keyAudioLanguage,
		"playback.auto_skip_intro":          "playback.auto_skip_intro",
		"playback.auto_skip_credits":        "playback.auto_skip_credits",
		"playback.auto_skip_recap":          "playback.auto_skip_recap",
		"playback.auto_play_next_preview":   "playback.auto_play_next_preview",
		"playback.auto_play_next":           keyAutoPlayNext,
		"playback.next_up_prompt_seconds":   keyNextUpPrompt,
		"subtitle_appearance":               keySubtitleAppearance,
		"ui.library_page_state":             "ui.library_page_state",
		"ui.remember_library_page_state":    "ui.remember_library_page_state",
		"search.media_scope":                "search.media_scope",
		"ui.date_format":                    "ui.date_format",
		"ui.time_format":                    "ui.time_format",
		"player.hdr_enabled":                "player.hdr_enabled",
		"player.dolby_vision_enabled":       "player.dolby_vision_enabled",
		"player.dv_profile7_hdr10_fallback": "player.dv_profile7_hdr10_fallback",
		"player.seek_cache_enabled":         "player.seek_cache_enabled",
		"player.playback_speed":             keyPlaybackSpeed,
		"player.audio_sync_ms":              "player.audio_sync_ms",
		"player.subtitle_sync_ms":           "player.subtitle_sync_ms",
		"player.video_gravity":              "player.video_gravity",
		"player.orientation_mode":           "player.orientation_mode",

		// Unregistered keys the extension bag accepted from the web client.
		"ui_theme":             "ui.theme",
		"ui_text_scale":        "ui.text_scale",
		"ui_text_weight":       "ui.text_weight",
		"ui_high_contrast":     "ui.high_contrast",
		"ui_custom_theme_vars": "ui.custom_theme_vars",
		"ui_custom_css":        "ui.custom_css",

		// Unregistered device-setting keys Android writes today.
		"player.match_frame_rate":            "player.match_frame_rate",
		"player.sleep_timer_default_minutes": "player.sleep_timer_default_minutes",
		"player.next_up_prompt_seconds":      keyNextUpPrompt,

		// Profile columns that become settings.
		"user_profiles.language":                    keyAudioLanguage,
		"user_profiles.subtitle_language":           "playback.subtitle_language",
		"user_profiles.subtitle_mode":               keySubtitleMode,
		"user_profiles.quality_preference":          keyPreferredQuality,
		"user_profiles.preferred_metadata_language": "catalog.metadata_language",
		"user_profiles.show_forced_subtitles":       "playback.show_forced_subtitles",
	}

	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	for legacy, canonical := range legacyToCanonical {
		if _, ok := manifest.Lookup(canonical); !ok {
			t.Errorf("legacy key %q maps to %q, which has no manifest definition", legacy, canonical)
		}
	}
}

func TestDefaultsValidateAgainstTheirOwnSchema(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	for i := range manifest.Definitions {
		def := &manifest.Definitions[i]
		t.Run(def.Key, func(t *testing.T) {
			if len(def.DefaultValue) == 0 {
				t.Fatal("default_value is missing")
			}
			if string(def.DefaultValue) == jsonNull {
				if !def.ValueSchema.Nullable {
					t.Fatal("default is null but the schema is not nullable")
				}
				return
			}
			if err := def.ValueSchema.ValidateValue(def.DefaultValue, objSchemas); err != nil {
				t.Fatalf("default %s is invalid: %v", def.DefaultValue, err)
			}
		})
	}
}

func TestResolutionOrdersAreComplete(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	for i := range manifest.Definitions {
		def := &manifest.Definitions[i]
		t.Run(def.Key, func(t *testing.T) {
			order := def.ResolutionOrder
			if len(order) == 0 {
				t.Fatal("resolution_order is empty")
			}
			if order[len(order)-1] != ScopeDefault {
				t.Fatalf("resolution_order ends with %q, want %q", order[len(order)-1], ScopeDefault)
			}

			seen := map[Scope]int{}
			for _, scope := range order[:len(order)-1] {
				seen[scope]++
				if seen[scope] > 1 {
					t.Errorf("resolution_order repeats %q", scope)
				}
				if !def.AllowsScope(scope) {
					t.Errorf("resolution_order resolves %q, absent from allowed_scopes", scope)
				}
			}
			for _, entry := range def.AllowedScopes {
				if seen[entry.Scope] == 0 {
					t.Errorf("scope %q is writable but never read", entry.Scope)
				}
			}
		})
	}
}

// TestCeilingConstraintsAreOrdered guards the failure the policy seam exists to
// prevent: a ceiling declared on a type where "cap this" has no meaning would
// silently do nothing.
func TestCeilingConstraintsAreOrdered(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	for i := range manifest.Definitions {
		def := &manifest.Definitions[i]
		if def.ConstrainedBy == nil {
			continue
		}
		kind := def.ConstrainedBy.Constraint
		if kind != ConstraintCeiling && kind != ConstraintFloor {
			continue
		}
		ordered := def.ValueSchema.Type == TypeInteger ||
			def.ValueSchema.Type == TypeNumber ||
			(def.ValueSchema.Type == TypeEnum && def.ValueSchema.Ordered)
		if !ordered {
			t.Errorf("%s declares a %s constraint on unordered type %s",
				def.Key, kind, def.ValueSchema.Type)
		}
	}
}

func TestRevisionTagsNeverExceedManifestRevision(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	for i := range manifest.Definitions {
		def := &manifest.Definitions[i]
		if def.IntroducedIn > manifest.Revision {
			t.Errorf("%s: introduced_in %d exceeds manifest revision %d",
				def.Key, def.IntroducedIn, manifest.Revision)
		}
		for _, entry := range def.AllowedScopes {
			if entry.IntroducedIn > manifest.Revision {
				t.Errorf("%s: scope %q introduced_in %d exceeds manifest revision %d",
					def.Key, entry.Scope, entry.IntroducedIn, manifest.Revision)
			}
		}
		for _, member := range def.ValueSchema.Values {
			if member.IntroducedIn > manifest.Revision {
				t.Errorf("%s: enum member %v introduced_in %d exceeds manifest revision %d",
					def.Key, member.Value, member.IntroducedIn, manifest.Revision)
			}
		}
	}
}

func TestClientLocalDefinitionsNeverReachTheAPI(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	for i := range manifest.Definitions {
		def := &manifest.Definitions[i]
		if def.Persistence != PersistenceClientLocal {
			continue
		}
		if def.IsRemote() {
			t.Errorf("%s: client_local definition reports IsRemote", def.Key)
		}
		for _, entry := range def.AllowedScopes {
			if entry.Scope.IsRemote() {
				t.Errorf("%s: client_local definition allows remote scope %q", def.Key, entry.Scope)
			}
		}
		if def.ConstrainedBy != nil {
			t.Errorf("%s: client_local definition declares a policy constraint the server cannot apply", def.Key)
		}
	}
}

// TestPrivateLocalKeysAreRejected covers the escape hatch's boundary: a
// local.* key must never appear in the shared contract.
func TestPrivateLocalKeysAreRejected(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	for i := range manifest.Definitions {
		if strings.HasPrefix(manifest.Definitions[i].Key, "local.") {
			t.Errorf("%s: private local.* keys must stay out of the shared manifest",
				manifest.Definitions[i].Key)
		}
	}
}

func TestCanonicalBytesAreStable(t *testing.T) {
	first, err := CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalizing: %v", err)
	}
	second, err := CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalizing again: %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("canonical bytes differ between calls")
	}

	// Canonical output must itself be canonical, or the digest depends on how
	// many times you ran it.
	recanonicalized, err := canonicalize(first)
	if err != nil {
		t.Fatalf("re-canonicalizing: %v", err)
	}
	if string(recanonicalized) != string(first) {
		t.Fatal("canonicalization is not idempotent")
	}

	tag, err := ETag()
	if err != nil {
		t.Fatalf("computing ETag: %v", err)
	}
	if !strings.HasPrefix(tag, `"`) || !strings.HasSuffix(tag, `"`) || len(tag) != 66 {
		t.Fatalf("ETag %q is not a quoted SHA-256 hex digest", tag)
	}
}

func TestCanonicalizationSortsKeysAndNormalizesNumbers(t *testing.T) {
	got, err := canonicalize([]byte(`{"b":1,"a":{"d":3.0,"c":0.05},"e":[2.50,1e2]}`))
	if err != nil {
		t.Fatalf("canonicalizing: %v", err)
	}
	want := `{"a":{"c":0.05,"d":3},"b":1,"e":[2.5,100]}`
	if string(got) != want {
		t.Fatalf("canonicalize() = %s, want %s", got, want)
	}
}

func TestPublicManifestStripsMaintainerNotes(t *testing.T) {
	public, err := PublicBytes()
	if err != nil {
		t.Fatalf("building public manifest: %v", err)
	}

	var doc struct {
		Definitions []map[string]json.RawMessage `json:"definitions"`
	}
	if err := json.Unmarshal(public, &doc); err != nil {
		t.Fatalf("parsing public manifest: %v", err)
	}
	if len(doc.Definitions) == 0 {
		t.Fatal("public manifest has no definitions")
	}
	for _, def := range doc.Definitions {
		if _, present := def["notes"]; present {
			t.Errorf("public manifest leaked maintainer notes: %s", def["key"])
		}
	}

	// The private manifest is what carries notes; if it stopped carrying any,
	// this test would pass vacuously.
	raw, err := RawBytes()
	if err != nil {
		t.Fatalf("reading raw manifest: %v", err)
	}
	if !strings.Contains(string(raw), `"notes"`) {
		t.Fatal("no definition carries notes, so the stripping test proves nothing")
	}
}

func TestRevisionAwareFilteringHidesNewerElements(t *testing.T) {
	def := &Definition{
		Key:          fixtureKey,
		IntroducedIn: 5,
		AllowedScopes: []ScopeEntry{
			{Scope: ScopeProfile},
			{Scope: ScopeProfileDevice, IntroducedIn: 9},
		},
		ValueSchema: ValueSchema{
			Type: TypeEnum,
			Values: []EnumMember{
				{Value: "old"},
				{Value: "new", IntroducedIn: 9},
			},
		},
	}

	if def.VisibleAtRevision(4) {
		t.Error("definition introduced at 5 should be hidden from a revision-4 peer")
	}
	if !def.VisibleAtRevision(5) {
		t.Error("definition introduced at 5 should be visible to a revision-5 peer")
	}

	if got := def.ScopesAtRevision(5); len(got) != 1 || got[0] != ScopeProfile {
		t.Errorf("ScopesAtRevision(5) = %v, want [profile]", got)
	}
	if got := def.ScopesAtRevision(9); len(got) != 2 {
		t.Errorf("ScopesAtRevision(9) = %v, want both scopes", got)
	}

	if got := def.EnumValuesAtRevision(5); len(got) != 1 || got[0].Value != "old" {
		t.Errorf("EnumValuesAtRevision(5) = %v, want [old]", got)
	}
	if got := def.EnumValuesAtRevision(9); len(got) != 2 {
		t.Errorf("EnumValuesAtRevision(9) = %v, want both members", got)
	}
}

func TestValidateValueRejectsOutOfContractValues(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	cases := []struct {
		name    string
		key     string
		value   string
		wantErr bool
	}{
		// The verified Android drift: the server contract stops at 3.0.
		{name: "speed at contract maximum", key: keyPlaybackSpeed, value: "3.0"},
		{name: "speed above contract maximum", key: keyPlaybackSpeed, value: "4.0", wantErr: true},
		{name: "speed below contract minimum", key: keyPlaybackSpeed, value: "0.1", wantErr: true},

		{name: "prompt seconds in range", key: keyNextUpPrompt, value: "30"},
		{name: "prompt seconds above range", key: keyNextUpPrompt, value: "121", wantErr: true},
		{name: "prompt seconds not an integer", key: keyNextUpPrompt, value: "30.5", wantErr: true},

		{name: "boolean true", key: keyAutoPlayNext, value: "true"},
		{name: "stringly boolean rejected", key: keyAutoPlayNext, value: `"true"`, wantErr: true},

		{name: "known enum member", key: keySubtitleMode, value: `"always"`},
		{name: "unknown enum member", key: keySubtitleMode, value: `"sometimes"`, wantErr: true},
		{name: "legacy empty string is not a mode", key: keySubtitleMode, value: `""`, wantErr: true},

		{name: "language tag", key: keyAudioLanguage, value: `"en-US"`},
		{name: "language tag with script", key: keyAudioLanguage, value: `"zh-Hans-CN"`},
		{name: "nullable language tag", key: keyAudioLanguage, value: jsonNull},
		{name: "malformed language tag", key: keyAudioLanguage, value: `"not a language"`, wantErr: true},

		{name: "non-nullable boolean rejects null", key: keyAutoPlayNext, value: jsonNull, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			def, ok := manifest.Lookup(tc.key)
			if !ok {
				t.Fatalf("no definition for %q", tc.key)
			}
			err := def.ValueSchema.ValidateValue(json.RawMessage(tc.value), objSchemas)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateValue(%s) succeeded, want rejection", tc.value)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateValue(%s) = %v, want success", tc.value, err)
			}
		})
	}
}

func TestObjectValuesValidateAgainstTheirSchema(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	def, ok := manifest.Lookup(keySubtitleAppearance)
	if !ok {
		t.Fatal("playback.subtitle_appearance is not registered")
	}

	valid := `{
		"fontSize": "large", "fontFamily": "sans-serif", "fontColor": "#ffffff",
		"backgroundColor": "#000000", "backgroundStyle": "shadow", "backgroundOpacity": 75,
		"textOutline": false, "textOutlineColor": "#000000", "position": "bottom"
	}`
	if err := def.ValueSchema.ValidateValue(json.RawMessage(valid), objSchemas); err != nil {
		t.Fatalf("valid subtitle appearance rejected: %v", err)
	}

	for name, invalid := range map[string]string{
		"unknown field": `{"fontSize":"large","fontFamily":"sans-serif","fontColor":"#ffffff",
			"backgroundColor":"#000000","backgroundStyle":"shadow","backgroundOpacity":75,
			"textOutline":false,"textOutlineColor":"#000000","position":"bottom","extra":1}`,
		"opacity out of range": `{"fontSize":"large","fontFamily":"sans-serif","fontColor":"#ffffff",
			"backgroundColor":"#000000","backgroundStyle":"shadow","backgroundOpacity":250,
			"textOutline":false,"textOutlineColor":"#000000","position":"bottom"}`,
		"malformed color": `{"fontSize":"large","fontFamily":"sans-serif","fontColor":"white",
			"backgroundColor":"#000000","backgroundStyle":"shadow","backgroundOpacity":75,
			"textOutline":false,"textOutlineColor":"#000000","position":"bottom"}`,
		"missing field":  `{"fontSize":"large"}`,
		"arbitrary json": `{"anything":"goes"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := def.ValueSchema.ValidateValue(json.RawMessage(invalid), objSchemas); err == nil {
				t.Fatal("invalid subtitle appearance was accepted")
			}
		})
	}
}

// TestValidateRejectsMalformedManifests exercises the invariant checks against
// hand-built manifests, since the checked-in one is expected to be valid.
func TestValidateRejectsMalformedManifests(t *testing.T) {
	if _, err := Load(); err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	base := func() Definition {
		return Definition{
			Key:             fixtureKey,
			IntroducedIn:    1,
			Persistence:     PersistenceRemote,
			AllowedScopes:   []ScopeEntry{{Scope: ScopeProfile}},
			ResolutionOrder: []Scope{ScopeProfile, ScopeDefault},
			ValueSchema:     ValueSchema{Type: TypeBoolean},
			DefaultValue:    json.RawMessage("false"),
			Category:        "playback",
			Label:           "Example",
			Description:     "Example.",
		}
	}

	cases := map[string]func(*Definition){
		"resolution order missing default": func(d *Definition) {
			d.ResolutionOrder = []Scope{ScopeProfile}
		},
		"resolution order references unlisted scope": func(d *Definition) {
			d.ResolutionOrder = []Scope{ScopeProfileDevice, ScopeProfile, ScopeDefault}
		},
		"writable scope never read": func(d *Definition) {
			d.AllowedScopes = append(d.AllowedScopes, ScopeEntry{Scope: ScopeProfileDevice})
		},
		"duplicate scope": func(d *Definition) {
			d.AllowedScopes = []ScopeEntry{{Scope: ScopeProfile}, {Scope: ScopeProfile}}
		},
		"default violates schema": func(d *Definition) {
			d.DefaultValue = json.RawMessage(`"yes"`)
		},
		"null default on non-nullable schema": func(d *Definition) {
			d.DefaultValue = json.RawMessage(jsonNull)
		},
		"remote setting with client_local scope": func(d *Definition) {
			d.AllowedScopes = []ScopeEntry{{Scope: ScopeClientLocal}}
			d.ResolutionOrder = []Scope{ScopeClientLocal, ScopeDefault}
		},
		"client_local setting with remote scope": func(d *Definition) {
			d.Persistence = PersistenceClientLocal
		},
		"ceiling on unordered enum": func(d *Definition) {
			d.ValueSchema = ValueSchema{
				Type:   TypeEnum,
				Values: []EnumMember{{Value: "a"}, {Value: "b"}},
			}
			d.DefaultValue = json.RawMessage(`"a"`)
			d.ConstrainedBy = &Constraint{PolicyInput: "example", Constraint: ConstraintCeiling}
		},
		"introduced_in after manifest revision": func(d *Definition) {
			d.IntroducedIn = 99
		},
		"scope introduced before its definition": func(d *Definition) {
			d.AllowedScopes = []ScopeEntry{{Scope: ScopeProfile, IntroducedIn: 1}}
			d.IntroducedIn = 2
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			def := base()
			mutate(&def)
			manifest := &Manifest{APIVersion: 1, Revision: 2, Definitions: []Definition{def}}
			if err := manifest.index(); err != nil {
				t.Fatalf("indexing: %v", err)
			}
			if err := manifest.Validate(objSchemas); err == nil {
				t.Fatal("Validate accepted a manifest that violates an invariant")
			}
		})
	}
}

func TestDuplicateKeysAreRejected(t *testing.T) {
	manifest := &Manifest{
		APIVersion: 1,
		Revision:   1,
		Definitions: []Definition{
			{Key: fixtureKey},
			{Key: fixtureKey},
		},
	}
	if err := manifest.index(); err == nil {
		t.Fatal("index accepted a duplicate key")
	}
}
