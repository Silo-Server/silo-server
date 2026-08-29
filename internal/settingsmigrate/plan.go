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
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/Silo-Server/silo-server/internal/jellycompat/displayprefs"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
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
	AutoSkipIntro             *bool
	AutoSkipCredits           *bool
	AutoSkipRecap             *bool
	AutoPlayNextPreview       *bool
}

// LegacySeriesPreference is one user_subtitle_preferences or
// user_audio_preferences row. Track indexes and signatures are deliberately
// absent: they identify a concrete track rather than expressing a preference,
// and they stay in their specialized tables.
type LegacySeriesPreference struct {
	ProfileID string
	SeriesID  string
	// AudioSourceTable and SubtitleSourceTable preserve which physical legacy
	// table supplied each half of this merged record. PostgreSQL and per-user
	// SQLite use different table names, and rejected rows must point operators
	// back to the table that actually owns the value.
	AudioSourceTable    string
	SubtitleSourceTable string

	AudioLanguage       *string
	SubtitleLanguage    *string
	SubtitleMode        *string
	ShowForcedSubtitles *bool
}

// LegacyLibraryPreference is one user_library_playback_preferences row.
type LegacyLibraryPreference struct {
	ProfileID string
	LibraryID int
	// SourceTable is backend-specific: user_library_playback_preferences in
	// PostgreSQL and library_playback_preferences in per-user SQLite.
	SourceTable string

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

	// Source provenance is carried until the orphan-profile pass. A planned
	// row can no longer be reconstructed from its canonical scope alone: a
	// profile_series row may have come from either the audio or subtitle table.
	sourceTable    string
	sourceKey      string
	sourceIdentity json.RawMessage

	// fromRenamedKey records that this row's source was stored under a legacy
	// spelling in legacyKeyRenames. It exists only for the dedup pass: when a
	// renamed row and a canonically-keyed row land on the same identity, this is
	// how the pass knows which one the user wrote last.
	fromRenamedKey bool
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

// RuntimeValue is one canonical mutation implied by a write through a shipped
// legacy generic-settings endpoint. Nil Value means the canonical row must be
// cleared. The generic handlers use this live counterpart of the backfill so
// aliases, typed coercion, object upgrades, and compound quality values cannot
// drift between upgrade-time and runtime conversion.
type RuntimeValue struct {
	Key   string
	Value json.RawMessage
}

// PlanRuntimeValue converts one legacy string setting into its canonical
// mutations. Quality always returns both axes: values without a bitrate clear
// an older max_bitrate_kbps row rather than leaving half of the previous
// compound preference behind.
func (p *Planner) PlanRuntimeValue(legacyKey, raw string) ([]RuntimeValue, error) {
	key := CanonicalKey(legacyKey)
	if key == keyPreferredQuality {
		var result Result
		p.addQuality(&result, sourceUserDeviceSettings, identityJSON(map[string]any{}),
			settingscontract.ScopeProfileDevice, Row{}, raw, "")
		if len(result.Rejects) > 0 {
			return nil, fmt.Errorf("%s: %s", key, result.Rejects[0].Reason)
		}
		planned := map[string]json.RawMessage{}
		for _, row := range result.Rows {
			planned[row.Key] = row.Value
		}
		return withRuntimeMirrors([]RuntimeValue{
			{Key: keyPreferredQuality, Value: planned[keyPreferredQuality]},
			{Key: settingskeys.PlaybackMaxBitrateKbps, Value: planned[settingskeys.PlaybackMaxBitrateKbps]},
		})
	}

	def, ok := p.contract.Lookup(key)
	if !ok || !def.IsRemote() {
		return nil, fmt.Errorf("%s has no remote contract definition", key)
	}
	value, err := p.coerce(def, raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return withRuntimeMirrors([]RuntimeValue{{Key: key, Value: value}})
}

// withRuntimeMirrors appends the companion mutation for every planned value
// whose key has a replacement.
//
// It sits here rather than in the handler because three legacy write paths
// share PlanRuntimeValue — the account-wide fan-out, the per-device setting,
// and the inheritance a newly created profile picks up — and a mirror applied
// at only some of them would make a preference's meaning depend on which
// legacy route last touched it.
//
// A nil Value means "clear this row", and the companion is cleared with it for
// the same reason DELETE clears both: a surviving half would go on resolving as
// an explicit choice nobody made.
func withRuntimeMirrors(planned []RuntimeValue) ([]RuntimeValue, error) {
	out := make([]RuntimeValue, 0, len(planned)+1)
	out = append(out, planned...)
	for _, mutation := range planned {
		if mutation.Value == nil {
			if mirrorKey, ok := settingscontract.MirrorKey(mutation.Key); ok {
				out = append(out, RuntimeValue{Key: mirrorKey})
			}
			continue
		}
		mirror, ok, err := settingscontract.MirrorWrite(mutation.Key, mutation.Value)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, RuntimeValue{Key: mirror.Key, Value: mirror.Value})
		}
	}
	return out, nil
}

