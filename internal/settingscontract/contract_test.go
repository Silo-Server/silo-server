package settingscontract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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

func TestSubtitleAppearanceDefaultUsesBoxBackground(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	def, ok := manifest.Lookup(keySubtitleAppearance)
	if !ok {
		t.Fatal("playback.subtitle_appearance is not registered")
	}

	var appearance struct {
		BackgroundStyle   string `json:"backgroundStyle"`
		BackgroundOpacity int    `json:"backgroundOpacity"`
	}
	if err := json.Unmarshal(def.DefaultValue, &appearance); err != nil {
		t.Fatalf("decoding subtitle appearance default: %v", err)
	}
	if appearance.BackgroundStyle != "box" || appearance.BackgroundOpacity != 75 {
		t.Errorf("subtitle background default = %s %d%%, want box 75%%",
			appearance.BackgroundStyle, appearance.BackgroundOpacity)
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

		// Unregistered keys the web client writes through the extension bag.
		// Two of these the server also reads back, so they cannot be demoted to
		// client-local state: next_up_mode decides home section assembly, and
		// card_overlays falls back to a server-wide admin default.
		"card_overlays":        "ui.card_overlays",
		"next_up_mode":         "ui.next_up_mode",
		"sidebar_pins":         "ui.sidebar_pins",
		"disabled_library_ids": "ui.disabled_library_ids",
		"library_order":        "ui.library_order",

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

// TestNumbersMatchECMAScript pins the cases where Go's own float formatting
// disagrees with ECMAScript's Number::toString, which is what RFC 8785
// requires. Every expectation below is the literal output of String(x) in
// node; a client running a conforming JCS library must agree byte for byte or
// its digest of the same manifest will differ from the server's.
func TestNumbersMatchECMAScript(t *testing.T) {
	cases := []struct{ in, want string }{
		// Go's 'g' would switch to exponential at 1e-5; ECMAScript holds off
		// until the exponent drops below -6.
		{"0.00001", "0.00001"},
		{"1e-5", "0.00001"},
		{"0.000001", "0.000001"},
		{"1e-6", "0.000001"},
		// Below the threshold both use exponential, but Go zero-pads.
		{"1e-7", "1e-7"},
		{"-1e-7", "-1e-7"},
		{"5e-324", "5e-324"},
		// Go prints negative zero as "-0"; JCS has no negative zero.
		{"-0", "0"},
		{"0", "0"},
		// Integers render without a fraction up to 1e21, exponential above.
		{"1e20", "100000000000000000000"},
		{"1e21", "1e+21"},
		{"1e22", "1e+22"},
		{"1e100", "1e+100"},
		{"3.0", "3"},
		{"1e2", "100"},
		// Shortest round-trip digits, not the exact binary value.
		{"1234567.5", "1234567.5"},
		{"0.1", "0.1"},
		{"2.50", "2.5"},
		{"-2.5", "-2.5"},
		{"1.5e300", "1.5e+300"},
		{"123.456", "123.456"},
	}

	for _, tc := range cases {
		got, err := canonicalize([]byte(tc.in))
		if err != nil {
			t.Errorf("canonicalize(%s): %v", tc.in, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("canonicalize(%s) = %s, want %s (ECMAScript String())", tc.in, got, tc.want)
		}
	}
}

// TestStringsAreNotHTMLEscaped guards the other half of JCS string handling.
// encoding/json escapes <, > and & by default, which would silently fork the
// server's digest from every conforming client the first time a label contains
// an ampersand.
func TestStringsAreNotHTMLEscaped(t *testing.T) {
	got, err := canonicalize([]byte(`{"label":"Audio & subtitles","hint":"<auto> or >90%"}`))
	if err != nil {
		t.Fatalf("canonicalizing: %v", err)
	}
	want := `{"hint":"<auto> or >90%","label":"Audio & subtitles"}`
	if string(got) != want {
		t.Fatalf("canonicalize() = %s, want %s", got, want)
	}

	// The escapes JCS does require are still applied.
	got, err = canonicalize([]byte(`{"a":"q\"uote\\back\ttab\nline"}`))
	if err != nil {
		t.Fatalf("canonicalizing escapes: %v", err)
	}
	if want := `{"a":"q\"uote\\back\ttab\nline"}`; string(got) != want {
		t.Fatalf("canonicalize() = %s, want %s", got, want)
	}

	// Control characters below 0x20 with no short escape use \u00xx.
	got, err = canonicalize([]byte(`{"a":"\u0001"}`))
	if err != nil {
		t.Fatalf("canonicalizing control character: %v", err)
	}
	if want := `{"a":"\u0001"}`; string(got) != want {
		t.Fatalf("canonicalize() = %s, want %s", got, want)
	}
}

// TestETagCoversValueSchemas fails if the entity tag is ever narrowed back to
// manifest.json alone. The schemas decide which object values the server
// accepts, so a change to one changes the contract; if it did not move the tag,
// every client would 304 forever against validation rules that had shifted
// underneath them.
func TestETagCoversValueSchemas(t *testing.T) {
	schemas, err := SchemaBytes()
	if err != nil {
		t.Fatalf("reading value schemas: %v", err)
	}
	if len(schemas) == 0 {
		t.Fatal("no value schemas, so this test proves nothing")
	}

	manifest, err := CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalizing manifest: %v", err)
	}
	baseline, err := digestWithSchemas(manifest)
	if err != nil {
		t.Fatalf("digesting: %v", err)
	}

	live, err := ETag()
	if err != nil {
		t.Fatalf("computing ETag: %v", err)
	}
	if live != baseline {
		t.Fatalf("ETag() = %s, want the schema-inclusive digest %s", live, baseline)
	}

	if reference := sha256Of(manifest, schemas); reference != baseline {
		t.Fatalf("test digest helper disagrees with digestWithSchemas: %s vs %s",
			reference, baseline)
	}

	// Tightening a schema — the change that alters what the server accepts
	// while leaving manifest.json byte-identical — must move the digest.
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	tightened := map[string][]byte{}
	for name, body := range schemas {
		tightened[name] = body
	}
	tightened[names[0]] = []byte(`{"type":"object","additionalProperties":false}`)
	if sha256Of(manifest, tightened) == baseline {
		t.Error("changing a value schema's contents did not change the contract digest")
	}

	// So must renaming one, since the filename is what a definition's
	// schema_ref binds to.
	renamed := map[string][]byte{}
	for name, body := range schemas {
		renamed[name] = body
	}
	renamed["renamed-"+names[0]] = renamed[names[0]]
	delete(renamed, names[0])
	if sha256Of(manifest, renamed) == baseline {
		t.Error("renaming a value schema did not change the contract digest")
	}

	// Whitespace, on the other hand, must not: the schemas are canonicalized
	// before hashing, so reformatting a file is not a contract change.
	reformatted := map[string][]byte{}
	for name, body := range schemas {
		reformatted[name] = body
	}
	reformatted[names[0]] = append(append([]byte(nil), reformatted[names[0]]...), '\n', ' ')
	if sha256Of(manifest, reformatted) != baseline {
		t.Error("reformatting a value schema changed the contract digest")
	}
}

func sha256Of(manifest []byte, schemas map[string][]byte) string {
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}
	sort.Strings(names)

	digest := sha256.New()
	digest.Write(manifest)
	for _, name := range names {
		canonical, err := canonicalize(schemas[name])
		if err != nil {
			// A deliberately-perturbed schema may not parse; hash the raw bytes
			// so the test still observes a change.
			canonical = schemas[name]
		}
		_, _ = fmt.Fprintf(digest, "\n%d:%s\n%d:", len(name), name, len(canonical))
		digest.Write(canonical)
	}
	return `"` + hex.EncodeToString(digest.Sum(nil)) + `"`
}

// TestDerivedRepresentationsAreMemoized keeps the manifest endpoint cheap: all
// four values are pure functions of files fixed at compile time, and a
// conditional GET must not pay a full parse and re-serialize to answer 304.
func TestDerivedRepresentationsAreMemoized(t *testing.T) {
	first, err := CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalizing: %v", err)
	}
	second, err := CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalizing again: %v", err)
	}

	// Callers get their own copy, so mutating one must not corrupt the cache.
	if len(first) > 0 {
		first[0] = 'X'
	}
	third, err := CanonicalBytes()
	if err != nil {
		t.Fatalf("canonicalizing a third time: %v", err)
	}
	if string(second) != string(third) {
		t.Fatal("mutating a returned slice corrupted the memoized canonical bytes")
	}

	allocs := testing.AllocsPerRun(100, func() {
		if _, err := ETag(); err != nil {
			t.Fatalf("computing ETag: %v", err)
		}
		if _, err := PublicETag(); err != nil {
			t.Fatalf("computing PublicETag: %v", err)
		}
	})
	if allocs > 0 {
		t.Errorf("ETag()+PublicETag() allocate %.0f times per call; both should be memoized", allocs)
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
	if !strings.Contains(string(public), `"option_sets"`) {
		t.Fatal("public manifest omitted advisory option_sets")
	}
}

