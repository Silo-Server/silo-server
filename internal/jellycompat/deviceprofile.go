package jellycompat

import (
	"encoding/json"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
)

// DeviceProfile captures the subset of Jellyfin client capabilities the
// compat layer needs for direct-play, remux, and transcode negotiation.
type DeviceProfile struct {
	Name                string               `json:"Name,omitempty"`
	MaxStreamingBitrate int64                `json:"MaxStreamingBitrate,omitempty"`
	DirectPlayProfiles  []DirectPlayProfile  `json:"DirectPlayProfiles,omitempty"`
	TranscodingProfiles []TranscodingProfile `json:"TranscodingProfiles,omitempty"`
	CodecProfiles       []CodecProfile       `json:"CodecProfiles,omitempty"`
}

type DirectPlayProfile struct {
	Type       string `json:"Type,omitempty"`
	Container  string `json:"Container,omitempty"`
	VideoCodec string `json:"VideoCodec,omitempty"`
	AudioCodec string `json:"AudioCodec,omitempty"`
}

type TranscodingProfile struct {
	Type       string `json:"Type,omitempty"`
	Container  string `json:"Container,omitempty"`
	Protocol   string `json:"Protocol,omitempty"`
	Context    string `json:"Context,omitempty"`
	VideoCodec string `json:"VideoCodec,omitempty"`
	AudioCodec string `json:"AudioCodec,omitempty"`
}

type CodecProfile struct {
	Type            string             `json:"Type,omitempty"`
	Codec           string             `json:"Codec,omitempty"`
	Container       string             `json:"Container,omitempty"`
	SubContainer    string             `json:"SubContainer,omitempty"`
	Conditions      []ProfileCondition `json:"Conditions,omitempty"`
	ApplyConditions []ProfileCondition `json:"ApplyConditions,omitempty"`
}

type ProfileCondition struct {
	Condition  string `json:"Condition,omitempty"`
	Property   string `json:"Property,omitempty"`
	Value      string `json:"Value,omitempty"`
	IsRequired bool   `json:"IsRequired,omitempty"`
}

// DeviceProfileStore keeps the last reported device profile per compat token.
type DeviceProfileStore struct {
	mu       sync.RWMutex
	profiles map[string]storedDeviceProfile
	ttl      time.Duration
	now      func() time.Time
}

type storedDeviceProfile struct {
	profile   DeviceProfile
	expiresAt time.Time
}

// NewDeviceProfileStore creates a new in-memory device profile store.
func NewDeviceProfileStore(ttl time.Duration, now func() time.Time) *DeviceProfileStore {
	if now == nil {
		now = time.Now
	}
	if ttl <= 0 {
		ttl = 6 * time.Hour
	}
	return &DeviceProfileStore{
		profiles: make(map[string]storedDeviceProfile),
		ttl:      ttl,
		now:      now,
	}
}

// Put stores a device profile for a compat session token.
func (s *DeviceProfileStore) Put(token string, profile DeviceProfile) {
	if token == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.profiles[token] = storedDeviceProfile{
		profile:   profile,
		expiresAt: s.now().Add(s.ttl),
	}
}

// Get returns the last device profile reported for a compat session token.
func (s *DeviceProfileStore) Get(token string) (DeviceProfile, bool) {
	s.mu.RLock()
	entry, ok := s.profiles[token]
	s.mu.RUnlock()
	if !ok {
		return DeviceProfile{}, false
	}
	if !entry.expiresAt.After(s.now()) {
		s.Delete(token)
		return DeviceProfile{}, false
	}
	return entry.profile, true
}

// Delete removes a stored device profile.
func (s *DeviceProfileStore) Delete(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.profiles, token)
}

// HasData reports whether the profile contains usable capability information.
func (p DeviceProfile) HasData() bool {
	return strings.TrimSpace(p.Name) != "" ||
		p.MaxStreamingBitrate > 0 ||
		len(p.DirectPlayProfiles) > 0 ||
		len(p.TranscodingProfiles) > 0 ||
		len(p.CodecProfiles) > 0
}

// SupportsDirectPlay reports whether a version can be served as-is.
func (p DeviceProfile) SupportsDirectPlay(version catalog.FileVersion) bool {
	return p.SupportsDirectPlayForAudioStream(version, nil)
}

