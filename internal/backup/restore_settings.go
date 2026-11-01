package backup

import (
	"slices"
	"sort"
	"strings"

	"github.com/Silo-Server/silo-server/internal/branding"
)

// SettingClass says whether a restore offers a setting.
type SettingClass string

const (
	// SettingPortable is offered for restore.
	SettingPortable SettingClass = "portable"
	// SettingExternalService is offered and flagged in review as connecting
	// to an outside service.
	SettingExternalService SettingClass = "external_service"
	// SettingServerSpecific is never offered: it names this server, its
	// storage, its infrastructure, or its credentials.
	SettingServerSpecific SettingClass = "server_specific"
	// settingOtherSection is restored by another section (branding assets
	// travel with their blobs in collections and artwork), never as a
	// setting.
	settingOtherSection SettingClass = "other_section"
)

// SettingsPage is an admin page that edits settings, the unit settings are
// chosen in.
type SettingsPage struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// settingsPages names the pages of web/src/lib/adminSettingsSearch.ts and
// the admin pages that edit settings outside it. "other" holds settings no
// page edits.
var settingsPages = []SettingsPage{
	{"general", "General"}, {pageAppearance, "Appearance"}, {"storage", "Storage"}, {"logging", "Logging"},
	{"sign-in", "Sign-in"}, {"security", "Security"}, {pageLibrary, "Library & Metadata"}, {"search", "Search"},
	{"playback", "Playback"}, {"downloads", "Downloads"}, {"requests", "Requests"}, {"providers", "Providers"},
	{"ai", "AI Services"}, {"email", "Email"}, {"notifications", "Notifications"}, {"compatibility", "Compatibility"},
	{"recommendations", "Recommendations"}, {"nodes", "Nodes"}, {pageBackups, "Backups"}, {pageOther, "Other settings"},
}

func settingsPageByID(id string) (SettingsPage, bool) {
	for _, p := range settingsPages {
		if p.ID == id {
			return p, true
		}
	}
	return SettingsPage{}, false
}

// Pages the code names. pageOther holds settings no admin page edits.
const (
	pageOther      = "other"
	pageAppearance = "appearance"
	pageBackups    = "backups"
	pageLibrary    = "library"
)

type settingRule struct {
	page  string
	class SettingClass
}