func TestLanguageOptionSetsAreCanonicalAndReferenced(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	wantReferences := map[string]string{
		"playback.audio_language":    "playback_audio_languages",
		"playback.subtitle_language": "playback_subtitle_languages",
		"catalog.metadata_language":  "catalog_metadata_languages",
	}
	for key, optionSetID := range wantReferences {
		def, ok := manifest.Lookup(key)
		if !ok {
			t.Fatalf("%s is not registered", key)
		}
		if def.SuggestedOptions != optionSetID {
			t.Errorf("%s suggested_options = %q, want %q",
				key, def.SuggestedOptions, optionSetID)
		}
		optionSet, ok := manifest.OptionSets[optionSetID]
		if !ok {
			t.Fatalf("%s references missing option set %q", key, optionSetID)
		}
		if len(optionSet.Options) < 30 {
			t.Errorf("%s has only %d suggestions; want the shared language floor",
				optionSetID, len(optionSet.Options))
		}
		for _, option := range optionSet.Options {
			normalized, valid := NormalizeLanguageTag(option.Value)
			if !valid || normalized != option.Value {
				t.Errorf("%s contains non-canonical value %q", optionSetID, option.Value)
			}
		}
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

	optionSet := OptionSet{Options: []SuggestedOption{
		{Value: "en", IntroducedIn: 5},
		{Value: "fr", IntroducedIn: 9},
	}}
	if got := optionSet.OptionsAtRevision(5); len(got) != 1 || got[0].Value != "en" {
		t.Errorf("OptionsAtRevision(5) = %v, want [en]", got)
	}
	if got := optionSet.OptionsAtRevision(9); len(got) != 2 {
		t.Errorf("OptionsAtRevision(9) = %v, want both suggestions", got)
	}
}

func TestOptionSetValidationRejectsBrokenPresentationMetadata(t *testing.T) {
	base := func() *Manifest {
		return &Manifest{
			APIVersion: 1,
			Revision:   2,
			OptionSets: map[string]OptionSet{
				"languages": {
					Type: TypeLanguageTag,
					Options: []SuggestedOption{
						{Value: "en", IntroducedIn: 1},
					},
				},
			},
			Definitions: []Definition{{
				Key:              fixtureKey,
				IntroducedIn:     1,
				Persistence:      PersistenceRemote,
				AllowedScopes:    []ScopeEntry{{Scope: ScopeProfile}},
				ResolutionOrder:  []Scope{ScopeProfile, ScopeDefault},
				ValueSchema:      ValueSchema{Type: TypeLanguageTag, Nullable: true},
				DefaultValue:     json.RawMessage(jsonNull),
				Category:         "playback",
				Label:            "Example",
				Description:      "Example.",
				SuggestedOptions: "languages",
				UnsetLabel:       "None",
			}},
		}
	}

	cases := map[string]func(*Manifest){
		"missing reference": func(m *Manifest) {
			m.Definitions[0].SuggestedOptions = "missing"
		},
		"type mismatch": func(m *Manifest) {
			m.Definitions[0].ValueSchema.Type = TypeString
			max := 20
			m.Definitions[0].ValueSchema.MaxLength = &max
			m.Definitions[0].DefaultValue = json.RawMessage(`null`)
		},
		"duplicate value": func(m *Manifest) {
			set := m.OptionSets["languages"]
			set.Options = append(set.Options, SuggestedOption{Value: "en", IntroducedIn: 1})
			m.OptionSets["languages"] = set
		},
		"non-canonical tag": func(m *Manifest) {
			set := m.OptionSets["languages"]
			set.Options[0].Value = "EN_us"
			m.OptionSets["languages"] = set
		},
		"future revision": func(m *Manifest) {
			set := m.OptionSets["languages"]
			set.Options[0].IntroducedIn = 3
			m.OptionSets["languages"] = set
		},
		"unset on non-nullable": func(m *Manifest) {
			m.Definitions[0].ValueSchema.Nullable = false
			m.Definitions[0].DefaultValue = json.RawMessage(`"en"`)
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := base()
			mutate(manifest)
			if err := manifest.index(); err != nil {
				t.Fatalf("indexing: %v", err)
			}
			if err := manifest.Validate(nil); err == nil {
				t.Fatal("Validate accepted broken presentation metadata")
			}
		})
	}
}

