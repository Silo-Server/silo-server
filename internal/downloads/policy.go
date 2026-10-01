package downloads

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	policyengine "github.com/Silo-Server/silo-server/internal/policy"
	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// ActionDecider is the narrow policy decision interface used by downloads.
// *policy.PDP satisfies it.
type ActionDecider interface {
	CheckAction(context.Context, policyengine.ActionInput) (policyengine.ActionDecision, policyengine.Meta, error)
}

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

// PolicyUser is the resolved (access-group-merged) download policy for an
// account. Download checks never read raw models.User policy fields: those
// are inherit/override pointers and only make sense after resolution.
type PolicyUser struct {
	ID     int
	Policy access.EffectiveUserPolicy
}

// DownloadQualityResolver validates a client-facing quality request and maps it
// to the concrete delivery format and encode target the server should record.
type DownloadQualityResolver struct {
	actionDecider ActionDecider
}

// Resolve returns a concrete delivery decision for file. Empty quality defaults
// to "original"; legacy user-facing delivery formats are intentionally rejected.
//
// A bitrate preset transcodes onto the shared resolution ladder (see
// playback.ResolveDownloadTranscodeTarget). When that ladder would leave the
// source untouched — it already fits the preset's resolution and bitrate and
// the device plays it — the source is served instead of a re-encode that could
// only lose quality; the row still records the requested preset.
func (r DownloadQualityResolver) Resolve(
	ctx context.Context,
	requested string,
	user *PolicyUser,
	cfg config.DownloadConfig,
	file *models.MediaFile,
	caps playback.ClientCapabilities,
	artifactsAvailable bool,
	deviceID string,
) (QualityDecision, error) {
	quality := normalizeQuality(requested)
	if !ValidQuality(quality) {
		return QualityDecision{}, ErrInvalidQuality
	}

	if quality != QualityOriginal {
		ceiling, err := r.ensureTranscodeAvailable(ctx, user, cfg, artifactsAvailable, quality, deviceID)
		if err != nil {
			return QualityDecision{}, err
		}
		presetKbps := QualityBitrateKbps(quality)
		target, ok := downloadTranscodeTarget(file, caps, cfg, user, presetKbps, ceiling)
		if !ok {
			return QualityDecision{}, ErrQualityUnavailable
		}
		if sourceFitsPreset(file, caps, target, presetKbps) {
			method := playback.Resolve(file, caps, playback.AdminSettings{TranscodeEnabled: true}).Method
			// A source the ladder would not change is still re-encoded when
			// the device cannot play it or policy refuses its resolution.
			if decision, err := r.sourceDecision(ctx, quality, method, user, cfg, file, caps, artifactsAvailable, deviceID); err == nil {
				return decision, nil
			}
		}
		return transcodeDecision(quality, quality, target), nil
	}

	method := playback.PlayDirect
	if hasCapabilities(caps) {
		method = playback.Resolve(file, caps, playback.AdminSettings{
			TranscodeEnabled: cfg.TranscodeEnabled && user.Policy.DownloadTranscodeAllowed,
		}).Method
	}
	if method != playback.PlayTranscode {
		return r.sourceDecision(ctx, QualityOriginal, method, user, cfg, file, caps, artifactsAvailable, deviceID)
	}
	ceiling, err := r.ensureTranscodeAvailable(ctx, user, cfg, artifactsAvailable, quality, deviceID)
	if err != nil {
		return QualityDecision{}, err
	}
	target, ok := downloadTranscodeTarget(file, caps, cfg, user, QualityBitrateKbps(Quality20Mbps), ceiling)
	if !ok {
		return QualityDecision{}, ErrQualityUnavailable
	}
	return transcodeDecision(QualityOriginal, Quality20Mbps, target), nil
}

