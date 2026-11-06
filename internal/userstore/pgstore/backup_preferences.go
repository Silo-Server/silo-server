package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/backup/userdata"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// PreferencesBackupData is a profile's playback, subtitle, and display
// preferences (userdata.Preferences): the preference columns of its profile
// row, its setting values at profile, client-family, library, and series
// scope, and its per-series and per-library track and quality choices.
// Importing it overwrites each value that differs; values it does not carry
// stay as they are.
//
// Never carried: device-scoped values (they belong to device registrations)
// and account-scoped values. Home section overrides (user_settings) are
// carried and mapped to this server's pages, sections, and collections; an
// override with no counterpart here is left out (mapSectionPages), and an
// import replaces the overrides of each page it carries. A value that names
// a library is remapped through the library map; navigation documents that
// name libraries, sections, and collections are kept only on the same
// server.
type PreferencesBackupData struct {
	// NormalizeSort is the sort editor's normalization of a collection sort
	// preference (catalog.NormalizeCollectionSortPreference): the identity,
	// field, and order the editor would store it under, or false when the
	// editor refuses it. nil refuses every collection sort.
	NormalizeSort func(kind, collection, field, order string) (string, string, string, string, bool)
}

func (PreferencesBackupData) ID() string { return userdata.Preferences }

func (PreferencesBackupData) Tables() []string {
	return []string{"user_setting_values", "user_audio_preferences", "user_subtitle_preferences",
		"user_library_playback_preferences", "user_series_playback_preferences", "user_collection_sort_preferences", "user_settings"}
}

type preferencesDocument struct {
	Profile         *profilePreferences  `json:"profile"`
	Settings        []settingPreference  `json:"settings"`
	Audio           []audioPreference    `json:"audio"`
	Subtitles       []subtitlePreference `json:"subtitles"`
	LibraryPlayback []libraryPreference  `json:"library_playback"`
	SeriesPlayback  []seriesPreference   `json:"series_playback"`
	CollectionSort  []collectionSort     `json:"collection_sort"`
	// SectionOverrides are the profile's home and library page layout.
	SectionOverrides []sectionOverrideEntry `json:"section_overrides"`
}

// profilePreferences are the preference columns of user_profiles; access
// columns are profile fields, never preferences.
type profilePreferences struct {
	QualityPreference          string `json:"quality_preference"`
	Language                   string `json:"language"`
	PreferredMetadataLanguage  string `json:"preferred_metadata_language"`
	SubtitleLanguage           string `json:"subtitle_language"`
	SubtitleMode               string `json:"subtitle_mode"`
	AutoSkipIntro              bool   `json:"auto_skip_intro"`
	AutoSkipCredits            bool   `json:"auto_skip_credits"`
	AutoSkipRecap              bool   `json:"auto_skip_recap"`
	AutoPlayNextPreview        bool   `json:"auto_play_next_preview"`
	ShowForcedSubtitles        bool   `json:"show_forced_subtitles"`
	RemoveWatchedFromWatchlist bool   `json:"remove_watched_from_watchlist"`
}

type settingPreference struct {
	Key          string          `json:"key"`
	Scope        string          `json:"scope"`
	ClientFamily string          `json:"client_family,omitempty"`
	Library      int             `json:"library,omitempty"`
	Series       string          `json:"series,omitempty"`
	Value        json.RawMessage `json:"value"`
}