// TestWidenedBoundsStayResolvableAtOlderRevisions is the case a bare scalar
// plus a revision tag could not express. player.sleep_timer_default_minutes
// ships with a 240 maximum; widening it to 480 later must not erase the 240,
// because a client pinned to the newer revision still has to talk to servers
// that enforce the older one.
func TestWidenedBoundsStayResolvableAtOlderRevisions(t *testing.T) {
	widened := &Bound{History: []BoundEntry{
		{Value: 240},
		{Value: 480, IntroducedIn: 3},
	}}

	for _, tc := range []struct {
		revision int
		want     float64
	}{
		{1, 240}, // the revision that introduced the definition
		{2, 240}, // still before the widening
		{3, 480}, // the widening itself
		{9, 480}, // and everything after it
	} {
		got, ok := widened.AtRevision(tc.revision)
		if !ok {
			t.Errorf("AtRevision(%d) found no bound", tc.revision)
			continue
		}
		if got != tc.want {
			t.Errorf("AtRevision(%d) = %g, want %g", tc.revision, got, tc.want)
		}
	}

	if got, _ := widened.Current(); got != 480 {
		t.Errorf("Current() = %g, want the newest bound 480", got)
	}

	// The server validates against its own newest bound regardless of any
	// peer's revision.
	schema := &ValueSchema{Type: TypeInteger, Minimum: fixedBound(0), Maximum: widened}
	if err := schema.ValidateValue(json.RawMessage(`480`), nil); err != nil {
		t.Errorf("ValidateValue(480) = %v, want nil", err)
	}
	if err := schema.ValidateValue(json.RawMessage(`481`), nil); err == nil {
		t.Error("481 was accepted above the widened maximum")
	}
}

// TestBoundsRoundTripInTheirAuthoredShape keeps the manifest diff honest: a
// bound nobody has widened must not sprout a history array when the contract is
// re-serialized, or the ETag changes for a file nobody edited.
func TestBoundsRoundTripInTheirAuthoredShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"bare", `240`},
		{"history", `[{"value":240},{"value":480,"introduced_in":3}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bound Bound
			if err := json.Unmarshal([]byte(tc.raw), &bound); err != nil {
				t.Fatalf("unmarshalling %s: %v", tc.raw, err)
			}
			encoded, err := json.Marshal(bound)
			if err != nil {
				t.Fatalf("marshaling: %v", err)
			}
			if string(encoded) != tc.raw {
				t.Errorf("round trip = %s, want %s", encoded, tc.raw)
			}
		})
	}
}

func TestBoundHistoriesAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name    string
		maximum *Bound
		want    string
	}{
		{
			name:    "narrowing is not a widening",
			maximum: &Bound{History: []BoundEntry{{Value: 480}, {Value: 240, IntroducedIn: 6}}},
			want:    "narrows",
		},
		{
			name:    "history must move forward",
			maximum: &Bound{History: []BoundEntry{{Value: 240}, {Value: 480, IntroducedIn: 5}}},
			want:    "not ordered",
		},
		{
			name:    "later entries must say when they arrived",
			maximum: &Bound{History: []BoundEntry{{Value: 240}, {Value: 480}}},
			want:    "must declare introduced_in",
		},
		{
			name:    "a bound cannot predate its definition",
			maximum: &Bound{History: []BoundEntry{{Value: 240, IntroducedIn: 2}}},
			want:    "the definition was introduced in 5",
		},
		{
			name:    "a bound cannot postdate the manifest",
			maximum: &Bound{History: []BoundEntry{{Value: 240}, {Value: 480, IntroducedIn: 99}}},
			want:    "after the manifest revision",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := &ValueSchema{Type: TypeInteger, Minimum: fixedBound(0), Maximum: tc.maximum}
			errs := schema.validate(5, 10, nil)
			if len(errs) == 0 {
				t.Fatalf("validate accepted %s", tc.name)
			}
			if joined := errors.Join(errs...).Error(); !strings.Contains(joined, tc.want) {
				t.Errorf("error %q does not mention %q", joined, tc.want)
			}
		})
	}
}

// TestEnumMembersCannotPredateTheirDefinition is the enum half of the same
// lower-bound rule allowed_scopes has always enforced.
func TestEnumMembersCannotPredateTheirDefinition(t *testing.T) {
	schema := &ValueSchema{
		Type:   TypeEnum,
		Values: []EnumMember{{Value: "old"}, {Value: "new", IntroducedIn: 2}},
	}
	errs := schema.validate(5, 10, nil)
	if len(errs) == 0 {
		t.Fatal("an enum member claiming to predate its definition was accepted")
	}
	if joined := errors.Join(errs...).Error(); !strings.Contains(joined, "before the definition's own 5") {
		t.Errorf("error %q does not name the definition revision", joined)
	}
}

// TestDuplicatePropertiesAreRejected covers the case where the parser, not the
// contract, decides what a value means. Both encoding/json and the schema
// validator keep the last occurrence silently.
func TestDuplicatePropertiesAreRejected(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	def, ok := manifest.Lookup(keySubtitleAppearance)
	if !ok {
		t.Fatal("playback.subtitle_appearance is not registered")
	}

	for name, raw := range map[string]string{
		"top level": `{"fontSize":"small","fontSize":"large"}`,
		"repeated with other properties": `{"fontSize":"small","position":"top",` +
			`"fontSize":"large"}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := def.ValueSchema.ValidateValue(json.RawMessage(raw), objSchemas)
			if err == nil {
				t.Fatal("a value with a repeated property was accepted")
			}
			if !strings.Contains(err.Error(), "repeats the property") {
				t.Errorf("error %q does not name the repeated property", err)
			}
		})
	}

	// The same value without the repeat still validates, so the check is not
	// simply rejecting everything.
	if err := def.ValueSchema.ValidateValue(
		json.RawMessage(`{"fontSize":"large","position":"top"}`), objSchemas); err != nil {
		t.Fatalf("a value with distinct properties was rejected: %v", err)
	}
}

