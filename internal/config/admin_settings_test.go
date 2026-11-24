//nolint:goconst // Settings contract tests intentionally repeat literal keys in input and expected maps.
package config

import (
	"context"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/playback"
)

func TestEffectiveAdminSettingsEmptyCanonicalS3ValuesUseLegacyFallbacks(t *testing.T) {
	effective := EffectiveAdminSettings(map[string]string{
		"s3.public_path_style":      "",
		"s3.private_path_style":     "",
		"s3.operational_path_style": "false",
		"s3.public_token_ttl":       "",
		"s3.operational_token_ttl":  "3600",
	})

	if got := effective["s3.public_path_style"]; got != "false" {
		t.Fatalf("s3.public_path_style = %q, want legacy false", got)
	}
	if got := effective["s3.private_path_style"]; got != "false" {
		t.Fatalf("s3.private_path_style = %q, want legacy false", got)
	}
	if got := effective["s3.public_token_ttl"]; got != "3600" {
		t.Fatalf("s3.public_token_ttl = %q, want legacy 3600", got)
	}
}

type mapSettings map[string]string

func (m mapSettings) Get(_ context.Context, key string) (string, error) { return m[key], nil }

// TestNormalizeAdminSettingRejectsInvalidValues verifies invalid admin settings are rejected.
func TestNormalizeAdminSettingRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		key   string
		value string
	}{
		{key: "direct_connections.enabled", value: "maybe"},
		{key: "direct_connections.port_mapping", value: "maybe"},
		{key: "direct_connections.stun_servers", value: "example.com:0"},
		{key: "direct_connections.stun_servers", value: "example.com:65536"},
		{key: "direct_connections.stun_servers", value: "example.com"},
		{key: "direct_connections.stun_servers", value: "a:1,b:1,c:1,d:1,e:1,f:1,g:1,h:1,i:1"},
		{key: "direct_connections.stun_servers", value: "bad_host:3478"},
		{key: "direct_connections.stun_servers", value: "192.168.1.256:3478"},
		{key: "direct_connections.stun_servers", value: "10.0.0:3478"},
		{key: "database.max_connections", value: "0"},
		{key: "metadata.cache_images", value: "maybe"},
		{key: ChapterThumbnailSoftwareToneMapSettingKey, value: "maybe"},
		{key: PlaybackTranscodeHardwareToneMapSettingKey, value: "maybe"},
		{key: PlaybackTranscodeSoftwareToneMapSettingKey, value: "maybe"},
		{key: "auth.access_token_expiry", value: "forever"},
		{key: AuthPasswordMinLengthSettingKey, value: "7"},
		{key: AuthPasswordMinLengthSettingKey, value: "73"},
		{key: AuthPasswordRequireUppercaseSettingKey, value: "maybe"},
		{key: AuthPasswordRequireSpecialSettingKey, value: "maybe"},
		{key: "recommendations.embeddings_cron", value: "not a cron"},
		{key: "notifications.server_channels.batch_seconds", value: "119"},
		{key: "catalog.search.meilisearch.semantic_ratio", value: "1.2"},
		{key: "email.smtp_port", value: "70000"},
		{key: "theme.catalog_url", value: "http://raw.githubusercontent.com/Silo-Server/silo-themes/main/catalog.json"},
		{key: "theme.catalog_url", value: "https://example.com/catalog.json"},
		{key: "redis.url", value: "not-a-url"},
		{key: "playback.segment_retention_seconds", value: "119"},
		{key: playback.PausedSessionTimeoutSettingKey, value: "-1"},
		{key: playback.PausedSessionTimeoutSettingKey, value: "1441"},
		{key: playback.PausedSessionTimeoutSettingKey, value: "15m"},
		{key: "scanner.max_concurrent_libraries", value: "0"},
		{key: "scanner.max_concurrent_scoped", value: "-1"},
		{key: "scanner.empty_trash_after_scan", value: "sometimes"},
		{key: "scanner.realtime_monitoring", value: "sometimes"},
		{key: "scanner.file_removal_grace", value: "a while"},
		{key: "matcher.enable_tv_series_root_queue", value: "yes please"},
		{key: "matcher.enable_tv_series_group_queue", value: "yes please"},
		{key: "policy.editor_enabled", value: "maybe"},
		{key: "policy.eval_timeout_ms", value: "0"},
		{key: "subtitle_ai.live_asr_chunk_seconds", value: "0"},
		{key: "opslog.capture_level", value: "chatty"},
		{key: "s3.metadata_presign_expiry", value: "0s"},
		{key: "recommendations.embeddings_job_timeout", value: "soon"},
		{key: PlaybackRoutingRemuxExecutionSettingKey, value: "sometimes_worker"},
		{key: PlaybackRoutingRemuxEgressSettingKey, value: "worker"},
		{key: ServerLocalURLSettingKey, value: "192.168.1.20:8080"},
		{key: ServerLocalURLSettingKey, value: "ftp://192.168.1.20"},
		{key: ServerLocalURLSettingKey, value: "http://192.168.1.20:8080/silo"},
		{key: ServerLocalURLSettingKey, value: "http://192.168.1.20:8080/?x=1"},
		{key: ServerLocalURLSettingKey, value: "http://user@192.168.1.20:8080"},
		// An administrator default is validated against the value schema of the
		// setting it stands in for, so the resolver never has to discard one.
		{key: "defaults.ui.title_art", value: "off"},
		{key: "defaults.ui.title_art", value: `"false"`},
		{key: "defaults.ui.title_art", value: "1"},
		{key: "defaults.ui.title_art", value: "null"},
		{key: "autoscan.events_retention_days", value: "0"},
		{key: "autoscan.events_retention_days", value: "3651"},
		{key: DashboardHiddenAttentionSettingKey, value: "email"},
		{key: DashboardHiddenAttentionSettingKey, value: `{"email":true}`},
		{key: DashboardHiddenAttentionSettingKey, value: `[1]`},
		{key: DashboardHiddenAttentionSettingKey, value: `null`},
		{key: DashboardHiddenAttentionSettingKey, value: `["email",null]`},
		{key: DashboardHiddenAttentionSettingKey, value: `["` + strings.Repeat("x", 65) + `"]`},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			if _, err := NormalizeAdminSetting(tc.key, tc.value); err == nil {
				t.Fatalf("NormalizeAdminSetting(%q, %q) returned nil error", tc.key, tc.value)
			}
		})
	}
}

