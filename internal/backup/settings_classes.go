package backup

import (
	"slices"
	"strings"
)

// SettingKind is how a restore treats one server setting. Every setting
// has exactly one, so a new one cannot reach a restore without someone
// deciding how; both restores read it:
//
//   - SettingKindRestored: restored as the backup has it.
//   - SettingKindServerSpecific: it describes this server (its identity,
//     storage, infrastructure, keys, and state it writes for itself). A
//     selective restore never writes it; a full restore brings back the
//     backup's server, whose storage and keys it checks on its own.
//   - SettingKindStricter: a security, access, delivery, or trust switch
//     (accessSettings). A restore keeps the stricter of the backup's and
//     the live value.
//   - SettingKindRoute: an outside service's address or the secret this
//     server sends there (settingRoutes), which restore together.
//
// TestEverySettingIsClassified fails on a setting the registries name that
// has no kind, and on a listed key or prefix that names no setting.
type SettingKind string

const (
	SettingKindRestored       SettingKind = "restored"
	SettingKindServerSpecific SettingKind = "server_specific"
	SettingKindStricter       SettingKind = "stricter"
	SettingKindRoute          SettingKind = "route"
)

// serverSpecificSettings are the server-specific keys and prefixes (ending
// in "." or "_"). The AI sign-in mode and models are this server's choice
// among what its own AI providers and ChatGPT account offer, which only
// they can validate.
var serverSpecificSettings = strings.Fields(`
	server.identity_id server.public_url server.local_url
	database. redis. userdb. storage. artwork. s3. backup. setup.
	auth.jwt_secret audiobooks.abs.jwt_secret
	ratelimit.
	catalog.search.provider catalog.search.meilisearch.url catalog.search.meilisearch.api_key
	catalog.search.meilisearch.index
	playback.ffmpeg_path playback.transcode_dir playback.hw_accel playback.hw_device
	download.artifact_dir
	ai.providers ai.chatgpt. ai.text_provider_id ai.speech_provider_id
	ai.auth_mode ai.chat_model ai.reasoning_effort subtitle_ai.chat_model
	notifications.push_relay_ notifications.web_push.vapid_keypair
	jellyfin_compat.public_url jellyfin_compat.web_install_dir
	recommendations.embedding_provider_id recommendations.embedding_lock recommendations.policy_marked_generation
	media_images. chapter_thumbnails. ui.dashboard.hidden_attention
`)

// restoredSettings are the keys and prefixes restored as the backup has
// them.
var restoredSettings = strings.Fields(`
	server.lan_discovery server.log_level server.log_quiet
	branding. theme. overlays. defaults. ui.admin_theme_vars
	opslog. policy. auth. password_reset. media_servers.
	access. catalog. metadata. scanner. matcher. subtitles.
	playback. markers. allow_4k_transcode enable_transcode_throttle transcode_throttle_seconds
	download. requests. tmdb. mdblist. introdb.
	ai. subtitle_ai. metadata_ai. email. discord. notifications.
	jellyfin_compat. audiobookshelf_compat. recommendations.
	watchsync. taskmanager. autoscan.
`)

// Reasons a switch setting is restored as the backup has it.
const (
	whyCapacity = "transcoding capacity"
	whyLocal    = "local processing"
	whyTuning   = "playback tuning"
)

// restoredFlagSettings are the switch settings (a boolean default) a
// restore brings back as the backup has them, each with why that is safe:
// none grants access, sends anything off the server, or reaches a
// destination. TestEverySwitchSettingIsDecided fails on a switch setting in
// neither this list nor accessSettings.
var restoredFlagSettings = map[string]string{
	"allow_4k_transcode":                                   whyCapacity,
	"catalog.scope_versions_to_library":                    "presentation",
	"catalog.search.meilisearch.binary_quantized":          "search index tuning",
	"download.local_transcode_fallback":                    "how a permitted download is prepared",
	"enable_transcode_throttle":                            whyCapacity,
	"markers.detect_credits":                               whyLocal,
	"markers.detect_intros":                                whyLocal,
	"markers.lazy_playback":                                whyLocal,
	"matcher.enable_tv_series_group_queue":                 "local matching",
	"matcher.enable_tv_series_root_queue":                  "local matching",
	"metadata.cache_images":                                "local caching",
	"notifications.discord.allow_per_episode":              "message granularity on a channel its own switch gates",
	"notifications.email.allow_per_episode":                "message granularity on a channel its own switch gates",
	"notifications.server_channels.mention_requesters":     "message content on channels their own switch gates",
	"overlays.enabled":                                     "presentation",
	"playback.allow_hevc_encoding":                         whyCapacity,
	"playback.bandwidth_probe_automatic":                   whyTuning,
	"playback.chapter_thumbnail_software_tone_map_enabled": whyLocal,
	"playback.copy_keyframe_playlist":                      whyTuning,
	"playback.prefer_lower_version_transcode":              whyTuning,
	"playback.transcode_enabled":                           "transcoding capacity: it changes how a permitted stream plays, not who may play",
	"playback.transcode_hardware_tone_map_enabled":         whyLocal,
	"playback.transcode_software_tone_map_enabled":         whyLocal,
	"policy.editor_enabled":                                "the admin policy editor; admins only",
	"scanner.empty_trash_after_scan":                       "catalog upkeep",
	"scanner.realtime_monitoring":                          whyLocal,
	"subtitles.auto_sync":                                  whyLocal,
}