// TestLoneSurrogatesAreRejected pins RFC 8785's requirement to terminate rather
// than substitute. encoding/json turns an unpaired escape into U+FFFD and
// reports success, which would have the server issue an ETag for bytes a
// conforming client must refuse.
func TestLoneSurrogatesAreRejected(t *testing.T) {
	for name, raw := range map[string]string{
		"leading alone":       `"\ud800"`,
		"trailing alone":      `"\udc00"`,
		"leading then normal": `"\ud800A"`,
		"nested in an object": `{"a":"x\udbffy"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := canonicalize([]byte(raw)); err == nil {
				t.Fatalf("canonicalized %s despite a lone surrogate", raw)
			}
		})
	}

	// A well-formed pair is a real character and must survive untouched.
	encoded, err := canonicalize([]byte(`"😀"`))
	if err != nil {
		t.Fatalf("a valid surrogate pair was rejected: %v", err)
	}
	if want := "\"\U0001F600\""; string(encoded) != want {
		t.Errorf("canonicalized pair = %s, want %s", encoded, want)
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

	// A stored value is a sparse override merged over the default, and the
	// current API already stores exactly these shapes — see the round trip in
	// settings_device_test.go. Requiring all nine properties would make the
	// migration quarantine preferences users really set.
	for name, partial := range map[string]string{
		"one property":   `{"fontSize":"small"}`,
		"two properties": `{"fontSize":"xxlarge","position":"top"}`,
		"only a color":   `{"fontColor":"#ff0000"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := def.ValueSchema.ValidateValue(json.RawMessage(partial), objSchemas); err != nil {
				t.Fatalf("partial subtitle appearance %s rejected: %v", partial, err)
			}
		})
	}

	// Font family names are stored verbatim from the platform's font list, and
	// they are not ASCII: Apple ships families whose canonical names are CJK.
	for name, family := range map[string]string{
		"stock macOS Japanese": "ヒラギノ角ゴ ProN",
		"simplified Chinese":   "宋体",
		"Latin-1 supplement":   "Åkzidenz Grotesk",
		"plain ASCII":          "Helvetica Neue",
		"generic":              "sans-serif",
	} {
		t.Run("font family "+name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"fontFamily": family})
			if err := def.ValueSchema.ValidateValue(raw, objSchemas); err != nil {
				t.Errorf("family %q rejected: %v", family, err)
			}
		})
	}
	// The exclusions hold: characters that would escape a CSS interpolation
	// or a font lookup stay rejected.
	for name, family := range map[string]string{
		"quote":         `Helvetica" Neue`,
		"brace":         "family{",
		"semicolon":     "family;",
		"leading space": " family",
		"backslash":     `fam\ily`,
	} {
		t.Run("font family rejects "+name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"fontFamily": family})
			if err := def.ValueSchema.ValidateValue(raw, objSchemas); err == nil {
				t.Errorf("family %q was accepted", family)
			}
		})
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
		// Sparse is allowed; empty is not. An override that overrides nothing is
		// the same state as no override, and the contract represents that as
		// unset rather than as a stored value.
		"empty override": `{}`,
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

// TestStrictUnmarshalRejectsTrailingContent covers the bytes a caller slicing a
// value out of a larger document is most likely to hand over. json.Decoder's
// More() answers false for a stray closing bracket, so relying on it let
// `true]` validate as a boolean.
func TestStrictUnmarshalRejectsTrailingContent(t *testing.T) {
	rejected := []string{
		`true]`, `true}`, `30]`, `30}`, `"always"}`, `"a" ]`,
		`true false`, `30 40`, `{} {}`, `[] []`,
	}
	for _, raw := range rejected {
		var value any
		if err := strictUnmarshal([]byte(raw), &value); err == nil {
			t.Errorf("strictUnmarshal(%s) accepted trailing content", raw)
		}
	}

	accepted := []string{`true`, `30`, `"always"`, `{"a":1}`, `[1,2]`, `null`, ` 30 `}
	for _, raw := range accepted {
		var value any
		if err := strictUnmarshal([]byte(raw), &value); err != nil {
			t.Errorf("strictUnmarshal(%s) = %v, want nil", raw, err)
		}
	}
}

