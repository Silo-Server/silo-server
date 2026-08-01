package settingsmigrate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

func planner(t *testing.T) *Planner {
	t.Helper()
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	return New(contract, settingscontract.ObjectSchemas())
}

func str(v string) *string { return &v }
func boolp(v bool) *bool   { return &v }

// find returns the single row for a key at an identity, failing if there is not
// exactly one.
func find(t *testing.T, res Result, key string, match func(Row) bool) Row {
	t.Helper()
	var hits []Row
	for _, row := range res.Rows {
		if row.Key == key && (match == nil || match(row)) {
			hits = append(hits, row)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("found %d rows for %s, want 1 (rows: %+v, rejects: %+v)",
			len(hits), key, res.Rows, res.Rejects)
	}
	return hits[0]
}

func hasKey(res Result, key string) bool {
	for _, row := range res.Rows {
		if row.Key == key {
			return true
		}
	}
	return false
}

// TestLegacyEffectiveDefaultsArePreserved pins the two schema defaults whose
// effective behavior differs from the contract defaults. Suppressing either
// would change playback at cutover: audio would stop preferring English, and
// quality would move from the legacy 1080p/6000 kbps cap to uncapped auto.
func TestLegacyEffectiveDefaultsArePreserved(t *testing.T) {
	res := planner(t).Plan(Input{Profiles: []LegacyProfile{{
		ID:                  "p1",
		Language:            str("en"),    // the column default, but see above
		SubtitleMode:        str("auto"),  // the column default
		QualityPreference:   str("1080p"), // the column default
		ShowForcedSubtitles: boolp(true),  // the column default
		SubtitleLanguage:    str(""),      // legacy spelling of unset
	}}})

	if len(res.Rejects) != 0 {
		t.Fatalf("unexpected rejects: %+v", res.Rejects)
	}
	language := find(t, res, "playback.audio_language", nil)
	if string(language.Value) != `"en"` {
		t.Errorf("audio language = %s, want the effective \"en\" preserved", language.Value)
	}
	quality := find(t, res, "playback.preferred_quality", nil)
	if string(quality.Value) != `"1080p"` {
		t.Errorf("quality = %s, want the effective \"1080p\" preserved", quality.Value)
	}
	bitrate := find(t, res, "playback.max_bitrate_kbps", nil)
	if string(bitrate.Value) != `6000` {
		t.Errorf("bitrate = %s, want the effective 6000 kbps preserved", bitrate.Value)
	}
	if len(res.Rows) != 3 {
		t.Fatalf("unexpected rows for legacy defaults: %+v", res.Rows)
	}
}

// The mirror: a value that differs from the column default is a real choice and
// has to survive.
func TestNonDefaultProfileValuesMigrate(t *testing.T) {
	res := planner(t).Plan(Input{Profiles: []LegacyProfile{{
		ID:                        "p1",
		Language:                  str("ja"),
		SubtitleLanguage:          str("en-US"),
		SubtitleMode:              str("always"),
		ShowForcedSubtitles:       boolp(false),
		QualityPreference:         str("720p"),
		PreferredMetadataLanguage: str("fr"),
	}}})

	if len(res.Rejects) != 0 {
		t.Fatalf("unexpected rejects: %+v", res.Rejects)
	}
	for key, want := range map[string]string{
		"playback.audio_language":        `"ja"`,
		"playback.subtitle_language":     `"en-US"`,
		"playback.subtitle_mode":         `"always"`,
		"playback.show_forced_subtitles": `false`,
		"catalog.metadata_language":      `"fr"`,
		"playback.preferred_quality":     `"720p"`,
		"playback.max_bitrate_kbps":      `2000`,
	} {
		row := find(t, res, key, nil)
		if string(row.Value) != want {
			t.Errorf("%s = %s, want %s", key, row.Value, want)
		}
		if row.Scope != settingscontract.ScopeProfile || row.ProfileID != "p1" {
			t.Errorf("%s landed at %s/%s, want profile/p1", key, row.Scope, row.ProfileID)
		}
	}
}

// TestLegacyQualityDecomposesIntoTwoAxes covers the whole ladder. None of these
// may land in the rejects table: the point of splitting quality into resolution
// and bitrate was that every legacy value converts losslessly.
func TestLegacyQualityDecomposesIntoTwoAxes(t *testing.T) {
	for legacy, want := range map[string]struct {
		resolution string
		kbps       string
	}{
		"1080p-high":   {`"1080p"`, `10000`},
		"1080p":        {`"1080p"`, `6000`},
		"1080p-medium": {`"1080p"`, `4500`},
		"1080p-8":      {`"1080p"`, `6000`},
		"720p-high":    {`"720p"`, `4000`},
		"720p-medium":  {`"720p"`, `3000`},
		"720p":         {`"720p"`, `2000`},
		"480p":         {`"480p"`, `1500`},
		"420p":         {`"480p"`, `720`},
		"328p":         {`"480p"`, `720`},
	} {
		t.Run(legacy, func(t *testing.T) {
			res := planner(t).Plan(Input{DeviceSettings: []LegacyDeviceSetting{{
				ProfileID: "p1", DeviceID: "d1",
				Key: "playback.preferred_quality", Value: legacy,
			}}})

			if len(res.Rejects) != 0 {
				t.Fatalf("%s was rejected: %+v", legacy, res.Rejects)
			}
			quality := find(t, res, "playback.preferred_quality", nil)
			if string(quality.Value) != want.resolution {
				t.Errorf("resolution = %s, want %s", quality.Value, want.resolution)
			}
			bitrate := find(t, res, "playback.max_bitrate_kbps", nil)
			if string(bitrate.Value) != want.kbps {
				t.Errorf("bitrate = %s, want %s", bitrate.Value, want.kbps)
			}
			if bitrate.Scope != settingscontract.ScopeProfileDevice || bitrate.DeviceID != "d1" {
				t.Errorf("bitrate landed at %s/%s", bitrate.Scope, bitrate.DeviceID)
			}
		})
	}

	// The three that carry no bitrate implication stay one row.
	for _, sentinel := range []string{"auto", "original", "2160p"} {
		t.Run(sentinel, func(t *testing.T) {
			res := planner(t).Plan(Input{DeviceSettings: []LegacyDeviceSetting{{
				ProfileID: "p1", DeviceID: "d1",
				Key: "playback.preferred_quality", Value: sentinel,
			}}})
			if hasKey(res, "playback.max_bitrate_kbps") {
				t.Errorf("%s invented a bitrate cap", sentinel)
			}
			row := find(t, res, "playback.preferred_quality", nil)
			if string(row.Value) != `"`+sentinel+`"` {
				t.Errorf("value = %s", row.Value)
			}
		})
	}
}

// TestAccountSettingsFanOutToEveryProfile covers the account-to-profile move.
// A household that shared one theme must each end up owning theirs, or the
// first profile to change it would silently restyle everyone.
func TestAccountSettingsFanOutToEveryProfile(t *testing.T) {
	res := planner(t).Plan(Input{
		Settings: []LegacySetting{{Key: "ui_theme", Value: "cobalt-studio"}},
		Profiles: []LegacyProfile{{ID: "p1"}, {ID: "p2"}, {ID: "p3"}},
	})

	seen := map[string]string{}
	for _, row := range res.Rows {
		if row.Key != "ui.theme" {
			continue
		}
		if row.Scope != settingscontract.ScopeProfile {
			t.Errorf("ui.theme landed at %s, want profile", row.Scope)
		}
		seen[row.ProfileID] = string(row.Value)
	}
	if len(seen) != 3 {
		t.Fatalf("ui.theme reached %d profiles, want 3: %+v", len(seen), seen)
	}
	for id, value := range seen {
		if value != `"cobalt-studio"` {
			t.Errorf("profile %s got %s", id, value)
		}
	}
}

// TestLegacyKeysAreRenamed pins the rename table. A key that keeps its legacy
// spelling would be unreachable through generated bindings.
func TestLegacyKeysAreRenamed(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "p1"}},
		Settings: []LegacySetting{
			{Key: "ui_text_scale", Value: "large"},
			{Key: "ui_high_contrast", Value: "true"},
			{Key: "next_up_mode", Value: "separate"},
			{Key: "disabled_library_ids", Value: `[3,9]`},
			{Key: "library_order", Value: `[9,3]`},
		},
	})
	if len(res.Rejects) != 0 {
		t.Fatalf("unexpected rejects: %+v", res.Rejects)
	}
	for key, want := range map[string]string{
		"ui.text_scale":           `"large"`,
		"ui.high_contrast":        `true`,
		"ui.next_up_mode":         `"separate"`,
		"ui.disabled_library_ids": `[3,9]`,
		"ui.library_order":        `[9,3]`,
	} {
		row := find(t, res, key, nil)
		if string(row.Value) != want {
			t.Errorf("%s = %s, want %s", key, row.Value, want)
		}
	}
}