// sourceDecision serves the source file as-is (PlayDirect) or remuxed
// (PlayRemux) for a download that requested quality. Either keeps the source
// resolution, so the policy's quality gate is asserted against it here at
// create time: otherwise an over-ceiling original registers a row that
// serveDownloadBytes can never satisfy (review finding C6).
func (r DownloadQualityResolver) sourceDecision(
	ctx context.Context,
	requested string,
	method playback.PlayMethod,
	user *PolicyUser,
	cfg config.DownloadConfig,
	file *models.MediaFile,
	caps playback.ClientCapabilities,
	artifactsAvailable bool,
	deviceID string,
) (QualityDecision, error) {
	switch method {
	case playback.PlayDirect:
		if err := r.ensureServedQualityAllowed(ctx, user, cfg, artifactsAvailable, file, deviceID); err != nil {
			return QualityDecision{}, err
		}
		return QualityDecision{
			RequestedQuality: requested,
			EffectiveQuality: QualityOriginal,
			DeliveryFormat:   FormatOriginal,
		}, nil
	case playback.PlayRemux:
		if !artifactsAvailable {
			return QualityDecision{}, ErrQualityUnavailable
		}
		if err := r.ensureServedQualityAllowed(ctx, user, cfg, artifactsAvailable, file, deviceID); err != nil {
			return QualityDecision{}, err
		}
		return QualityDecision{
			RequestedQuality: requested,
			EffectiveQuality: QualityOriginal,
			DeliveryFormat:   FormatRemux,
			PrepareTarget: playback.ResolveRemuxTarget(file, caps, playback.AdminSettings{
				TranscodeEnabled: cfg.TranscodeEnabled && user.Policy.DownloadTranscodeAllowed,
			}),
			RequiresArtifact: true,
		}, nil
	default:
		return QualityDecision{}, ErrQualityUnavailable
	}
}

func transcodeDecision(requested, effective string, target playback.PrepareTarget) QualityDecision {
	return QualityDecision{
		RequestedQuality:  requested,
		EffectiveQuality:  effective,
		DeliveryFormat:    FormatTranscode,
		TargetBitrateKbps: target.TargetBitrateKbps,
		PrepareTarget:     target,
		RequiresArtifact:  true,
	}
}

// downloadTranscodeTarget resolves a bitrate-capped encode for file. The
// user's max playback quality and the policy ceiling (non-empty only when a
// custom override narrows it) cap the ladder class: they apply to what is
// served, so a capped transcode of an over-ceiling source stays downloadable —
// mirroring the serve-time rule in serveDownloadBytes. Without 4K transcoding
// the class stops at 1080p, as quality_options advertises, so a 4K source is
// never served or kept at 4K under a preset. ok is false when the device's
// caps attest no decoder the output can fit.
func downloadTranscodeTarget(file *models.MediaFile, caps playback.ClientCapabilities, cfg config.DownloadConfig, user *PolicyUser, kbps int, ceiling string) (playback.PrepareTarget, bool) {
	maxHeight := qualityHeight(ceiling)
	if user != nil {
		if height := qualityHeight(user.Policy.MaxPlaybackQuality); height > 0 && (maxHeight == 0 || height < maxHeight) {
			maxHeight = height
		}
	}
	if !cfg.Allow4KTranscode && (maxHeight == 0 || maxHeight > nonUHDMaxHeight) {
		maxHeight = nonUHDMaxHeight
	}
	return playback.ResolveDownloadTranscodeTarget(file, caps, kbps, playback.DownloadTranscodeSettings{
		AllowHEVCEncoding: cfg.AllowHEVCEncoding,
		MaxHeight:         maxHeight,
	})
}

// nonUHDMaxHeight is the tallest preset output while 4K transcoding is off.
const nonUHDMaxHeight = 1080

// sourceFitsPreset reports whether a preset transcode would keep the source's
// frame size and bitrate, so a re-encode could only lose quality. It needs the
// device's caps to prove playback and complete SDR probe facts: an HDR source
// is still converted, because the preset promises an SDR file every screen
// shows correctly.
func sourceFitsPreset(file *models.MediaFile, caps playback.ClientCapabilities, target playback.PrepareTarget, presetKbps int) bool {
	if !hasCapabilities(caps) || target.Resolution != "" || file.Bitrate <= 0 || len(file.VideoTracks) == 0 {
		return false
	}
	totalKbps := file.Bitrate
	if totalKbps > 10_000_000 {
		totalKbps /= 1000
	}
	if totalKbps > presetKbps {
		return false
	}
	dynamicRange := tonemap.MetadataForFile(file).DynamicRange
	return dynamicRange == "" || dynamicRange == playback.DynamicRangeSDRV3
}

