package streamtoken

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	// PlayMethodDownload identifies a token minted only after the API has
	// authorized a file download. Proxy download routes reject playback tokens.
	PlayMethodDownload = "download"
	// PlayMethodToneMapDownload makes attested prepared downloads fail closed on
	// older proxies that do not validate the receipt fields.
	PlayMethodToneMapDownload = "download_tonemap_v1"
	// PlayMethodToneMapTranscode makes frozen tone-map reconstruction fail
	// closed on older readers that do not understand its recipe fields.
	PlayMethodToneMapTranscode = "transcode_tonemap_v1"
)

// Claims holds everything a stateless proxy or transcode node needs
// to serve a streaming session without database access.
//
// Under token-carried reconstruction (TR-lease) the token is also the durable
// reconstruction descriptor: its claims carry the frozen byte-affecting encode
// parameters (the former Postgres "recipe card"), while environment-specific
// fields such as the tone-map filter are resolved from the live node. A
// front-end that loses its in-memory session can rebuild ffmpeg from the token
// the client re-presents — no shared per-session store. The ownership claims
// (uid/pid/mfid) are lookup keys re-resolved against the authority on
// reconstruct; they are never trusted on their own.
type Claims struct {
	SessionID            string `json:"sid"`
	MediaPath            string `json:"path"`
	PlayMethod           string `json:"method"`
	TranscodeAudio       bool   `json:"ta,omitempty"`
	TranscodeNode        string `json:"tnode,omitempty"`
	TranscodeTransportID string `json:"tid,omitempty"`
	TargetCodec          string `json:"tc,omitempty"`
	TargetRes            string `json:"tres,omitempty"`
	AudioCodec           string `json:"ac,omitempty"`
	AudioChannels        int    `json:"ach,omitempty"`
	AudioTrackIndex      int    `json:"ati,omitempty"`
	AudioOnly            bool   `json:"ao,omitempty"`
	// DVProfile is the file's Dolby Vision profile (0 = none); remux nodes
	// use it to strip dangling profile 7 RPUs. Absent in older tokens, which
	// decodes as 0 (no strip — the pre-existing behavior).
	DVProfile int `json:"dvp,omitempty"`
	// RemuxDVMode freezes whether a Profile 7 remux preserves or strips DV
	// metadata. Empty is the legacy auto behavior for old tokens.
	RemuxDVMode string `json:"dvm,omitempty"`

	// Ownership / authorization lookup keys (re-resolved at reconstruct).
	// Not trust assertions.
	UserID      int    `json:"uid,omitempty"`
	ProfileID   string `json:"pid,omitempty"`
	MediaFileID int    `json:"mfid,omitempty"`
	// OriginalStartedAtUnixNano is decoded directly into int64 by golang-jwt,
	// preserving nanosecond precision. A future map[string]any decode path must
	// not pass this through float64, which cannot represent this magnitude exactly.
	OriginalStartedAtUnixNano int64 `json:"ostn,omitempty"`
	// DownloadArtifactID is an opaque transcode-node artifact handle. For
	// download tokens TranscodeNode is its authenticated origin; MediaPath stays
	// empty so node-local filesystem paths never leave the owning node.
	DownloadArtifactID string `json:"daid,omitempty"`
	// DownloadArtifactRowID identifies the authoritative database row so a
	// proxy can fence and requeue a signed remote locator that returns 404.
	DownloadArtifactRowID        string `json:"darid,omitempty"`
	DownloadArtifactSize         int64  `json:"dasz,omitempty"`
	DownloadExecutionFingerprint string `json:"daef,omitempty"`
	// DownloadFilename is the client-facing attachment name. Remote artifact
	// ids are internal attempt handles and must never become saved filenames.
	DownloadFilename string `json:"dfn,omitempty"`

	// Reconstruction recipe — the byte-affecting encode parameters, mirroring the
	// former playback.RecipeCard. Zero for direct/remux tokens, which reconstruct
	// from identity alone plus the client-supplied position.
	SourceVideoCodec           string  `json:"svc,omitempty"`
	SourceVideoProfile         string  `json:"svp,omitempty"`
	SourceVideoBitDepth        int     `json:"svb,omitempty"`
	SoftwareVideoDecode        bool    `json:"svd,omitempty"`
	ToneMapPolicy              string  `json:"tmp,omitempty"`
	ToneMapMode                string  `json:"tmm,omitempty"`
	ToneMapSourceKind          string  `json:"tms,omitempty"`
	ToneMapRecipeVersion       string  `json:"tmv,omitempty"`
	ToneMapPreflightRequired   bool    `json:"tmpf,omitempty"`
	ToneMapSourceRevision      string  `json:"tmsr,omitempty"`
	ToneMapDVConfigPresent     bool    `json:"tmdc,omitempty"`
	ToneMapDVBLCompatIDPresent bool    `json:"tmdbci,omitempty"`
	ToneMapDVBLPresent         bool    `json:"tmdb,omitempty"`
	ToneMapDVRPUPresent        bool    `json:"tmdr,omitempty"`
	VideoBitstreamFilter       string  `json:"vbsf,omitempty"`
	VideoSampleEntry           string  `json:"vse,omitempty"`
	OutputSubdir               string  `json:"osd,omitempty"`
	SeekSeconds                float64 `json:"seek,omitempty"`
	StreamOriginSeconds        float64 `json:"origin,omitempty"`
	CopySeekAnchorResolved     bool    `json:"origin_ok,omitempty"`
	SegmentDuration            int     `json:"segd,omitempty"`
	StartSegmentNumber         int     `json:"ssn,omitempty"`
	SubtitleTrackIndex         int     `json:"sti,omitempty"`
	SubtitleBurnIn             bool    `json:"sbi,omitempty"`
	SubtitleCodec              string  `json:"sbc,omitempty"`
	TargetBitrateKbps          int     `json:"tbr,omitempty"`
	TotalDuration              float64 `json:"dur,omitempty"`
	FastStart                  bool    `json:"fs,omitempty"`
	TargetCodecAudio           string  `json:"tca,omitempty"`
	TargetAudioChannels        int     `json:"tac,omitempty"`
	TargetAudioBitrateKbps     int     `json:"tabr,omitempty"`

	// Recipe staleness hint, bumped on each re-mint after a recipe mutation
	// (audio/quality/seek switch). An optional client-side hint only.
	Version int `json:"ver,omitempty"`

	jwt.RegisteredClaims
}