// TestLegacyStringsBecomeTypedJSON: the legacy store held everything as a
// string, so "true" has to become a boolean and "30" a number, or every
// generated binding fails to decode what the migration wrote.
func TestLegacyStringsBecomeTypedJSON(t *testing.T) {
	res := planner(t).Plan(Input{DeviceSettings: []LegacyDeviceSetting{
		{ProfileID: "p1", DeviceID: "d1", Key: "playback.auto_skip_intro", Value: "true"},
		{ProfileID: "p1", DeviceID: "d1", Key: "playback.next_up_prompt_seconds", Value: "30"},
		{ProfileID: "p1", DeviceID: "d1", Key: "player.playback_speed", Value: "1.5"},
	}})
	if len(res.Rejects) != 0 {
		t.Fatalf("unexpected rejects: %+v", res.Rejects)
	}
	for key, want := range map[string]string{
		"playback.auto_skip_intro":        `true`,
		"playback.next_up_prompt_seconds": `30`,
		"player.playback_speed":           `1.5`,
	} {
		if got := string(find(t, res, key, nil).Value); got != want {
			t.Errorf("%s = %s, want %s (typed, not a quoted string)", key, got, want)
		}
	}
}

// TestOffStepNumbersSnapToTheGrid. The legacy endpoint never enforced the
// declared step, so a stored playback speed of 0.26 is a real preference;
// rejecting it at migration would destroy the choice over a rounding artifact.
func TestOffStepNumbersSnapToTheGrid(t *testing.T) {
	res := planner(t).Plan(Input{DeviceSettings: []LegacyDeviceSetting{
		{ProfileID: "p1", DeviceID: "d1", Key: "player.playback_speed", Value: "0.26"},
	}})
	if len(res.Rejects) != 0 {
		t.Fatalf("an off-step legacy speed was rejected: %+v", res.Rejects)
	}
	if got := string(find(t, res, "player.playback_speed", nil).Value); got != "0.25" {
		t.Errorf("0.26 migrated as %s, want snapped to 0.25", got)
	}
}

