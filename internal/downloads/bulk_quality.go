package downloads

import (
	"context"
	"errors"

	"github.com/Silo-Server/silo-server/internal/config"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// SkipReasonQualityUnavailable marks a batch or monitor episode whose file
// cannot be prepared at the requested quality.
const SkipReasonQualityUnavailable = "quality_unavailable"

// uniformDecisions repeats one decision for n items.
func uniformDecisions(decision QualityDecision, n int) []QualityDecision {
	decisions := make([]QualityDecision, n)
	for i := range decisions {
		decisions[i] = decision
	}
	return decisions
}

// resolveItemDecisions resolves quality for each item's own file, the way a
// single download does. An item the quality cannot reach is dropped and
// reported as skipped; any other error (downloads or transcoding not allowed,
// preparation unavailable) applies to the whole request and is returned,
// except that retryLater also drops an item whose capability check failed for
// now, for a monitor whose next sync tries it again.
// Prepared targets are tone-map resolved here, before any quota lock, so the
// lock never spans remote capability probes.
func (s *Service) resolveItemDecisions(
	ctx context.Context,
	quality string,
	user *PolicyUser,
	cfg config.DownloadConfig,
	caps playback.ClientCapabilities,
	deviceID string,
	items []managedItem,
	retryLater bool,
) ([]managedItem, []QualityDecision, []SkippedDownload, error) {
	kept := make([]managedItem, 0, len(items))
	decisions := make([]QualityDecision, 0, len(items))
	var skipped []SkippedDownload
	for _, it := range items {
		decision, err := s.policy.Resolve(ctx, quality, user, cfg, it.file, caps, s.artifacts != nil, deviceID)
		if err == nil && decision.RequiresArtifact {
			decision.PrepareTarget, err = s.artifacts.resolveToneMapTarget(ctx, it.file, decision.PrepareTarget)
		}
		switch {
		case errors.Is(err, ErrQualityUnavailable):
			skipped = append(skipped, SkippedDownload{EpisodeID: it.episodeID, Reason: SkipReasonQualityUnavailable})
			continue
		case retryLater && (errors.Is(err, ErrCapabilityUnavailable) || errors.Is(err, ErrCapacityUnavailable)):
			continue
		case err != nil:
			return nil, nil, nil, err
		}
		kept = append(kept, it)
		decisions = append(decisions, decision)
	}
	return kept, decisions, skipped, nil
}

// managedRowSource is where a managed row's bytes come from: the source file
// (ready now) or a prepared artifact (ready or preparing). The decision's
// target must already be tone-map resolved (see resolveItemDecisions).
func (s *Service) managedRowSource(ctx context.Context, it managedItem, decision QualityDecision) (status string, size int64, artifactID string, err error) {
	if !decision.RequiresArtifact {
		return StatusReady, it.file.FileSize, "", nil
	}
	artifact, err := s.artifacts.ensureResolved(ctx, it.file, decision.DeliveryFormat, decision.PrepareTarget)
	if err != nil {
		return "", 0, "", err
	}
	status, size = artifactRowStatus(artifact, it.file)
	return status, size, artifact.ID, nil
}

// confirmIfLinked reconciles a row linked to an artifact (see
// confirmArtifactLink); a row serving the source file needs nothing.
func (s *Service) confirmIfLinked(ctx context.Context, d *Download) *Download {
	if d == nil || d.ArtifactID == "" {
		return d
	}
	return s.confirmArtifactLink(ctx, d)
}

// SubscriptionQuality is the stored form of a monitor's quality; rows from
// before monitors had one read as original.
func SubscriptionQuality(quality string) string {
	if quality == "" {
		return QualityOriginal
	}
	return quality
}

// validateMonitorQuality normalizes a monitor's requested quality and, for a
// bitrate preset, checks the profile may have downloads prepared at all. Each
// episode's own file is judged at sync time; one the preset cannot reach is
// skipped there.
func (s *Service) validateMonitorQuality(ctx context.Context, requested string, user *PolicyUser, cfg config.DownloadConfig, deviceID string) (string, error) {
	quality := normalizeQuality(requested)
	if !ValidQuality(quality) {
		return "", ErrInvalidQuality
	}
	if quality == QualityOriginal {
		return quality, nil
	}
	if _, err := s.policy.ensureTranscodeAvailable(ctx, user, cfg, s.artifacts != nil, quality, deviceID); err != nil {
		return "", err
	}
	return quality, nil
}
