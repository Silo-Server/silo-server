package backup

import (
	"errors"
	"slices"
	"testing"
)

// TestGuardRoutesNeverSendsALiveSecretElsewhere: a restore that changes
// where a service connects brings the service's secrets from the backup or
// clears them here, turning the service off, and expects every one of them
// unchanged under the settings lock.
func TestGuardRoutesNeverSendsALiveSecretElsewhere(t *testing.T) {
	const host, password, enabled = "email.smtp_host", "email.smtp_password", "email.enabled"
	live := map[string]string{host: "mail.example.com", password: "the live password", enabled: "true"}
	differs := func(key, value string) settingPlan {
		return settingPlan{page: pageEmail, value: value, live: live[key], preview: PreviewSetting{Key: key, Status: ItemDiffers}}
	}
	status := func(plans []settingPlan, key string) (settingPlan, bool) {
		i := slices.IndexFunc(plans, func(p settingPlan) bool { return p.preview.Key == key })
		if i < 0 {
			return settingPlan{}, false
		}
		return plans[i], true
	}
	for name, tc := range map[string]struct {
		plans   []settingPlan
		cleared bool
	}{
		"another host, no password in the backup": {plans: []settingPlan{differs(host, "smtp.attacker.example")}, cleared: true},
		"another host, a password this server cannot read": {plans: []settingPlan{differs(host, "smtp.attacker.example"),
			{page: pageEmail, preview: PreviewSetting{Key: password, Status: ItemNeedsAnotherKey}}}, cleared: true},
		"another host with its own password": {plans: []settingPlan{differs(host, "smtp.example.org"), differs(password, "the backup's password")}},
		"the same host":                      {plans: []settingPlan{{page: pageEmail, value: live[host], preview: PreviewSetting{Key: host, Status: ItemOnThisServer}}}},
	} {
		t.Run(name, func(t *testing.T) {
			plans := guardRoutes(slices.Clone(tc.plans), func(key string) string { return live[key] })
			p, ok := status(plans, password)
			if tc.cleared {
				if !ok || p.preview.Status != SettingCleared || p.value != "" || !p.expect {
					t.Fatalf("password plan = %+v, want cleared and expected unchanged", p)
				}
				if on, ok := status(plans, enabled); !ok || on.preview.Status != SettingCleared || on.value != "false" {
					t.Fatalf("email.enabled plan = %+v, want turned off", on)
				}
				return
			}
			if ok && p.preview.Status == SettingCleared {
				t.Fatalf("password cleared: %+v", p)
			}
			if on, ok := status(plans, enabled); ok && on.writes() {
				t.Fatalf("email turned off: %+v", on)
			}
		})
	}
}

// TestRoutesAreRecheckedUnderTheLock: a restore that writes any setting of
// a route, the same address with the backup's password included, expects
// the route's address, secrets, switch, and transport unchanged under the
// settings lock, and stops the write when an admin changed one meanwhile.
func TestRoutesAreRecheckedUnderTheLock(t *testing.T) {
	const host, password, enabled = "email.smtp_host", "email.smtp_password", "email.enabled"
	live := map[string]string{host: "mail.example.com", password: "the live password", enabled: "true", settingSMTPSecurity: "tls"}
	plans := guardRoutes([]settingPlan{
		{page: pageEmail, value: live[host], live: live[host], preview: PreviewSetting{Key: host, Status: ItemOnThisServer}},
		{page: pageEmail, value: "the backup's password", live: live[password], preview: PreviewSetting{Key: password, Status: ItemDiffers}},
	}, func(key string) string { return live[key] })
	values, expected := map[string]string{}, map[string]string{}
	for _, p := range plans {
		if p.writes() {
			values[p.preview.Key] = p.value
		}
		if p.expect {
			expected[p.preview.Key] = p.live
		}
	}
	for _, key := range []string{host, password, enabled, settingSMTPSecurity} {
		if expected[key] != live[key] {
			t.Fatalf("expected %v, want every route setting as read", expected)
		}
	}
	check := settingsUnchanged(values, expected)
	if err := check(live); err != nil {
		t.Fatalf("an unchanged route: %v", err)
	}
	moved := map[string]string{host: "mail.elsewhere.example", password: "a new password", enabled: "true", settingSMTPSecurity: "tls"}
	if err := check(moved); !errors.Is(err, errSettingsChanged) {
		t.Fatalf("a route changed under the restore = %v, want errSettingsChanged", err)
	}
}

// TestSettingRoutesAreOnOnePage: a settings page restores as a unit, so a
// route's address and secrets are restored together only when they are on
// one page, offered by the restore.
func TestSettingRoutesAreOnOnePage(t *testing.T) {
	for _, r := range settingRoutes {
		rule, ok := RestorableSetting(r.endpoint)
		if !ok || rule.class != SettingExternalService {
			t.Errorf("%s is not offered as an outside service's setting: %+v", r.endpoint, rule)
		}
		for _, key := range append(slices.Clone(r.secrets), r.enabled) {
			if key == "" {
				continue
			}
			if other, ok := RestorableSetting(key); !ok || other.page != rule.page {
				t.Errorf("%s is not on %s's page %q: %+v", key, r.endpoint, rule.page, other)
			}
		}
	}
}