// TestEmptyStringIsUnsetNotAValue. The legacy string API had no way to send
// null, so both Android and web spell "clear my choice" as "". Storing that as
// an empty string would make an explicitly-cleared setting outrank the default.
func TestEmptyStringIsUnsetNotAValue(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "p1", SubtitleLanguage: str(""), SubtitleMode: str("")}},
		DeviceSettings: []LegacyDeviceSetting{
			{ProfileID: "p1", DeviceID: "d1", Key: "playback.audio_language", Value: ""},
		},
		SeriesPrefs: []LegacySeriesPreference{
			{ProfileID: "p1", SeriesID: "s1", AudioLanguage: str(""), SubtitleMode: str("")},
		},
	})
	if len(res.Rows) != 0 {
		t.Fatalf("empty legacy values produced %d rows: %+v", len(res.Rows), res.Rows)
	}
	if len(res.Rejects) != 0 {
		t.Fatalf("empty values were rejected rather than skipped: %+v", res.Rejects)
	}
}

// TestSeriesAndLibraryPreferencesLandAtTheirScopes.
func TestSeriesAndLibraryPreferencesLandAtTheirScopes(t *testing.T) {
	res := planner(t).Plan(Input{
		SeriesPrefs: []LegacySeriesPreference{{
			ProfileID: "p1", SeriesID: "s1",
			SubtitleLanguage: str("ja"), ShowForcedSubtitles: boolp(true),
		}},
		LibraryPrefs: []LegacyLibraryPreference{{
			ProfileID: "p1", LibraryID: 7,
			AudioLanguage: str("de"), SubtitleMode: str("always"),
		}},
	})
	if len(res.Rejects) != 0 {
		t.Fatalf("unexpected rejects: %+v", res.Rejects)
	}

	series := find(t, res, "playback.subtitle_language", nil)
	if series.Scope != settingscontract.ScopeProfileSeries || series.SeriesID != "s1" {
		t.Errorf("series pref landed at %s/%s", series.Scope, series.SeriesID)
	}
	library := find(t, res, "playback.audio_language", nil)
	if library.Scope != settingscontract.ScopeProfileLibrary || library.LibraryID != 7 {
		t.Errorf("library pref landed at %s/%d", library.Scope, library.LibraryID)
	}

	// show_forced_subtitles is nullable at these scopes, so true IS a real
	// override here even though it is the default on the profile column.
	forced := find(t, res, "playback.show_forced_subtitles", nil)
	if string(forced.Value) != `true` || forced.Scope != settingscontract.ScopeProfileSeries {
		t.Errorf("forced = %s at %s, want true at profile_series", forced.Value, forced.Scope)
	}
}