func TestNormalizeAdminSettingAcceptsDirectConnectionValues(t *testing.T) {
	tests := []struct{ key, value, want string }{
		{key: "direct_connections.enabled", value: " TRUE ", want: "true"},
		{key: "direct_connections.stun_servers", value: " STUN.Example.COM:3478,stun.example.com:3478,[2001:db8::1]:19302 ", want: "stun.example.com:3478,[2001:db8::1]:19302"},
		{key: "direct_connections.stun_servers", value: "", want: ""},
		// An IPv4-mapped literal becomes the plain IPv4 endpoint before
		// deduplication; native IPv6 literals and DNS names stay as they are.
		{key: "direct_connections.stun_servers", value: "[::ffff:192.0.2.53]:3478", want: "192.0.2.53:3478"},
		{key: "direct_connections.stun_servers", value: "[::FFFF:C000:235]:3478", want: "192.0.2.53:3478"},
		{key: "direct_connections.stun_servers", value: "[::ffff:192.0.2.53]:3478,192.0.2.53:3478", want: "192.0.2.53:3478"},
		{key: "direct_connections.stun_servers", value: "192.0.2.53:3478,[::ffff:192.0.2.53]:3478", want: "192.0.2.53:3478"},
		{key: "direct_connections.stun_servers", value: "[2001:db8::1]:3478,stun.example.com:3478", want: "[2001:db8::1]:3478,stun.example.com:3478"},
		// Zero turns the paused-session timeout off.
		{key: playback.PausedSessionTimeoutSettingKey, value: "0", want: "0"},
		{key: playback.PausedSessionTimeoutSettingKey, value: "1440", want: "1440"},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			got, err := NormalizeAdminSetting(tc.key, tc.value)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeAdminSetting(%q, %q) = %q, %v; want %q", tc.key, tc.value, got, err, tc.want)
			}
		})
	}
}

// The login credential travels to this origin, so only a bare https origin may
// be stored, and a stray "?" must not leak into the published stripe URL.
func TestAdminSettingWebMediaURLValidation(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string // empty with wantErr unset means the setting is off
		err   bool
	}{
		{name: "origin", value: "https://media.example.test", want: "https://media.example.test"},
		{name: "port", value: "https://media.example.test:8443", want: "https://media.example.test:8443"},
		{name: "trailing slash", value: "https://media.example.test/", want: "https://media.example.test"},
		{name: "empty query marker", value: "https://media.example.test?", want: "https://media.example.test"},
		{name: "empty is off", value: "", want: ""},
		{name: "plain http", value: "http://media.example.test", err: true},
		{name: "user information", value: "https://user@media.example.test", err: true},
		{name: "path", value: "https://media.example.test/path", err: true},
		{name: "query", value: "https://media.example.test?q=1", err: true},
		{name: "fragment", value: "https://media.example.test#f", err: true},
		{name: "port out of range", value: "https://media.example.test:65536", err: true},
		{name: "port without a hostname", value: "https://:443", err: true},
		{name: "hostname a browser rejects", value: "https://a<b.example", err: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeAdminSetting(WebMediaURLSettingKey, tc.value)
			if tc.err {
				if err == nil {
					t.Fatalf("NormalizeAdminSetting(%q) = %q, nil; want an error", tc.value, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeAdminSetting(%q) = %q, %v; want %q", tc.value, got, err, tc.want)
			}
		})
	}
}

func TestValidateAdminSettingsRejectsImpossibleHardPlaybackRoute(t *testing.T) {
	values := map[string]string{
		PlaybackRoutingRemuxExecutionSettingKey: "api_only",
		PlaybackRoutingRemuxEgressSettingKey:    "proxy_only",
	}
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("API-only remux with proxy-only egress was accepted")
	}
	values = map[string]string{
		PlaybackRoutingVideoTranscodeExecutionSettingKey: "api_only",
		PlaybackRoutingVideoTranscodeEgressSettingKey:    "proxy_only",
	}
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("API-only video transcode with proxy-only egress was accepted")
	}
}

func TestValidateAdminSettingsRequiresDurableRedisTransport(t *testing.T) {
	values := map[string]string{"ratelimit.backend": "redis"}
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("ValidateAdminSettings() accepted Redis backend without a durable transport")
	}

	if err := ValidateAdminSettingsWithCapabilities(values, AdminSettingsCapabilities{
		RedisBootstrapAvailable: true,
	}); err != nil {
		t.Fatalf("bootstrap Redis URL was rejected: %v", err)
	}

	values["redis.url"] = "redis://cache.example.invalid:6379"
	if err := ValidateAdminSettings(values); err != nil {
		t.Fatalf("persisted Redis URL was rejected: %v", err)
	}

	values["redis.url"] = "not-a-url"
	if err := ValidateAdminSettings(values); err == nil {
		t.Fatal("ValidateAdminSettings() accepted a malformed Redis URL")
	}
}