// settingRulesByPage is the allowlist, grouped by the admin page that edits
// each setting: every restorable key or key prefix (ending in "." or "_")
// with its class. Exact keys win over prefixes, and a longer prefix over a
// shorter one. A key that matches nothing is never offered, and
// TestEverySettingIsClassified fails for any admin setting default left out,
// so a new setting must be classified here.
var settingRulesByPage = []struct {
	page  string
	rules map[string]SettingClass
}{
	{"general", map[string]SettingClass{
		"server.identity_id":       SettingServerSpecific,
		"server.public_url":        SettingServerSpecific,
		"server.local_url":         SettingServerSpecific,
		"clientip.trusted_proxies": SettingServerSpecific,
		"database.":                SettingServerSpecific,
		"redis.":                   SettingServerSpecific,
		"userdb.":                  SettingServerSpecific,
		"server.lan_discovery":     SettingPortable,
		branding.KeyServerName:     SettingPortable,
		"branding.login_subtitle":  SettingPortable,
	}},
	{pageAppearance, map[string]SettingClass{
		"theme.catalog_url":      SettingExternalService,
		"branding.accent_color":  SettingPortable,
		"branding.default_theme": SettingPortable,
		"overlays.":              SettingPortable,
		"defaults.":              SettingPortable,
		"ui.admin_theme_vars":    SettingPortable,
	}},
	{"storage", map[string]SettingClass{
		"storage.":              SettingServerSpecific,
		"artwork.":              SettingServerSpecific,
		"s3.":                   SettingServerSpecific,
		"metadata.cache_images": SettingPortable,
	}},
	{"logging", map[string]SettingClass{
		"server.log_level": SettingPortable,
		"server.log_quiet": SettingPortable,
		"opslog.":          SettingPortable,
		"policy.":          SettingPortable,
	}},
	{"sign-in", map[string]SettingClass{
		"auth.jwt_secret": SettingServerSpecific,
		"auth.":           SettingPortable,
		"signup.":         SettingPortable,
		"password_reset.": SettingPortable,
	}},
	{"security", map[string]SettingClass{
		"ratelimit.":     SettingServerSpecific,
		"media_servers.": SettingPortable,
	}},
	{pageLibrary, map[string]SettingClass{
		"access.":    SettingPortable,
		"catalog.":   SettingPortable,
		"metadata.":  SettingPortable,
		"scanner.":   SettingPortable,
		"matcher.":   SettingPortable,
		"subtitles.": SettingPortable,
	}},
	{"search", map[string]SettingClass{
		"catalog.search.provider":                SettingServerSpecific,
		"catalog.search.meilisearch.url":         SettingServerSpecific,
		"catalog.search.meilisearch.api_key":     SettingServerSpecific,
		"catalog.search.meilisearch.index":       SettingServerSpecific,
		"catalog.search.meilisearch.index_types": SettingPortable,
		"catalog.search.meilisearch.":            SettingPortable,
	}},
	{"playback", map[string]SettingClass{
		"playback.ffmpeg_path":       SettingServerSpecific,
		"playback.transcode_dir":     SettingServerSpecific,
		"playback.hw_accel":          SettingServerSpecific,
		"playback.hw_device":         SettingServerSpecific,
		"playback.":                  SettingPortable,
		"markers.":                   SettingPortable,
		"allow_4k_transcode":         SettingPortable,
		"enable_transcode_throttle":  SettingPortable,
		"transcode_throttle_seconds": SettingPortable,
	}},
	{"downloads", map[string]SettingClass{
		"download.artifact_dir": SettingServerSpecific,
		"download.":             SettingPortable,
	}},
	{"requests", map[string]SettingClass{
		"requests.": SettingExternalService,
	}},
	{"providers", map[string]SettingClass{
		"tmdb.api_key":    SettingExternalService,
		"mdblist.api_key": SettingExternalService,
		"introdb.api_key": SettingExternalService,
	}},
	{"ai", map[string]SettingClass{
		"ai.providers":          SettingServerSpecific,
		"ai.chatgpt.":           SettingServerSpecific,
		"ai.text_provider_id":   SettingServerSpecific,
		"ai.speech_provider_id": SettingServerSpecific,
		"ai.api_key":            SettingExternalService,
		"ai.asr_api_key":        SettingExternalService,
		"subtitle_ai.api_key":   SettingExternalService,
		"ai.":                   SettingPortable,
		"subtitle_ai.":          SettingPortable,
		"metadata_ai.":          SettingPortable,
	}},
	{"email", map[string]SettingClass{
		"email.": SettingExternalService,
	}},
	{"notifications", map[string]SettingClass{
		"notifications.push_relay_":            SettingServerSpecific,
		"notifications.web_push.vapid_keypair": SettingServerSpecific,
		"discord.":                             SettingExternalService,
		"notifications.discord_enabled":        SettingExternalService,
		"notifications.discord.":               SettingExternalService,
		"notifications.":                       SettingPortable,
	}},
	{"compatibility", map[string]SettingClass{
		"audiobooks.abs.jwt_secret":       SettingServerSpecific,
		"jellyfin_compat.public_url":      SettingServerSpecific,
		"jellyfin_compat.web_install_dir": SettingServerSpecific,
		"jellyfin_compat.server_id":       SettingServerSpecific,
		"jellyfin_compat.":                SettingPortable,
		"audiobookshelf_compat.":          SettingPortable,
	}},
	{"recommendations", map[string]SettingClass{
		"recommendations.embedding_provider_id": SettingServerSpecific,
		"recommendations.embedding_auth_token":  SettingExternalService,
		"recommendations.openai_api_key":        SettingExternalService,
		"recommendations.":                      SettingPortable,
	}},
	{"nodes", map[string]SettingClass{
		"playback.routing.": SettingPortable,
	}},
	{pageBackups, map[string]SettingClass{
		"backup.": SettingServerSpecific,
	}},
	{pageOther, map[string]SettingClass{
		"setup.":                        SettingServerSpecific,
		"media_images.":                 SettingServerSpecific,
		"chapter_thumbnails.":           SettingServerSpecific,
		"ui.dashboard.hidden_attention": SettingServerSpecific,
		"watchsync.simkl.":              SettingExternalService,
		"taskmanager.":                  SettingPortable,
		"autoscan.":                     SettingPortable,
	}},
}

// settingRules indexes settingRulesByPage by key or prefix.
var settingRules = func() map[string]settingRule {
	out := map[string]settingRule{}
	for _, group := range settingRulesByPage {
		for key, class := range group.rules {
			out[key] = settingRule{page: group.page, class: class}
		}
	}
	// Branding assets travel with their blobs in the collections section.
	for _, key := range branding.AssetSettingKeys() {
		out[key] = settingRule{page: pageAppearance, class: settingOtherSection}
	}
	return out
}()

// classifySetting finds a key's rule: the exact key, else the longest
// matching prefix.
func classifySetting(key string) (settingRule, bool) {
	if rule, ok := settingRules[key]; ok && !strings.HasSuffix(key, ".") && !strings.HasSuffix(key, "_") {
		return rule, true
	}
	best, found := "", false
	for prefix := range settingRules {
		if !strings.HasSuffix(prefix, ".") && !strings.HasSuffix(prefix, "_") {
			continue
		}
		if strings.HasPrefix(key, prefix) && len(prefix) > len(best) {
			best, found = prefix, true
		}
	}
	if !found {
		return settingRule{}, false
	}
	return settingRules[best], true
}

// RestorableSetting reports the page and class of a key a restore offers.
// Server-specific keys, keys another section restores, and unclassified keys
// are not offered.
func RestorableSetting(key string) (settingRule, bool) {
	rule, ok := classifySetting(key)
	if !ok || (rule.class != SettingPortable && rule.class != SettingExternalService) {
		return settingRule{}, false
	}
	return rule, true
}

// ServerSpecificSettings lists the keys and prefixes (ending in "." or "_")
// a restore never writes, sorted.
func ServerSpecificSettings() []string {
	var out []string
	for key, rule := range settingRules {
		if rule.class == SettingServerSpecific {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// SettingsPages lists the pages in display order.
func SettingsPages() []SettingsPage {
	return slices.Clone(settingsPages)
}