// TestUnconvertibleValuesAreRejectedNotDropped. The whole reason the schema
// ships a rejects table is that an operator should be able to see what did not
// survive rather than learn it from a support ticket.
func TestUnconvertibleValuesAreRejectedNotDropped(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{
			{ID: "p1", Language: str("!!!")},
			{ID: "p2", QualityPreference: str("240p")},
		},
		Settings: []LegacySetting{
			{Key: "totally.unknown", Value: "x"},
			{Key: "ui_theme", Value: "not-a-theme"},
		},
		DeviceSettings: []LegacyDeviceSetting{
			{ProfileID: "p1", DeviceID: "d1", Key: "playback.auto_skip_intro", Value: "yes-please"},
			{ProfileID: "p1", DeviceID: "d1", Key: "player.playback_speed", Value: "99"},
		},
	})

	reasons := map[string]string{}
	for _, reject := range res.Rejects {
		reasons[reject.SourceKey] = reject.Reason
	}
	for _, want := range []string{
		"playback.audio_language",    // "!!!" is not a language tag
		"playback.preferred_quality", // 240p was never a quality
		"totally.unknown",
		"ui_theme",                 // not an enum member
		"playback.auto_skip_intro", // not a boolean
		"player.playback_speed",    // out of range
	} {
		if _, ok := reasons[want]; !ok {
			t.Errorf("%s was dropped silently rather than rejected (rejects: %+v)", want, res.Rejects)
		}
	}
	for _, reject := range res.Rejects {
		if strings.TrimSpace(reject.Reason) == "" {
			t.Errorf("reject %+v carries no reason", reject)
		}
	}
}

// TestJellycompatBlobsAreLeftAlone. Those rows ride the same table under
// synthetic keys but are that subsystem's storage, not user settings.
func TestJellycompatBlobsAreLeftAlone(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "p1"}},
		Settings: []LegacySetting{
			{Key: "jellycompat:displayprefs:usersettings:emby", Value: `{"a":1}`},
		},
	})
	if len(res.Rows) != 0 || len(res.Rejects) != 0 {
		t.Fatalf("jellycompat blob was migrated or rejected: rows=%+v rejects=%+v",
			res.Rows, res.Rejects)
	}
}

