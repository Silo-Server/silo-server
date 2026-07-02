package downloads

import (
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// QualityDecision is the resolved server-side target for a public download
// quality request.
type QualityDecision struct {
	RequestedQuality  string
	EffectiveQuality  string
	DeliveryFormat    string
	TargetBitrateKbps int
	PrepareTarget     playback.PrepareTarget
	RequiresArtifact  bool
}

// DownloadQualityResolver validates a client-facing quality request and maps it
// to the concrete delivery format and encode target the server should record.
type DownloadQualityResolver struct{}

// Resolve returns a concrete delivery decision for file. Empty quality defaults
// to "original"; legacy user-facing delivery formats are intentionally rejected.
func (DownloadQualityResolver) Resolve(
	requested string,
	user *models.User,
	cfg config.DownloadConfig,
	file *models.MediaFile,
	caps playback.ClientCapabilities,
	artifactsAvailable bool,
) (QualityDecision, error) {
	quality := normalizeQuality(requested)
	if !ValidQuality(quality) {
		return QualityDecision{}, ErrInvalidQuality
	}

	if quality != QualityOriginal {
		if err := ensureTranscodeAllowed(user, cfg); err != nil {
			return QualityDecision{}, err
		}
		if !artifactsAvailable {
			return QualityDecision{}, ErrQualityUnavailable
		}
		bitrate := QualityBitrateKbps(quality)
		target := playback.ResolvePrepareTarget(file, FormatTranscode, caps, playback.AdminSettings{
			TranscodeEnabled: true,
			Allow4KTranscode: true,
		})
		target.TargetBitrateKbps = bitrate
		return QualityDecision{
			RequestedQuality:  quality,
			EffectiveQuality:  quality,
			DeliveryFormat:    FormatTranscode,
			TargetBitrateKbps: bitrate,
			PrepareTarget:     target,
			RequiresArtifact:  true,
		}, nil
	}

	decision := playback.PlayDirect
	if hasCapabilities(caps) {
		playDecision := playback.Resolve(file, caps, playback.AdminSettings{
			TranscodeEnabled: cfg.TranscodeEnabled && user.DownloadTranscodeAllowed,
			Allow4KTranscode: true,
		})
		decision = playDecision.Method
	}

	switch decision {
	case playback.PlayDirect:
		return QualityDecision{
			RequestedQuality: QualityOriginal,
			EffectiveQuality: QualityOriginal,
			DeliveryFormat:   FormatOriginal,
		}, nil
	case playback.PlayRemux:
		if !artifactsAvailable {
			return QualityDecision{}, ErrQualityUnavailable
		}
		target := playback.ResolvePrepareTarget(file, FormatRemux, caps, playback.AdminSettings{
			TranscodeEnabled: cfg.TranscodeEnabled && user.DownloadTranscodeAllowed,
			Allow4KTranscode: true,
		})
		return QualityDecision{
			RequestedQuality: QualityOriginal,
			EffectiveQuality: QualityOriginal,
			DeliveryFormat:   FormatRemux,
			PrepareTarget:    target,
			RequiresArtifact: true,
		}, nil
	default:
		if err := ensureTranscodeAllowed(user, cfg); err != nil {
			return QualityDecision{}, err
		}
		if !artifactsAvailable {
			return QualityDecision{}, ErrQualityUnavailable
		}
		target := playback.ResolvePrepareTarget(file, FormatTranscode, caps, playback.AdminSettings{
			TranscodeEnabled: true,
			Allow4KTranscode: true,
		})
		target.TargetBitrateKbps = QualityBitrateKbps(Quality20Mbps)
		return QualityDecision{
			RequestedQuality:  QualityOriginal,
			EffectiveQuality:  Quality20Mbps,
			DeliveryFormat:    FormatTranscode,
			TargetBitrateKbps: QualityBitrateKbps(Quality20Mbps),
			PrepareTarget:     target,
			RequiresArtifact:  true,
		}, nil
	}
}

// PresetsFor returns the ordered quality list currently fulfillable for a
// user. Always non-nil: the capability contract documents quality_presets as
// an array, and a nil slice would serialize as JSON null.
func (DownloadQualityResolver) PresetsFor(user *models.User, cfg config.DownloadConfig, artifactsAvailable bool) []string {
	if !cfg.Enabled || user == nil || !user.DownloadAllowed {
		return []string{}
	}
	presets := []string{QualityOriginal}
	if artifactsAvailable && cfg.TranscodeEnabled && user.DownloadTranscodeAllowed {
		presets = append(presets, Quality20Mbps, Quality10Mbps, Quality5Mbps, Quality2Mbps, Quality1Mbps)
	}
	return presets
}

// ValidQuality reports whether q is a public download quality value.
func ValidQuality(q string) bool {
	switch q {
	case QualityOriginal, Quality20Mbps, Quality10Mbps, Quality5Mbps, Quality2Mbps, Quality1Mbps:
		return true
	default:
		return false
	}
}

// QualityBitrateKbps returns the video bitrate cap for a bitrate preset. It is
// zero for original and invalid inputs.
func QualityBitrateKbps(q string) int {
	switch q {
	case Quality20Mbps:
		return 20000
	case Quality10Mbps:
		return 10000
	case Quality5Mbps:
		return 5000
	case Quality2Mbps:
		return 2000
	case Quality1Mbps:
		return 1000
	default:
		return 0
	}
}

func normalizeQuality(q string) string {
	if q == "" {
		return QualityOriginal
	}
	return q
}

func ensureTranscodeAllowed(user *models.User, cfg config.DownloadConfig) error {
	if !cfg.TranscodeEnabled {
		return ErrTranscodeDisabled
	}
	if user == nil || !user.DownloadTranscodeAllowed {
		return ErrDownloadNotAllowed
	}
	return nil
}

func hasCapabilities(caps playback.ClientCapabilities) bool {
	return len(caps.CodecsVideo) > 0 || len(caps.CodecsAudio) > 0 ||
		len(caps.AudioPassthroughCodecs) > 0 || len(caps.Containers) > 0 ||
		caps.MaxResolution != "" || caps.HDR
}
