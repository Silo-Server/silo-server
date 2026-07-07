package streamtoken

import (
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims holds everything a stateless proxy or transcode node needs
// to serve a streaming session without database access.
//
// Under token-carried reconstruction (TR-lease) the token is also the durable
// reconstruction descriptor: its claims carry the full set of byte-affecting
// encode parameters (the former Postgres "recipe card"), so a front-end that has
// lost its in-memory session can rebuild ffmpeg from the token the client
// re-presents — no shared per-session store. The ownership claims (uid/pid/mfid)
// are lookup keys re-resolved against the authority on reconstruct; they are
// never trusted on their own.
type Claims struct {
	SessionID       string `json:"sid"`
	MediaPath       string `json:"path"`
	PlayMethod      string `json:"method"`
	TranscodeAudio  bool   `json:"ta,omitempty"`
	TranscodeNode   string `json:"tnode,omitempty"`
	TargetCodec     string `json:"tc,omitempty"`
	TargetRes       string `json:"tres,omitempty"`
	AudioCodec      string `json:"ac,omitempty"`
	AudioChannels   int    `json:"ach,omitempty"`
	AudioTrackIndex int    `json:"ati,omitempty"`
	// DVProfile is the file's Dolby Vision profile (0 = none); remux nodes
	// use it to strip dangling profile 7 RPUs. Absent in older tokens, which
	// decodes as 0 (no strip — the pre-existing behavior).
	DVProfile int `json:"dvp,omitempty"`

	// Ownership / authorization lookup keys (re-resolved at reconstruct).
	// Not trust assertions.
	UserID      int    `json:"uid,omitempty"`
	ProfileID   string `json:"pid,omitempty"`
	MediaFileID int    `json:"mfid,omitempty"`

	// Reconstruction recipe — the byte-affecting encode parameters, mirroring the
	// former playback.RecipeCard. Zero for direct/remux tokens, which reconstruct
	// from identity alone plus the client-supplied position.
	SourceVideoCodec   string  `json:"svc,omitempty"`
	SeekSeconds        float64 `json:"seek,omitempty"`
	SegmentDuration    int     `json:"segd,omitempty"`
	StartSegmentNumber int     `json:"ssn,omitempty"`
	SubtitleTrackIndex int     `json:"sti,omitempty"`
	SubtitleBurnIn     bool    `json:"sbi,omitempty"`
	TargetBitrateKbps  int     `json:"tbr,omitempty"`
	TotalDuration      float64 `json:"dur,omitempty"`
	FastStart          bool    `json:"fs,omitempty"`
	TargetCodecAudio   string  `json:"tca,omitempty"`

	// Recipe staleness hint, bumped on each re-mint after a recipe mutation
	// (audio/quality/seek switch). An optional client-side hint only.
	Version int `json:"ver,omitempty"`

	jwt.RegisteredClaims
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
