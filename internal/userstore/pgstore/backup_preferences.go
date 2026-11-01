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
// Never carried: device-scoped values (they belong to device registrations),
// account-scoped values, and home section overrides (user_settings), which
// name this server's sections. A value that names a library is remapped
// through the library map; navigation documents that name libraries,
// sections, and collections are kept only on the same server.
type PreferencesBackupData struct{}

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
	ExternalPath        string          `json:"external_path"`
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

func (PreferencesBackupData) Export(ctx context.Context, tx pgx.Tx, owner userdata.Owner) (userdata.Export, error) {
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
	if err := exportTrackPreferences(ctx, tx, owner, &doc, &items); err != nil {
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
func exportTrackPreferences(ctx context.Context, tx pgx.Tx, owner userdata.Owner, doc *preferencesDocument, items *[]string) error {
	err := collectRows(ctx, tx, `SELECT series_id, audio_track_index, audio_language, audio_track_signature::text, updated_at
		FROM user_audio_preferences WHERE user_id = $1 AND profile_id = $2 ORDER BY series_id`, owner, func(rows pgx.Rows) error {
		var a audioPreference
		var signature string
		if err := rows.Scan(&a.Series, &a.TrackIndex, &a.Language, &signature, &a.UpdatedAt); err != nil {
			return err
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

func decodePreferences(data json.RawMessage) (preferencesDocument, userdata.Summary, error) {
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
	err := doc.validateTracks(&sum)
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

func (doc preferencesDocument) validateSettings(sum *userdata.Summary) error {
	series := seriesCheck(sum)
	for _, s := range doc.Settings {
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

func (doc preferencesDocument) validateTracks(sum *userdata.Summary) error {
	series := seriesCheck(sum)
	for _, a := range doc.Audio {
		if err := series(a.Series); err != nil {
			return err
		}
		if len(a.Signature) > 0 && !json.Valid(a.Signature) {
			return errors.New("an audio preference carries invalid JSON")
		}
	}
	for _, s := range doc.Subtitles {
		if err := series(s.Series); err != nil {
			return err
		}
		if (len(s.Signature) > 0 && !json.Valid(s.Signature)) || !userdata.ValidText(s.ExternalPath, 4096) {
			return errors.New("a subtitle preference is not valid")
		}
	}
	for _, l := range doc.LibraryPlayback {
		if l.Library <= 0 {
			return errors.New("a library playback preference names no library")
		}
	}
	for _, s := range doc.SeriesPlayback {
		if err := series(s.Series); err != nil {
			return err
		}
	}
	for _, c := range doc.CollectionSort {
		if (c.Kind != scopeLibrary && c.Kind != "watchlist" && c.Kind != "favorites") || (c.Order != "" && c.Order != "asc" && c.Order != "desc") ||
			!userdata.ValidText(c.Field, 64) || !userdata.ValidText(c.Collection, 128) {
			return errors.New("a collection sort preference is not valid")
		}
	}
	return nil
}

func (PreferencesBackupData) Validate(data json.RawMessage) (userdata.Summary, error) {
	_, sum, err := decodePreferences(data)
	return sum, err
}

// upsertCounted runs an upsert whose conflict update is guarded by "values
// differ" and that returns (xmax = 0): no row means nothing changed.
func upsertCounted(ctx context.Context, tx pgx.Tx, res *userdata.Result, sql string, args ...any) error {
	var inserted bool
	err := tx.QueryRow(ctx, sql, args...).Scan(&inserted)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		res.Unchanged++
	case err != nil:
		return err
	case inserted:
		res.Added++
	default:
		res.Updated++
	}
	return nil
}

func (PreferencesBackupData) Import(ctx context.Context, tx pgx.Tx, target userdata.Target, data json.RawMessage) (userdata.Result, error) {
	doc, _, err := decodePreferences(data)
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
	stored := storedSetting(tx, target.UserID)
	for _, s := range doc.Settings {
		plan, err := planSetting(ctx, tx, target, s, stored)
		if err != nil {
			return res, err
		}
		switch plan.outcome {
		case settingRejected:
			rejected++
			res.LeftOut++
			continue
		case settingNavigation:
			navigationDropped++
			res.LeftOut++
			continue
		case settingUnmapped:
			res.LeftOut++
			continue
		case settingSame:
			res.Unchanged++
			continue
		}
		if _, err := upsertSettingValue(ctx, tx, target.UserID, plan.id, plan.value); err != nil {
			return res, err
		}
		if plan.outcome == settingNew {
			res.Added++
		} else {
			res.Updated++
		}
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

// storedSetting reads one value at a time, in the import's transaction.
func storedSetting(tx pgx.Tx, userID int) settingLookup {
	return func(ctx context.Context, id userstore.SettingIdentity) (json.RawMessage, bool, error) {
		current, err := getSettingValue(ctx, tx, userID, id)
		if err != nil || current == nil {
			return nil, false, err
		}
		return current.Value, true, nil
	}
}

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

func importTrackPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, doc preferencesDocument, res *userdata.Result) error {
	if err := importAudioPreferences(ctx, tx, target, doc.Audio, res); err != nil {
		return err
	}
	if err := importSubtitlePreferences(ctx, tx, target, doc.Subtitles, res); err != nil {
		return err
	}
	if err := importLibraryPreferences(ctx, tx, target, doc.LibraryPlayback, res); err != nil {
		return err
	}
	if err := importSeriesPreferences(ctx, tx, target, doc.SeriesPlayback, res); err != nil {
		return err
	}
	return importCollectionSorts(ctx, tx, target, doc.CollectionSort, res)
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

// localCollection is the sort's collection id here: a library's id is
// mapped, false when it has no library here.
func (c collectionSort) localCollection(target userdata.Target) (string, bool) {
	if c.Kind != scopeLibrary {
		return c.Collection, true
	}
	source, err := strconv.Atoi(c.Collection)
	if err != nil {
		return "", false
	}
	local, ok := target.MapLibrary(source)
	if !ok {
		return "", false
	}
	return strconv.Itoa(local), true
}

func importAudioPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []audioPreference, res *userdata.Result) error {
	for _, a := range entries {
		series, ok, err := resolveSeries(ctx, target, a.Series, res)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := upsertCounted(ctx, tx, res, `
			INSERT INTO user_audio_preferences (user_id, profile_id, series_id, audio_track_index, audio_language, audio_track_signature, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7)
			ON CONFLICT (user_id, profile_id, series_id) DO UPDATE SET audio_track_index = EXCLUDED.audio_track_index,
				audio_language = EXCLUDED.audio_language, audio_track_signature = EXCLUDED.audio_track_signature, updated_at = EXCLUDED.updated_at
			WHERE (user_audio_preferences.audio_track_index, user_audio_preferences.audio_language, user_audio_preferences.audio_track_signature)
				IS DISTINCT FROM (EXCLUDED.audio_track_index, EXCLUDED.audio_language, EXCLUDED.audio_track_signature)
			RETURNING (xmax = 0)`,
			target.UserID, target.ProfileID, series, a.TrackIndex, a.Language, trackSignature(a.Signature), a.UpdatedAt); err != nil {
			return fmt.Errorf("restoring an audio preference: %w", err)
		}
	}
	return nil
}

func importSubtitlePreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []subtitlePreference, res *userdata.Result) error {
	for _, s := range entries {
		series, ok, err := resolveSeries(ctx, target, s.Series, res)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := upsertCounted(ctx, tx, res, `
			INSERT INTO user_subtitle_preferences (user_id, profile_id, series_id, subtitle_language, subtitle_track_index, external_subtitle_path,
				subtitle_mode, show_forced_subtitles, subtitle_track_signature, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
			ON CONFLICT (user_id, profile_id, series_id) DO UPDATE SET subtitle_language = EXCLUDED.subtitle_language,
				subtitle_track_index = EXCLUDED.subtitle_track_index, external_subtitle_path = EXCLUDED.external_subtitle_path,
				subtitle_mode = EXCLUDED.subtitle_mode, show_forced_subtitles = EXCLUDED.show_forced_subtitles,
				subtitle_track_signature = EXCLUDED.subtitle_track_signature, updated_at = EXCLUDED.updated_at
			WHERE (user_subtitle_preferences.subtitle_language, user_subtitle_preferences.subtitle_track_index, user_subtitle_preferences.external_subtitle_path,
				user_subtitle_preferences.subtitle_mode, user_subtitle_preferences.show_forced_subtitles, user_subtitle_preferences.subtitle_track_signature)
				IS DISTINCT FROM (EXCLUDED.subtitle_language, EXCLUDED.subtitle_track_index, EXCLUDED.external_subtitle_path,
				EXCLUDED.subtitle_mode, EXCLUDED.show_forced_subtitles, EXCLUDED.subtitle_track_signature)
			RETURNING (xmax = 0)`,
			target.UserID, target.ProfileID, series, s.Language, s.TrackIndex, s.localPath(target), s.Mode, s.ShowForcedSubtitles, trackSignature(s.Signature), s.UpdatedAt); err != nil {
			return fmt.Errorf("restoring a subtitle preference: %w", err)
		}
	}
	return nil
}

func importLibraryPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []libraryPreference, res *userdata.Result) error {
	for _, l := range entries {
		library, ok := target.MapLibrary(l.Library)
		if !ok {
			res.LeftOut++
			continue
		}
		if err := upsertCounted(ctx, tx, res, `
			INSERT INTO user_library_playback_preferences (user_id, profile_id, library_id, audio_language, subtitle_language, subtitle_mode,
				show_forced_subtitles, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (user_id, profile_id, library_id) DO UPDATE SET audio_language = EXCLUDED.audio_language,
				subtitle_language = EXCLUDED.subtitle_language, subtitle_mode = EXCLUDED.subtitle_mode,
				show_forced_subtitles = EXCLUDED.show_forced_subtitles, updated_at = EXCLUDED.updated_at
			WHERE (user_library_playback_preferences.audio_language, user_library_playback_preferences.subtitle_language,
				user_library_playback_preferences.subtitle_mode, user_library_playback_preferences.show_forced_subtitles)
				IS DISTINCT FROM (EXCLUDED.audio_language, EXCLUDED.subtitle_language, EXCLUDED.subtitle_mode, EXCLUDED.show_forced_subtitles)
			RETURNING (xmax = 0)`,
			target.UserID, target.ProfileID, library, l.AudioLanguage, l.SubtitleLanguage, l.SubtitleMode, l.ShowForcedSubtitles, l.UpdatedAt); err != nil {
			return fmt.Errorf("restoring a library playback preference: %w", err)
		}
	}
	return nil
}

func importSeriesPreferences(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []seriesPreference, res *userdata.Result) error {
	for _, s := range entries {
		series, ok, err := resolveSeries(ctx, target, s.Series, res)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if err := upsertCounted(ctx, tx, res, `
			INSERT INTO user_series_playback_preferences (user_id, profile_id, series_id, resolution, hdr, codec_video, file_id, edition_key, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (user_id, profile_id, series_id) DO UPDATE SET resolution = EXCLUDED.resolution, hdr = EXCLUDED.hdr,
				codec_video = EXCLUDED.codec_video, file_id = EXCLUDED.file_id, edition_key = EXCLUDED.edition_key, updated_at = EXCLUDED.updated_at
			WHERE (user_series_playback_preferences.resolution, user_series_playback_preferences.hdr, user_series_playback_preferences.codec_video,
				user_series_playback_preferences.file_id, user_series_playback_preferences.edition_key)
				IS DISTINCT FROM (EXCLUDED.resolution, EXCLUDED.hdr, EXCLUDED.codec_video, EXCLUDED.file_id, EXCLUDED.edition_key)
			RETURNING (xmax = 0)`,
			target.UserID, target.ProfileID, series, s.Resolution, s.HDR, s.CodecVideo, s.localFileID(target), s.EditionKey, s.UpdatedAt); err != nil {
			return fmt.Errorf("restoring a series playback preference: %w", err)
		}
	}
	return nil
}

func importCollectionSorts(ctx context.Context, tx pgx.Tx, target userdata.Target, entries []collectionSort, res *userdata.Result) error {
	for _, c := range entries {
		collection, ok := c.localCollection(target)
		if !ok {
			res.LeftOut++
			continue
		}
		if err := upsertCounted(ctx, tx, res, `
			INSERT INTO user_collection_sort_preferences (user_id, profile_id, collection_kind, collection_id, sort_field, sort_order, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_id, profile_id, collection_kind, collection_id) DO UPDATE SET sort_field = EXCLUDED.sort_field,
				sort_order = EXCLUDED.sort_order, updated_at = EXCLUDED.updated_at
			WHERE (user_collection_sort_preferences.sort_field, user_collection_sort_preferences.sort_order)
				IS DISTINCT FROM (EXCLUDED.sort_field, EXCLUDED.sort_order)
			RETURNING (xmax = 0)`,
			target.UserID, target.ProfileID, c.Kind, collection, c.Field, c.Order, c.UpdatedAt); err != nil {
			return fmt.Errorf("restoring a collection sort preference: %w", err)
		}
	}
	return nil
}
