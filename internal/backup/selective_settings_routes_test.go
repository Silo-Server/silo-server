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

// TestEnvironmentKeepsWholeConnections: when this server's environment sets
// any setting of an outside-service connection, every setting of it that
// differs keeps this server's value, so a restore never pairs, say, the
// environment's plain-text mail transport with the backup's host and
// password. Settings outside the connection still restore.
func TestEnvironmentKeepsWholeConnections(t *testing.T) {
	const username, subtitle = "email.smtp_username", "branding.login_subtitle"
	// Email's switch is an access setting: turning it on is kept stricter.
	before := map[string]string{subtitle: ItemDiffers, settingSMTPHost: ItemDiffers, settingSMTPPassword: ItemDiffers,
		username: ItemDiffers, settingEmailEnabled: SettingKeptStricter}
	mail := []string{settingSMTPHost, settingSMTPPassword, username, settingEmailEnabled}
	for name, tc := range map[string]struct {
		env       string
		untouched []string
	}{
		"the mail transport from the environment": {env: settingSMTPSecurity, untouched: []string{subtitle}},
		"nothing from the environment":            {untouched: append([]string{subtitle}, mail...)},
	} {
		t.Run(name, func(t *testing.T) {
			var plans []settingPlan
			for key, status := range before {
				plans = append(plans, settingPlan{preview: PreviewSetting{Key: key, Status: status}})
			}
			keepEnvironmentBundles(plans, func(key string) bool { return key == tc.env })
			for _, p := range plans {
				want := SettingManagedByEnvironment
				if slices.Contains(tc.untouched, p.preview.Key) {
					want = before[p.preview.Key]
				}
				if p.preview.Status != want {
					t.Fatalf("%s = %s, want %s", p.preview.Key, p.preview.Status, want)
				}
			}
		})
	}
}