// TestEveryPlannedRowValidates is the safety net: whatever the rules above
// decide, nothing may reach storage that the mutation endpoint would refuse.
func TestEveryPlannedRowValidates(t *testing.T) {
	contract, err := settingscontract.Load()
	if err != nil {
		t.Fatalf("loading contract: %v", err)
	}
	schemas := settingscontract.ObjectSchemas()

	res := New(contract, schemas).Plan(Input{
		Profiles: []LegacyProfile{{
			ID: "p1", Language: str("ja"), SubtitleMode: str("always"),
			QualityPreference: str("1080p-high"), ShowForcedSubtitles: boolp(false),
		}},
		Settings: []LegacySetting{
			{Key: "ui_theme", Value: "cobalt-studio"},
			{Key: "ui_custom_css", Value: "body { color: red; }"},
			{Key: "subtitle_appearance", Value: `{"fontSize":"large"}`},
		},
		DeviceSettings: []LegacyDeviceSetting{
			{ProfileID: "p1", DeviceID: "d1", Key: "player.audio_sync_ms", Value: "-250"},
		},
		SeriesPrefs:  []LegacySeriesPreference{{ProfileID: "p1", SeriesID: "s1", SubtitleLanguage: str("en")}},
		LibraryPrefs: []LegacyLibraryPreference{{ProfileID: "p1", LibraryID: 2, AudioLanguage: str("de")}},
	})

	if len(res.Rows) == 0 {
		t.Fatal("nothing was planned")
	}
	for _, row := range res.Rows {
		def, ok := contract.Lookup(row.Key)
		if !ok {
			t.Errorf("planned a row for unregistered key %q", row.Key)
			continue
		}
		if err := def.ValueSchema.ValidateValue(row.Value, schemas); err != nil {
			t.Errorf("%s = %s would be refused by the mutation endpoint: %v",
				row.Key, row.Value, err)
		}
		if !def.AllowsScope(row.Scope) {
			t.Errorf("%s planned at %s, which the definition does not allow", row.Key, row.Scope)
		}
		var decoded any
		if err := json.Unmarshal(row.Value, &decoded); err != nil {
			t.Errorf("%s value is not valid JSON: %v", row.Key, err)
		}
	}
}

// TestRejectIdentityIsAlwaysValidJSON. Both backends store this column as JSON —
// Postgres declares it jsonb NOT NULL, SQLite guards it with a json_valid CHECK
// — so a reject carrying prose would fail to insert, and the migration would
// abort on the very rows it exists to record.
func TestRejectIdentityIsAlwaysValidJSON(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "p1", Language: str("!!!")}},
		Settings: []LegacySetting{{Key: "totally.unknown", Value: "x"}},
		DeviceSettings: []LegacyDeviceSetting{
			{ProfileID: "p1", DeviceID: "d1", Key: "playback.auto_skip_intro", Value: "maybe"},
		},
		SeriesPrefs: []LegacySeriesPreference{
			{ProfileID: "p1", SeriesID: "s1", SubtitleLanguage: str("!!!")},
		},
		LibraryPrefs: []LegacyLibraryPreference{
			{ProfileID: "p1", LibraryID: 4, AudioLanguage: str("!!!")},
		},
	})

	if len(res.Rejects) == 0 {
		t.Fatal("nothing was rejected, so this proves nothing")
	}
	for _, reject := range res.Rejects {
		var decoded map[string]any
		if err := json.Unmarshal(reject.Identity, &decoded); err != nil {
			t.Errorf("identity %q for %s is not a JSON object: %v",
				reject.Identity, reject.SourceKey, err)
		}
	}
}

// TestRenamedAndCanonicalKeysCollapseToOneRow: the legacy device table keys on
// the stored spelling, so one device can hold both player.next_up_prompt_seconds
// and playback.next_up_prompt_seconds. Both become one canonical identity, and
// planning both would trip the unique index — on SQLite that aborts NewUserDB
// and the user's store never opens again. The canonical-keyed value must win:
// the runtime handler writes it first and only best-effort-deletes the alias,
// so it is the newer write.
func TestRenamedAndCanonicalKeysCollapseToOneRow(t *testing.T) {
	for name, ordered := range map[string][]LegacyDeviceSetting{
		"alias first": {
			{ProfileID: "p1", DeviceID: "d1", Key: "player.next_up_prompt_seconds", Value: "45"},
			{ProfileID: "p1", DeviceID: "d1", Key: "playback.next_up_prompt_seconds", Value: "20"},
		},
		"canonical first": {
			{ProfileID: "p1", DeviceID: "d1", Key: "playback.next_up_prompt_seconds", Value: "20"},
			{ProfileID: "p1", DeviceID: "d1", Key: "player.next_up_prompt_seconds", Value: "45"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := planner(t).Plan(Input{DeviceSettings: ordered})
			if len(res.Rejects) != 0 {
				t.Fatalf("unexpected rejects: %+v", res.Rejects)
			}
			row := find(t, res, "playback.next_up_prompt_seconds", nil)
			if string(row.Value) != `20` {
				t.Errorf("surviving value = %s, want the canonical-keyed 20", row.Value)
			}
		})
	}

	// A different device is a different identity and must keep its own row.
	res := planner(t).Plan(Input{DeviceSettings: []LegacyDeviceSetting{
		{ProfileID: "p1", DeviceID: "d1", Key: "playback.next_up_prompt_seconds", Value: "20"},
		{ProfileID: "p1", DeviceID: "d2", Key: "player.next_up_prompt_seconds", Value: "45"},
	}})
	if len(res.Rows) != 2 {
		t.Fatalf("distinct devices were merged: %+v", res.Rows)
	}
}