type StartedAtSource string

const (
	StartedAtSourceClaim    StartedAtSource = "claim"
	StartedAtSourceIssuedAt StartedAtSource = "issued_at"
	StartedAtSourceNone     StartedAtSource = "none"
)

// StartedAt resolves the session's creation time from the explicit claim first,
// then the registered issue time. Only the explicit claim is authoritative:
// Sign rewrites RegisteredClaims on every mint, so iat is issue time.
func (c *Claims) StartedAt() (time.Time, StartedAtSource) {
	if c == nil {
		return time.Time{}, StartedAtSourceNone
	}
	if c.OriginalStartedAtUnixNano != 0 {
		return time.Unix(0, c.OriginalStartedAtUnixNano).UTC(), StartedAtSourceClaim
	}
	if c.IssuedAt != nil {
		return c.IssuedAt.UTC(), StartedAtSourceIssuedAt
	}
	return time.Time{}, StartedAtSourceNone
}

// Sign creates a signed JWT string from the given claims.
func Sign(c Claims, secret string, ttl time.Duration) (string, error) {
	now := time.Now()
	c.RegisteredClaims = jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		IssuedAt:  jwt.NewNumericDate(now),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	return token.SignedString([]byte(secret))
}

// Verify parses and validates a stream token JWT string.
func Verify(tokenString, secret string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (any, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid stream token: %w", err)
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid stream token claims")
	}
	return claims, nil
}