// RuntimeKeys returns every canonical row owned by a legacy generic key. It is
// used by DELETE, where there is no value to run through PlanRuntimeValue.
func (p *Planner) RuntimeKeys(legacyKey string) []string {
	key := CanonicalKey(legacyKey)
	var keys []string
	switch key {
	case keyPreferredQuality:
		keys = []string{keyPreferredQuality, settingskeys.PlaybackMaxBitrateKbps}
	default:
		if def, ok := p.contract.Lookup(key); ok && def.IsRemote() {
			keys = []string{key}
		}
	}
	if len(keys) == 0 {
		return nil // the caller reads this as "no canonical target"
	}
	// Clearing through the legacy route has to reach the same rows a write
	// through it created, mirror included.
	out := make([]string, 0, len(keys)+1)
	out = append(out, keys...)
	for _, owned := range keys {
		if mirrorKey, ok := settingscontract.MirrorKey(owned); ok {
			out = append(out, mirrorKey)
		}
	}
	return out
}

// profileColumnDefaults are the values the legacy schema wrote when nobody
// chose anything. A column still holding its default is not a preference, and
// migrating it would turn "never decided" into an explicit choice that outranks
// the contract default forever.
//
// The language column's 'en' default is deliberately absent: see planProfiles.
const defaultSubtitleMode = "auto"

// Source table names, recorded on every reject so an operator can find the row
// the migration could not convert.
const (
	sourceUserSettings       = "user_settings"
	sourceUserDeviceSettings = "user_device_settings"
	sourceUserProfiles       = "user_profiles"
	sourceAudioPrefs         = "audio_preferences"
	sourceSubtitlePrefs      = "subtitle_preferences"
	sourceLibraryPrefs       = "library_playback_preferences"
)

// keyPreferredQuality is the one key with bespoke handling: it decomposes into
// two canonical keys rather than converting to one.
const keyPreferredQuality = "playback.preferred_quality"

// keyCardOverlays needs a format upgrade rather than a plain conversion: old
// web clients stored a v1 document the contract schema does not accept.
const keyCardOverlays = "ui.card_overlays"

// keyAudioLanguage has a device-scope quarantine: see planDeviceSettings.
const keyAudioLanguage = "playback.audio_language"

// These identity fields make migration rejects directly queryable by the
// content context that owned the legacy value.
const (
	fieldProfileID = "profile_id"
	fieldSeriesID  = "series_id"
	fieldLibraryID = "library_id"
)

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
	p.planMirroredRows(&res)

	res.Rows = dedupeRows(res.Rows)
	res.Rows = dropOrphanProfileRows(res.Rows, in.Profiles, &res)

	return res
}

// planMirroredRows carries every planned row whose key has a replacement onto
// that replacement, at the same identity.
//
// The SQL migration that introduced playback.intro_skip_mode copies the rows
// that were already canonical when it ran. This is the other half: the backfill
// converts a user's legacy storage the first time their store opens, which for
// an install upgrading later is after that migration, so a legacy
// auto_skip_intro would otherwise arrive with no companion and a current client
// would read the contract default instead of the household's choice.
//
// It runs over the planned rows rather than inside planProfiles so it covers
// every source a mirrored key can come from — the profile column, the account
// table's fan-out, and per-device rows alike — and cannot fall behind when one
// of them changes. Companions are appended, so dedupeRows keeps an explicitly
// stored value ahead of a derived one.
//
// When legacy storage already holds *both* halves at one identity and they
// disagree — a client wrote the enum through the legacy device route while the
// boolean beside it kept the older answer — appending is not enough. Dropping
// the derived companion would leave both originals standing, and the migration
// would hand old and new clients opposite behavior from its first run. The
// replacement is authoritative in that case, so its converted value overwrites
// the deprecated row rather than being discarded. Which half is the replacement
// comes from the manifest's deprecation flag, not from naming the keys here:
// this pass must keep working for the next pair the contract supersedes.
//
// A conversion failure is dropped rather than rejected: the source row was
// planned, which means it already validated against the contract, so the only
// way to fail here is a defect in the pairing, and losing the companion is
// strictly better than losing the row that produced it.
func (p *Planner) planMirroredRows(res *Result) {
	planned := len(res.Rows)

	// Where each explicitly planned half of a mirrored pair landed, so a
	// companion can find the row it would collide with.
	explicit := make(map[rowIdentity]int, planned)
	for i := 0; i < planned; i++ {
		if _, ok := settingscontract.MirrorKey(res.Rows[i].Key); ok {
			explicit[identityOfRow(res.Rows[i])] = i
		}
	}

	for i := 0; i < planned; i++ {
		row := res.Rows[i]
		mirror, ok, err := settingscontract.MirrorWrite(row.Key, row.Value)
		if err != nil || !ok {
			continue
		}
		companion := row
		companion.Key = mirror.Key
		companion.Value = mirror.Value

		if at, collides := explicit[identityOfRow(companion)]; collides {
			if p.supersedes(row.Key, res.Rows[at].Key) {
				res.Rows[at].Value = companion.Value
			}
			continue
		}
		res.Rows = append(res.Rows, companion)
	}
}