// TestAutoSkipColumnsMigrateOnlyWhenTrue: the four auto-skip switches default
// to false in both the columns and the contract, so an explicit true is the
// only decision worth carrying — a migrated false would turn "never touched"
// into a stored choice.
func TestAutoSkipColumnsMigrateOnlyWhenTrue(t *testing.T) {
	res := planner(t).Plan(Input{Profiles: []LegacyProfile{{
		ID:                  "p1",
		AutoSkipIntro:       boolp(true),
		AutoSkipCredits:     boolp(false),
		AutoSkipRecap:       boolp(true),
		AutoPlayNextPreview: boolp(false),
	}}})
	if len(res.Rejects) != 0 {
		t.Fatalf("unexpected rejects: %+v", res.Rejects)
	}

	for _, key := range []string{"playback.auto_skip_intro", "playback.auto_skip_recap"} {
		row := find(t, res, key, nil)
		if string(row.Value) != `true` || row.Scope != settingscontract.ScopeProfile {
			t.Errorf("%s = %s at %s, want true at profile", key, row.Value, row.Scope)
		}
	}
	for _, key := range []string{"playback.auto_skip_credits", "playback.auto_play_next_preview"} {
		if hasKey(res, key) {
			t.Errorf("%s became a row from a false column", key)
		}
	}
}

// TestCardOverlaysV1DocumentsUpgrade: old web clients stored a flat
// Record<overlayId, config> the contract schema does not accept. The planner
// has to upgrade it the way web/src/lib/overlays/schema.ts does at read time,
// or every v1 row lands in the rejects table and the user's badge config is
// silently lost.
func TestCardOverlaysV1DocumentsUpgrade(t *testing.T) {
	v1 := `{
		"resolution": {"enabled": true, "position": "top-right"},
		"rating_imdb": {"enabled": true, "position": "bottom-left", "accentColor": "#f5c518"},
		"hdr": {"enabled": false, "position": "top-right", "showIcon": true},
		"no_such_overlay": {"enabled": true, "position": "top-left"},
		"year": {"enabled": true, "position": "sideways"}
	}`
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "p1"}},
		Settings: []LegacySetting{{Key: "card_overlays", Value: v1}},
	})
	if len(res.Rejects) != 0 {
		t.Fatalf("v1 document was rejected: %+v", res.Rejects)
	}

	row := find(t, res, "ui.card_overlays", nil)
	var doc struct {
		Version int                       `json:"version"`
		Preset  string                    `json:"preset"`
		Order   []string                  `json:"order"`
		Items   map[string]map[string]any `json:"items"`
	}
	if err := json.Unmarshal(row.Value, &doc); err != nil {
		t.Fatalf("upgraded document is not JSON: %v", err)
	}
	if doc.Version != 2 || doc.Preset != "classic" || len(doc.Order) != 0 {
		t.Errorf("envelope = v%d preset %q order %v, want v2 classic []",
			doc.Version, doc.Preset, doc.Order)
	}
	if item := doc.Items["rating_imdb"]; item["accentColor"] != "#f5c518" {
		t.Errorf("accentColor was dropped: %+v", item)
	}
	if item := doc.Items["hdr"]; item["enabled"] != false || item["showIcon"] != true {
		t.Errorf("hdr config mangled: %+v", item)
	}
	if _, ok := doc.Items["no_such_overlay"]; ok {
		t.Error("an id outside the schema enum survived and would fail validation")
	}
	if _, ok := doc.Items["year"]; ok {
		t.Error("an item with an invalid position survived and would fail validation")
	}

	// A v2 document must pass through untouched rather than be re-wrapped.
	v2 := `{"version":2,"preset":"pill","order":["hdr"],"items":{"hdr":{"enabled":true,"position":"top-left"}}}`
	res = planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "p1"}},
		Settings: []LegacySetting{{Key: "card_overlays", Value: v2}},
	})
	if len(res.Rejects) != 0 {
		t.Fatalf("v2 document was rejected: %+v", res.Rejects)
	}
	if got := string(find(t, res, "ui.card_overlays", nil).Value); got != v2 {
		t.Errorf("v2 document was rewritten: %s", got)
	}

	// Garbage is still garbage: the upgrade must not launder an unusable value
	// into something that validates.
	res = planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "p1"}},
		Settings: []LegacySetting{{Key: "card_overlays", Value: `["not","an","object"]`}},
	})
	if len(res.Rows) != 0 || len(res.Rejects) != 1 {
		t.Fatalf("garbage produced rows=%+v rejects=%+v, want one reject", res.Rows, res.Rejects)
	}
}

