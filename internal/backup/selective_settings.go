package backup

// Selective restore of settings: planning a settings page against this
// server and writing it through the admin settings write path.

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/secret"
)

func readStoredSettings(ctx context.Context, q queryer) (map[string]string, error) {
	rows, err := q.Query(ctx, `SELECT key, value FROM server_settings`)
	if err != nil {
		return nil, fmt.Errorf("reading settings: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

// settingPlan is one backup setting against this server: its plaintext when
// readable.
type settingPlan struct {
	preview PreviewSetting
	page    string
	value   string
	// live is the live value, decrypted ("" when there is none or it does
	// not decrypt), and expect marks a plan the write depends on: the live
	// value must be unchanged under the settings lock (settingsUnchanged),
	// where the settings store hands over decrypted values.
	live   string
	expect bool
}

// planSettings compares each setting this server's table offers. An
// encrypted value is read only with a matching key; otherwise it needs
// another key and is never written.
func (p *planner) planSettings(ctx context.Context) ([]settingPlan, error) {
	stored, err := readStoredSettings(ctx, p.q)
	if err != nil {
		return nil, err
	}
	cipher := p.s.opts.Cipher
	var out []settingPlan
	for _, e := range p.rs.Settings.Settings {
		rule, ok := RestorableSetting(e.Key)
		if !ok {
			continue
		}
		plan := settingPlan{page: rule.page, live: liveValue(stored, cipher, e.Key), preview: PreviewSetting{Key: e.Key, Class: rule.class, RestartRequired: config.RestartRequired(e.Key),
			Masked: e.Encrypted || catalog.SensitiveSettingKeys[e.Key]}}
		// Where an outside service connects, and with what, comes only from
		// a backup this server verified.
		if rule.class == SettingExternalService && !p.authenticated {
			plan.preview.Status = SettingUnverifiedBackup
			out = append(out, plan)
			continue
		}
		value := e.Value
		if e.Encrypted {
			if !p.keyMatches || cipher == nil {
				plan.preview.Status = ItemNeedsAnotherKey
				out = append(out, plan)
				continue
			}
			if value, err = cipher.Decrypt(e.Value, secret.SettingsAAD(e.Key)); err != nil {
				plan.preview.Status = ItemNeedsAnotherKey
				out = append(out, plan)
				continue
			}
		}
		plan.value = value
		current, have := stored[e.Key]
		if have && cipher != nil {
			if plain, err := cipher.DecryptIfEncrypted(current, secret.SettingsAAD(e.Key)); err == nil {
				current = plain
			} else {
				have = false
			}
		}
		live := current
		if !have {
			live = ""
		}
		switch _, wider := AccessSettingWider(e.Key, value, live); {
		case have && current == value:
			plan.preview.Status = ItemOnThisServer
		case wider:
			// A restore never widens access: the live value stays.
			plan.preview.Status = SettingKeptStricter
		default:
			plan.preview.Status = ItemDiffers
		}
		if !plan.preview.Masked {
			plan.preview.BackupValue, plan.preview.CurrentValue = value, current
		}
		out = append(out, plan)
	}
	return guardRoutes(out, func(key string) string { return liveValue(stored, cipher, key) }), nil
}

// liveValue is key's stored value, decrypted; "" when there is none, or
// when it does not decrypt here.
func liveValue(stored map[string]string, cipher *secret.Cipher, key string) string {
	value := stored[key]
	if cipher == nil || value == "" {
		return value
	}
	plain, err := cipher.DecryptIfEncrypted(value, secret.SettingsAAD(key))
	if err != nil {
		return ""
	}
	return plain
}

// keptStricterNote says that n settings keep the live, stricter value.
func keptStricterNote(n int) string {
	if n == 1 {
		return "1 setting would widen access, so this server keeps its stricter current value."
	}
	return fmt.Sprintf("%d settings would widen access, so this server keeps its stricter current values.", n)
}

// settingsPageItems groups settings by page in display order.
func settingsPageItems(plans []settingPlan) []PreviewItem {
	byPage := map[string][]settingPlan{}
	for _, plan := range plans {
		byPage[plan.page] = append(byPage[plan.page], plan)
	}
	var out []PreviewItem
	for _, page := range SettingsPages() {
		list := byPage[page.ID]
		if len(list) == 0 {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return list[i].preview.Key < list[j].preview.Key })
		item := PreviewItem{ID: page.ID, Kind: KindSettingsPage, Label: page.Title, Status: ItemOnThisServer, Reason: ReasonSame}
		var counts settingCounts
		for _, plan := range list {
			if plan.preview.Status == "" {
				continue // compared under the lock, not restored
			}
			item.Settings = append(item.Settings, plan.preview)
			counts.add(plan)
			if plan.writes() {
				item.RestartRequired = item.RestartRequired || plan.preview.RestartRequired
				item.ExternalService = item.ExternalService || plan.preview.Class == SettingExternalService
			}
		}
		notes := counts.notes()
		switch {
		case counts.writes() > 0:
			item.Status, item.Reason, item.Selectable = ItemDiffers, "", true
		case counts.needsKey > 0:
			item.Status, item.Reason = ItemNeedsAnotherKey, ReasonNeedsAnotherKey
			notes = append([]string{"The settings that differ were encrypted with another SECRET_KEY."}, notes...)
		case counts.stricter > 0:
			item.Status, item.Reason = ItemDiffers, ReasonKeptStricter
			notes = []string{"Every setting that differs would widen access, so this server keeps its stricter current values."}
		case counts.unverified > 0:
			item.Status, item.Reason = ItemDiffers, ReasonUnverifiedBackup
			notes = []string{msgUnverifiedServices}
		}
		item.Message = strings.Join(notes, " ")
		out = append(out, item)
	}
	return out
}

// ---- Applying ----

func (a *applier) applySettingsPage(ctx context.Context, tx pgx.Tx, id string) (applied, error) {
	page, known := settingsPageByID(id)
	item := ReportItem{Kind: KindSettingsPage, Label: id}
	if !known {
		return leftOut(item, ReasonNotInBackup, "There is no such settings page."), nil
	}
	item.Label = page.Title
	plans, err := a.s.newPlanner(a.rs, tx).planSettings(ctx)
	if err != nil {
		return applied{}, err
	}
	values, expected := map[string]string{}, map[string]string{}
	var counts settingCounts
	for _, p := range plans {
		if p.page != id {
			continue
		}
		counts.add(p)
		if p.writes() {
			values[p.preview.Key] = p.value
		}
		if p.expect {
			expected[p.preview.Key] = p.live
		}
	}
	item.Notes = append(item.Notes, counts.notes()...)
	if len(values) == 0 {
		switch {
		case counts.needsKey > 0:
			return leftOut(item, ReasonNeedsAnotherKey, "Every setting that differs was encrypted with another SECRET_KEY."), nil
		case counts.stricter > 0:
			return leftOut(item, ReasonKeptStricter, "Every setting that differs would widen access, so this server kept its stricter current values."), nil
		case counts.unverified > 0:
			return leftOut(item, ReasonUnverifiedBackup, msgUnverifiedServices), nil
		}
		return unchanged(item), nil
	}
	if a.s.targets.ApplySettings == nil {
		return applied{}, errors.New("settings restore is not available on this server")
	}
	restartKeys, after, err := a.s.targets.ApplySettings(ctx, tx, values, settingsUnchanged(values, expected))
	if err != nil {
		return applied{}, err
	}
	keys := slices.Sorted(maps.Keys(values))
	item.Outcome = OutcomeRestored
	item.Message = "Restored " + strings.Join(keys, ", ") + "."
	item.RestartKeys = restartKeys
	item.RestartRequired = len(restartKeys) > 0
	return applied{item: item, after: after}, nil
}

// errSettingsChanged stops a settings write that a setting it compared no
// longer allows, read again under the settings lock: an access setting an
// admin made stricter, or a service secret changed since the plan. The item
// starts again from its read.
var errSettingsChanged = errors.New("a setting this restore compared changed since it was read")

// settingsUnchanged checks, under the settings lock, what planSettings
// checked before it: no access setting of values grants more than the
// stored value (AccessSettingWider), and each expected row is as it
// was read.
func settingsUnchanged(values, expected map[string]string) func(stored map[string]string) error {
	return func(stored map[string]string) error {
		for key, value := range values {
			if _, wider := AccessSettingWider(key, value, stored[key]); wider {
				return fmt.Errorf("%w: %s", errSettingsChanged, key)
			}
		}
		for key, live := range expected {
			if stored[key] != live {
				return fmt.Errorf("%w: %s", errSettingsChanged, key)
			}
		}
		return nil
	}
}
