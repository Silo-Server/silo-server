package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
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
		&req.SubtitleMode, req.ShowForcedSubtitles)
}

// planUpdateProfileSettingsSync plans the canonical writes for PUT
// /profiles/{id}. A nil field was not part of the request and must not touch
// the canonical row; the shipped clients send single-field deltas.
func planUpdateProfileSettingsSync(req updateProfileRequest) ([]profileSettingSync, error) {
	return planProfileSettingsSync(
		req.Language, req.SubtitleLanguage, req.PreferredMetadataLanguage,
		req.SubtitleMode, req.ShowForcedSubtitles)
}

func planProfileSettingsSync(
	audioLang, subtitleLang, metadataLang, subtitleMode *string,
	showForced *bool,
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
	if showForced != nil {
		out = append(out, profileSettingSync{
			key:   settingskeys.PlaybackShowForcedSubtitles,
			value: json.RawMessage(strconv.FormatBool(*showForced)),
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

// applyProfileSettingsSync writes the planned canonical rows for one profile
// and publishes a user_settings.changed event for every row that moved, so
// the target user's clients refresh exactly as they would after a
// /settings/values write.
func (h *ProfileHandler) applyProfileSettingsSync(
	ctx context.Context,
	store userstore.UserStore,
	userID int,
	profileID string,
	writes []profileSettingSync,
) error {
	for _, write := range writes {
		identity := userstore.SettingIdentity{
			Key:       write.key,
			Scope:     settingscontract.ScopeProfile,
			ProfileID: profileID,
		}
		if write.value == nil {
			removed, err := store.DeleteSettingValue(ctx, identity)
			if err != nil {
				return fmt.Errorf("clearing %s: %w", write.key, err)
			}
			if !removed {
				continue // nothing was stored, so nothing changed
			}
		} else if _, err := store.UpsertSettingValue(ctx, identity, write.value); err != nil {
			return fmt.Errorf("storing %s: %w", write.key, err)
		}
		publishUserSettingsEvent(ctx, h.EventsHub, userID, profileID,
			write.key, string(settingscontract.ScopeProfile))
	}
	return nil
}