// TestStrandedDeviceAudioLanguageIsQuarantined. The Apple clients wrote
// playback.audio_language device rows that no pre-contract playback path ever
// read — and migration 098 fanned the account value onto every known device
// besides. Promoting one to a real profile_device override would make a value
// that never influenced playback outrank the profile language after upgrade,
// so it is recorded for an operator instead.
func TestStrandedDeviceAudioLanguageIsQuarantined(t *testing.T) {
	res := planner(t).Plan(Input{DeviceSettings: []LegacyDeviceSetting{
		{ProfileID: "p1", DeviceID: "d1", Key: "playback.audio_language", Value: "ja"},
		{ProfileID: "p1", DeviceID: "d1", Key: "playback.subtitle_language", Value: "de"},
	}})

	for _, row := range res.Rows {
		if row.Key == "playback.audio_language" {
			t.Errorf("a stranded device audio language became a real override: %+v", row)
		}
	}
	// The other device preferences on the same device still migrate.
	if got := string(find(t, res, "playback.subtitle_language", nil).Value); got != `"de"` {
		t.Errorf("subtitle language = %s, want \"de\" — only audio language is quarantined", got)
	}

	var quarantined bool
	for _, reject := range res.Rejects {
		if reject.SourceKey == "playback.audio_language" && reject.Value == "ja" {
			quarantined = true
		}
	}
	if !quarantined {
		t.Errorf("the stranded row was dropped rather than recorded: %+v", res.Rejects)
	}
}

// TestOrphanedProfileRowsAreDropped reproduces a real dev-server migration
// failure: user_device_settings held rows for profiles that had been deleted —
// 46 of them across 14 profiles, predating the cascade the legacy table now
// carries. The canonical table declares the same foreign key, so copying one
// aborted the migration transaction and the server could not start.
func TestOrphanedProfileRowsAreDropped(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "live"}},
		DeviceSettings: []LegacyDeviceSetting{
			{ProfileID: "live", DeviceID: "d1", Key: "subtitle_appearance",
				Value: `{"fontSize":"large"}`},
			{ProfileID: "deleted", DeviceID: "d1", Key: "subtitle_appearance",
				Value: `{"fontSize":"large"}`},
		},
	})

	for _, row := range res.Rows {
		if row.ProfileID == "deleted" {
			t.Errorf("a row for a deleted profile survived and would trip the FK: %+v", row)
		}
	}
	if !hasKey(res, "playback.subtitle_appearance") {
		t.Error("the live profile's row was dropped along with the orphan")
	}

	// Dropped, not silently discarded: an operator has to be able to see it.
	var recorded bool
	for _, reject := range res.Rejects {
		if strings.Contains(string(reject.Identity), "deleted") {
			recorded = true
		}
	}
	if !recorded {
		t.Errorf("the orphan was dropped without a reject: %+v", res.Rejects)
	}
}

func TestLoadedEmptyProfileListDropsEveryProfileScopedRow(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{},
		DeviceSettings: []LegacyDeviceSetting{{
			ProfileID: "deleted", DeviceID: "d1", Key: "subtitle_appearance",
			Value: `{"fontSize":"large"}`,
		}},
	})

	if len(res.Rows) != 0 {
		t.Fatalf("profile-scoped rows survived an empty loaded profile list: %+v", res.Rows)
	}
	if len(res.Rejects) != 1 || !strings.Contains(string(res.Rejects[0].Identity), "deleted") {
		t.Fatalf("orphan was not recorded as a reject: %+v", res.Rejects)
	}
}

