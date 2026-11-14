package backup

import (
	"fmt"
	"slices"
	"strings"
)

// guardRoutes applies settingRoutes to plans: when a restore changes a
// service's address, each of its secrets must come from the backup too.
// One the backup does not have, or that this server cannot read, is cleared
// here rather than sent to the new address, and the service is turned off.
// The decision rests on the route as it is here, so when a restore writes
// any of a route's settings, every one of them is expected unchanged under
// the settings lock (settingsUnchanged); a setting the backup does not
// carry is added as a plan with no status, which nothing shows or writes.
func guardRoutes(plans []settingPlan, live func(key string) string) []settingPlan {
	index := map[string]int{}
	for i, p := range plans {
		index[p.preview.Key] = i
	}
	plan := func(key string) *settingPlan {
		if i, ok := index[key]; ok {
			return &plans[i]
		}
		rule, _ := classifySetting(key)
		plans = append(plans, settingPlan{page: rule.page, live: live(key),
			preview: PreviewSetting{Key: key, Class: rule.class, Masked: true}})
		index[key] = len(plans) - 1
		return &plans[len(plans)-1]
	}
	for _, r := range settingRoutes {
		if i, ok := index[r.endpoint]; !ok || plans[i].preview.Status != ItemDiffers {
			continue
		}
		cleared := false
		for _, key := range r.secrets {
			_, inBackup := index[key]
			p := plan(key)
			if inBackup && (p.preview.Status == ItemDiffers || p.preview.Status == ItemOnThisServer) {
				continue
			}
			if live(key) != "" {
				p.preview.Status, p.value, cleared = SettingCleared, "", true
			}
		}
		if cleared && r.enabled != "" && live(r.enabled) != settingOff {
			p := plan(r.enabled)
			p.preview.Masked = false
			p.preview.Status, p.value = SettingCleared, settingOff
			p.preview.BackupValue, p.preview.CurrentValue = settingOff, live(r.enabled)
		}
	}
	for _, r := range settingRoutes {
		if !slices.ContainsFunc(r.keys(), func(key string) bool { i, ok := index[key]; return ok && plans[i].writes() }) {
			continue
		}
		for _, key := range r.keys() {
			plan(key).expect = true
		}
	}
	return plans
}

// writes reports whether a restore writes the plan's setting.
func (p settingPlan) writes() bool {
	return p.preview.Status == ItemDiffers || p.preview.Status == SettingCleared
}

// settingCounts tallies a settings page's plans by status.
type settingCounts struct {
	differs, cleared, needsKey, stricter, unverified int
	clearedKeys                                      []string
}

func (c *settingCounts) add(p settingPlan) {
	switch p.preview.Status {
	case ItemDiffers:
		c.differs++
	case SettingCleared:
		c.cleared++
		c.clearedKeys = append(c.clearedKeys, p.preview.Key)
	case ItemNeedsAnotherKey:
		c.needsKey++
	case SettingKeptStricter:
		c.stricter++
	case SettingUnverifiedBackup:
		c.unverified++
	}
}

// writes counts the settings a restore of the page writes.
func (c *settingCounts) writes() int { return c.differs + c.cleared }

// notes says what a restore of the page leaves out or clears, when it
// writes something.
func (c *settingCounts) notes() []string {
	if c.writes() == 0 {
		return nil
	}
	var notes []string
	if c.needsKey > 0 {
		notes = append(notes, fmt.Sprintf("%d encrypted settings on this page were encrypted with another SECRET_KEY and are left out.", c.needsKey))
	}
	if c.stricter > 0 {
		notes = append(notes, keptStricterNote(c.stricter))
	}
	if c.unverified > 0 {
		notes = append(notes, msgUnverifiedServices)
	}
	if c.cleared > 0 {
		notes = append(notes, "A service's address changes without its credentials in the backup, so restoring clears "+
			strings.Join(c.clearedKeys, ", ")+" here (and turns the service off) until they are entered again.")
	}
	return notes
}

// msgUnverifiedServices says why settings that connect to outside services
// are left out of an unverified backup's restore.
const msgUnverifiedServices = "Settings that connect to outside services are not restored from a backup this server cannot verify."
