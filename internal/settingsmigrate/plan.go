// Package settingsmigrate turns legacy settings storage into canonical setting
// values.
//
// The rules are subtle enough — and the cost of getting them wrong high enough,
// since this runs once against every user's data — that they live here as
// ordinary Go rather than twice in two SQL dialects. Both backends read their
// own rows, hand them to Plan, and write what comes back. The interesting
// decisions are then testable without a database, and SQLite and Postgres
// cannot drift apart in what they decide.
//
// Nothing here touches storage. Plan is a pure function from legacy rows to
// canonical rows plus rejects.
package settingsmigrate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
)

// LegacySetting is one row of the account-wide user_settings key/value table.
type LegacySetting struct {
	Key   string
	Value string
}

// LegacyDeviceSetting is one row of user_device_settings.
type LegacyDeviceSetting struct {
	ProfileID string
	DeviceID  string
	Key       string
	Value     string
}

// LegacyProfile carries the preference columns on a profile row.
//
// Every field is a pointer or a "set" flag because the two backends disagree
// about nullability: Postgres declares these NOT NULL with defaults while
// SQLite leaves them nullable, so "the user chose the default" and "nobody ever
// wrote here" are spelled differently per backend. The caller resolves that
// when it reads; Plan sees one shape.
type LegacyProfile struct {
	ID string

	Language                  *string
	SubtitleLanguage          *string
	SubtitleMode              *string
	ShowForcedSubtitles       *bool
	QualityPreference         *string
	PreferredMetadataLanguage *string
}

// LegacySeriesPreference is one user_subtitle_preferences or
// user_audio_preferences row. Track indexes and signatures are deliberately
// absent: they identify a concrete track rather than expressing a preference,
// and they stay in their specialized tables.
type LegacySeriesPreference struct {
	ProfileID string
	SeriesID  string

	AudioLanguage       *string
	SubtitleLanguage    *string
	SubtitleMode        *string
	ShowForcedSubtitles *bool
}

// LegacyLibraryPreference is one user_library_playback_preferences row.
type LegacyLibraryPreference struct {
	ProfileID string
	LibraryID int

	AudioLanguage       *string
	SubtitleLanguage    *string
	SubtitleMode        *string
	ShowForcedSubtitles *bool
}

// Input is everything one user's migration reads.
type Input struct {
	Settings       []LegacySetting
	DeviceSettings []LegacyDeviceSetting
	Profiles       []LegacyProfile
	SeriesPrefs    []LegacySeriesPreference
	LibraryPrefs   []LegacyLibraryPreference
}

// Row is one canonical value to write.
type Row struct {
	Key       string
	Scope     settingscontract.Scope
	ProfileID string
	DeviceID  string
	LibraryID int
	SeriesID  string
	Value     json.RawMessage
}

// Reject records a legacy value that could not be converted. It is written to
// user_setting_migration_rejects so an operator can see exactly what did not
// survive, rather than discovering it from a support ticket.
//
// Identity is JSON because both backends store it as one: Postgres declares the
// column jsonb NOT NULL and SQLite guards it with a json_valid CHECK. A
// free-form description would violate the schema on write, and a structured one
// is queryable — "every reject for profile p1" is a jsonb predicate rather than
// a LIKE over prose.
type Reject struct {
	SourceTable string
	SourceKey   string
	Identity    json.RawMessage
	Value       string
	Reason      string
}

// identityJSON builds the reject identity document. Only the fields that locate
// the row are emitted, so a profile-column reject does not carry an empty
// device id.
func identityJSON(fields map[string]any) json.RawMessage {
	encoded, err := json.Marshal(fields)
	if err != nil {
		// Only strings and ints reach here, so this cannot fire; an empty
		// object still satisfies both backends' JSON constraint.
		return json.RawMessage(`{}`)
	}
	return encoded
}

// Result is what one user's migration produced.
type Result struct {
	Rows    []Row
	Rejects []Reject
}

// profileColumnDefaults are the values the legacy schema wrote when nobody
// chose anything. A column still holding its default is not a preference, and
// migrating it would turn "never decided" into an explicit choice that outranks
// the contract default forever.
//
// quality_preference is the one that bites: the column defaults to 1080p while
// the contract defaults to auto, so migrating it unconditionally would pin
// every profile in the install to 1080p having never asked for it.
const (
	defaultLanguage          = "en"
	defaultSubtitleMode      = "auto"
	defaultQualityPreference = "1080p"
)