// TestEnumMatchingIsTypeSafe guards the value types manifest.schema.json
// already permits. Comparing formatted tokens made the string "3" satisfy an
// integer member and "true" satisfy a boolean one.
func TestEnumMatchingIsTypeSafe(t *testing.T) {
	schema := &ValueSchema{
		Type: TypeEnum,
		Values: []EnumMember{
			{Value: "auto"},
			{Value: float64(3)},
			{Value: true},
			{Value: float64(1000000)},
		},
	}

	accepted := []string{`"auto"`, `3`, `true`, `3.0`, `1000000`, `1e6`}
	for _, raw := range accepted {
		if err := schema.ValidateValue(json.RawMessage(raw), nil); err != nil {
			t.Errorf("ValidateValue(%s) = %v, want nil", raw, err)
		}
	}

	rejected := []string{`"3"`, `"true"`, `"1000000"`, `4`, `false`, `"AUTO"`}
	for _, raw := range rejected {
		if err := schema.ValidateValue(json.RawMessage(raw), nil); err == nil {
			t.Errorf("ValidateValue(%s) was accepted; wrong JSON type or value", raw)
		}
	}

	// A string member and a numeric member that print alike are distinct, not
	// a duplicate.
	mixed := &ValueSchema{
		Type:   TypeEnum,
		Values: []EnumMember{{Value: "3"}, {Value: float64(3)}},
	}
	if errs := mixed.validate(1, 1, nil); len(errs) != 0 {
		t.Errorf(`members "3" and 3 reported as duplicates: %v`, errs)
	}
}

// fixedBound is a bound that has never been widened, which is every bound built
// by hand in a test.
func fixedBound(value float64) *Bound {
	return &Bound{History: []BoundEntry{{Value: value}}}
}

// TestStepIsEnforced closes the gap between a declared constraint and the
// single validation path. player.playback_speed advertises a 0.05 step, so a
// server that stores 1.4372 hands every client a value its stepper cannot
// represent.
func TestStepIsEnforced(t *testing.T) {
	step := 0.05
	schema := &ValueSchema{
		Type: TypeNumber, Minimum: fixedBound(0.25), Maximum: fixedBound(3.0), Step: &step,
	}

	for _, raw := range []string{`0.25`, `0.75`, `1`, `1.25`, `1.4`, `2.5`, `3`} {
		if err := schema.ValidateValue(json.RawMessage(raw), nil); err != nil {
			t.Errorf("ValidateValue(%s) = %v, want nil", raw, err)
		}
	}
	for _, raw := range []string{`0.26`, `1.4372`, `1.01`, `2.99`} {
		if err := schema.ValidateValue(json.RawMessage(raw), nil); err == nil {
			t.Errorf("ValidateValue(%s) was accepted despite the 0.05 step", raw)
		}
	}

	// Range still wins where both apply.
	if err := schema.ValidateValue(json.RawMessage(`3.05`), nil); err == nil {
		t.Error("a value above the maximum was accepted")
	}
}

// TestLanguageTagsAcceptWhatClientsProduce pins the shapes the narrower
// original pattern rejected. Every tag here is one a shipped client can emit
// without the user doing anything unusual.
func TestLanguageTagsAcceptWhatClientsProduce(t *testing.T) {
	wellFormed := map[string]string{
		"en":                 "en",
		"EN":                 "en",
		"en-US":              "en-US",
		"en-us":              "en-US",
		"EN-us":              "en-US",
		"en_US":              "en-US", // iOS Locale.identifier, Android Locale.toString()
		"zh-Hant-TW":         "zh-Hant-TW",
		"zh-hant-tw":         "zh-Hant-TW",
		"ca-ES-valencia":     "ca-ES-valencia",
		"ar-EG-u-nu-latn":    "ar-EG-u-nu-latn",
		"de-DE-u-co-phonebk": "de-DE-u-co-phonebk",
		"es-419":             "es-419",
		"en-US-x-private":    "en-US-x-private",
		// Extlang and private-use-only tags: the legacy length-only validator
		// accepted these, so the contract grammar must keep them storable.
		"zh-cmn":         "zh-cmn",
		"zh-yue":         "zh-yue",
		"zh-cmn-Hans-CN": "zh-cmn-Hans-CN",
		"zh-cmn-hans-cn": "zh-cmn-Hans-CN",
		"x-private":      "x-private",
		"X-Private":      "x-private",
		"x-abcd":         "x-abcd",
	}
	for input, want := range wellFormed {
		got, ok := NormalizeLanguageTag(input)
		if !ok {
			t.Errorf("NormalizeLanguageTag(%q) rejected a tag a client produces", input)
			continue
		}
		if got != want {
			t.Errorf("NormalizeLanguageTag(%q) = %q, want %q", input, got, want)
		}
	}

	// The empty string is not a language tag: "no preference" is null, which
	// the nullable flag on each language setting expresses.
	malformed := []string{"", " ", "e", "toolongprimary", "en-", "-US", "en--US", "123", "x", "x-", "x-toolongsubtag1"}
	for _, input := range malformed {
		if got, ok := NormalizeLanguageTag(input); ok {
			t.Errorf("NormalizeLanguageTag(%q) = %q, want rejection", input, got)
		}
	}
}

// TestNormalizeValueCanonicalizesLanguageTags proves normalization is reachable
// through the shared path, not just available as a helper. Without it en-US,
// en-us and EN-us are three rows for one preference and track matching misses
// on two of them.
func TestNormalizeValueCanonicalizesLanguageTags(t *testing.T) {
	schema := &ValueSchema{Type: TypeLanguageTag, Nullable: true}

	got, err := schema.NormalizeValue(json.RawMessage(`"en_us"`), nil)
	if err != nil {
		t.Fatalf("NormalizeValue: %v", err)
	}
	if string(got) != `"en-US"` {
		t.Fatalf("NormalizeValue = %s, want \"en-US\"", got)
	}

	got, err = schema.NormalizeValue(json.RawMessage(`null`), nil)
	if err != nil {
		t.Fatalf("NormalizeValue(null): %v", err)
	}
	if string(got) != jsonNull {
		t.Fatalf("NormalizeValue(null) = %s, want null", got)
	}

	if _, err := schema.NormalizeValue(json.RawMessage(`""`), nil); err == nil {
		t.Error(`NormalizeValue("") was accepted; unset must be null, not the empty string`)
	}
}

