package nodecontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodesessions"
	"github.com/Silo-Server/silo-server/internal/opslog"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/streamtelemetry"
)

// Paths of the core's node control endpoints, relative to the core URL a
// transcode node is given (SILO_CORE_URL).
const (
	PathConfig        = "/api/internal/nodes/self/config"
	PathReport        = "/api/internal/nodes/self/report"
	PathLogs          = "/api/internal/nodes/self/logs"
	PathHealthChanged = "/api/internal/nodes/self/health-changed"
	PathResolve       = "/api/internal/nodes/self/resolve"
)

// headerIfNoneMatch is the conditional header a node's configuration poll
// sends.
const headerIfNoneMatch = "If-None-Match"

// Report and log batch bounds. A node that has more sends the newest and
// marks the report truncated; the core refuses anything larger.
const (
	// MaxBodyBytes bounds every node request body; it is the signature
	// verifier's own bound.
	MaxBodyBytes = 1 << 20
	// MaxReportSessions bounds both the session records and the telemetry
	// sessions of one report.
	MaxReportSessions = 2000
	// MaxReportTransfers bounds the telemetry transfers of one report.
	MaxReportTransfers = 2000
	// MaxLogBatchEntries bounds one log batch.
	MaxLogBatchEntries = 100
)

// ReportInterval is how often a transcode node reports. Telemetry readers
// treat a snapshot older than five seconds as stale, so one report a second
// leaves room for four to go missing.
const ReportInterval = time.Second

// maxClockSkew bounds how far in the future a node may date anything it
// reports. Later times are moved back to the core's now plus this.
const maxClockSkew = 30 * time.Second

// NodeConfig is the configuration document GET /config returns: everything a
// transcode node takes from the core, for that node alone. Revision, also the
// response's ETag, is the SHA-256 of the rest of the document, so it changes
// exactly when something in it does.
type NodeConfig struct {
	Revision string `json:"revision"`
	NodeConfigBody
}

// NodeConfigBody is the part of a NodeConfig its revision covers.
type NodeConfigBody struct {
	Node NodeIdentity `json:"node"`
	// SourceAccess is how the node reads source media: "local" at the
	// catalog paths through its own mount, or "core" over HTTP.
	SourceAccess string `json:"source_access"`
	// DrainRequested is an administrator's request that the node take no new
	// work while its sessions finish.
	DrainRequested bool            `json:"drain_requested"`
	Playback       PlaybackConfig  `json:"playback"`
	Download       DownloadConfig  `json:"download"`
	Subtitles      SubtitlesConfig `json:"subtitles"`
	Logging        LoggingConfig   `json:"logging"`
}

// NodeIdentity is the node's registered row.
type NodeIdentity struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// PlaybackConfig carries the playback settings a node runs with, its own
// acceleration overrides already applied. FFmpegPath is the setting as
// stored; the node resolves it on its own host.
type PlaybackConfig struct {
	FFmpegPath              string `json:"ffmpeg_path"`
	HWAccel                 string `json:"hw_accel"`
	HWDevice                string `json:"hw_device"`
	TranscodeDir            string `json:"transcode_dir"`
	SegmentRetentionSeconds int    `json:"segment_retention_seconds"`
}

// DownloadConfig carries the node's prepared-download storage, its own
// overrides already applied.
type DownloadConfig struct {
	ArtifactDir      string `json:"artifact_dir"`
	ArtifactMaxBytes int64  `json:"artifact_max_bytes"`
}

// SubtitlesConfig carries the subtitle settings a node uses.
type SubtitlesConfig struct {
	SyncNodeCapacity int `json:"sync_node_capacity"`
}

// LoggingConfig carries the node's log settings: the level, format and quiet
// filter of its own output, and the lowest level it ships to the core.
type LoggingConfig struct {
	Level        string `json:"level"`
	Format       string `json:"format"`
	Quiet        string `json:"quiet"`
	CaptureLevel string `json:"capture_level"`
}

// newNodeConfig wraps body with its revision.
func newNodeConfig(body NodeConfigBody) (NodeConfig, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return NodeConfig{}, err
	}
	sum := sha256.Sum256(encoded)
	return NodeConfig{Revision: hex.EncodeToString(sum[:]), NodeConfigBody: body}, nil
}