func (p DeviceProfile) SupportsDirectPlayForAudioStream(version catalog.FileVersion, audioStreamIndex *int) bool {
	if len(p.DirectPlayProfiles) == 0 {
		return p.codecProfileCompatibility(version, audioStreamIndex).supportsDirectPlay()
	}
	audioCodec := compatAudioCodec(version, audioStreamIndex)
	for _, profile := range p.DirectPlayProfiles {
		if !matchesVideoType(profile.Type) {
			continue
		}
		if matchesCSV(profile.Container, version.Container) &&
			matchesCSV(profile.VideoCodec, version.CodecVideo) &&
			matchesCSV(profile.AudioCodec, audioCodec) {
			return p.codecProfileCompatibility(version, audioStreamIndex).supportsDirectPlay()
		}
	}
	return false
}

// SupportsDirectStream reports whether a version can be remuxed without a full
// video transcode.
func (p DeviceProfile) SupportsDirectStream(version catalog.FileVersion) bool {
	if len(p.DirectPlayProfiles) == 0 {
		return true
	}
	for _, profile := range p.DirectPlayProfiles {
		if !matchesVideoType(profile.Type) {
			continue
		}
		if matchesCSV(profile.VideoCodec, version.CodecVideo) &&
			matchesCSV(profile.AudioCodec, version.CodecAudio) {
			return true
		}
	}
	return false
}

// SupportsTranscoding reports whether the client advertises HLS transcoding.
func (p DeviceProfile) SupportsTranscoding(version catalog.FileVersion) bool {
	if len(p.TranscodingProfiles) == 0 {
		return true
	}
	for _, profile := range p.TranscodingProfiles {
		if !matchesVideoType(profile.Type) {
			continue
		}
		if protocol := strings.ToLower(strings.TrimSpace(profile.Protocol)); protocol != "" && protocol != "hls" {
			continue
		}
		if !matchesCSV(profile.VideoCodec, "h264") {
			continue
		}
		if !matchesCSV(profile.AudioCodec, "aac") {
			continue
		}
		if profile.Container != "" && !matchesCSV(profile.Container, "ts") && !matchesCSV(profile.Container, "mpegts") {
			continue
		}
		return true
	}
	return false
}

// SupportsHLSRemuxForAudioStream reports whether the client accepts the
// source codecs in an HLS fragmented-MP4 stream. Codec-profile conditions are
// evaluated against the sample entry Silo will write, not against an unknown
// source-container tag.
func (p DeviceProfile) SupportsHLSRemuxForAudioStream(version catalog.FileVersion, audioStreamIndex *int) bool {
	if len(p.TranscodingProfiles) == 0 {
		return false
	}
	audioCodec := compatAudioCodec(version, audioStreamIndex)
	for _, profile := range p.TranscodingProfiles {
		if !matchesVideoType(profile.Type) {
			continue
		}
		if protocol := strings.ToLower(strings.TrimSpace(profile.Protocol)); protocol != "" && protocol != "hls" {
			continue
		}
		if !matchesCSV(profile.Container, "mp4") ||
			!matchesCSV(profile.VideoCodec, version.CodecVideo) ||
			!matchesCSV(profile.AudioCodec, audioCodec) {
			continue
		}
		return p.hlsRemuxCodecProfileCompatibility(version, audioStreamIndex).supportsDirectPlay()
	}
	return false
}

func (p DeviceProfile) supportsHLSRemuxWithAudioTranscodeForAudioStream(version catalog.FileVersion, audioStreamIndex *int) bool {
	if len(p.TranscodingProfiles) == 0 {
		return false
	}
	for _, profile := range p.TranscodingProfiles {
		if !matchesVideoType(profile.Type) {
			continue
		}
		if protocol := strings.ToLower(strings.TrimSpace(profile.Protocol)); protocol != "" && protocol != "hls" {
			continue
		}
		if !matchesCSV(profile.Container, "mp4") ||
			!matchesCSV(profile.VideoCodec, version.CodecVideo) ||
			!matchesCSV(profile.AudioCodec, compatTargetAudioCodec) {
			continue
		}

		outputVersion := version
		outputAudio := compatAudioTrack(version, audioStreamIndex)
		outputAudio.Codec = compatTargetAudioCodec
		outputAudio.Profile = ""
		outputAudio.Bitrate = 192_000
		if outputAudio.Channels > 0 {
			outputAudio.Channels = 2
		}
		outputAudio.Default = true
		outputVersion.CodecAudio = compatTargetAudioCodec
		outputVersion.AudioTracks = []models.AudioTrack{outputAudio}
		outputAudioStreamIndex := len(outputVersion.VideoTracks)
		return p.hlsRemuxCodecProfileCompatibility(outputVersion, &outputAudioStreamIndex).supportsDirectPlay()
	}
	return false
}

