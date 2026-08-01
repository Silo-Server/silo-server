package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/settingsresolve"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// The legacy profile endpoints are still the write path shipped clients use
// for the preference columns, but every server-side reader of those
// preferences now resolves them canonically from user_setting_values:
// access.Resolver and policy.ViewerResolver for catalog.metadata_language,
// playback start and catalog detail for the playback.* preferences. The
// settings backfill runs once, so a column write that never reaches the
// canonical store simply never takes effect — the stale backfilled row, or
// the contract default, wins forever.
//
// Until the clients move to /settings/values, every profile create or update
// therefore mirrors its preference fields into the profile-scope canonical
// rows the readers consult. The mapping is the live-write counterpart of
// settingsmigrate.planProfiles with one deliberate difference: the migration
// skips a column still holding its schema default because it cannot tell
// "never decided" from "chose the default", while a live request names the
// field explicitly, so its value — default or not — is a real choice and is
// stored.
//
// quality_preference is deliberately not mirrored: the server never resolves
// the legacy column (playback requests carry the quality preference
// per-request), and the two-axis quality picker already writes
// playback.preferred_quality and playback.max_bitrate_kbps through
// /settings/values directly.

// profileSettingSync is one canonical write implied by a legacy profile
// mutation. A nil value clears the profile-scope row so resolution falls
// back to the contract default, which is how the legacy empty string spells
// "no preference".
type profileSettingSync struct {
	key   string
	value json.RawMessage
}

// planCreateProfileSettingsSync plans the canonical writes for POST
// /profiles. Create requests carry plain strings, so an absent field arrives
// as "" and plans a no-op delete against the freshly created profile.
func planCreateProfileSettingsSync(req createProfileRequest) ([]profileSettingSync, error) {
	return planProfileSettingsSync(
		&req.Language, &req.SubtitleLanguage, &req.PreferredMetadataLanguage,
		&req.SubtitleMode, req.ShowForcedSubtitles,
		profileSkipFields{
			autoSkipIntro:       &req.AutoSkipIntro,
			autoSkipCredits:     &req.AutoSkipCredits,
			autoSkipRecap:       &req.AutoSkipRecap,
			autoPlayNextPreview: &req.AutoPlayNextPreview,
		})
}

// planUpdateProfileSettingsSync plans the canonical writes for PUT
// /profiles/{id}. A nil field was not part of the request and must not touch
// the canonical row; the shipped clients send single-field deltas.
func planUpdateProfileSettingsSync(req updateProfileRequest) ([]profileSettingSync, error) {
	return planProfileSettingsSync(
		req.Language, req.SubtitleLanguage, req.PreferredMetadataLanguage,
		req.SubtitleMode, req.ShowForcedSubtitles,
		profileSkipFields{
			autoSkipIntro:       req.AutoSkipIntro,
			autoSkipCredits:     req.AutoSkipCredits,
			autoSkipRecap:       req.AutoSkipRecap,
			autoPlayNextPreview: req.AutoPlayNextPreview,
		})
}

// profileSkipFields groups the four boolean playback toggles the profile DTO
// carries. They travel together because they behave identically: a nil field
// was not in the request, and a present one mirrors verbatim.
type profileSkipFields struct {
	autoSkipIntro       *bool
	autoSkipCredits     *bool
	autoSkipRecap       *bool
	autoPlayNextPreview *bool
}

