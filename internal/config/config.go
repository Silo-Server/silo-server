package config

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Listen    string `yaml:"listen"`
	Mode      string `yaml:"mode"`
	LogLevel  string `yaml:"log_level"`
	LogFormat string `yaml:"log_format"`
	LogQuiet  string `yaml:"log_quiet"`
}

// DatabaseConfig holds the primary PostgreSQL connection settings.
type DatabaseConfig struct {
	URL            string `yaml:"url"`
	MaxConnections int    `yaml:"max_connections"`
}

// S3BucketSettings holds the common configuration needed to access a bucket.
type S3BucketSettings struct {
	Endpoint  string `yaml:"-"`
	Region    string `yaml:"-"`
	PathStyle bool   `yaml:"-"`
	Bucket    string `yaml:"-"`
	KeyPrefix string `yaml:"-"`
	AccessKey string `yaml:"-"`
	SecretKey string `yaml:"-"`
}

// S3PublicAssetsSettings holds client-facing asset storage configuration.
// The bucket may still be private when URLAuth is set to presigned.
type S3PublicAssetsSettings struct {
	S3BucketSettings
	ReadEndpoint string `yaml:"-"` // custom domain / public read endpoint for public or tokenized reads
	URLAuth      string `yaml:"-"` // "presigned" (default), "public", or "cloudflare_token"
	TokenSecret  string `yaml:"-"` // HMAC secret for cloudflare_token auth
	TokenParam   string `yaml:"-"` // query param name (default: "verify")
	TokenTTL     int    `yaml:"-"` // token lifetime in seconds (default: 10800)
}

// S3Config holds S3-compatible object storage settings.
type S3Config struct {
	Public                S3PublicAssetsSettings `yaml:"-"`
	Private               S3BucketSettings       `yaml:"-"`
	UserDB                S3BucketSettings       `yaml:"-"`
	MetadataPresignExpiry time.Duration          `yaml:"-"`
}

// s3ConfigRaw is the raw YAML representation with duration strings.
type s3ConfigRaw struct {
	// Public assets
	PublicEndpoint        string `yaml:"public_endpoint"`
	PublicReadEndpoint    string `yaml:"public_read_endpoint"`
	PublicRegion          string `yaml:"public_region"`
	PublicPathStyle       bool   `yaml:"public_path_style"`
	PublicBucket          string `yaml:"public_bucket"`
	PublicKeyPrefix       string `yaml:"public_key_prefix"`
	PublicAccessKey       string `yaml:"public_access_key"`
	PublicSecretKey       string `yaml:"public_secret_key"`
	PublicURLAuth         string `yaml:"public_url_auth"`
	PublicTokenSecret     string `yaml:"public_token_secret"`
	PublicTokenParam      string `yaml:"public_token_param"`
	PublicTokenTTL        int    `yaml:"public_token_ttl"`
	MetadataPresignExpiry string `yaml:"metadata_presign_expiry"`

	// Private internal
	PrivateEndpoint  string `yaml:"private_endpoint"`
	PrivateRegion    string `yaml:"private_region"`
	PrivatePathStyle bool   `yaml:"private_path_style"`
	PrivateBucket    string `yaml:"private_bucket"`
	PrivateKeyPrefix string `yaml:"private_key_prefix"`
	PrivateAccessKey string `yaml:"private_access_key"`
	PrivateSecretKey string `yaml:"private_secret_key"`

	// Legacy operational aliases retained for YAML import compatibility.
	OperationalEndpoint       string `yaml:"operational_endpoint"`
	OperationalPublicEndpoint string `yaml:"operational_public_endpoint"`
	OperationalRegion         string `yaml:"operational_region"`
	OperationalPathStyle      bool   `yaml:"operational_path_style"`
	OperationalBucket         string `yaml:"operational_bucket"`
	OperationalKeyPrefix      string `yaml:"operational_key_prefix"`
	OperationalAccessKey      string `yaml:"operational_access_key"`
	OperationalSecretKey      string `yaml:"operational_secret_key"`
	OperationalURLAuth        string `yaml:"operational_url_auth"`
	OperationalTokenSecret    string `yaml:"operational_token_secret"`
	OperationalTokenParam     string `yaml:"operational_token_param"`
	OperationalTokenTTL       int    `yaml:"operational_token_ttl"`

	// User DB
	UserDBEndpoint  string `yaml:"user_db_endpoint"`
	UserDBRegion    string `yaml:"user_db_region"`
	UserDBPathStyle bool   `yaml:"user_db_path_style"`
	UserDBBucket    string `yaml:"user_db_bucket"`
	UserDBKeyPrefix string `yaml:"user_db_key_prefix"`
	UserDBAccessKey string `yaml:"user_db_access_key"`
	UserDBSecretKey string `yaml:"user_db_secret_key"`
}