// TestQuotedNumbersAreRejected covers a type confusion encoding/json will not
// catch. json.Number is a string kind, so `"1.5"` unmarshals into it happily
// and Float64 then parses the quoted digits — the value validates, and
// NormalizeValue stores the quoted form into jsonb where every consumer reading
// it as a number disagrees with the row.
func TestQuotedNumbersAreRejected(t *testing.T) {
	for name, tc := range map[string]struct {
		schema *ValueSchema
		raw    string
	}{
		"number":            {&ValueSchema{Type: TypeNumber}, `"1.5"`},
		"integer":           {&ValueSchema{Type: TypeInteger}, `"3"`},
		"integer with sign": {&ValueSchema{Type: TypeInteger}, `"-3"`},
	} {
		t.Run(name, func(t *testing.T) {
			if err := tc.schema.ValidateValue(json.RawMessage(tc.raw), nil); err == nil {
				t.Fatalf("%s was accepted for a %s setting", tc.raw, tc.schema.Type)
			}
			if _, err := tc.schema.NormalizeValue(json.RawMessage(tc.raw), nil); err == nil {
				t.Fatalf("%s was stored for a %s setting", tc.raw, tc.schema.Type)
			}
		})
	}

	// The unquoted forms still validate, so the check is not rejecting numbers
	// outright.
	if err := (&ValueSchema{Type: TypeNumber}).ValidateValue(
		json.RawMessage(`1.5`), nil); err != nil {
		t.Errorf("1.5 was rejected for a number setting: %v", err)
	}
	if err := (&ValueSchema{Type: TypeInteger}).ValidateValue(
		json.RawMessage(`3`), nil); err != nil {
		t.Errorf("3 was rejected for an integer setting: %v", err)
	}
}

// TestRawInvalidUTF8IsRejected covers the other path to the substitution
// TestLoneSurrogatesAreRejectedForEveryType guards. A raw 0xff byte inside a
// quoted string — what an HTTP body carries when a client encodes text in the
// wrong charset — is not an escape, so the surrogate scan never sees it, but
// encoding/json still turns it into U+FFFD and reports success. The value is
// then stored verbatim, which SQLite's json_valid accepts and Postgres jsonb
// refuses.
func TestRawInvalidUTF8IsRejected(t *testing.T) {
	for name, schema := range map[string]*ValueSchema{
		"string": {Type: TypeString},
		"object": {Type: TypeObject, SchemaRef: "subtitle-appearance.json"},
	} {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage([]byte{'"', 0xff, '"'})
			if name == "object" {
				raw = json.RawMessage(append(append([]byte(`{"fontSize":"`), 0xff), `"}`...))
			}
			if err := schema.ValidateValue(raw, objSchemas); err == nil {
				t.Fatal("a value with a raw invalid UTF-8 byte was accepted")
			}
			if _, err := schema.NormalizeValue(raw, objSchemas); err == nil {
				t.Fatal("a value with a raw invalid UTF-8 byte was stored")
			}
		})
	}

	// Valid multi-byte UTF-8 must still pass; the check is on well-formedness,
	// not on being ASCII.
	if err := (&ValueSchema{Type: TypeString}).ValidateValue(
		json.RawMessage(`"日本語 — café 😀"`), nil); err != nil {
		t.Errorf("valid multi-byte UTF-8 was rejected: %v", err)
	}
}

// TestLoneSurrogatesAreRejectedForEveryType pins the check to the whole
// validation path rather than the object branch it started in. A lone surrogate
// decodes to U+FFFD on SQLite and is refused outright by Postgres jsonb, so a
// string setting that skipped the check made the two backends disagree about
// whether the same value could be stored at all.
func TestLoneSurrogatesAreRejectedForEveryType(t *testing.T) {
	for name, schema := range map[string]*ValueSchema{
		"string":       {Type: TypeString},
		"language tag": {Type: TypeLanguageTag},
		"enum":         {Type: TypeEnum, Values: []EnumMember{{Value: "a"}}},
	} {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(`"\ud800"`)
			if err := schema.ValidateValue(raw, nil); err == nil {
				t.Fatal("a lone surrogate was accepted")
			}
			if _, err := schema.NormalizeValue(raw, nil); err == nil {
				t.Fatal("a lone surrogate was stored")
			}
		})
	}

	// ui.custom_css is the setting that actually carries free text, so pin it
	// against the real manifest definition too.
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	def, ok := manifest.Lookup("ui.custom_css")
	if !ok {
		t.Fatal("ui.custom_css is not registered")
	}
	if err := def.ValueSchema.ValidateValue(
		json.RawMessage(`"body { content: \"\ud800\"; }"`), objSchemas); err == nil {
		t.Error("ui.custom_css accepted a lone surrogate")
	}
	// A well-formed pair is an ordinary character and must still store.
	if err := def.ValueSchema.ValidateValue(
		json.RawMessage(`"body::after { content: \"😀\"; }"`), objSchemas); err != nil {
		t.Errorf("ui.custom_css rejected a valid surrogate pair: %v", err)
	}
}