func planProfileSettingsSync(
	audioLang, subtitleLang, metadataLang, subtitleMode *string,
	showForced *bool,
	skips profileSkipFields,
) ([]profileSettingSync, error) {
	var out []profileSettingSync
	var err error

	for _, field := range []struct {
		key string
		raw *string
	}{
		{settingskeys.PlaybackAudioLanguage, audioLang},
		{settingskeys.PlaybackSubtitleLanguage, subtitleLang},
		{settingskeys.CatalogMetadataLanguage, metadataLang},
		{settingskeys.PlaybackSubtitleMode, subtitleMode},
	} {
		if out, err = appendStringSync(out, field.key, field.raw); err != nil {
			return nil, err
		}
	}
	// The booleans have no "unset" spelling on the wire — the legacy columns
	// are NOT NULL — so a present field always writes an explicit value.
	for _, field := range []struct {
		key string
		raw *bool
	}{
		{settingskeys.PlaybackShowForcedSubtitles, showForced},
		{settingskeys.PlaybackAutoSkipIntro, skips.autoSkipIntro},
		{settingskeys.PlaybackAutoSkipCredits, skips.autoSkipCredits},
		{settingskeys.PlaybackAutoSkipRecap, skips.autoSkipRecap},
		{settingskeys.PlaybackAutoPlayNextPreview, skips.autoPlayNextPreview},
	} {
		if field.raw == nil {
			continue
		}
		out = append(out, profileSettingSync{
			key:   field.key,
			value: json.RawMessage(strconv.FormatBool(*field.raw)),
		})
	}
	return out, nil
}