// SettingKindOf is key's kind: a stricter or route setting by its table,
// else the exact key, else the longest matching prefix. ok is false for a
// key none names.
func SettingKindOf(key string) (kind SettingKind, ok bool) {
	if _, ok := accessSettings[key]; ok {
		return SettingKindStricter, true
	}
	if isRouteSetting(key) {
		return SettingKindRoute, true
	}
	best := ""
	for _, list := range []struct {
		kind SettingKind
		keys []string
	}{{SettingKindServerSpecific, serverSpecificSettings}, {SettingKindRestored, restoredSettings}} {
		for _, entry := range list.keys {
			switch {
			case entry == key:
				return list.kind, true
			case isSettingPrefix(entry) && strings.HasPrefix(key, entry) && len(entry) > len(best):
				best, kind = entry, list.kind
			}
		}
	}
	return kind, best != ""
}

// isSettingPrefix reports whether a classification entry is a prefix.
func isSettingPrefix(entry string) bool {
	return strings.HasSuffix(entry, ".") || strings.HasSuffix(entry, "_")
}

// Secret settings of the routes, named once for this package.
const (
	settingASRKey             = "ai.asr_api_key"
	settingSubtitleAIKey      = "subtitle_ai.api_key"
	settingEmbeddingToken     = "recommendations.embedding_auth_token"
	settingOpenAIEmbeddingKey = "recommendations.openai_api_key"
)

// settingRoute is an outside service's address with the secrets this server
// sends there. A restore never pairs a backup's address with this server's
// secret: the backup's address could be anyone's.
type settingRoute struct {
	endpoint string
	secrets  []string
	// enabled turns the service on; a restore that clears the secrets turns
	// it off until they are entered again.
	enabled string
	// transport is the setting that protects what is sent there.
	transport string
}

// settingRoutes are the settings that name where a service connects
// together with the secrets it sends there. The S3 and storage endpoints,
// the search engine, AI providers, and the push relay are server-specific;
// Discord, TMDB, MDBList, IntroDB, and the watch providers connect to fixed
// hosts; a plugin's configuration is restored whole or not at all.
var settingRoutes = []settingRoute{
	{endpoint: settingSMTPHost, secrets: []string{settingSMTPPassword}, enabled: settingEmailEnabled, transport: settingSMTPSecurity},
	{endpoint: settingAIBaseURL, secrets: []string{settingAIKey}},
	{endpoint: "ai.asr_base_url", secrets: []string{settingASRKey}},
	{endpoint: "subtitle_ai.base_url", secrets: []string{settingSubtitleAIKey}},
	{endpoint: "recommendations.embedding_base_url", secrets: []string{settingEmbeddingToken, settingOpenAIEmbeddingKey}},
}

// keys are every setting of the route.
func (r settingRoute) keys() []string {
	keys := append([]string{r.endpoint}, r.secrets...)
	for _, key := range []string{r.enabled, r.transport} {
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// isRouteSetting reports whether key is a route's address or secret; its
// switch and transport are stricter settings.
func isRouteSetting(key string) bool {
	return slices.ContainsFunc(settingRoutes, func(r settingRoute) bool {
		return r.endpoint == key || slices.Contains(r.secrets, key)
	})
}

// ServerSpecificSettings lists the server-specific keys and prefixes, and
// the selective restore says it never writes them.
func ServerSpecificSettings() []string {
	return slices.Sorted(slices.Values(serverSpecificSettings))
}