// Source table names, recorded on every reject so an operator can find the row
// the migration could not convert.
const (
	sourceUserSettings       = "user_settings"
	sourceUserDeviceSettings = "user_device_settings"
	sourceUserProfiles       = "user_profiles"
	sourceSeriesPrefs        = "series_preferences"
	sourceLibraryPrefs       = "library_playback_preferences"
)

// keyPreferredQuality is the one key with bespoke handling: it decomposes into
// two canonical keys rather than converting to one.
const keyPreferredQuality = "playback.preferred_quality"

// fieldProfileID is the identity field every non-account reject carries.
const fieldProfileID = "profile_id"

// accountIdentity locates a reject from the account-wide key/value table, which
// has no profile, device or content to name.
func accountIdentity() json.RawMessage {
	return identityJSON(map[string]any{"scope": "account"})
}

// legacyQualityDecomposition maps each legacy compound quality value to the two
// axes that replaced it.
//
// The suffixes were never a third dimension — web/src/player/hooks/
// useTranscodeQuality.ts already treats 1080p-high as resolution 1080p at
// 10000 kbps and sends the two separately, so this table is that file's ladder
// read back out. Bitrates come from there, not invented here.
//
// A lookup table reads as a table, so the resolutions stay inline: naming
// "1080p" would hide the mapping this exists to make obvious.
//
//nolint:goconst // declarative table; see above
var legacyQualityDecomposition = map[string]struct {
	Resolution string
	KBPS       int
}{
	"1080p-high":   {"1080p", 10000},
	"1080p":        {"1080p", 6000},
	"1080p-medium": {"1080p", 4500},
	"1080p-8":      {"1080p", 6000},
	"720p-high":    {"720p", 4000},
	"720p-medium":  {"720p", 3000},
	"720p":         {"720p", 2000},
	"480p":         {"480p", 1500},
	"420p":         {"480p", 720},
	"328p":         {"480p", 720},
}

// legacyKeyRenames maps a legacy key to its canonical name. A key absent here
// keeps its name.
//
// As above: the left column is the legacy spelling and the right is canonical,
// so both sides belong inline.
//
//nolint:goconst // declarative table; see above
var legacyKeyRenames = map[string]string{
	"subtitle_appearance":           "playback.subtitle_appearance",
	"player.next_up_prompt_seconds": "playback.next_up_prompt_seconds",
	"ui_theme":                      "ui.theme",
	"ui_text_scale":                 "ui.text_scale",
	"ui_text_weight":                "ui.text_weight",
	"ui_high_contrast":              "ui.high_contrast",
	"ui_custom_theme_vars":          "ui.custom_theme_vars",
	"ui_custom_css":                 "ui.custom_css",
	"card_overlays":                 "ui.card_overlays",
	"next_up_mode":                  "ui.next_up_mode",
	"sidebar_pins":                  "ui.sidebar_pins",
	"disabled_library_ids":          "ui.disabled_library_ids",
	"library_order":                 "ui.library_order",
}

// Planner converts legacy rows using one contract.
type Planner struct {
	contract *settingscontract.Manifest
	schemas  map[string]*jsonschema.Schema
}

// New returns a Planner over the given contract. objectSchemas is the compiled
// schema set from settingscontract.ObjectSchemas, used to validate every value
// before it is planned — nothing reaches storage that the mutation endpoint
// would refuse.
func New(contract *settingscontract.Manifest, objectSchemas map[string]*jsonschema.Schema) *Planner {
	return &Planner{contract: contract, schemas: objectSchemas}
}

// Plan converts one user's legacy rows.
//
// Profiles are needed even when nothing else is: an account-scoped legacy row
// fans out to every profile, because the contract moved appearance and search
// scope from the account to the profile and there is no account-scope reader
// left for them.
func (p *Planner) Plan(in Input) Result {
	var res Result

	p.planProfiles(in.Profiles, &res)
	p.planAccountSettings(in.Settings, in.Profiles, &res)
	p.planDeviceSettings(in.DeviceSettings, &res)
	p.planSeriesPrefs(in.SeriesPrefs, &res)
	p.planLibraryPrefs(in.LibraryPrefs, &res)

	return res
}