// appendStringSync plans one string-valued column. The empty string is the
// legacy spelling of "unset" for both the language columns and subtitle_mode,
// so it clears the canonical row; anything else must normalize under the
// contract — the same check /settings/values applies — so nothing reaches
// storage that the canonical endpoint would refuse, and an invalid value is
// reported instead of silently never taking effect.
func appendStringSync(out []profileSettingSync, key string, raw *string) ([]profileSettingSync, error) {
	if raw == nil {
		return out, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if trimmed == "" {
		return append(out, profileSettingSync{key: key}), nil
	}

	encoded, err := json.Marshal(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	normalized, err := normalizeCanonicalSettingValue(key, encoded)
	if err != nil {
		return nil, err
	}
	return append(out, profileSettingSync{key: key, value: normalized}), nil
}

// normalizeCanonicalSettingValue runs a planned value through the same
// contract validation the canonical mutation endpoint uses.
func normalizeCanonicalSettingValue(key string, raw json.RawMessage) (json.RawMessage, error) {
	contract, err := settingscontract.Load()
	if err != nil {
		return nil, fmt.Errorf("loading the settings contract: %w", err)
	}
	def, ok := contract.Lookup(key)
	if !ok {
		return nil, fmt.Errorf("%s has no contract definition", key)
	}
	normalized, err := def.ValueSchema.NormalizeValue(raw, settingscontract.ObjectSchemas())
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return normalized, nil
}

// createProfileWithSettingsSync creates the profile, snapshots surviving
// account-wide legacy settings, and writes every canonical row in one store
// transaction. PostgreSQL's transaction wrapper also holds a per-user
// advisory lock shared with legacy account-setting fan-out, closing the
// cross-replica create/write race.
func (h *ProfileHandler) createProfileWithSettingsSync(
	ctx context.Context,
	store userstore.UserStore,
	userID int,
	profile userstore.Profile,
	writes []profileSettingSync,
) error {
	transactioner, ok := store.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		return fmt.Errorf("user store does not support atomic preference settings synchronization")
	}
	var changedKeys []string
	err := transactioner.WithPreferenceSettingsTransaction(ctx, func(tx userstore.PreferenceSettingsWriter) error {
		if err := tx.CreateProfile(ctx, profile); err != nil {
			return err
		}
		inherited, err := planInheritedLegacyUserSettings(ctx, tx)
		if err != nil {
			return err
		}
		changedKeys, err = writeCanonicalSettingsSync(ctx, tx, userstore.SettingIdentity{
			Scope: settingscontract.ScopeProfile, ProfileID: profile.ID,
		}, append(writes, inherited...))
		return err
	})
	if err != nil {
		return err
	}
	for _, key := range changedKeys {
		publishUserSettingsEvent(ctx, h.EventsHub, userID, profile.ID, key, string(settingscontract.ScopeProfile))
	}
	return nil
}

func (h *ProfileHandler) applyProfileUpdateSettingsSync(
	ctx context.Context,
	store userstore.UserStore,
	userID int,
	profileID string,
	input userstore.UpdateProfileInput,
	writes []profileSettingSync,
) error {
	return applyLegacyPreferenceSettingsSync(ctx, store, h.EventsHub, userID, userstore.SettingIdentity{
		Scope: settingscontract.ScopeProfile, ProfileID: profileID,
	}, writes, func(tx userstore.PreferenceSettingsWriter) error {
		return tx.UpdateProfile(ctx, profileID, input)
	})
}

// applyLegacyPreferenceSettingsSync is the live-write counterpart of the
// migration planner for legacy preference endpoints. The legacy mutation and
// every canonical row commit in one store transaction; events are deliberately
// published afterwards so subscribers can never observe uncommitted state.
func applyLegacyPreferenceSettingsSync(
	ctx context.Context,
	store userstore.UserStore,
	events *evt.Hub,
	userID int,
	base userstore.SettingIdentity,
	writes []profileSettingSync,
	legacyMutation func(userstore.PreferenceSettingsWriter) error,
) error {
	var changedKeys []string
	transactioner, ok := store.(userstore.PreferenceSettingsTransactioner)
	if !ok {
		return fmt.Errorf("user store does not support atomic preference settings synchronization")
	}
	err := transactioner.WithPreferenceSettingsTransaction(ctx, func(tx userstore.PreferenceSettingsWriter) error {
		if err := legacyMutation(tx); err != nil {
			return err
		}
		var err error
		changedKeys, err = writeCanonicalSettingsSync(ctx, tx, base, writes)
		return err
	})
	if err != nil {
		return err
	}
	for _, key := range changedKeys {
		publishUserSettingsEvent(ctx, events, userID, base.ProfileID, key, string(base.Scope))
	}
	return nil
}

func writeCanonicalSettingsSync(
	ctx context.Context,
	store userstore.PreferenceSettingsWriter,
	base userstore.SettingIdentity,
	writes []profileSettingSync,
) ([]string, error) {
	changedKeys := make([]string, 0, len(writes))
	for _, write := range writes {
		identity := base
		identity.Key = write.key
		if write.value == nil {
			removed, err := store.DeleteSettingValue(ctx, identity)
			if err != nil {
				return nil, fmt.Errorf("clearing %s: %w", write.key, err)
			}
			if !removed {
				continue // nothing was stored, so nothing changed
			}
		} else if _, err := store.UpsertSettingValue(ctx, identity, write.value); err != nil {
			return nil, fmt.Errorf("storing %s: %w", write.key, err)
		}
		changedKeys = append(changedKeys, write.key)
	}
	return changedKeys, nil
}

// --- Read side ---
//
// The profile DTO's preference fields are served from the same canonical rows
// the sync above writes, not from the legacy columns. Without this, a
// preference saved through PUT /settings/values lands in user_setting_values
// and is invisible in every profile DTO reader on every platform: the columns
// only move when a client goes through POST/PUT /profiles, and the cutover
// direction is that they stop being read rather than start being dual-written.
//
// The fallback is the contract default, never the column. A column holding a
// pre-cutover value that the one-time backfill already converted would
// otherwise resurface the moment its canonical row is unset — the "clear this
// preference" path would read as "restore the value from before the cutover".

// profilePreferences is the resolved form of the DTO's preference block. Each
// field is the effective value for one profile, already defaulted, so the
// serializer copies rather than decides.
type profilePreferences struct {
	AudioLanguage       string
	MetadataLanguage    string
	SubtitleLanguage    string
	SubtitleMode        string
	ShowForcedSubtitles bool
}

// profilePreferenceKeys are the canonical keys behind the DTO's preference
// fields, in DTO field order.
//
// quality_preference has no entry: the legacy column is a single compound
// value while the contract splits it across playback.preferred_quality and
// playback.max_bitrate_kbps, so there is no lossless read and the field stays
// column-backed. The auto_skip_* and auto_play_next_preview fields do sync on
// write, but this list drives the DTO's read block, whose shape the clients
// pin; they keep reading their columns, which the sync now keeps current.
var profilePreferenceKeys = []string{
	settingskeys.PlaybackAudioLanguage,
	settingskeys.CatalogMetadataLanguage,
	settingskeys.PlaybackSubtitleLanguage,
	settingskeys.PlaybackSubtitleMode,
	settingskeys.PlaybackShowForcedSubtitles,
}

// resolveProfilePreferences resolves the preference block for every listed
// profile in one store read.
//
// One read for the whole household rather than one per profile: GET /profiles
// serves several profiles and this is on its hot path. A resolution failure
// degrades to contract defaults rather than failing the request — these are
// presentation preferences, not an access boundary — but it is logged, because
// a store outage that silently hands every profile the defaults is otherwise
// indistinguishable from a household that never set anything.
func resolveProfilePreferences(
	ctx context.Context,
	store userstore.UserStore,
	profileIDs []string,
) map[string]profilePreferences {
	defaults := contractProfilePreferences()
	out := make(map[string]profilePreferences, len(profileIDs))
	for _, id := range profileIDs {
		out[id] = defaults
	}
	if store == nil || len(profileIDs) == 0 {
		return out
	}

	contract, err := settingscontract.Load()
	if err != nil {
		slog.WarnContext(ctx, "profile preferences degraded to contract defaults: loading settings contract failed",
			"component", "api", "error", err)
		return out
	}
	resolved, err := settingsresolve.New(contract).ResolveProfiles(
		ctx, store, profileIDs, profilePreferenceKeys, nil)
	if err != nil {
		slog.WarnContext(ctx, "profile preferences degraded to contract defaults: reading setting values failed",
			"component", "api", "profiles", len(profileIDs), "error", err)
		return out
	}

	for profileID, effective := range resolved {
		prefs := defaults
		for _, eff := range effective {
			applyProfilePreference(&prefs, eff.Key, eff.Value)
		}
		out[profileID] = prefs
	}
	return out
}

// contractProfilePreferences is the block every profile starts from: the
// contract's own defaults, decoded once per request.
//
// It is derived from the manifest rather than hard-coded so a default that
// changes there changes here too. A contract that fails to load leaves the Go
// zero values, which is the same "no preference" the empty string and false
// have always spelled in this DTO.
func contractProfilePreferences() profilePreferences {
	var prefs profilePreferences
	contract, err := settingscontract.Load()
	if err != nil {
		return prefs
	}
	for _, key := range profilePreferenceKeys {
		def, ok := contract.Lookup(key)
		if !ok {
			continue
		}
		applyProfilePreference(&prefs, key, def.DefaultValue)
	}
	return prefs
}

// applyProfilePreference decodes one canonical value into its DTO field.
//
// A value that fails to decode leaves the field as it was, so a single
// malformed row degrades one field to its default instead of the whole block.
// The language keys default to JSON null, which unmarshals into "" — the same
// spelling of "no preference" the legacy columns used.
func applyProfilePreference(prefs *profilePreferences, key string, value json.RawMessage) {
	switch key {
	case settingskeys.PlaybackAudioLanguage:
		decodeSettingString(value, &prefs.AudioLanguage)
	case settingskeys.CatalogMetadataLanguage:
		decodeSettingString(value, &prefs.MetadataLanguage)
	case settingskeys.PlaybackSubtitleLanguage:
		decodeSettingString(value, &prefs.SubtitleLanguage)
	case settingskeys.PlaybackSubtitleMode:
		decodeSettingString(value, &prefs.SubtitleMode)
	case settingskeys.PlaybackShowForcedSubtitles:
		var forced bool
		if json.Unmarshal(value, &forced) == nil {
			prefs.ShowForcedSubtitles = forced
		}
	}
}

func decodeSettingString(value json.RawMessage, dst *string) {
	var decoded string
	if json.Unmarshal(value, &decoded) == nil {
		*dst = strings.TrimSpace(decoded)
	}
}