// qualityHeight converts a policy quality ceiling ("1080p", "2160p") to its
// height; an empty or unrecognized ceiling is 0, meaning none.
func qualityHeight(quality string) int {
	height, _ := strconv.Atoi(strings.TrimSuffix(access.NormalizePlaybackQuality(quality), "p"))
	return height
}

// Output video codecs a converted download can use.
const (
	outputCodecH264 = "h264"
	outputCodecHEVC = "hevc"
)

// QualityOption describes one quality preset for client labels: the video
// bitrate cap and the tallest output the preset can produce on this server
// ("10 Mbps, up to 1080p"). Both are zero for original, which keeps the source.
type QualityOption struct {
	Preset      string
	BitrateKbps int
	MaxHeight   int
}

// qualityOptionsFor describes presets in order. MaxHeight is the ladder class
// the bitrate earns at <=30 fps in the most efficient codec the server may
// encode, so it is an honest "up to": a 60 fps source, a smaller source, or a
// device that decodes less all land at or below it. 4K sources convert only
// when 4K transcoding is allowed, and the user's quality ceiling and a policy
// override's transcode ceiling (policyCeiling) cap it too.
func qualityOptionsFor(presets []string, cfg config.DownloadConfig, user *PolicyUser, policyCeiling string) []QualityOption {
	codec := outputCodecH264
	if cfg.AllowHEVCEncoding {
		codec = outputCodecHEVC
	}
	ceiling := 0
	if !cfg.Allow4KTranscode {
		ceiling = nonUHDMaxHeight
	}
	limits := []string{policyCeiling}
	if user != nil {
		limits = append(limits, user.Policy.MaxPlaybackQuality)
	}
	for _, limit := range limits {
		if height := qualityHeight(limit); height > 0 && (ceiling == 0 || height < ceiling) {
			ceiling = height
		}
	}
	options := make([]QualityOption, 0, len(presets))
	for _, preset := range presets {
		option := QualityOption{Preset: preset, BitrateKbps: QualityBitrateKbps(preset)}
		if option.BitrateKbps > 0 {
			option.MaxHeight = playback.LadderClassForBitrate(option.BitrateKbps, 0, codec)
			if ceiling > 0 {
				option.MaxHeight = min(option.MaxHeight, ceiling)
			}
		}
		options = append(options, option)
	}
	return options
}

// PresetsFor returns the ordered quality list currently fulfillable for a
// user. Always non-nil: the capability contract documents quality_presets as
// an array, and a nil slice would serialize as JSON null.
func (DownloadQualityResolver) PresetsFor(user *PolicyUser, cfg config.DownloadConfig, artifactsAvailable bool) []string {
	if !cfg.Enabled || user == nil || !user.Policy.DownloadAllowed {
		return []string{}
	}
	presets := []string{QualityOriginal}
	if artifactsAvailable && cfg.TranscodeEnabled && user.Policy.DownloadTranscodeAllowed {
		presets = append(presets, Quality20Mbps, Quality10Mbps, Quality5Mbps, Quality2Mbps, Quality1Mbps)
	}
	return presets
}

// SetActionDecider wires the optional policy action decider. When unset, the
// service and resolver keep using the legacy inline checks.
func (s *Service) SetActionDecider(decider ActionDecider) {
	s.actionDecider = decider
	s.policy.actionDecider = decider
}