func TestNilProfileListLeavesOwnershipUnchecked(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: nil,
		DeviceSettings: []LegacyDeviceSetting{{
			ProfileID: "unknown", DeviceID: "d1", Key: "subtitle_appearance",
			Value: `{"fontSize":"large"}`,
		}},
	})
	if !hasKey(res, "playback.subtitle_appearance") {
		t.Fatalf("nil profile list unexpectedly filtered rows: rows=%+v rejects=%+v", res.Rows, res.Rejects)
	}
}

func TestPlanRuntimeValueUsesMigrationAliasesAndQualityDecomposition(t *testing.T) {
	p := planner(t)
	appearance, err := p.PlanRuntimeValue("subtitle_appearance", `{"fontSize":"large"}`)
	if err != nil {
		t.Fatalf("PlanRuntimeValue appearance: %v", err)
	}
	if len(appearance) != 1 || appearance[0].Key != "playback.subtitle_appearance" {
		t.Fatalf("appearance plan = %+v", appearance)
	}

	quality, err := p.PlanRuntimeValue("playback.preferred_quality", "1080p-high")
	if err != nil {
		t.Fatalf("PlanRuntimeValue quality: %v", err)
	}
	if len(quality) != 2 || string(quality[0].Value) != `"1080p"` || string(quality[1].Value) != "10000" {
		t.Fatalf("quality plan = %+v", quality)
	}
	auto, err := p.PlanRuntimeValue("playback.preferred_quality", "auto")
	if err != nil {
		t.Fatalf("PlanRuntimeValue auto: %v", err)
	}
	if len(auto) != 2 || string(auto[0].Value) != `"auto"` || auto[1].Value != nil {
		t.Fatalf("auto plan = %+v", auto)
	}
}

func TestOrphanRejectsPreserveSourceTableAndContentIdentity(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "live"}},
		SeriesPrefs: []LegacySeriesPreference{{
			ProfileID: "deleted", SeriesID: "series-9",
			AudioSourceTable: "user_audio_preferences", AudioLanguage: str("ja"),
			SubtitleSourceTable: "user_subtitle_preferences", SubtitleMode: str("always"),
		}},
		LibraryPrefs: []LegacyLibraryPreference{{
			ProfileID: "deleted", LibraryID: 42,
			SourceTable: "user_library_playback_preferences", SubtitleLanguage: str("de"),
		}},
	})

	if len(res.Rows) != 0 {
		t.Fatalf("orphan rows survived: %+v", res.Rows)
	}
	want := map[string]struct {
		table    string
		identity string
		value    any
	}{
		"playback.audio_language":    {"user_audio_preferences", "series_id", "series-9"},
		"playback.subtitle_mode":     {"user_subtitle_preferences", "series_id", "series-9"},
		"playback.subtitle_language": {"user_library_playback_preferences", "library_id", float64(42)},
	}
	for _, reject := range res.Rejects {
		expected, ok := want[reject.SourceKey]
		if !ok {
			continue
		}
		if reject.SourceTable != expected.table {
			t.Errorf("%s source table = %q, want %q", reject.SourceKey, reject.SourceTable, expected.table)
		}
		var identity map[string]any
		if err := json.Unmarshal(reject.Identity, &identity); err != nil {
			t.Fatalf("decoding %s identity: %v", reject.SourceKey, err)
		}
		if got := identity[expected.identity]; got != expected.value {
			t.Errorf("%s identity %s = %#v, want %#v", reject.SourceKey, expected.identity, got, expected.value)
		}
		delete(want, reject.SourceKey)
	}
	if len(want) != 0 {
		t.Fatalf("missing provenance rejects: %+v (all rejects: %+v)", want, res.Rejects)
	}
}

// Account-scope rows carry no profile, so they must survive the orphan filter.
func TestAccountScopeRowsSurviveTheOrphanFilter(t *testing.T) {
	res := planner(t).Plan(Input{
		Profiles: []LegacyProfile{{ID: "p1"}},
		Settings: []LegacySetting{{Key: "ui_theme", Value: "cobalt-studio"}},
	})
	if !hasKey(res, "ui.theme") {
		t.Errorf("the account fan-out was dropped: rows=%+v rejects=%+v", res.Rows, res.Rejects)
	}
}