// planProfiles converts the six preference columns on user_profiles.
func (p *Planner) planProfiles(profiles []LegacyProfile, res *Result) {
	for _, profile := range profiles {
		id := func(field string) json.RawMessage {
			return identityJSON(map[string]any{fieldProfileID: profile.ID, "column": field})
		}

		// Language columns: the empty string is the legacy spelling of "no
		// preference", and the contract spells that as the absence of a row.
		p.addLanguage(res, sourceUserProfiles, id("language"),
			"playback.audio_language", settingscontract.ScopeProfile,
			Row{ProfileID: profile.ID}, profile.Language, defaultLanguage)
		p.addLanguage(res, sourceUserProfiles, id("subtitle_language"),
			"playback.subtitle_language", settingscontract.ScopeProfile,
			Row{ProfileID: profile.ID}, profile.SubtitleLanguage, "")
		p.addLanguage(res, sourceUserProfiles, id("preferred_metadata_language"),
			"catalog.metadata_language", settingscontract.ScopeProfile,
			Row{ProfileID: profile.ID}, profile.PreferredMetadataLanguage, "")

		if mode := deref(profile.SubtitleMode); mode != "" && mode != defaultSubtitleMode {
			p.addValue(res, sourceUserProfiles, id("subtitle_mode"),
				"playback.subtitle_mode", settingscontract.ScopeProfile,
				Row{ProfileID: profile.ID}, jsonString(mode))
		}

		// show_forced_subtitles is NOT NULL DEFAULT true, so only false is a
		// decision worth carrying: true is indistinguishable from untouched.
		if profile.ShowForcedSubtitles != nil && !*profile.ShowForcedSubtitles {
			p.addValue(res, sourceUserProfiles, id("show_forced_subtitles"),
				"playback.show_forced_subtitles", settingscontract.ScopeProfile,
				Row{ProfileID: profile.ID}, json.RawMessage("false"))
		}

		p.addQuality(res, sourceUserProfiles, id("quality_preference"),
			settingscontract.ScopeProfile, Row{ProfileID: profile.ID},
			deref(profile.QualityPreference), defaultQualityPreference)
	}
}

// planAccountSettings converts the account-wide key/value table.
//
// Every surviving definition for these keys is profile-scoped, so each value
// fans out to every profile on the account. That is the account-to-profile move
// the contract makes for appearance and search scope: one row becomes n, and a
// household that shared a theme now each own theirs.
func (p *Planner) planAccountSettings(
	settings []LegacySetting, profiles []LegacyProfile, res *Result,
) {
	for _, setting := range settings {
		key := canonicalKey(setting.Key)

		// jellycompat stashes its DisplayPreferences blobs in this table under
		// synthetic keys. They are that subsystem's storage, not user settings,
		// and they move to dedicated jellycompat storage rather than here.
		if strings.HasPrefix(setting.Key, "jellycompat:") {
			continue
		}

		def, ok := p.contract.Lookup(key)
		if !ok {
			res.Rejects = append(res.Rejects, Reject{
				SourceTable: sourceUserSettings, SourceKey: setting.Key,
				Identity: accountIdentity(),
				Value:    setting.Value,
				Reason:   "no contract definition; the key was only ever accepted by the unknown-key extension bag",
			})
			continue
		}

		value, err := p.coerce(def, setting.Value)
		if err != nil {
			res.Rejects = append(res.Rejects, Reject{
				SourceTable: sourceUserSettings, SourceKey: setting.Key,
				Identity: accountIdentity(),
				Value:    setting.Value, Reason: err.Error(),
			})
			continue
		}
		if value == nil {
			continue // legacy spelling of unset
		}

		scope := settingscontract.ScopeProfile
		if !def.AllowsScope(scope) {
			res.Rejects = append(res.Rejects, Reject{
				SourceTable: sourceUserSettings, SourceKey: setting.Key,
				Identity: accountIdentity(),
				Value:    setting.Value,
				Reason:   fmt.Sprintf("%s does not allow profile scope", key),
			})
			continue
		}
		for _, profile := range profiles {
			res.Rows = append(res.Rows, Row{
				Key: key, Scope: scope, ProfileID: profile.ID, Value: value,
			})
		}
	}
}