// policyPresetsFor returns the presets the policy engine allows and the
// quality ceiling it puts on converted downloads, so the capability labels
// what Resolve will produce.
func (s *Service) policyPresetsFor(
	ctx context.Context,
	user *PolicyUser,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
) ([]string, string) {
	if err := s.checkDownloadAction(ctx, policyengine.ActionDownload, userIDForPolicy(user), user, cfg, artifactsAvailable, ""); err != nil {
		return []string{}, ""
	}
	presets := []string{QualityOriginal}
	ceiling, err := s.policy.ensureTranscodeAvailable(ctx, user, cfg, artifactsAvailable, "", "")
	if err == nil {
		presets = append(presets, Quality20Mbps, Quality10Mbps, Quality5Mbps, Quality2Mbps, Quality1Mbps)
	}
	return presets, ceiling
}

func (s *Service) downloadConfigForUser(
	ctx context.Context,
	userID int,
	deviceID string,
) (config.DownloadConfig, *PolicyUser, error) {
	cfg, err := s.downloadConfigForFeature(ctx, userID, deviceID)
	if err != nil {
		return cfg, nil, err
	}
	user, err := s.downloadUserForConfig(ctx, userID, cfg, deviceID)
	return cfg, user, err
}

func (s *Service) downloadConfigForFeature(ctx context.Context, userID int, deviceID string) (config.DownloadConfig, error) {
	cfg := s.loadConfig(ctx)
	if cfg.Enabled {
		return cfg, nil
	}
	if err := s.checkDownloadAction(ctx, policyengine.ActionDownload, userID, nil, cfg, s.artifacts != nil, deviceID); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func (s *Service) downloadUserForConfig(
	ctx context.Context,
	userID int,
	cfg config.DownloadConfig,
	deviceID string,
) (*PolicyUser, error) {
	account, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("loading user: %w", err)
	}
	user, err := s.effectiveDownloadUser(ctx, account)
	if err != nil {
		return nil, ErrDownloadNotAllowed
	}
	if err := s.checkDownloadAction(ctx, policyengine.ActionDownload, userID, user, cfg, s.artifacts != nil, deviceID); err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Service) checkDownloadAction(
	ctx context.Context,
	action string,
	userID int,
	user *PolicyUser,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
	deviceID string,
) error {
	if s.actionDecider == nil {
		if !cfg.Enabled {
			return ErrFeatureDisabled
		}
		if user == nil || !user.Policy.DownloadAllowed {
			return ErrDownloadNotAllowed
		}
		return nil
	}
	decision, _, err := s.actionDecider.CheckAction(ctx, downloadActionInput(
		action,
		userID,
		user,
		cfg,
		artifactsAvailable,
		deviceID,
	))
	if err != nil {
		return ErrDownloadNotAllowed
	}
	if !decision.Allowed {
		return downloadActionDenyError(decision.ReasonCode)
	}
	return nil
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

// ensureTranscodeAvailable checks the download_transcode action and returns the
// policy's quality ceiling (non-empty only when a custom override narrows the
// user's max playback quality) so the caller can cap the prepared artifact.
func (r DownloadQualityResolver) ensureTranscodeAvailable(
	ctx context.Context,
	user *PolicyUser,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
	requestedQuality string,
	deviceID string,
) (string, error) {
	if r.actionDecider == nil {
		if err := ensureTranscodeAllowed(user, cfg); err != nil {
			return "", err
		}
		if !artifactsAvailable {
			return "", ErrQualityUnavailable
		}
		return "", nil
	}
	input := downloadActionInput(
		policyengine.ActionDownloadTranscode,
		userIDForPolicy(user),
		user,
		cfg,
		artifactsAvailable,
		deviceID,
	)
	input.RequestedQuality = requestedQuality
	decision, _, err := r.actionDecider.CheckAction(ctx, input)
	if err != nil {
		return "", ErrDownloadNotAllowed
	}
	if !decision.Allowed {
		return "", downloadActionDenyError(decision.ReasonCode)
	}
	return decision.QualityCeiling, nil
}

// ensureServedQualityAllowed runs the final download action check for paths
// that serve the source resolution unchanged (direct originals and remuxes),
// with FileQuality populated so the policy's quality gate sees what will
// actually be served. Capped transcode paths never assert FileQuality — their
// ceiling applies to the prepared artifact (see downloadActionInput).
func (r DownloadQualityResolver) ensureServedQualityAllowed(
	ctx context.Context,
	user *PolicyUser,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
	file *models.MediaFile,
	deviceID string,
) error {
	if r.actionDecider == nil {
		if user != nil && !access.QualityAllowed(file.Resolution, user.Policy.MaxPlaybackQuality) {
			return ErrQualityUnavailable
		}
		return nil
	}
	input := downloadActionInput(
		policyengine.ActionDownload,
		userIDForPolicy(user),
		user,
		cfg,
		artifactsAvailable,
		deviceID,
	)
	input.RequestedQuality = QualityOriginal
	input.FileQuality = file.Resolution
	decision, _, err := r.actionDecider.CheckAction(ctx, input)
	if err != nil {
		return ErrDownloadNotAllowed
	}
	if !decision.Allowed {
		return downloadActionDenyError(decision.ReasonCode)
	}
	// A custom override may narrow the ceiling below the served resolution;
	// nothing can cap an original/remux, so that narrowing is a denial here.
	if decision.QualityCeiling != "" && !access.QualityAllowed(file.Resolution, decision.QualityCeiling) {
		return ErrQualityUnavailable
	}
	return nil
}

func ensureTranscodeAllowed(user *PolicyUser, cfg config.DownloadConfig) error {
	if !cfg.TranscodeEnabled {
		return ErrTranscodeDisabled
	}
	if user == nil || !user.Policy.DownloadTranscodeAllowed {
		return ErrDownloadNotAllowed
	}
	return nil
}

// downloadActionInput builds the policy facts downloads can assert. FileQuality
// is left empty here and populated only where the source resolution is what
// gets served (direct originals and remuxes — see ensureServedQualityAllowed):
// asserting it on capped transcode paths would wrongly deny transcodes of
// over-ceiling sources, whose ceiling applies to the prepared artifact. The
// content-rating pair stays empty on every download path — rating ceilings are
// enforced by the scope-derived access filter at item access
// (EnsureAccessible) before any action check runs.
func downloadActionInput(
	action string,
	userID int,
	user *PolicyUser,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
	deviceID string,
) policyengine.ActionInput {
	input := policyengine.ActionInput{
		SchemaVersion:      1,
		Action:             action,
		UserID:             userID,
		DownloadsEnabled:   cfg.Enabled,
		TranscodeEnabled:   cfg.TranscodeEnabled,
		ArtifactsAvailable: artifactsAvailable,
		RequestTime:        time.Now().UTC().Format(time.RFC3339),
		DeviceID:           deviceID,
	}
	if user != nil {
		input.UserID = user.ID
		input.DownloadAllowed = user.Policy.DownloadAllowed
		input.DownloadTranscodeAllowed = user.Policy.DownloadTranscodeAllowed
		input.MaxPlaybackQuality = user.Policy.MaxPlaybackQuality
	}
	return input
}

func userIDForPolicy(user *PolicyUser) int {
	if user == nil {
		return 0
	}
	return user.ID
}

// downloadActionDenyError maps a typed policy reason code — never the
// free-text reason — to a downloads error. Unrecognized codes (including
// custom_denial from admin overrides) fall back to the generic denial.
func downloadActionDenyError(reasonCode string) error {
	switch reasonCode {
	case policyengine.ReasonCodeDownloadsDisabled:
		return ErrFeatureDisabled
	case policyengine.ReasonCodeTranscodeDisabled:
		return ErrTranscodeDisabled
	case policyengine.ReasonCodeDownloadArtifactsUnavailable,
		policyengine.ReasonCodeQualityCeilingExceeded:
		return ErrQualityUnavailable
	default:
		return ErrDownloadNotAllowed
	}
}

func hasCapabilities(caps playback.ClientCapabilities) bool {
	return caps.VideoEvidence != "" || len(caps.VideoDecode) > 0 ||
		len(caps.CodecsVideo) > 0 || len(caps.CodecsAudio) > 0 ||
		len(caps.AudioPassthroughCodecs) > 0 || len(caps.Containers) > 0 ||
		caps.MaxResolution != "" || caps.HDR
}
