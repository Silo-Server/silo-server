package backup

import (
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/branding"
	"github.com/Silo-Server/silo-server/internal/catalog"
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
	{pageSignIn, "Sign-in"}, {"security", "Security"}, {pageLibrary, "Library & Metadata"}, {"search", "Search"},
	{"playback", "Playback"}, {"downloads", "Downloads"}, {pageRequests, "Requests"}, {"providers", "Providers"},
	{"ai", "AI Services"}, {pageEmail, "Email"}, {"notifications", "Notifications"}, {"compatibility", "Compatibility"},
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
	pageSignIn     = "sign-in"
	pageEmail      = "email"
	pageRequests   = "requests"
)

// settingRateLimitOn is the switch that throttles sign-in.
const settingRateLimitOn = "ratelimit.enabled"

type settingRule struct {
	page  string
	class SettingClass
}

// settingRulesByPage places each setting on the admin page that edits it,
// with how the review shows it: portable, connecting to an outside service,
// or server-specific. Whether a restore writes a setting at all is its
// kind (SettingKindOf, the one classification both restores read); a key's
// server-specific class here must match that kind, which
// TestEverySettingHasAPage checks with every other setting having a page.
// Exact keys win over prefixes, and a longer prefix over a shorter one.
var settingRulesByPage = []struct {
	page  string
	rules map[string]SettingClass
}{
	{"general", map[string]SettingClass{
		"server.identity_id":       SettingServerSpecific,
		"server.public_url":        SettingServerSpecific,
		"server.local_url":         SettingServerSpecific,
		"clientip.trusted_proxies": SettingPortable,
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
	{pageSignIn, map[string]SettingClass{
		settingJWTSecret: SettingServerSpecific,
		"auth.":          SettingPortable,
		// Who may sign in and how: restored only when no wider than here
		// (SettingKindStricter).
		"signup.":         SettingPortable,
		"password_reset.": SettingPortable,
	}},
	{"security", map[string]SettingClass{
		"ratelimit.":       SettingServerSpecific,
		settingRateLimitOn: SettingPortable,
		"ratelimit.auth.":  SettingPortable,
		"ratelimit.ip.":    SettingPortable,
		"media_servers.":   SettingPortable,
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
		catalog.SearchSettingMeilisearchURL:      SettingServerSpecific,
		catalog.SearchSettingMeilisearchAPIKey:   SettingServerSpecific,
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
	{pageRequests, map[string]SettingClass{
		"requests.": SettingExternalService,
	}},
	{"providers", map[string]SettingClass{
		"tmdb.api_key":    SettingExternalService,
		"mdblist.api_key": SettingExternalService,
		"introdb.api_key": SettingExternalService,
	}},
	{"ai", map[string]SettingClass{
		"ai.providers":           SettingServerSpecific,
		"ai.chatgpt.":            SettingServerSpecific,
		"ai.text_provider_id":    SettingServerSpecific,
		"ai.speech_provider_id":  SettingServerSpecific,
		"ai.auth_mode":           SettingServerSpecific,
		"ai.chat_model":          SettingServerSpecific,
		"ai.reasoning_effort":    SettingServerSpecific,
		"subtitle_ai.chat_model": SettingServerSpecific,
		"ai.api_key":             SettingExternalService,
		"ai.base_url":            SettingExternalService,
		"ai.asr_base_url":        SettingExternalService,
		"subtitle_ai.base_url":   SettingExternalService,
		settingASRKey:            SettingExternalService,
		settingSubtitleAIKey:     SettingExternalService,
		"ai.":                    SettingPortable,
		"subtitle_ai.":           SettingPortable,
		"metadata_ai.":           SettingPortable,
	}},
	{pageEmail, map[string]SettingClass{
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
		settingABSJWTSecret:               SettingServerSpecific,
		"jellyfin_compat.public_url":      SettingServerSpecific,
		"jellyfin_compat.web_install_dir": SettingServerSpecific,
		// Where the server downloads the Jellyfin web client it serves.
		"jellyfin_compat.web_source_url": SettingExternalService,
		"jellyfin_compat.":               SettingPortable,
		"audiobookshelf_compat.":         SettingPortable,
	}},
	{"recommendations", map[string]SettingClass{
		"recommendations.embedding_provider_id": SettingServerSpecific,
		// Written by the server, not an admin: the model and dimensions of
		// the stored vectors, and the access-policy generation they reflect.
		"recommendations.embedding_lock":           SettingServerSpecific,
		"recommendations.policy_marked_generation": SettingServerSpecific,
		settingEmbeddingToken:                      SettingExternalService,
		settingOpenAIEmbeddingKey:                  SettingExternalService,
		"recommendations.embedding_base_url":       SettingExternalService,
		"recommendations.":                         SettingPortable,
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

// RestorableSetting reports the page and class of a key a selective restore
// offers: one whose kind is not server-specific (SettingKindOf), on a page
// that edits it. Keys another section restores (branding assets) and keys
// with no page are not offered.
func RestorableSetting(key string) (settingRule, bool) {
	if kind, ok := SettingKindOf(key); !ok || kind == SettingKindServerSpecific {
		return settingRule{}, false
	}
	rule, ok := classifySetting(key)
	if !ok || (rule.class != SettingPortable && rule.class != SettingExternalService) {
		return settingRule{}, false
	}
	return rule, true
}

// SettingsPages lists the pages in display order.
func SettingsPages() []SettingsPage {
	return slices.Clone(settingsPages)
}