// Report is what a transcode node posts every ReportInterval: everything it
// would otherwise have written to Redis itself. The core files it under the
// node the request is signed by, never under an identity in the body.
type Report struct {
	// BootID identifies the node process: its start time in Unix
	// nanoseconds, in decimal. Seq increases with every report a process
	// sends. The core accepts a report only when (BootID, Seq) is greater
	// than the last one it accepted from the node, boot first.
	BootID string `json:"boot_id"`
	Seq    uint64 `json:"seq"`
	// Truncated reports that the node left out its oldest sessions or
	// transfers to keep the report within bounds.
	Truncated bool                          `json:"truncated,omitempty"`
	Sessions  []nodesessions.SessionInfo    `json:"sessions"`
	Telemetry *streamtelemetry.NodeSnapshot `json:"telemetry,omitempty"`
}

// LogBatch is one batch of a node's operational log entries.
type LogBatch struct {
	Entries []opslog.Entry `json:"entries"`
}

// Purposes of a resolve request: rebuilding an HLS transcode the node lost,
// or serving one progressive remux request.
const (
	ResolvePurposeHLS   = "hls"
	ResolvePurposeRemux = "remux"
)

// ResolveRequest asks the core for the recipe of one transport the calling
// node serves. StreamToken is the X-Silo-Stream-Token header a relay forwarded
// with the request, passed through unread: the node never verifies tokens.
type ResolveRequest struct {
	TransportID string `json:"transport_id"`
	Purpose     string `json:"purpose"`
	StreamToken string `json:"stream_token,omitempty"`
}

// Resolution is the core's answer to a ResolveRequest: the recipe to run, the
// source grant the node opens the recipe's input with, and the playback
// identity the node files its session and telemetry under.
type Resolution struct {
	Recipe playback.RecipeCard `json:"recipe"`
	// SourceGrant is a fresh grant for the recipe's media file or theme,
	// for the calling node (see internal/nodesource).
	SourceGrant       string `json:"source_grant"`
	PlaybackSessionID string `json:"playback_session_id"`
	UserID            int    `json:"user_id"`
	ProfileID         string `json:"profile_id"`
	MediaFileID       int    `json:"media_file_id"`
	// StartedAt is when the playback session started, as the token or the
	// stored recipe names it; StartedAtSource says where that came from
	// (streamtoken.StartedAtSource). Both are empty when neither names one.
	StartedAt       time.Time `json:"started_at,omitzero"`
	StartedAtSource string    `json:"started_at_source,omitempty"`
	// Theme identifies the theme file of a theme conversion, which the
	// recipe card has no fields for.
	Theme *ResolvedTheme `json:"theme,omitempty"`
}

// ResolvedTheme is the theme file a theme conversion token names.
type ResolvedTheme struct {
	ID               int64 `json:"id"`
	Size             int64 `json:"size"`
	ModifiedUnixNano int64 `json:"modified_unix_nano"`
}

// Reasons a resolve request is refused, in the body of a 403, 404 or 410.
// They let the node answer its relay exactly as it did when it checked the
// token itself.
const (
	// ResolveNotFound (404): no recipe for the transport, or a token that does
	// not describe it.
	ResolveNotFound = "not_found"
	// ResolveDenied (410): the session's deny marker is set.
	ResolveDenied = "denied"
	// ResolveStopped (410): a remux whose stored authority is gone or no
	// longer matches the token.
	ResolveStopped = "stopped"
	// ResolveSourceChanged (410): the theme file is not the one the token
	// describes, or the recipe's source is gone.
	ResolveSourceChanged = "source_changed"
	// ResolveInvalidToken (403): a remux request without a valid token.
	ResolveInvalidToken = "invalid_token"
	// ResolveWrongRoute (403): a remux token routed to another node or not
	// to transcode execution at all, or an HLS recipe started on another
	// node or bound to none.
	ResolveWrongRoute = "wrong_route"
)

// ResolveRefusal is the JSON body of a refused resolve request.
type ResolveRefusal struct {
	Reason string `json:"reason"`
}