// supersedes reports whether a value stored at key outranks one stored at
// other when the two are halves of the same preference: the replacement wins
// over the definition the manifest marks deprecated.
func (p *Planner) supersedes(key, other string) bool {
	def, ok := p.contract.Lookup(key)
	if !ok {
		return false
	}
	otherDef, otherOK := p.contract.Lookup(other)
	if !otherOK {
		return false
	}
	return !def.Deprecated && otherDef.Deprecated
}

// rowIdentity is the unique-index identity of a planned row: everything the
// canonical table keys on.
type rowIdentity struct {
	key       string
	scope     settingscontract.Scope
	profileID string
	deviceID  string
	libraryID int
	seriesID  string
}

func identityOfRow(row Row) rowIdentity {
	return rowIdentity{
		key: row.Key, scope: row.Scope,
		profileID: row.ProfileID, deviceID: row.DeviceID,
		libraryID: row.LibraryID, seriesID: row.SeriesID,
	}
}

// dropOrphanProfileRows removes rows whose profile no longer exists.
//
// The legacy per-profile tables carry an ON DELETE CASCADE on (user_id,
// profile_id) today, but rows predating that constraint survived their
// profile's deletion — a real install had 46 such rows across 14 deleted
// profiles. The canonical table declares the same foreign key, so copying one
// aborts the whole migration transaction and the server cannot start.
//
// Dropping rather than repairing: a device override belonging to a profile
// nobody can select is not a preference anyone can be shown or reset. They are
// recorded as rejects rather than discarded silently, so an operator inspecting
// user_setting_migration_rejects sees what was left behind and why.
func dropOrphanProfileRows(rows []Row, profiles []LegacyProfile, res *Result) []Row {
	// Nil means the caller did not load profiles and cannot safely validate
	// ownership. A loaded-but-empty slice means the account has no profiles, so
	// every profile-anchored legacy row is necessarily orphaned.
	if profiles == nil {
		return rows
	}
	known := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		known[profile.ID] = struct{}{}
	}

	kept := rows[:0]
	for _, row := range rows {
		if row.ProfileID == "" {
			kept = append(kept, row) // account scope: no profile to orphan
			continue
		}
		if _, ok := known[row.ProfileID]; ok {
			kept = append(kept, row)
			continue
		}
		res.Rejects = append(res.Rejects, Reject{
			SourceTable: row.sourceTable,
			SourceKey:   row.sourceKey,
			Identity:    row.sourceIdentity,
			Value:       string(row.Value),
			Reason:      "profile no longer exists; the legacy row outlived the profile it belonged to",
		})
	}
	return kept
}

// dedupeRows collapses rows that landed on the same canonical identity. The
// legacy tables key on the stored spelling, so one device can legitimately hold
// both player.next_up_prompt_seconds and playback.next_up_prompt_seconds; after
// the rename both become one identity, and writing both would trip the unique
// index — on SQLite that aborts NewUserDB and the user's store never opens
// again.
//
// The tiebreak mirrors how the rows got there: the runtime handler writes the
// canonical key first and only best-effort-deletes the alias, so when the alias
// survived alongside a canonical row, the canonical row is the newer write and
// wins. Between two rows of equal provenance the first in input order is kept,
// which keeps the pass deterministic.
func dedupeRows(rows []Row) []Row {
	kept := make(map[rowIdentity]int, len(rows))
	out := rows[:0]
	for _, row := range rows {
		id := identityOfRow(row)
		at, seen := kept[id]
		if !seen {
			kept[id] = len(out)
			out = append(out, row)
			continue
		}
		if out[at].fromRenamedKey && !row.fromRenamedKey {
			out[at] = row
		}
	}
	return out
}