// UserDBConfig holds per-user database pool settings.
type UserDBConfig struct {
	Backend           string        `yaml:"backend"` // "postgres" (default) or "sqlite"
	PoolMaxOpen       int           `yaml:"pool_max_open"`
	IdleTimeout       time.Duration `yaml:"-"`
	LitestreamSync    time.Duration `yaml:"-"`
	StaleGraceSeconds int           `yaml:"stale_grace_seconds"`
}

// userDBConfigRaw is the raw YAML representation with duration strings.
type userDBConfigRaw struct {
	Backend           string `yaml:"backend"`
	PoolMaxOpen       int    `yaml:"pool_max_open"`
	IdleTimeout       string `yaml:"idle_timeout"`
	LitestreamSync    string `yaml:"litestream_sync"`
	StaleGraceSeconds int    `yaml:"stale_grace_seconds"`
}

// ScannerConfig holds media scanner settings.
type ScannerConfig struct {
	FileRemovalGrace       time.Duration `yaml:"-"`
	Workers                int           `yaml:"workers"`
	MaxConcurrentLibraries int           `yaml:"max_concurrent_libraries"`
	MaxConcurrentScoped    int           `yaml:"max_concurrent_scoped"`
	EmptyTrashAfterScan    bool          `yaml:"-"`
}

// scannerConfigRaw is the raw YAML representation with duration strings.
type scannerConfigRaw struct {
	FileRemovalGrace       string `yaml:"file_removal_grace"`
	Workers                int    `yaml:"workers"`
	MaxConcurrentLibraries int    `yaml:"max_concurrent_libraries"`
	MaxConcurrentScoped    int    `yaml:"max_concurrent_scoped"`
	EmptyTrashAfterScan    bool   `yaml:"empty_trash_after_scan"`
}

// MatcherConfig holds metadata matching settings.
type MatcherConfig struct {
	Workers                        int  `yaml:"workers"`
	BatchSize                      int  `yaml:"batch_size"`
	EnableTVSeriesRootQueue        bool `yaml:"enable_tv_series_root_queue"`
	EnableTVSeriesGroupQueueLegacy bool `yaml:"enable_tv_series_group_queue"`
}

func (c MatcherConfig) TVSeriesRootQueueEnabled() bool {
	return c.EnableTVSeriesRootQueue || c.EnableTVSeriesGroupQueueLegacy
}

// PlaybackConfig holds transcoding and playback settings.
type PlaybackConfig struct {
	FFmpegPath                   string `yaml:"ffmpeg_path"`
	TranscodeDir                 string `yaml:"transcode_dir"`
	HWAccel                      string `yaml:"hw_accel"`
	HWDevice                     string `yaml:"hw_device"`
	ChapterThumbnailWorkers      int    `yaml:"chapter_thumbnail_workers"`
	ChapterThumbnailExecution    string `yaml:"chapter_thumbnail_execution"`
	ChapterThumbnailNodeCapacity int    `yaml:"chapter_thumbnail_node_capacity"`
	TranscodeEnabled             bool   `yaml:"transcode_enabled"`
	AllowHEVCEncoding            bool   `yaml:"allow_hevc_encoding"`
	TranscodeAheadSegments       int    `yaml:"transcode_ahead_segments"`
	SegmentDuration              int    `yaml:"segment_duration"`
}

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	URL               string   `yaml:"url"`
	SentinelMaster    string   `yaml:"sentinel_master"`
	SentinelAddresses []string `yaml:"sentinel_addresses"`
	SentinelPassword  string   `yaml:"sentinel_password"`
}