// TestLibraryPageStateAcceptsTheWebClientsRealSearchStrings pins the search
// bound against what web/src/pages/libraryPageSearchParams.ts actually produces.
//
// An advanced view encodes every filter rule as three
// groups[i][rules][j][field|op|value] keys, so the serialized string grows
// about 150 characters per rule — measured at 216 for one rule, 518 for three,
// 820 for five. The bound started at 256, which the current endpoint has never
// enforced (it only checks that the value parses), so those rows are already
// stored in production and would have failed the migration that types them.
func TestLibraryPageStateAcceptsTheWebClientsRealSearchStrings(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	def, ok := manifest.Lookup("ui.library_page_state")
	if !ok {
		t.Fatal("ui.library_page_state is not registered")
	}

	// One group of n rules, in the key shape GROUP_RULE_PATTERN matches.
	search := func(rules int) string {
		parts := []string{"tab=movies", "sort=dateAdded", "order=desc", "groups%5B0%5D%5Bmatch%5D=all"}
		for i := 0; i < rules; i++ {
			for _, field := range []string{"field", "op", "value"} {
				parts = append(parts, fmt.Sprintf(
					"groups%%5B0%%5D%%5Brules%%5D%%5B%d%%5D%%5B%s%%5D=Science+Fiction", i, field))
			}
		}
		return strings.Join(parts, "&")
	}

	for _, rules := range []int{1, 3, 5, 10} {
		t.Run(fmt.Sprintf("%d rules", rules), func(t *testing.T) {
			value, err := json.Marshal(map[string]any{
				"version":   1,
				"libraries": map[string]any{"7": map[string]any{"search": search(rules)}},
			})
			if err != nil {
				t.Fatalf("encoding: %v", err)
			}
			if err := def.ValueSchema.ValidateValue(value, objSchemas); err != nil {
				t.Fatalf("a %d-rule saved view was rejected: %v", rules, err)
			}
		})
	}

	// The bound still exists — this is not an unbounded string.
	oversized, err := json.Marshal(map[string]any{
		"version":   1,
		"libraries": map[string]any{"7": map[string]any{"search": strings.Repeat("x", 8192)}},
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}
	if err := def.ValueSchema.ValidateValue(oversized, objSchemas); err == nil {
		t.Error("an 8KiB search string was accepted; the bound is not enforced")
	}
}

// TestObjectSchemasAcceptTheShapesClientsStore exercises every schema_ref
// against a real value.
//
// Defaults alone are insufficient: several definitions are nullable and stop
// at the null branch, while an empty-object default does not exercise dynamic
// properties. These real client shapes make every referenced schema prove its
// actual value vocabulary.
func TestObjectSchemasAcceptTheShapesClientsStore(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	valid := map[string]string{
		"catalog.metadata_language_overrides": `{"ja":"en","no":"x-silo-original"}`,
		"ui.card_overlays": `{"version":2,"preset":"minimal","order":["hdr","year"],` +
			`"items":{"hdr":{"enabled":true,"position":"top-left","accentColor":"#f5c518"},` +
			`"year":{"enabled":false,"position":"bottom-right","showIcon":true}}}`,
		"ui.sidebar_pins": `{"7":[{"type":"section","id":"recently-added","label":"Recently Added"}],` +
			`"12":[{"type":"collection","id":"c-9","label":"Marvel"}]}`,
		"ui.disabled_library_ids":      `[3,9]`,
		"ui.library_order":             `[9,3,1]`,
		"playback.subtitle_appearance": `{"fontSize":"large","position":"top"}`,
		// The web importer accepts computed CSS values, and multi-stop
		// gradients routinely pass 128 characters; a stored theme with one
		// must stay valid.
		"ui.custom_theme_vars": `{"color-bg":"#101014","gradient-hero":` +
			`"linear-gradient(135deg, rgba(16,16,20,0.98) 0%, rgba(28,20,44,0.94) 18%, ` +
			`rgba(44,24,64,0.9) 36%, rgba(64,28,84,0.86) 54%, rgba(84,32,104,0.82) 72%, ` +
			`rgba(104,36,124,0.78) 100%)"}`,
	}
	for key, value := range valid {
		t.Run(key, func(t *testing.T) {
			def, ok := manifest.Lookup(key)
			if !ok {
				t.Fatalf("%s is not registered", key)
			}
			if err := def.ValueSchema.ValidateValue(json.RawMessage(value), objSchemas); err != nil {
				t.Fatalf("a value the client stores was rejected: %v", err)
			}
		})
	}

	// Each schema must actually constrain something, or it is decoration.
	invalid := map[string]string{
		"catalog.metadata_language_overrides": `{"Norwegian":"not a language tag"}`,
		"ui.card_overlays":                    `{"version":2,"preset":"nonesuch","order":[],"items":{}}`,
		"ui.sidebar_pins":                     `{"7":[{"type":"bogus","id":"x","label":"L"}]}`,
		"ui.disabled_library_ids":             `[0,-1]`,
		"ui.library_order":                    `[1,1]`,
	}
	for key, value := range invalid {
		t.Run(key+" rejects", func(t *testing.T) {
			def, ok := manifest.Lookup(key)
			if !ok {
				t.Fatalf("%s is not registered", key)
			}
			if err := def.ValueSchema.ValidateValue(json.RawMessage(value), objSchemas); err == nil {
				t.Fatalf("%s was accepted", value)
			}
		})
	}
}

func TestUICustomizationObjectSchemasAreStrict(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}

	valid := map[string]string{
		"nav.primary_menu": `{"items":[` +
			`{"type":"builtin","destination":"home"},` +
			`{"type":"library","library_id":7,"label":"Movies"},` +
			`{"type":"section","library_id":7,"section_id":"recently-added","label":"Recently Added"},` +
			`{"type":"collection","collection_id":"horror","library_id":7,"label":"Horror"}]}`,
		"nav.shortcuts": `{"items":[` +
			`{"type":"library","library_id":7,"label":"Movies"},` +
			`{"type":"section","library_id":7,"section_id":"recently-added","label":"Recently Added"},` +
			`{"type":"collection","collection_id":"horror","label":"Horror"}]}`,
		"ui.card_presentation": `{"poster_size":"large","caption":"artwork"}`,
	}
	for key, value := range valid {
		t.Run(key+" accepts canonical shape", func(t *testing.T) {
			def, ok := manifest.Lookup(key)
			if !ok {
				t.Fatalf("%s is not registered", key)
			}
			if err := def.ValueSchema.ValidateValue(json.RawMessage(value), objSchemas); err != nil {
				t.Fatalf("valid %s value rejected: %v", key, err)
			}
		})
	}

	invalid := []struct {
		name  string
		key   string
		value string
	}{
		{
			name:  "primary menu requires home",
			key:   "nav.primary_menu",
			value: `{"items":[{"type":"builtin","destination":"movies"}]}`,
		},
		{
			name:  "primary menu allows home exactly once",
			key:   "nav.primary_menu",
			value: `{"items":[{"type":"builtin","destination":"home"},{"type":"builtin","destination":"home"}]}`,
		},
		{
			name:  "menu labels cannot be empty",
			key:   "nav.primary_menu",
			value: `{"items":[{"type":"builtin","destination":"home"},{"type":"library","library_id":7,"label":""}]}`,
		},
		{
			name:  "menu labels cannot be only whitespace",
			key:   "nav.primary_menu",
			value: `{"items":[{"type":"builtin","destination":"home"},{"type":"library","library_id":7,"label":"   "}]}`,
		},
		{
			name:  "menu target ids cannot be only whitespace",
			key:   "nav.primary_menu",
			value: `{"items":[{"type":"builtin","destination":"home"},{"type":"section","library_id":7,"section_id":"   ","label":"Recent"}]}`,
		},
		{
			name: "menu destination identity ignores labels",
			key:  "nav.primary_menu",
			value: `{"items":[{"type":"builtin","destination":"home"},` +
				`{"type":"library","library_id":7,"label":"Movies"},` +
				`{"type":"library","library_id":7,"label":"Films"}]}`,
		},
		{
			name:  "shortcuts cannot contain builtins",
			key:   "nav.shortcuts",
			value: `{"items":[{"type":"builtin","destination":"home"}]}`,
		},
		{
			name:  "shortcuts require positive library ids",
			key:   "nav.shortcuts",
			value: `{"items":[{"type":"library","library_id":0,"label":"Movies"}]}`,
		},
		{
			name:  "shortcut target ids cannot be only whitespace",
			key:   "nav.shortcuts",
			value: `{"items":[{"type":"collection","collection_id":"\t ","label":"Collection"}]}`,
		},
		{
			name: "shortcut destination identity includes collection context",
			key:  "nav.shortcuts",
			value: `{"items":[` +
				`{"type":"collection","collection_id":"horror","library_id":7,"label":"Horror"},` +
				`{"type":"collection","collection_id":"horror","library_id":7,"label":"Scary"}]}`,
		},
		{
			name:  "card presets reject unknown sizes",
			key:   "ui.card_presentation",
			value: `{"poster_size":"huge","caption":"artwork"}`,
		},
		{
			name:  "card presets reject extension fields",
			key:   "ui.card_presentation",
			value: `{"poster_size":"large","caption":"artwork","columns":8}`,
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			def, ok := manifest.Lookup(tc.key)
			if !ok {
				t.Fatalf("%s is not registered", tc.key)
			}
			if err := def.ValueSchema.ValidateValue(json.RawMessage(tc.value), objSchemas); err == nil {
				t.Fatalf("invalid %s value was accepted: %s", tc.key, tc.value)
			}
		})
	}
}