// planProfiles converts the preference columns on user_profiles.
func (p *Planner) planProfiles(profiles []LegacyProfile, res *Result) {
	for _, profile := range profiles {
		id := func(field string) json.RawMessage {
			return identityJSON(map[string]any{fieldProfileID: profile.ID, "column": field})
		}

		// Language columns: the empty string is the legacy spelling of "no
		// preference", and the contract spells that as the absence of a row.
		//
		// The audio language deliberately gets no columnDefault, unlike quality
		// below: the column's 'en' default was the effective behavior — playback
		// preferred English tracks for it — while the contract default is null,
		// under which audio_select skips language matching entirely. Suppressing
		// 'en' as "never decided" would silently change what plays for every
		// profile that was getting English selection, chosen or not. The other
		// language columns default to empty, which already means unset on both
		// sides, so only this one carries behavior worth preserving.
		p.addLanguage(res, sourceUserProfiles, id("language"),
			"playback.audio_language", settingscontract.ScopeProfile,
			Row{ProfileID: profile.ID}, profile.Language, "")
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

		// The legacy schema's 1080p default was also the effective playback
		// behavior. The contract default is auto, so even an untouched legacy
		// profile needs an explicit canonical row or the cutover silently lifts
		// its resolution and bitrate cap.
		p.addQuality(res, sourceUserProfiles, id("quality_preference"),
			settingscontract.ScopeProfile, Row{ProfileID: profile.ID},
			deref(profile.QualityPreference), "")

		// The auto-skip switches default to false in both the columns and the
		// contract, so only an explicit true is a decision: migrating a false
		// would turn "never touched the switch" into a stored choice that
		// outranks the contract default forever, the show_forced_subtitles
		// problem mirrored.
		//
		//nolint:goconst // declarative table mapping columns to keys
		for _, flag := range []struct {
			column string
			key    string
			value  *bool
		}{
			{"auto_skip_intro", "playback.auto_skip_intro", profile.AutoSkipIntro},
			{"auto_skip_credits", "playback.auto_skip_credits", profile.AutoSkipCredits},
			{"auto_skip_recap", "playback.auto_skip_recap", profile.AutoSkipRecap},
			{"auto_play_next_preview", "playback.auto_play_next_preview", profile.AutoPlayNextPreview},
		} {
			if flag.value != nil && *flag.value {
				p.addValue(res, sourceUserProfiles, id(flag.column),
					flag.key, settingscontract.ScopeProfile,
					Row{ProfileID: profile.ID}, json.RawMessage("true"))
			}
		}
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
		key := CanonicalKey(setting.Key)

		// jellycompat stashed its DisplayPreferences blobs in this table under
		// synthetic keys. They are that subsystem's storage, not user settings:
		// the migration step that follows this backfill moves them to the
		// dedicated jellycompat_displayprefs table, so this planner must
		// neither convert nor reject them.
		if strings.HasPrefix(setting.Key, displayprefs.NamespacePrefix) {
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
				fromRenamedKey: key != setting.Key,
				sourceTable:    sourceUserSettings, sourceKey: setting.Key,
				sourceIdentity: accountIdentity(),
			})
		}
	}
}

func (p *Planner) planDeviceSettings(devices []LegacyDeviceSetting, res *Result) {
	for _, row := range devices {
		key := CanonicalKey(row.Key)
		identity := identityJSON(map[string]any{
			fieldProfileID: row.ProfileID, "device_id": row.DeviceID,
		})

		if key == keyPreferredQuality {
			p.addQuality(res, sourceUserDeviceSettings, identity,
				settingscontract.ScopeProfileDevice,
				Row{ProfileID: row.ProfileID, DeviceID: row.DeviceID}, row.Value, "")
			continue
		}

		// Device-scope audio language rows are stranded, not preferences: no
		// pre-contract playback path ever read them (track selection used
		// profile.Language), and migration 098 machine-fanned the account
		// value onto every known device besides. Promoting one to a real
		// profile_device override would make a value that never influenced
		// playback suddenly outrank the profile language after upgrade.
		// Quarantined rather than dropped so an operator can restore any row
		// a user insists was a choice.
		if key == keyAudioLanguage {
			// An empty value is the legacy spelling of unset, which produces
			// no row anywhere and so has nothing to quarantine.
			if strings.TrimSpace(row.Value) != "" {
				res.Rejects = append(res.Rejects, Reject{
					SourceTable: sourceUserDeviceSettings, SourceKey: row.Key,
					Identity: identity, Value: row.Value,
					Reason: "stranded device-scope audio language: never read by playback before the contract, so promoting it would change track selection",
				})
			}
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
			fromRenamedKey: key != row.Key,
			sourceTable:    sourceUserDeviceSettings, sourceKey: row.Key,
			sourceIdentity: identity,
		})
	}
}