func (p *Planner) planDeviceSettings(devices []LegacyDeviceSetting, res *Result) {
	for _, row := range devices {
		key := canonicalKey(row.Key)
		identity := identityJSON(map[string]any{
			fieldProfileID: row.ProfileID, "device_id": row.DeviceID,
		})

		if key == keyPreferredQuality {
			p.addQuality(res, sourceUserDeviceSettings, identity,
				settingscontract.ScopeProfileDevice,
				Row{ProfileID: row.ProfileID, DeviceID: row.DeviceID}, row.Value, "")
			continue
		}

		def, ok := p.contract.Lookup(key)
		if !ok {
			res.Rejects = append(res.Rejects, Reject{
				SourceTable: sourceUserDeviceSettings, SourceKey: row.Key,
				Identity: identity, Value: row.Value,
				Reason: "no contract definition",
			})
			continue
		}
		value, err := p.coerce(def, row.Value)
		if err != nil {
			res.Rejects = append(res.Rejects, Reject{
				SourceTable: sourceUserDeviceSettings, SourceKey: row.Key,
				Identity: identity, Value: row.Value, Reason: err.Error(),
			})
			continue
		}
		if value == nil {
			continue
		}
		if !def.AllowsScope(settingscontract.ScopeProfileDevice) {
			res.Rejects = append(res.Rejects, Reject{
				SourceTable: sourceUserDeviceSettings, SourceKey: row.Key,
				Identity: identity, Value: row.Value,
				Reason: fmt.Sprintf("%s does not allow profile_device scope", key),
			})
			continue
		}
		res.Rows = append(res.Rows, Row{
			Key: key, Scope: settingscontract.ScopeProfileDevice,
			ProfileID: row.ProfileID, DeviceID: row.DeviceID, Value: value,
		})
	}
}

func (p *Planner) planSeriesPrefs(prefs []LegacySeriesPreference, res *Result) {
	for _, pref := range prefs {
		base := Row{ProfileID: pref.ProfileID, SeriesID: pref.SeriesID}
		identity := identityJSON(map[string]any{
			fieldProfileID: pref.ProfileID, "series_id": pref.SeriesID,
		})
		p.addPlaybackTriple(res, sourceSeriesPrefs, identity,
			settingscontract.ScopeProfileSeries, base,
			pref.AudioLanguage, pref.SubtitleLanguage, pref.SubtitleMode, pref.ShowForcedSubtitles)
	}
}

func (p *Planner) planLibraryPrefs(prefs []LegacyLibraryPreference, res *Result) {
	for _, pref := range prefs {
		base := Row{ProfileID: pref.ProfileID, LibraryID: pref.LibraryID}
		identity := identityJSON(map[string]any{
			fieldProfileID: pref.ProfileID, "library_id": pref.LibraryID,
		})
		p.addPlaybackTriple(res, sourceLibraryPrefs, identity,
			settingscontract.ScopeProfileLibrary, base,
			pref.AudioLanguage, pref.SubtitleLanguage, pref.SubtitleMode, pref.ShowForcedSubtitles)
	}
}

// addPlaybackTriple converts the four playback preferences that series and
// library rows share. Both tables carry the same columns with the same
// semantics, so they convert identically at different scopes.
func (p *Planner) addPlaybackTriple(
	res *Result, sourceTable string, identity json.RawMessage,
	scope settingscontract.Scope, base Row,
	audio, subtitle, mode *string, forced *bool,
) {
	p.addLanguage(res, sourceTable, identity,
		"playback.audio_language", scope, base, audio, "")
	p.addLanguage(res, sourceTable, identity,
		"playback.subtitle_language", scope, base, subtitle, "")

	if m := deref(mode); m != "" {
		p.addValue(res, sourceTable, identity,
			"playback.subtitle_mode", scope, base, jsonString(m))
	}
	// Nullable at these scopes, so a non-null value is a real override in
	// either direction — unlike the profile column, where true is the default.
	if forced != nil {
		p.addValue(res, sourceTable, identity,
			"playback.show_forced_subtitles", scope, base,
			json.RawMessage(strconv.FormatBool(*forced)))
	}
}

// addLanguage converts a legacy language column. The empty string is the legacy
// spelling of "no preference" and produces no row; so does a value still equal
// to the column default.
func (p *Planner) addLanguage(
	res *Result, sourceTable string, identity json.RawMessage, key string,
	scope settingscontract.Scope, base Row, raw *string, columnDefault string,
) {
	value := strings.TrimSpace(deref(raw))
	if value == "" || (columnDefault != "" && value == columnDefault) {
		return
	}
	normalized, ok := settingscontract.NormalizeLanguageTag(value)
	if !ok {
		res.Rejects = append(res.Rejects, Reject{
			SourceTable: sourceTable, SourceKey: key, Identity: identity,
			Value:  value,
			Reason: "not a well-formed BCP 47 language tag",
		})
		return
	}
	p.addValue(res, sourceTable, identity, key, scope, base, jsonString(normalized))
}