// TestQualityIsTwoIndependentAxes pins the split that replaced the compound
// ladder values. A client composes its own presets from these two, so the
// contract must not constrain them jointly.
func TestQualityIsTwoIndependentAxes(t *testing.T) {
	manifest, err := Load()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	quality, ok := manifest.Lookup(keyPreferredQuality)
	if !ok {
		t.Fatal("playback.preferred_quality is not registered")
	}
	bitrate, ok := manifest.Lookup("playback.max_bitrate_kbps")
	if !ok {
		t.Fatal("playback.max_bitrate_kbps is not registered")
	}

	// The resolution axis carries no bitrate spellings.
	for _, member := range quality.ValueSchema.Values {
		if s, isString := member.Value.(string); isString && strings.Contains(s, "-") {
			t.Errorf("quality member %q looks like a compound ladder rung; "+
				"bitrate belongs to playback.max_bitrate_kbps", s)
		}
	}

	// Uncapped has to be expressible, or every client invents a sentinel.
	if !bitrate.ValueSchema.Nullable {
		t.Error("max_bitrate_kbps must be nullable so uncapped is a real value")
	}
	if string(bitrate.DefaultValue) != jsonNull {
		t.Errorf("max_bitrate_kbps defaults to %s, want null (uncapped)", bitrate.DefaultValue)
	}

	// Both axes must resolve identically, or a device override of one and a
	// profile value of the other would compose into a pair the user never chose.
	if len(quality.ResolutionOrder) != len(bitrate.ResolutionOrder) {
		t.Fatalf("resolution orders differ: %v vs %v",
			quality.ResolutionOrder, bitrate.ResolutionOrder)
	}
	for i := range quality.ResolutionOrder {
		if quality.ResolutionOrder[i] != bitrate.ResolutionOrder[i] {
			t.Errorf("resolution orders differ at %d: %q vs %q",
				i, quality.ResolutionOrder[i], bitrate.ResolutionOrder[i])
		}
	}

	// The legacy compound values decompose losslessly; this is the table the
	// migration implements.
	for _, tc := range []struct {
		legacy     string
		resolution string
		kbps       int
	}{
		{"1080p-high", "1080p", 10000},
		{"1080p", "1080p", 6000},
		{"1080p-8", "1080p", 6000},
		{"720p-high", "720p", 4000},
		{"720p-medium", "720p", 3000},
		{"720p", "720p", 2000},
		{"480p", "480p", 1500},
		{"420p", "480p", 720},
		{"328p", "480p", 720},
	} {
		t.Run("decompose "+tc.legacy, func(t *testing.T) {
			res, _ := json.Marshal(tc.resolution)
			if err := quality.ValueSchema.ValidateValue(res, objSchemas); err != nil {
				t.Errorf("resolution %q is not a member: %v", tc.resolution, err)
			}
			kbps, _ := json.Marshal(tc.kbps)
			if err := bitrate.ValueSchema.ValidateValue(kbps, objSchemas); err != nil {
				t.Errorf("bitrate %d is out of range: %v", tc.kbps, err)
			}
		})
	}
}