func (p *Planner) planSeriesPrefs(prefs []LegacySeriesPreference, res *Result) {
	for _, pref := range prefs {
		base := Row{ProfileID: pref.ProfileID, SeriesID: pref.SeriesID}
		identity := identityJSON(map[string]any{
			fieldProfileID: pref.ProfileID, fieldSeriesID: pref.SeriesID,
		})
		audioSource := pref.AudioSourceTable
		if audioSource == "" {
			audioSource = sourceAudioPrefs
		}
		subtitleSource := pref.SubtitleSourceTable
		if subtitleSource == "" {
			subtitleSource = sourceSubtitlePrefs
		}
		p.addPlaybackTriple(res, audioSource, subtitleSource, identity,
			settingscontract.ScopeProfileSeries, base,
			pref.AudioLanguage, pref.SubtitleLanguage, pref.SubtitleMode, pref.ShowForcedSubtitles)
	}
}

func (p *Planner) planLibraryPrefs(prefs []LegacyLibraryPreference, res *Result) {
	for _, pref := range prefs {
		base := Row{ProfileID: pref.ProfileID, LibraryID: pref.LibraryID}
		identity := identityJSON(map[string]any{
			fieldProfileID: pref.ProfileID, fieldLibraryID: pref.LibraryID,
		})
		source := pref.SourceTable
		if source == "" {
			source = sourceLibraryPrefs
		}
		p.addPlaybackTriple(res, source, source, identity,
			settingscontract.ScopeProfileLibrary, base,
			pref.AudioLanguage, pref.SubtitleLanguage, pref.SubtitleMode, pref.ShowForcedSubtitles)
	}
}

