package downloads

import (
	"context"
	"fmt"
	"time"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	policyengine "github.com/Silo-Server/silo-server/internal/policy"
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

// DownloadQualityResolver validates a client-facing quality request and maps it
// to the concrete delivery format and encode target the server should record.
type DownloadQualityResolver struct {
	actionDecider ActionDecider
}

// Resolve returns a concrete delivery decision for file. Empty quality defaults
// to "original"; legacy user-facing delivery formats are intentionally rejected.
func (r DownloadQualityResolver) Resolve(
	ctx context.Context,
	requested string,
	user *models.User,
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
		bitrate := QualityBitrateKbps(quality)
		target := playback.ResolvePrepareTarget(file, FormatTranscode, caps, playback.AdminSettings{
			TranscodeEnabled: true,
			Allow4KTranscode: true,
		})
		target.TargetBitrateKbps = bitrate
		applyQualityCeiling(&target, file, ceiling)
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
		// Original bytes are served at the source resolution, so assert it at
		// create time: otherwise an over-ceiling original registers a row that
		// serveDownloadBytes can never satisfy (review finding C6).
		if err := r.ensureServedQualityAllowed(ctx, user, cfg, artifactsAvailable, file, deviceID); err != nil {
			return QualityDecision{}, err
		}
		return QualityDecision{
			RequestedQuality: QualityOriginal,
			EffectiveQuality: QualityOriginal,
			DeliveryFormat:   FormatOriginal,
		}, nil
	case playback.PlayRemux:
		if !artifactsAvailable {
			return QualityDecision{}, ErrQualityUnavailable
		}
		// A remux artifact keeps the source resolution, so it is served — and
		// must be asserted — like an original (unlike capped transcodes).
		if err := r.ensureServedQualityAllowed(ctx, user, cfg, artifactsAvailable, file, deviceID); err != nil {
			return QualityDecision{}, err
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
		ceiling, err := r.ensureTranscodeAvailable(ctx, user, cfg, artifactsAvailable, quality, deviceID)
		if err != nil {
			return QualityDecision{}, err
		}
		target := playback.ResolvePrepareTarget(file, FormatTranscode, caps, playback.AdminSettings{
			TranscodeEnabled: true,
			Allow4KTranscode: true,
		})
		target.TargetBitrateKbps = QualityBitrateKbps(Quality20Mbps)
		applyQualityCeiling(&target, file, ceiling)
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

// SetActionDecider wires the optional policy action decider. When unset, the
// service and resolver keep using the legacy inline checks.
func (s *Service) SetActionDecider(decider ActionDecider) {
	s.actionDecider = decider
	s.policy.actionDecider = decider
}

func (s *Service) policyPresetsFor(
	ctx context.Context,
	user *models.User,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
) []string {
	if err := s.checkDownloadAction(ctx, policyengine.ActionDownload, userIDForPolicy(user), user, cfg, artifactsAvailable, ""); err != nil {
		return []string{}
	}
	presets := []string{QualityOriginal}
	if err := s.checkDownloadAction(ctx, policyengine.ActionDownloadTranscode, userIDForPolicy(user), user, cfg, artifactsAvailable, ""); err == nil {
		presets = append(presets, Quality20Mbps, Quality10Mbps, Quality5Mbps, Quality2Mbps, Quality1Mbps)
	}
	return presets
}

func (s *Service) downloadConfigForUser(
	ctx context.Context,
	userID int,
	deviceID string,
) (config.DownloadConfig, *models.User, error) {
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
) (*models.User, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("loading user: %w", err)
	}
	user, err = s.effectiveDownloadUser(ctx, user)
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
	user *models.User,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
	deviceID string,
) error {
	if s.actionDecider == nil {
		if !cfg.Enabled {
			return ErrFeatureDisabled
		}
		if user == nil || !user.DownloadAllowed {
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
	user *models.User,
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
	user *models.User,
	cfg config.DownloadConfig,
	artifactsAvailable bool,
	file *models.MediaFile,
	deviceID string,
) error {
	if r.actionDecider == nil {
		if user != nil && !access.QualityAllowed(file.Resolution, user.MaxPlaybackQuality) {
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

// applyQualityCeiling downscales a transcode target so the prepared artifact
// stays within a policy quality ceiling. The ceiling applies to what is served
// (the artifact), so a capped transcode of an over-ceiling source stays
// downloadable — mirroring the serve-time rule in serveDownloadBytes.
func applyQualityCeiling(target *playback.PrepareTarget, file *models.MediaFile, ceiling string) {
	ceiling = access.NormalizePlaybackQuality(ceiling)
	if ceiling == "" {
		return
	}
	served := target.Resolution
	if served == "" {
		served = file.Resolution
	}
	if !access.QualityAllowed(served, ceiling) {
		target.Resolution = ceiling
	}
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
	user *models.User,
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
		input.DownloadAllowed = user.DownloadAllowed
		input.DownloadTranscodeAllowed = user.DownloadTranscodeAllowed
		input.MaxPlaybackQuality = user.MaxPlaybackQuality
	}
	return input
}

func userIDForPolicy(user *models.User) int {
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
	return len(caps.CodecsVideo) > 0 || len(caps.CodecsAudio) > 0 ||
		len(caps.AudioPassthroughCodecs) > 0 || len(caps.Containers) > 0 ||
		caps.MaxResolution != "" || caps.HDR
}