// RateLimitConfig holds rate limiting infrastructure settings.
type RateLimitConfig struct {
	Enabled bool   `yaml:"enabled"`
	Backend string `yaml:"backend"` // "memory" or "redis"
}

// AuthConfig holds authentication and token settings.
type AuthConfig struct {
	JWTSecret          string        `yaml:"jwt_secret"`
	AccessTokenExpiry  time.Duration `yaml:"-"`
	RefreshTokenExpiry time.Duration `yaml:"-"`
}

// authConfigRaw is the raw YAML representation with duration strings.
type authConfigRaw struct {
	JWTSecret          string `yaml:"jwt_secret"`
	AccessTokenExpiry  string `yaml:"access_token_expiry"`
	RefreshTokenExpiry string `yaml:"refresh_token_expiry"`
}

// JellyfinCompatConfig holds compatibility proxy settings.
type JellyfinCompatConfig struct {
	Listen                string        `yaml:"listen"`
	PublicURL             string        `yaml:"public_url"`
	EmulatedServerVersion string        `yaml:"emulated_server_version"`
	ServerID              string        `yaml:"server_id"`
	ServerName            string        `yaml:"server_name"`
	WebVersion            string        `yaml:"web_version"`
	WebDir                string        `yaml:"web_dir"`
	SessionTTL            time.Duration `yaml:"-"`
	PlaybackSessionTTL    time.Duration `yaml:"-"`
}

// jellyfinCompatConfigRaw is the raw YAML representation with duration strings.
type jellyfinCompatConfigRaw struct {
	Listen                string `yaml:"listen"`
	PublicURL             string `yaml:"public_url"`
	EmulatedServerVersion string `yaml:"emulated_server_version"`
	ServerID              string `yaml:"server_id"`
	ServerName            string `yaml:"server_name"`
	WebVersion            string `yaml:"web_version"`
	WebDir                string `yaml:"web_dir"`
	SessionTTL            string `yaml:"session_ttl"`
	PlaybackSessionTTL    string `yaml:"playback_session_ttl"`
}

// RecommendationsConfig holds AI recommendation engine settings.
type RecommendationsConfig struct {
	Enabled                bool    `yaml:"-"`
	EmbeddingBaseURL       string  `yaml:"-"`
	EmbeddingModel         string  `yaml:"-"`
	EmbeddingAuthToken     string  `yaml:"-"`
	EmbeddingsCron         string  `yaml:"-"`
	TasteProfilesCron      string  `yaml:"-"`
	RecommendationsCron    string  `yaml:"-"`
	TasteDecayHalfLifeDays float64 `yaml:"-"`
	DiversityLambda        float64 `yaml:"-"`
	CowatchCron            string  `yaml:"-"`
}

// SubtitleAIConfig holds settings for on-demand AI subtitle translation (and,
// in a follow-up, Whisper ASR generation) via a single OpenAI-compatible
// endpoint — the operator can point it at OpenAI, Groq, a local Ollama server,
// etc. Mirrors the recommendations embedding client's configuration style.
type SubtitleAIConfig struct {
	Enabled           bool   `yaml:"-"`
	BaseURL           string `yaml:"-"`
	APIKey            string `yaml:"-"`
	ChatModel         string `yaml:"-"`
	MaxConcurrentJobs int    `yaml:"-"`
	BatchSize         int    `yaml:"-"`
	ContextNeighbors  int    `yaml:"-"`
}