// addPlaybackTriple converts the four playback preferences that series and
// library rows share. Both tables carry the same columns with the same
// semantics, so they convert identically at different scopes.
func (p *Planner) addPlaybackTriple(
	res *Result, audioSourceTable, subtitleSourceTable string, identity json.RawMessage,
	scope settingscontract.Scope, base Row,
	audio, subtitle, mode *string, forced *bool,
) {
	p.addLanguage(res, audioSourceTable, identity,
		"playback.audio_language", scope, base, audio, "")
	p.addLanguage(res, subtitleSourceTable, identity,
		"playback.subtitle_language", scope, base, subtitle, "")

	if m := deref(mode); m != "" {
		p.addValue(res, subtitleSourceTable, identity,
			"playback.subtitle_mode", scope, base, jsonString(m))
	}
	// Nullable at these scopes, so a non-null value is a real override in
	// either direction — unlike the profile column, where true is the default.
	if forced != nil {
		p.addValue(res, subtitleSourceTable, identity,
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
			Reason: "not a recognized legacy quality value",
		})
		return
	}
	p.addValue(res, sourceTable, identity, keyPreferredQuality,
		scope, base, jsonString(decomposed.Resolution))
	p.addValue(res, sourceTable, identity, settingskeys.PlaybackMaxBitrateKbps,
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
	row.sourceTable = sourceTable
	row.sourceKey = key
	row.sourceIdentity = append(json.RawMessage(nil), identity...)
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
		parsed = snapToStep(def, parsed)
		candidate = json.RawMessage(strconv.FormatFloat(parsed, 'g', -1, 64))

	case settingscontract.TypeObject:
		// Already a JSON document in the legacy store.
		candidate = json.RawMessage(trimmed)
		if def.Key == keyCardOverlays {
			candidate = upgradeCardOverlaysV1(candidate)
		}

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

// snapToStep rounds a legacy number onto its definition's step grid. The
// legacy endpoint never enforced the declared step, so stored values like a
// playback speed of 0.26 are real user preferences; rejecting them at
// migration would destroy the preference over a rounding artifact, and every
// client's stepper was going to snap it on the next write anyway.
// Only the grid is corrected, never the range: a value outside the declared
// bounds was never storable through any endpoint, so clamping it would invent
// a preference rather than preserve one. Those still fall to validation and
// land in the rejects for an operator to see.
func snapToStep(def *settingscontract.Definition, value float64) float64 {
	if def.ValueSchema.Step == nil || *def.ValueSchema.Step <= 0 {
		return value
	}
	step := *def.ValueSchema.Step
	base := 0.0
	if minimum, ok := def.ValueSchema.Minimum.Current(); ok {
		base = minimum
	}
	return base + math.Round((value-base)/step)*step
}

// cardOverlayIDs mirrors the overlayId enum in
// contracts/settings/v1/schemas/card-overlays.json. The v1 upgrade needs it to
// drop ids the schema no longer knows: validation is all-or-nothing, so one
// stale id left in place would quarantine the user's whole badge config.
var cardOverlayIDs = map[string]bool{
	"resolution": true, "hdr": true, "resolution_hdr": true,
	"audio": true, "audio_channels": true, "video_codec": true,
	"container": true, "aspect_ratio": true, "release_type": true,
	"edition": true, "multi_audio": true, "multi_sub": true,
	"rating_imdb": true, "rating_tmdb": true, "rating_rt": true,
	"rating_rt_audience": true, "content_rating": true,
	"year": true, "runtime": true, "original_language": true,
	"studio": true, "network": true, "show_status": true,
	"imdb_top_250": true, "rt_certified_fresh": true,
}

// cardOverlayPositions and cardOverlayAccent mirror the per-item constraints in
// the same schema.
var (
	cardOverlayPositions = map[string]bool{
		"top-left": true, "top-right": true, "bottom-left": true, "bottom-right": true,
	}
	cardOverlayAccent = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
)

// looksLikeCardOverlaysV2 is web/src/lib/overlays/schema.ts's looksLikeV2
// heuristic: v1 documents are flat overlay-id records, v2 documents carry a
// version field or at minimum a preset string and an items object.
func looksLikeCardOverlaysV2(doc map[string]any) bool {
	if version, ok := doc["version"].(float64); ok && version == 2 {
		return true
	}
	_, presetIsString := doc["preset"].(string)
	_, itemsIsObject := doc["items"].(map[string]any)
	return presetIsString && itemsIsObject
}

// upgradeCardOverlaysV1 rewrites a v1 card_overlays document into the v2 shape
// the contract schema requires, mirroring the web client's migrateFromV1.
//
// Old web clients stored a flat Record<overlayId, {enabled, position}>; the web
// parser upgrades that at read time, but the contract schema only accepts v2,
// so without this every v1 row would be quarantined and the user's badge
// config silently lost. The upgrade keeps what migrateFromV1 keeps — known ids,
// the fields the v2 schema accepts, the "classic" default preset and an empty
// order — and drops the rest. Items missing a valid enabled or position are
// dropped whole rather than half-written: the v2 schema requires both, the
// registry defaults that would fill them live only in the web bundle, and an
// absent item already resolves to those same defaults at read time.
//
// Anything that is not an object, or already looks like v2, passes through
// unchanged for validation to judge.
func upgradeCardOverlaysV1(raw json.RawMessage) json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	if looksLikeCardOverlaysV2(doc) {
		return raw
	}

	items := map[string]any{}
	for id, entry := range doc {
		if !cardOverlayIDs[id] {
			continue
		}
		fields, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		enabled, hasEnabled := fields["enabled"].(bool)
		position, hasPosition := fields["position"].(string)
		if !hasEnabled || !hasPosition || !cardOverlayPositions[position] {
			continue
		}
		item := map[string]any{"enabled": enabled, "position": position}
		if accent, ok := fields["accentColor"].(string); ok && cardOverlayAccent.MatchString(accent) {
			item["accentColor"] = accent
		}
		if showIcon, ok := fields["showIcon"].(bool); ok {
			item["showIcon"] = showIcon
		}
		items[id] = item
	}

	upgraded, err := json.Marshal(map[string]any{
		"version": 2, "preset": "classic", "order": []string{}, "items": items,
	})
	if err != nil {
		// Maps of plain JSON values always marshal; this cannot fire.
		return raw
	}
	return upgraded
}

func (p *Planner) validate(def *settingscontract.Definition, value json.RawMessage) error {
	return def.ValueSchema.ValidateValue(value, p.schemas)
}

// CanonicalKey returns the contract spelling for a legacy settings key. Live
// compatibility paths and admin projections use the same rename table as the
// migration so newly added aliases cannot drift between them.
func CanonicalKey(legacy string) string {
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