// addQuality decomposes a legacy quality value into the two axes that replaced
// it, writing up to two rows.
func (p *Planner) addQuality(
	res *Result, sourceTable string, identity json.RawMessage,
	scope settingscontract.Scope, base Row, raw, columnDefault string,
) {
	value := strings.TrimSpace(raw)
	if value == "" || value == columnDefault {
		return
	}

	// auto and original are resolution answers with no bitrate implication.
	if value == "auto" || value == "original" || value == "2160p" {
		p.addValue(res, sourceTable, identity, "playback.preferred_quality",
			scope, base, jsonString(value))
		return
	}

	decomposed, ok := legacyQualityDecomposition[value]
	if !ok {
		res.Rejects = append(res.Rejects, Reject{
			SourceTable: sourceTable, SourceKey: keyPreferredQuality,
			Identity: identity, Value: value,
			Reason: "not a quality the server ever resolved; NormalizeQualityV3 folded it to auto",
		})
		return
	}
	p.addValue(res, sourceTable, identity, keyPreferredQuality,
		scope, base, jsonString(decomposed.Resolution))
	p.addValue(res, sourceTable, identity, "playback.max_bitrate_kbps",
		scope, base, json.RawMessage(strconv.Itoa(decomposed.KBPS)))
}

// addValue appends a row after checking the value against its own definition,
// so nothing reaches storage that the mutation endpoint would refuse.
func (p *Planner) addValue(
	res *Result, sourceTable string, identity json.RawMessage, key string,
	scope settingscontract.Scope, base Row, value json.RawMessage,
) {
	def, ok := p.contract.Lookup(key)
	if !ok {
		res.Rejects = append(res.Rejects, Reject{
			SourceTable: sourceTable, SourceKey: key, Identity: identity,
			Value: string(value), Reason: "no contract definition",
		})
		return
	}
	if err := p.validate(def, value); err != nil {
		res.Rejects = append(res.Rejects, Reject{
			SourceTable: sourceTable, SourceKey: key, Identity: identity,
			Value: string(value), Reason: err.Error(),
		})
		return
	}
	row := base
	row.Key = key
	row.Scope = scope
	row.Value = value
	res.Rows = append(res.Rows, row)
}

// coerce turns a legacy string value into the JSON its definition declares.
//
// The legacy store held everything as a string, including booleans, numbers and
// whole JSON documents, so this is where "true" becomes true and "30" becomes
// 30. A nil return with no error means the value was the legacy spelling of
// unset and should produce no row at all.
func (p *Planner) coerce(def *settingscontract.Definition, raw string) (json.RawMessage, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, nil
	}

	var candidate json.RawMessage
	switch def.ValueSchema.Type {
	case settingscontract.TypeBoolean:
		parsed, err := strconv.ParseBool(trimmed)
		if err != nil {
			return nil, fmt.Errorf("%q is not a boolean", raw)
		}
		candidate = json.RawMessage(strconv.FormatBool(parsed))

	case settingscontract.TypeInteger:
		parsed, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", raw)
		}
		candidate = json.RawMessage(strconv.FormatInt(parsed, 10))

	case settingscontract.TypeNumber:
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", raw)
		}
		candidate = json.RawMessage(strconv.FormatFloat(parsed, 'g', -1, 64))

	case settingscontract.TypeObject:
		// Already a JSON document in the legacy store.
		candidate = json.RawMessage(trimmed)

	case settingscontract.TypeLanguageTag:
		normalized, ok := settingscontract.NormalizeLanguageTag(trimmed)
		if !ok {
			return nil, fmt.Errorf("%q is not a well-formed BCP 47 language tag", raw)
		}
		candidate = jsonString(normalized)

	default:
		// Strings and enums were stored as themselves.
		candidate = jsonString(trimmed)
	}

	if err := p.validate(def, candidate); err != nil {
		return nil, err
	}
	return candidate, nil
}

func (p *Planner) validate(def *settingscontract.Definition, value json.RawMessage) error {
	return def.ValueSchema.ValidateValue(value, p.schemas)
}

func canonicalKey(legacy string) string {
	if canonical, ok := legacyKeyRenames[legacy]; ok {
		return canonical
	}
	return legacy
}

func jsonString(value string) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		// A Go string always marshals; this cannot fire.
		return json.RawMessage(`""`)
	}
	return encoded
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