// DownloadConfig holds server-wide download policy settings.
type DownloadConfig struct {
	Enabled              bool          `yaml:"-"`
	ServerBandwidthBPS   int64         `yaml:"-"` // server-wide bytes/sec cap (0 = unlimited)
	UserBandwidthBPS     int64         `yaml:"-"` // per-user bytes/sec across all their downloads (0 = unlimited)
	MaxConcurrentPerUser int           `yaml:"-"` // max simultaneous downloads per user (0 = unlimited)
	MaxPerPeriod         int           `yaml:"-"` // max downloads per user per period (0 = unlimited)
	PeriodDuration       time.Duration `yaml:"-"` // rolling window for MaxPerPeriod
}

// MetadataConfig holds metadata pipeline settings.
type MetadataConfig struct {
	CacheImages bool `yaml:"-"`
}

// Config is the top-level configuration for Silo.
type Config struct {
	Server          ServerConfig          `yaml:"server"`
	Database        DatabaseConfig        `yaml:"database"`
	S3              S3Config              `yaml:"-"`
	UserDB          UserDBConfig          `yaml:"-"`
	Scanner         ScannerConfig         `yaml:"-"`
	Matcher         MatcherConfig         `yaml:"matcher"`
	Metadata        MetadataConfig        `yaml:"-"`
	Playback        PlaybackConfig        `yaml:"playback"`
	Redis           RedisConfig           `yaml:"redis"`
	RateLimit       RateLimitConfig       `yaml:"rate_limiting"`
	Auth            AuthConfig            `yaml:"-"`
	JellyfinCompat  JellyfinCompatConfig  `yaml:"-"`
	Recommendations RecommendationsConfig `yaml:"-"`
	SubtitleAI      SubtitleAIConfig      `yaml:"-"`
	Download        DownloadConfig        `yaml:"-"`
	TMDBAPIKey      string                `yaml:"-"`
	MDBListAPIKey   string                `yaml:"-"`
}

// configRaw is used for initial YAML unmarshaling with string durations.
type configRaw struct {
	Server         ServerConfig            `yaml:"server"`
	Database       DatabaseConfig          `yaml:"database"`
	S3             s3ConfigRaw             `yaml:"s3"`
	UserDB         userDBConfigRaw         `yaml:"user_db"`
	Scanner        scannerConfigRaw        `yaml:"scanner"`
	Matcher        MatcherConfig           `yaml:"matcher"`
	Playback       PlaybackConfig          `yaml:"playback"`
	Redis          RedisConfig             `yaml:"redis"`
	RateLimit      RateLimitConfig         `yaml:"rate_limiting"`
	Auth           authConfigRaw           `yaml:"auth"`
	JellyfinCompat jellyfinCompatConfigRaw `yaml:"jellyfin_compat"`
}

var validModes = map[string]bool{
	"integrated": true,
	"api":        true,
	"proxy":      true,
	"transcode":  true,
	"frontend":   true,
}

// dayRegexp matches duration strings with "d" suffix (e.g., "30d").
var dayRegexp = regexp.MustCompile(`^(\d+)d$`)

var defaultJellyfinCompatServerID = uuid.NewSHA1(
	uuid.NameSpaceURL,
	[]byte("https://silo.local/jellycompat"),
).String()

const DefaultJellyfinWebVersion = "10.11.6"
const DefaultBundledJellyfinWebDir = "/srv/jellyfin-web/current"

// parseDuration parses a duration string that supports Go's time.ParseDuration
// formats plus a custom "Nd" format for days.
func parseDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration string")
	}

	// Check for day format first (e.g., "30d")
	if matches := dayRegexp.FindStringSubmatch(s); matches != nil {
		days, err := strconv.Atoi(matches[1])
		if err != nil {
			return 0, fmt.Errorf("invalid day duration %q: %w", s, err)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}

	// Fall back to standard Go duration parsing
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return d, nil
}