// DefaultDeviceProfile is a permissive fallback when a client has not reported
// capabilities yet.
func DefaultDeviceProfile() DeviceProfile {
	return DeviceProfile{
		Name: "generic",
		DirectPlayProfiles: []DirectPlayProfile{
			{Type: "Video"},
		},
		TranscodingProfiles: []TranscodingProfile{
			{Type: "Video", Protocol: "hls", Container: "ts", VideoCodec: "h264", AudioCodec: "aac"},
		},
	}
}

// SupportsVideoCodecForDirectStream reports whether the client can accept the
// source video codec for a remux-style stream, regardless of whether the audio
// codec must be transcoded separately.
func (p DeviceProfile) SupportsVideoCodecForDirectStream(version catalog.FileVersion) bool {
	return p.SupportsVideoCodecForDirectStreamForAudioStream(version, nil)
}

func (p DeviceProfile) SupportsVideoCodecForDirectStreamForAudioStream(version catalog.FileVersion, audioStreamIndex *int) bool {
	if len(p.DirectPlayProfiles) == 0 {
		return p.codecProfileCompatibility(version, audioStreamIndex).VideoSupported
	}
	for _, profile := range p.DirectPlayProfiles {
		if !matchesVideoType(profile.Type) {
			continue
		}
		if matchesCSV(profile.VideoCodec, version.CodecVideo) {
			return p.codecProfileCompatibility(version, audioStreamIndex).VideoSupported
		}
	}
	return false
}

// SupportsAudioCodecForDirectStream reports whether the client can accept the
// source audio codec in a remux-style stream, regardless of container.
func (p DeviceProfile) SupportsAudioCodecForDirectStream(version catalog.FileVersion) bool {
	return p.SupportsAudioCodecForDirectStreamForAudioStream(version, nil)
}

func (p DeviceProfile) SupportsAudioCodecForDirectStreamForAudioStream(version catalog.FileVersion, audioStreamIndex *int) bool {
	if len(p.DirectPlayProfiles) == 0 {
		return p.codecProfileCompatibility(version, audioStreamIndex).AudioSupported
	}
	audioCodec := compatAudioCodec(version, audioStreamIndex)
	for _, profile := range p.DirectPlayProfiles {
		if !matchesVideoType(profile.Type) {
			continue
		}
		if matchesCSV(profile.AudioCodec, audioCodec) {
			return p.codecProfileCompatibility(version, audioStreamIndex).AudioSupported
		}
	}
	return false
}

// decodeDeviceProfile extracts a device profile from either a wrapped
// Jellyfin request body or a direct DeviceProfile payload.
func decodeDeviceProfile(r io.Reader) (DeviceProfile, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return DeviceProfile{}, err
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return DeviceProfile{}, nil
	}

	var wrapper struct {
		DeviceProfile json.RawMessage `json:"DeviceProfile"`
	}
	if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.DeviceProfile) > 0 {
		var profile DeviceProfile
		if err := json.Unmarshal(wrapper.DeviceProfile, &profile); err != nil {
			return DeviceProfile{}, err
		}
		return profile, nil
	}

	var profile DeviceProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return DeviceProfile{}, err
	}
	return profile, nil
}

func matchesVideoType(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "*", "video":
		return true
	default:
		return false
	}
}

func matchesCSV(raw, want string) bool {
	if strings.TrimSpace(raw) == "" || raw == "*" {
		return true
	}
	want = normalizeCompatToken(want)
	for part := range strings.SplitSeq(raw, ",") {
		if normalizeCompatToken(part) == want {
			return true
		}
	}
	return false
}

func normalizeCompatToken(raw string) string {
	token := strings.ToLower(strings.TrimSpace(raw))
	switch token {
	case "ts":
		return "mpegts"
	case "x-matroska":
		return "mkv"
	case "h265":
		return "hevc"
	default:
		return token
	}
}
