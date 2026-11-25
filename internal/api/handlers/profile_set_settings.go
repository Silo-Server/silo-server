package handlers

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/Silo-Server/silo-server/internal/settingscontract"
	"github.com/Silo-Server/silo-server/internal/settingskeys"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// profileSetSettings is what one item's settings ask of its profile: the
// normalized values to store (mirror halves included), the keys whose
// profile value is removed, and the known keys whose value this server
// refused and kept its own.
type profileSetSettings struct {
	write   map[string]json.RawMessage
	reset   []string
	skipped []string
	// applied is write as the item named it, for the applied record: the
	// keys the item sent, normalized, without mirror halves it did not send.
	applied map[string]json.RawMessage
}

// profileSetKey reports whether a profile set carries key: a remote setting
// this server knows that may be stored per profile and does not name
// something on one server.
func profileSetKey(contract *settingscontract.Manifest, key string) (*settingscontract.Definition, bool) {
	def, ok := contract.Lookup(key)
	if !ok || !def.IsRemote() || !def.AllowsScope(settingscontract.ScopeProfile) || def.ServerLocal {
		return nil, false
	}
	return def, true
}

// planProfileSetSettings decides what an item's settings write. An unknown
// key, a key not stored per profile and a server_local key are ignored. A
// known key whose value does not pass the contract is skipped: this server
// keeps its own value and the answer reports it. Of a mirrored pair sent
// together, the replacement (playback.intro_skip_mode) wins; either half
// written alone writes its mirror too, and resetting either half resets
// both. A write wins over the reset of its own mirror.
func planProfileSetSettings(contract *settingscontract.Manifest, item profileSetItem) (profileSetSettings, error) {
	plan := profileSetSettings{write: map[string]json.RawMessage{}, applied: map[string]json.RawMessage{}}
	keys := make([]string, 0, len(item.Settings))
	for key := range item.Settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		def, ok := profileSetKey(contract, key)
		if !ok {
			continue
		}
		if mirror, paired := settingscontract.MirrorKey(key); paired && key == settingskeys.PlaybackAutoSkipIntro {
			if _, both := item.Settings[mirror]; both {
				continue
			}
		}
		normalized, err := def.ValueSchema.NormalizeValue(item.Settings[key], settingscontract.ObjectSchemas())
		if err != nil {
			plan.skipped = append(plan.skipped, key)
			continue
		}
		plan.write[key] = normalized
		plan.applied[key] = normalized
		if string(normalized) == jsonNullLiteral {
			continue
		}
		mirror, ok, err := settingscontract.MirrorWrite(key, normalized)
		if err != nil {
			return plan, fmt.Errorf("%s: %w", key, err)
		}
		if ok {
			plan.write[mirror.Key] = mirror.Value
		}
	}
	for _, key := range item.SettingsReset {
		if _, ok := profileSetKey(contract, key); !ok {
			continue
		}
		plan.reset = appendResetKey(plan, plan.reset, key)
		if mirror, ok := settingscontract.MirrorKey(key); ok {
			plan.reset = appendResetKey(plan, plan.reset, mirror)
		}
	}
	return plan, nil
}

func appendResetKey(plan profileSetSettings, reset []string, key string) []string {
	if _, written := plan.write[key]; written || slices.Contains(reset, key) {
		return reset
	}
	return append(reset, key)
}

// profileColumnPreferences are the settings the profile object still serves
// from profile columns; a set that writes or resets one moves its column
// with it, so a client reading the profile sees the value the set stored.
var profileColumnPreferences = []struct {
	key    string
	column func(*userstore.Profile) bool
	set    func(*userstore.UpdateProfileInput, *bool)
}{
	{settingskeys.PlaybackAutoSkipIntro, func(p *userstore.Profile) bool { return p.AutoSkipIntro },
		func(u *userstore.UpdateProfileInput, v *bool) { u.AutoSkipIntro = v }},
	{settingskeys.PlaybackAutoSkipCredits, func(p *userstore.Profile) bool { return p.AutoSkipCredits },
		func(u *userstore.UpdateProfileInput, v *bool) { u.AutoSkipCredits = v }},
	{settingskeys.PlaybackAutoSkipRecap, func(p *userstore.Profile) bool { return p.AutoSkipRecap },
		func(u *userstore.UpdateProfileInput, v *bool) { u.AutoSkipRecap = v }},
	{settingskeys.PlaybackAutoPlayNextPreview, func(p *userstore.Profile) bool { return p.AutoPlayNextPreview },
		func(u *userstore.UpdateProfileInput, v *bool) { u.AutoPlayNextPreview = v }},
}

// applyColumnPreferences adds to input each column-backed preference the
// plan moves to a value its column does not hold: the written value, or the
// contract default for a reset.
func applyColumnPreferences(contract *settingscontract.Manifest, plan profileSetSettings, row *userstore.Profile, input *userstore.UpdateProfileInput) {
	for _, pref := range profileColumnPreferences {
		value, written := plan.write[pref.key]
		if !written {
			if !slices.Contains(plan.reset, pref.key) {
				continue
			}
			def, ok := contract.Lookup(pref.key)
			if !ok {
				continue
			}
			value = def.DefaultValue
		}
		var enabled bool
		if err := json.Unmarshal(value, &enabled); err != nil {
			continue
		}
		if pref.column(row) != enabled {
			pref.set(input, &enabled)
		}
	}
}

// profileSetHeldSettings is a profile's stored profile-scope settings as
// the set reports them: keys a set carries only.
func profileSetHeldSettings(contract *settingscontract.Manifest, stored map[string]json.RawMessage) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for key, value := range stored {
		if _, ok := profileSetKey(contract, key); ok {
			out[key] = value
		}
	}
	return out
}