// setDefaults returns a configRaw with all default values populated.
func setDefaults() *configRaw {
	return &configRaw{
		Server: ServerConfig{
			Listen:    ":8080",
			Mode:      "integrated",
			LogLevel:  "info",
			LogFormat: "text",
		},
		Database: DatabaseConfig{
			MaxConnections: 20,
		},
		S3: s3ConfigRaw{
			PublicPathStyle:       true,
			PrivatePathStyle:      true,
			OperationalPathStyle:  true,
			UserDBPathStyle:       true,
			MetadataPresignExpiry: "4h",
		},
		UserDB: userDBConfigRaw{
			Backend:           "postgres",
			PoolMaxOpen:       500,
			IdleTimeout:       "12h",
			LitestreamSync:    "1s",
			StaleGraceSeconds: 120,
		},
		Scanner: scannerConfigRaw{
			FileRemovalGrace:       "24h",
			Workers:                8,
			MaxConcurrentLibraries: 1,
			MaxConcurrentScoped:    2,
		},
		Matcher: MatcherConfig{
			Workers:                 8,
			BatchSize:               500,
			EnableTVSeriesRootQueue: true,
		},
		Playback: PlaybackConfig{
			FFmpegPath:                   "/usr/lib/jellyfin-ffmpeg/ffmpeg",
			TranscodeDir:                 "/tmp/silo-transcode",
			HWAccel:                      "auto",
			ChapterThumbnailWorkers:      1,
			ChapterThumbnailExecution:    "local",
			ChapterThumbnailNodeCapacity: 1,
			TranscodeEnabled:             true,
			AllowHEVCEncoding:            false,
			TranscodeAheadSegments:       30,
			SegmentDuration:              6,
		},
		RateLimit: RateLimitConfig{
			Enabled: true,
			Backend: "memory",
		},
		Auth: authConfigRaw{
			AccessTokenExpiry:  "8h",
			RefreshTokenExpiry: "30d",
		},
		JellyfinCompat: jellyfinCompatConfigRaw{
			Listen:                ":8097",
			PublicURL:             "http://127.0.0.1:8097",
			EmulatedServerVersion: "10.12.0",
			ServerID:              defaultJellyfinCompatServerID,
			ServerName:            "Silo",
			WebVersion:            DefaultJellyfinWebVersion,
			WebDir:                DefaultBundledJellyfinWebDir,
			SessionTTL:            "87600h",
			PlaybackSessionTTL:    "6h",
		},
	}
}

// Validate checks that all required fields are set and values are valid.
func (c *Config) Validate() error {
	var errs []string

	if c.Server.Mode == "" || !validModes[c.Server.Mode] {
		errs = append(errs, fmt.Sprintf("server.mode %q is invalid; must be one of: %s",
			c.Server.Mode, strings.Join(validModesList(), ", ")))
	}

	// Database required for integrated, api, proxy, and transcode modes.
	needsDB := c.Server.Mode == "integrated" || c.Server.Mode == "api" ||
		c.Server.Mode == "proxy" || c.Server.Mode == "transcode"
	if needsDB && c.Database.URL == "" {
		errs = append(errs, "database.url is required for "+c.Server.Mode+" mode")
	}

	// Redis required for proxy and transcode modes (URL or Sentinel).
	needsRedis := c.Server.Mode == "proxy" || c.Server.Mode == "transcode"
	hasRedis := c.Redis.URL != "" || (c.Redis.SentinelMaster != "" && len(c.Redis.SentinelAddresses) > 0)
	if needsRedis && !hasRedis {
		errs = append(errs, "redis.url or redis sentinel config is required for "+c.Server.Mode+" mode")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// validModesList returns the valid modes as a sorted slice for error messages.
func validModesList() []string {
	modes := make([]string, 0, len(validModes))
	for m := range validModes {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	return modes
}