type audioPreference struct {
	Series     string          `json:"series"`
	TrackIndex *int            `json:"track_index"`
	Language   *string         `json:"language"`
	Signature  json.RawMessage `json:"signature"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type subtitlePreference struct {
	Series              string          `json:"series"`
	Language            string          `json:"language"`
	TrackIndex          int             `json:"track_index"`
	ExternalPath        string          `json:"external_path" bound:"4096"`
	Mode                string          `json:"mode"`
	ShowForcedSubtitles *bool           `json:"show_forced_subtitles"`
	Signature           json.RawMessage `json:"signature"`
	UpdatedAt           time.Time       `json:"updated_at"`
}

type libraryPreference struct {
	Library             int       `json:"library"`
	AudioLanguage       *string   `json:"audio_language"`
	SubtitleLanguage    *string   `json:"subtitle_language"`
	SubtitleMode        *string   `json:"subtitle_mode"`
	ShowForcedSubtitles *bool     `json:"show_forced_subtitles"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type seriesPreference struct {
	Series     string    `json:"series"`
	Resolution *string   `json:"resolution"`
	HDR        bool      `json:"hdr"`
	CodecVideo *string   `json:"codec_video"`
	FileID     *int64    `json:"file_id"`
	EditionKey *string   `json:"edition_key"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type collectionSort struct {
	Kind       string    `json:"kind"`
	Collection string    `json:"collection"`
	Field      string    `json:"field"`
	Order      string    `json:"order"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Settings whose values name libraries.
var (
	libraryListSettings = map[string]bool{"ui.disabled_library_ids": true, "ui.library_order": true}
	// navigationSettings name libraries, sections, and collections of this
	// server, so another server's are not restored.
	navigationSettings = map[string]bool{"nav.shortcuts": true, "nav.primary_menu": true, "ui.sidebar_pins": true}
)

// restorableScopes are the setting scopes a profile's preferences carry.
var restorableScopes = map[settingscontract.Scope]bool{
	settingscontract.ScopeProfile: true, settingscontract.ScopeProfileClient: true,
	settingscontract.ScopeProfileLibrary: true, settingscontract.ScopeProfileSeries: true,
}

func (PreferencesBackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner, keep userdata.Keep) (userdata.Export, error) {
	doc := preferencesDocument{Settings: []settingPreference{}, Audio: []audioPreference{}, Subtitles: []subtitlePreference{},
		LibraryPlayback: []libraryPreference{}, SeriesPlayback: []seriesPreference{}, CollectionSort: []collectionSort{}}
	var items []string
	p, err := readProfilePreferences(ctx, tx, owner)
	if err != nil {
		return userdata.Export{}, err
	}
	doc.Profile = &p
	err = collectRows(ctx, tx, `
		SELECT key, scope, COALESCE(client_family, ''), COALESCE(library_id, 0), COALESCE(series_id, ''), value::text
		FROM user_setting_values WHERE user_id = $1 AND profile_id = $2 AND scope IN ('profile', 'profile_client', 'profile_library', 'profile_series')
		ORDER BY key, scope, client_family, library_id, series_id`, owner, func(rows pgx.Rows) error {
		var s settingPreference
		var value string
		if err := rows.Scan(&s.Key, &s.Scope, &s.ClientFamily, &s.Library, &s.Series, &value); err != nil {
			return err
		}
		if s.Series != "" && !keep.Has(s.Series) {
			return nil
		}
		s.Value = json.RawMessage(value)
		doc.Settings = append(doc.Settings, s)
		if s.Series != "" {
			items = append(items, s.Series)
		}
		return nil
	})
	if err != nil {
		return userdata.Export{}, fmt.Errorf("reading setting values: %w", err)
	}
	if err := exportTrackPreferences(ctx, tx, owner, keep, &doc, &items); err != nil {
		return userdata.Export{}, err
	}
	if doc.SectionOverrides, err = exportSectionOverrides(ctx, tx, owner); err != nil {
		return userdata.Export{}, err
	}
	count := 1 + len(doc.Settings) + len(doc.Audio) + len(doc.Subtitles) + len(doc.LibraryPlayback) + len(doc.SeriesPlayback) + len(doc.CollectionSort) +
		len(doc.SectionOverrides)
	return exportDocument(doc, count, items)
}

// readProfilePreferences reads a profile's preference columns.
func readProfilePreferences(ctx context.Context, tx pgx.Tx, owner userdata.Owner) (profilePreferences, error) {
	var p profilePreferences
	err := tx.QueryRow(ctx, `
		SELECT quality_preference, language, preferred_metadata_language, subtitle_language, subtitle_mode, auto_skip_intro,
			auto_skip_credits, auto_skip_recap, auto_play_next_preview, show_forced_subtitles, remove_watched_from_watchlist
		FROM user_profiles WHERE user_id = $1 AND id = $2`, owner.UserID, owner.ProfileID).Scan(
		&p.QualityPreference, &p.Language, &p.PreferredMetadataLanguage, &p.SubtitleLanguage, &p.SubtitleMode, &p.AutoSkipIntro,
		&p.AutoSkipCredits, &p.AutoSkipRecap, &p.AutoPlayNextPreview, &p.ShowForcedSubtitles, &p.RemoveWatchedFromWatchlist)
	if err != nil {
		return p, fmt.Errorf("reading profile preferences: %w", err)
	}
	return p, nil
}

// differing names the columns whose values differ from other's.
func (p profilePreferences) differing(other profilePreferences) []string {
	var names []string
	a, b := reflect.ValueOf(p), reflect.ValueOf(other)
	for i := range a.NumField() {
		if a.Field(i).Interface() != b.Field(i).Interface() {
			names = append(names, a.Type().Field(i).Tag.Get("json"))
		}
	}
	return names
}

// exportTrackPreferences reads the per-series and per-library track and
// quality choices and the collection sort orders.
func exportTrackPreferences(ctx context.Context, tx pgx.Tx, owner userdata.Owner, keep userdata.Keep, doc *preferencesDocument, items *[]string) error {
	err := collectRows(ctx, tx, `SELECT series_id, audio_track_index, audio_language, audio_track_signature::text, updated_at
		FROM user_audio_preferences WHERE user_id = $1 AND profile_id = $2 ORDER BY series_id`, owner, func(rows pgx.Rows) error {
		var a audioPreference
		var signature string
		if err := rows.Scan(&a.Series, &a.TrackIndex, &a.Language, &signature, &a.UpdatedAt); err != nil {
			return err
		}
		if !keep.Has(a.Series) {
			return nil
		}
		a.Signature = json.RawMessage(signature)
		doc.Audio = append(doc.Audio, a)
		*items = append(*items, a.Series)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading audio preferences: %w", err)
	}
	err = collectRows(ctx, tx, `SELECT series_id, subtitle_language, subtitle_track_index, external_subtitle_path, subtitle_mode, show_forced_subtitles,
			subtitle_track_signature::text, updated_at
		FROM user_subtitle_preferences WHERE user_id = $1 AND profile_id = $2 ORDER BY series_id`, owner, func(rows pgx.Rows) error {
		var s subtitlePreference
		var signature string
		if err := rows.Scan(&s.Series, &s.Language, &s.TrackIndex, &s.ExternalPath, &s.Mode, &s.ShowForcedSubtitles, &signature, &s.UpdatedAt); err != nil {
			return err
		}
		if !keep.Has(s.Series) {
			return nil
		}
		s.Signature = json.RawMessage(signature)
		doc.Subtitles = append(doc.Subtitles, s)
		*items = append(*items, s.Series)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading subtitle preferences: %w", err)
	}
	err = collectRows(ctx, tx, `SELECT library_id, audio_language, subtitle_language, subtitle_mode, show_forced_subtitles, updated_at
		FROM user_library_playback_preferences WHERE user_id = $1 AND profile_id = $2 ORDER BY library_id`, owner, func(rows pgx.Rows) error {
		var l libraryPreference
		if err := rows.Scan(&l.Library, &l.AudioLanguage, &l.SubtitleLanguage, &l.SubtitleMode, &l.ShowForcedSubtitles, &l.UpdatedAt); err != nil {
			return err
		}
		doc.LibraryPlayback = append(doc.LibraryPlayback, l)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading library playback preferences: %w", err)
	}
	err = collectRows(ctx, tx, `SELECT series_id, resolution, hdr, codec_video, file_id, edition_key, updated_at
		FROM user_series_playback_preferences WHERE user_id = $1 AND profile_id = $2 ORDER BY series_id`, owner, func(rows pgx.Rows) error {
		var s seriesPreference
		if err := rows.Scan(&s.Series, &s.Resolution, &s.HDR, &s.CodecVideo, &s.FileID, &s.EditionKey, &s.UpdatedAt); err != nil {
			return err
		}
		if !keep.Has(s.Series) {
			return nil
		}
		doc.SeriesPlayback = append(doc.SeriesPlayback, s)
		*items = append(*items, s.Series)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading series playback preferences: %w", err)
	}
	err = collectRows(ctx, tx, `SELECT collection_kind, collection_id, sort_field, sort_order, updated_at
		FROM user_collection_sort_preferences WHERE user_id = $1 AND profile_id = $2 AND collection_kind <> 'user'
		ORDER BY collection_kind, collection_id`, owner, func(rows pgx.Rows) error {
		var c collectionSort
		if err := rows.Scan(&c.Kind, &c.Collection, &c.Field, &c.Order, &c.UpdatedAt); err != nil {
			return err
		}
		doc.CollectionSort = append(doc.CollectionSort, c)
		return nil
	})
	if err != nil {
		return fmt.Errorf("reading collection sort preferences: %w", err)
	}
	return nil
}

func collectRows(ctx context.Context, tx pgx.Tx, sql string, owner userdata.Owner, scan func(pgx.Rows) error) error {
	rows, err := tx.Query(ctx, sql, owner.UserID, owner.ProfileID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// settingDefinition checks one setting against the settings contract: a
// known, server-stored key at a scope it allows, with a value it accepts.
func settingDefinition(s settingPreference) (*settingscontract.Definition, json.RawMessage, error) {
	manifest, err := settingscontract.Load()
	if err != nil {
		return nil, nil, err
	}
	def, ok := manifest.Lookup(s.Key)
	if !ok {
		return nil, nil, fmt.Errorf("setting %q is not known", s.Key)
	}
	scope := settingscontract.Scope(s.Scope)
	if def.Persistence != settingscontract.PersistenceRemote || !restorableScopes[scope] ||
		!slices.ContainsFunc(def.AllowedScopes, func(e settingscontract.ScopeEntry) bool { return e.Scope == scope }) {
		return nil, nil, fmt.Errorf("setting %q cannot be restored at scope %q", s.Key, s.Scope)
	}
	normalized, err := def.ValueSchema.NormalizeValue(s.Value, settingscontract.ObjectSchemas())
	if err != nil {
		return nil, nil, fmt.Errorf("setting %q: %w", s.Key, err)
	}
	return def, normalized, nil
}

func (d PreferencesBackupData) decode(data json.RawMessage) (preferencesDocument, userdata.Summary, error) {
	var doc preferencesDocument
	var sum userdata.Summary
	if err := userdata.DecodeStrict(data, &doc); err != nil {
		return doc, sum, err
	}
	sum.Count = len(doc.Settings) + len(doc.Audio) + len(doc.Subtitles) + len(doc.LibraryPlayback) + len(doc.SeriesPlayback) + len(doc.CollectionSort) +
		len(doc.SectionOverrides)
	if err := userdata.CheckEntries("preferences", sum.Count); err != nil {
		return doc, sum, err
	}
	if p := doc.Profile; p != nil {
		sum.Count++
		for _, s := range []string{p.QualityPreference, p.Language, p.PreferredMetadataLanguage, p.SubtitleLanguage, p.SubtitleMode} {
			if !userdata.ValidText(s, 64) {
				return doc, sum, errors.New("a profile preference is too long")
			}
		}
	}
	if err := doc.validateSettings(&sum); err != nil {
		return doc, sum, err
	}
	if err := validateSectionOverrides(doc.SectionOverrides); err != nil {
		return doc, sum, err
	}
	err := doc.validateTracks(&sum, d.NormalizeSort)
	return doc, sum, err
}

// seriesCheck validates a series reference and records it.
func seriesCheck(sum *userdata.Summary) func(string) error {
	return func(id string) error {
		if !userdata.ValidContentID(id) {
			return fmt.Errorf("a preference names an invalid series %q", id)
		}
		sum.Items = append(sum.Items, id)
		return nil
	}
}

// maxImportedSettings bounds a document's settings: a profile's own,
// per-client, per-library, and per-series values together.
const maxImportedSettings = 20_000

func (doc preferencesDocument) validateSettings(sum *userdata.Summary) error {
	if len(doc.Settings) > maxImportedSettings {
		return fmt.Errorf("more than %d settings", maxImportedSettings)
	}
	series := seriesCheck(sum)
	seen := make(map[string]bool, len(doc.Settings))
	for _, s := range doc.Settings {
		// One value per identity: a repeat would only be planned again.
		identity := settingKey(s.Key, s.Scope, s.ClientFamily, s.Library, s.Series)
		if seen[identity] {
			return fmt.Errorf("setting %q is listed twice for one scope", s.Key)
		}
		seen[identity] = true
		// Keys and values are checked against this server's settings
		// contract when imported, so a newer server's settings are left out
		// rather than refusing the whole document.
		if len(s.Value) == 0 || !json.Valid(s.Value) || !restorableScopes[settingscontract.Scope(s.Scope)] {
			return fmt.Errorf("setting %q is not valid", s.Key)
		}
		id := userstore.SettingIdentity{Key: s.Key, Scope: settingscontract.Scope(s.Scope), ProfileID: "p",
			ClientFamily: settingscontract.ClientFamily(s.ClientFamily), LibraryID: s.Library, SeriesID: s.Series}
		if err := id.Validate(); err != nil {
			return fmt.Errorf("setting %q: %w", s.Key, err)
		}
		if s.Series != "" {
			if err := series(s.Series); err != nil {
				return err
			}
		}
	}
	return nil
}

func (doc preferencesDocument) validateTracks(sum *userdata.Summary, normalizeSort func(kind, collection, field, order string) (string, string, string, string, bool)) error {
	series := seriesCheck(sum)
	// Each list holds one entry per series, library, or collection, as
	// the store keeps one row per each; a repeat is refused.
	once := func(what string) func(key string) error {
		seen := map[string]bool{}
		return func(key string) error {
			if seen[key] {
				return fmt.Errorf("a %s preference is listed twice", what)
			}
			seen[key] = true
			return nil
		}
	}
	audio, subtitles, libraries, seriesPlayback, sorts := once("audio"), once("subtitle"), once("library playback"), once("series playback"), once("collection sort")
	for i := range doc.Audio {
		a := &doc.Audio[i]
		if err := errors.Join(series(a.Series), audio(a.Series)); err != nil {
			return err
		}
		// A signature is stored as the playback writer stores one: decoded
		// into its type and encoded again, never as the backup's bytes.
		var sig userstore.AudioTrackSignature
		canonical, err := canonicalSignature(a.Signature, &sig, func() ([]byte, error) { return userstore.MarshalAudioTrackSignature(&sig) })
		if err != nil {
			return errors.New("an audio preference carries an invalid track signature")
		}
		a.Signature = canonical
	}
	for i := range doc.Subtitles {
		s := &doc.Subtitles[i]
		if err := errors.Join(series(s.Series), subtitles(s.Series)); err != nil {
			return err
		}
		var sig userstore.SubtitleTrackSignature
		canonical, err := canonicalSignature(s.Signature, &sig, func() ([]byte, error) { return userstore.MarshalSubtitleTrackSignature(&sig) })
		if err != nil || !userdata.ValidText(s.ExternalPath, 4096) {
			return errors.New("a subtitle preference is not valid")
		}
		s.Signature = canonical
	}
	for _, l := range doc.LibraryPlayback {
		if l.Library <= 0 {
			return errors.New("a library playback preference names no library")
		}
		if err := libraries(strconv.Itoa(l.Library)); err != nil {
			return err
		}
	}
	for _, s := range doc.SeriesPlayback {
		if err := errors.Join(series(s.Series), seriesPlayback(s.Series)); err != nil {
			return err
		}
	}
	if len(doc.CollectionSort) > maxImportedCollectionSorts {
		return fmt.Errorf("more than %d collection sort preferences", maxImportedCollectionSorts)
	}
	for i := range doc.CollectionSort {
		// Each sort is stored under the identity, and with the field and
		// order, the sort editor would store it with: one per personal
		// list, whatever collection id the document gives it.
		c := &doc.CollectionSort[i]
		var kind, collection, field, order string
		ok := normalizeSort != nil
		if ok {
			kind, collection, field, order, ok = normalizeSort(c.Kind, c.Collection, c.Field, c.Order)
		}
		if !ok || kind == userstore.CollectionKindUser || !userdata.ValidText(collection, 128) {
			return errors.New("a collection sort preference is not valid")
		}
		c.Kind, c.Collection, c.Field, c.Order = kind, collection, field, order
		if err := sorts(kind + "\x00" + collection); err != nil {
			return err
		}
	}
	return nil
}

// maxImportedCollectionSorts bounds a document's collection sort
// preferences: one per admin collection the profile re-sorted, and one per
// personal list.
const maxImportedCollectionSorts = 10_000

func (d PreferencesBackupData) Validate(data json.RawMessage) (userdata.Summary, error) {
	_, sum, err := d.decode(data)
	return sum, err
}

func (d PreferencesBackupData) Import(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := d.decode(data)
	if err != nil {
		return userdata.Result{}, err
	}
	var res userdata.Result
	// Setting values change under the lock canonical setting mutations and
	// profile creation take for the account.
	if err := lockPreferenceSettings(ctx, tx, target.UserID); err != nil {
		return res, err
	}
	if p := doc.Profile; p != nil {
		tag, err := tx.Exec(ctx, `
			UPDATE user_profiles SET quality_preference = $3, language = $4, preferred_metadata_language = $5, subtitle_language = $6,
				subtitle_mode = $7, auto_skip_intro = $8, auto_skip_credits = $9, auto_skip_recap = $10, auto_play_next_preview = $11,
				show_forced_subtitles = $12, remove_watched_from_watchlist = $13, updated_at = now()
			WHERE user_id = $1 AND id = $2
			  AND (quality_preference, language, preferred_metadata_language, subtitle_language, subtitle_mode, auto_skip_intro,
				auto_skip_credits, auto_skip_recap, auto_play_next_preview, show_forced_subtitles, remove_watched_from_watchlist)
			  IS DISTINCT FROM ($3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			target.UserID, target.ProfileID, p.QualityPreference, p.Language, p.PreferredMetadataLanguage, p.SubtitleLanguage, p.SubtitleMode,
			p.AutoSkipIntro, p.AutoSkipCredits, p.AutoSkipRecap, p.AutoPlayNextPreview, p.ShowForcedSubtitles, p.RemoveWatchedFromWatchlist)
		if err != nil {
			return res, fmt.Errorf("restoring profile preferences: %w", err)
		}
		if tag.RowsAffected() > 0 {
			res.Updated++
		} else {
			res.Unchanged++
		}
	}
	navigationDropped, rejected := 0, 0
	// The profile's stored values are read once, not once per entry.
	stored, err := storedSettings(ctx, tx, target.Owner)
	if err != nil {
		return res, err
	}
	var writes []settingPlan
	for _, s := range doc.Settings {
		plan, err := planSetting(ctx, tx, target, s, stored)
		if err != nil {
			return res, err
		}
		switch plan.outcome {
		case settingRejected:
			rejected++
			res.LeftOut++
		case settingNavigation:
			navigationDropped++
			res.LeftOut++
		case settingUnmapped:
			res.LeftOut++
		case settingSame:
			res.Unchanged++
		case settingNew:
			res.Added++
			writes = append(writes, plan)
		default:
			res.Updated++
			writes = append(writes, plan)
		}
	}
	if err := upsertSettingValues(ctx, tx, target.UserID, writes); err != nil {
		return res, err
	}
	if rejected > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d settings this server does not accept were left out.", rejected))
	}
	if navigationDropped > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d navigation settings name another server's libraries and pages and were left out.", navigationDropped))
	}
	if err := importTrackPreferences(ctx, tx, target, doc, &res); err != nil {
		return res, err
	}
	if err := importSectionOverrides(ctx, tx, target, doc.SectionOverrides, &res); err != nil {
		return res, err
	}
	return res, nil
}

// What importing one setting does.
const (
	settingNew = iota
	settingChanged
	settingSame
	settingRejected
	settingNavigation
	settingUnmapped
)

// settingPlan is one setting's import: its identity and value here, and
// what writing it would do.
type settingPlan struct {
	outcome int
	id      userstore.SettingIdentity
	value   json.RawMessage
}

// planSetting checks a setting against the settings contract, maps its
// library and series, and compares it with the value here; it writes
// nothing, so Import and Pending share it.
func planSetting(ctx context.Context, tx pgx.Tx, target userdata.Target, s settingPreference, stored settingLookup) (settingPlan, error) {
	def, value, err := settingDefinition(s)
	if err != nil {
		return settingPlan{outcome: settingRejected}, nil //nolint:nilerr // a setting this server does not accept is left out
	}
	id := userstore.SettingIdentity{Key: s.Key, Scope: settingscontract.Scope(s.Scope), ProfileID: target.ProfileID,
		ClientFamily: settingscontract.ClientFamily(s.ClientFamily)}
	if s.Library > 0 {
		local, ok := target.MapLibrary(s.Library)
		if !ok {
			return settingPlan{outcome: settingUnmapped}, nil
		}
		id.LibraryID = local
	}
	if s.Series != "" {
		local, ok, err := target.Items.Resolve(ctx, s.Series)
		if err != nil || !ok {
			return settingPlan{outcome: settingUnmapped}, err
		}
		id.SeriesID = local
	}
	switch {
	case navigationSettings[s.Key] && !target.SameServer:
		return settingPlan{outcome: settingNavigation}, nil
	case libraryListSettings[s.Key]:
		if value, err = remapLibraryList(value, target); err != nil {
			return settingPlan{}, fmt.Errorf("setting %q: %w", s.Key, err)
		}
	}
	current, found, err := stored(ctx, id)
	if err != nil {
		return settingPlan{}, err
	}
	plan := settingPlan{outcome: settingNew, id: id, value: value}
	if !found {
		return plan, nil
	}
	plan.outcome = settingChanged
	if current, err = storedAsCarried(ctx, tx, def, s.Key, current); err != nil {
		return settingPlan{}, fmt.Errorf("setting %q: %w", s.Key, err)
	}
	if sameJSON(current, value) {
		plan.outcome = settingSame
	}
	return plan, nil
}

// storedAsCarried shapes a value stored here as a backup carries it, so
// the two compare by what a reader sees. It is normalized by the contract,
// as the backup's is: a stored "eng" and a normalized "en" are the same
// value. A library list drops ids of libraries deleted since, which the
// backup's list no longer has and which mean nothing to a reader.
func storedAsCarried(ctx context.Context, tx pgx.Tx, def *settingscontract.Definition, key string, current json.RawMessage) (json.RawMessage, error) {
	if normalized, err := def.ValueSchema.NormalizeValue(current, settingscontract.ObjectSchemas()); err == nil {
		current = normalized
	}
	if !libraryListSettings[key] {
		return current, nil
	}
	return librariesHere(ctx, tx, current)
}

// sameJSON compares two JSON documents by value.
func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// settingLookup reads the value stored here for a setting identity.
type settingLookup func(ctx context.Context, id userstore.SettingIdentity) (json.RawMessage, bool, error)

// settingKey names a profile's setting value by its identity.
func settingKey(key, scope, client string, library int, series string) string {
	return key + "\x00" + scope + "\x00" + client + "\x00" + strconv.Itoa(library) + "\x00" + series
}

// storedSettings reads every value of a profile's restorable scopes at
// once, for a comparison of a whole document.
func storedSettings(ctx context.Context, tx pgx.Tx, owner userdata.Owner) (settingLookup, error) {
	rows, err := tx.Query(ctx, `
		SELECT key, scope, COALESCE(client_family, ''), COALESCE(library_id, 0), COALESCE(series_id, ''), value::text
		FROM user_setting_values WHERE user_id = $1 AND profile_id = $2 AND scope IN ('profile', 'profile_client', 'profile_library', 'profile_series')`,
		owner.UserID, owner.ProfileID)
	if err != nil {
		return nil, fmt.Errorf("reading setting values: %w", err)
	}
	defer rows.Close()
	values := map[string]json.RawMessage{}
	for rows.Next() {
		var key, scope, client, series, value string
		var library int
		if err := rows.Scan(&key, &scope, &client, &library, &series, &value); err != nil {
			return nil, err
		}
		values[settingKey(key, scope, client, library, series)] = json.RawMessage(value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return func(_ context.Context, id userstore.SettingIdentity) (json.RawMessage, bool, error) {
		v, ok := values[settingKey(id.Key, string(id.Scope), string(id.ClientFamily), id.LibraryID, id.SeriesID)]
		return v, ok, nil
	}, nil
}

// librariesHere keeps the ids of a stored library id list that are
// libraries on this server, in order and once each, as remapLibraryList
// shapes the backup's list. A value that is not a list is returned as is.
func librariesHere(ctx context.Context, tx pgx.Tx, value json.RawMessage) (json.RawMessage, error) {
	var ids []int
	if err := json.Unmarshal(value, &ids); err != nil {
		return value, nil //nolint:nilerr // not a list: compared as stored, so it differs
	}
	rows, err := tx.Query(ctx, `SELECT id FROM media_folders WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("reading libraries: %w", err)
	}
	here, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return nil, fmt.Errorf("reading libraries: %w", err)
	}
	out := []int{}
	for _, id := range ids {
		if slices.Contains(here, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return json.Marshal(out)
}

// remapLibraryList maps a library id list, dropping ids with no library
// here.
func remapLibraryList(value json.RawMessage, target userdata.Target) (json.RawMessage, error) {
	var ids []int
	if err := json.Unmarshal(value, &ids); err != nil {
		return nil, err
	}
	out := []int{}
	for _, id := range ids {
		if local, ok := target.MapLibrary(id); ok && !slices.Contains(out, local) {
			out = append(out, local)
		}
	}
	return json.Marshal(out)
}

// resolveSeries maps a series id, counting one left out when it has no item
// here.
func resolveSeries(ctx context.Context, target userdata.Target, id string, res *userdata.Result) (string, bool, error) {
	local, ok, err := target.Items.Resolve(ctx, id)
	if err == nil && !ok {
		res.LeftOut++
	}
	return local, ok, err
}

// trackSignature is a stored signature, {} when none.
func trackSignature(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}

// localPath is the external subtitle path to store: a path on another
// server's disk is dropped.
func (s subtitlePreference) localPath(target userdata.Target) string {
	if !target.SameServer {
		return ""
	}
	return s.ExternalPath
}

// localFileID is the file id to store: another server's file id is dropped.
func (s seriesPreference) localFileID(target userdata.Target) *int64 {
	if !target.SameServer {
		return nil
	}
	return s.FileID
}

// canonicalSignature decodes a track signature into its type (into),
// bounded like every imported field, and returns encode's form of it; an
// empty one stays empty.
func canonicalSignature(raw json.RawMessage, into any, encode func() ([]byte, error)) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	if err := userdata.DecodeStrict(raw, into); err != nil {
		return nil, err
	}
	return encode()
}
