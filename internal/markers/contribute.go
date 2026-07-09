package markers

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// OutcomeStatus values for a contribution attempt, in addition to the provider
// SubmissionStatus* values.
const (
	OutcomeStatusError       = "error"
	OutcomeStatusRateLimited = "rate_limited"
	OutcomeStatusSkipped     = "skipped"
)

// ContributeOptions scopes a contribution run.
type ContributeOptions struct {
	// Provider restricts the run to a single provider id ("" = all enabled).
	Provider string
	// Segments restricts the run to specific kinds (nil = every present segment).
	Segments []MarkerKind
	// Auto marks a background run: a segment is only contributed when the
	// provider has contribute_auto_local enabled and the segment is a local
	// (scanner) intro at or above the provider's confidence threshold.
	Auto bool
}

// ContributionOutcome is the per (provider, segment) result of a run.
type ContributionOutcome struct {
	Provider     string
	Segment      MarkerKind
	Status       string // pending | accepted | rejected | error | rate_limited | skipped
	SubmissionID string
	Reason       string // skip reason or error message
	RetryAfter   time.Duration
}

// providerConfigReader is the read surface ContributionService needs from the
// provider config store (satisfied by *ProviderConfigStore).
type providerConfigReader interface {
	Get(provider string) (ProviderConfig, bool)
}

// contributionRecorder is the audit surface ContributionService needs
// (satisfied by *ContributionStore).
type contributionRecorder interface {
	AlreadySubmitted(ctx context.Context, fileID int, provider, segmentKind, contentHash string) (bool, error)
	Record(ctx context.Context, row ContributionRow) error
}

// ContributionService submits a file's eligible markers to enabled submitter
// providers, idempotently and audited. Both the on-demand admin path and the
// daily auto-submission task funnel through ContributeFile.
type ContributionService struct {
	registry *Registry
	resolver ExternalIDResolver
	config   providerConfigReader
	store    contributionRecorder
	logger   *slog.Logger
}

// NewContributionService constructs the service.
func NewContributionService(registry *Registry, resolver ExternalIDResolver, config providerConfigReader, store contributionRecorder, logger *slog.Logger) *ContributionService {
	if logger == nil {
		logger = slog.Default()
	}
	return &ContributionService{registry: registry, resolver: resolver, config: config, store: store, logger: logger}
}

type segmentData struct {
	kind       MarkerKind
	name       string
	start      *float64
	end        *float64
	source     *string
	confidence *float64
}

func fileSegments(file *models.MediaFile) []segmentData {
	return []segmentData{
		{MarkerKindIntro, "intro", file.IntroStart, file.IntroEnd, file.IntroMarkersSource, file.IntroMarkersConfidence},
		{MarkerKindCredits, "credits", file.CreditsStart, file.CreditsEnd, file.CreditsMarkersSource, file.CreditsMarkersConfidence},
		{MarkerKindRecap, "recap", file.RecapStart, file.RecapEnd, file.RecapMarkersSource, file.RecapMarkersConfidence},
		{MarkerKindPreview, "preview", file.PreviewStart, file.PreviewEnd, file.PreviewMarkersSource, file.PreviewMarkersConfidence},
	}
}

func (s *ContributionService) submitters() []Submitter {
	if s == nil || s.registry == nil {
		return nil
	}
	var out []Submitter
	for _, p := range s.registry.Providers() {
		if sub, ok := p.(Submitter); ok {
			out = append(out, sub)
		}
	}
	return out
}

// ContributeFile submits the file's eligible markers and returns one outcome
// per (provider, segment) attempted (including explicit skips).
func (s *ContributionService) ContributeFile(ctx context.Context, file *models.MediaFile, opts ContributeOptions) ([]ContributionOutcome, error) {
	if s == nil || file == nil || s.registry == nil || s.resolver == nil || s.config == nil || s.store == nil {
		return nil, nil
	}
	ids, err := s.resolver.ResolveForFile(ctx, file)
	if err != nil {
		return nil, err
	}
	if !ids.HasAnyID() {
		return nil, nil
	}

	want := func(k MarkerKind) bool {
		if len(opts.Segments) == 0 {
			return true
		}
		for _, kind := range opts.Segments {
			if kind == k {
				return true
			}
		}
		return false
	}

	var outcomes []ContributionOutcome
	for _, sub := range s.submitters() {
		providerID := sub.ID()
		if opts.Provider != "" && opts.Provider != providerID {
			continue
		}
		cfg, ok := s.config.Get(providerID)
		if !ok || !cfg.ContributeEnabled {
			continue
		}
		for _, seg := range fileSegments(file) {
			if !want(seg.kind) {
				continue
			}
			if outcome, attempted := s.contributeSegment(ctx, sub, providerID, cfg, file, ids, seg, opts.Auto); attempted {
				outcomes = append(outcomes, outcome)
				if outcome.Status == OutcomeStatusRateLimited {
					return outcomes, nil
				}
			}
		}
	}
	return outcomes, nil
}

func (s *ContributionService) contributeSegment(
	ctx context.Context,
	sub Submitter,
	providerID string,
	cfg ProviderConfig,
	file *models.MediaFile,
	ids ExternalIDs,
	seg segmentData,
	auto bool,
) (ContributionOutcome, bool) {
	if seg.start == nil || seg.end == nil {
		return ContributionOutcome{}, false
	}
	source := ""
	if seg.source != nil {
		source = *seg.source
	}
	// Only ever contribute our own data; never round-trip online markers.
	if source != models.MarkerSourceScanner && source != models.MarkerSourceManual {
		return ContributionOutcome{}, false
	}
	if auto {
		if !cfg.ContributeAutoLocal || source != models.MarkerSourceScanner || seg.kind != MarkerKindIntro {
			return ContributionOutcome{}, false
		}
		confidence := 0.0
		if seg.confidence != nil {
			confidence = *seg.confidence
		}
		if confidence < cfg.ContributeMinConfidence {
			return ContributionOutcome{}, false
		}
	}
	if missing := missingRequiredExternalIDs(sub, ids); len(missing) > 0 {
		return ContributionOutcome{
			Provider: providerID,
			Segment:  seg.kind,
			Status:   OutcomeStatusSkipped,
			Reason:   strings.Join(missing, ",") + " id required",
		}, true
	}

	startMs := int64(*seg.start * 1000)
	endMs := int64(*seg.end * 1000)
	durMs := int64(file.Duration) * 1000
	hash := ContentHash(seg.name, &startMs, &endMs, &durMs, contributionTargetParts(ids)...)

	already, err := s.store.AlreadySubmitted(ctx, file.ID, providerID, seg.name, hash)
	if err != nil {
		return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusError, Reason: err.Error()}, true
	}
	if already {
		return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusSkipped, Reason: "already submitted"}, true
	}

	startDur := time.Duration(*seg.start * float64(time.Second))
	endDur := time.Duration(*seg.end * float64(time.Second))
	req := SubmissionRequest{
		Kind:          ids.Kind,
		ExternalIDs:   ids.AsRequestMap(),
		SeasonNumber:  ids.SeasonNumber,
		EpisodeNumber: ids.EpisodeNumber,
		Segment:       seg.kind,
		Start:         &startDur,
		End:           &endDur,
		Duration:      time.Duration(file.Duration) * time.Second,
	}

	row := ContributionRow{
		MediaFileID:      file.ID,
		Provider:         providerID,
		SegmentKind:      seg.name,
		Source:           source,
		SubmittedStartMs: &startMs,
		SubmittedEndMs:   &endMs,
		VideoDurationMs:  &durMs,
		ContentHash:      hash,
	}

	result, err := sub.SubmitMarker(ctx, req)
	if err != nil {
		msg := err.Error()
		row.Status = OutcomeStatusError
		row.Error = &msg
		if recErr := s.store.Record(ctx, row); recErr != nil {
			s.logger.WarnContext(ctx, "record contribution error failed", "file_id", file.ID, "provider", providerID, "segment", seg.name, "error", recErr)
		}
		if after, ok := RetryAfter(err); ok {
			return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusRateLimited, Reason: msg, RetryAfter: after}, true
		}
		return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusError, Reason: msg}, true
	}

	row.Status = result.Status
	if row.Status == "" {
		row.Status = SubmissionStatusPending
	}
	if result.ID != "" {
		id := result.ID
		row.SubmissionID = &id
	}
	if err := s.store.Record(ctx, row); err != nil {
		s.logger.WarnContext(ctx, "record contribution failed", "file_id", file.ID, "provider", providerID, "segment", seg.name, "error", err)
	}
	return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: row.Status, SubmissionID: result.ID}, true
}

func contributionTargetParts(ids ExternalIDs) []string {
	return []string{
		itemTypeName(ids.Kind),
		ids.TmdbID,
		ids.ImdbID,
		ids.TvdbID,
		intPart(ids.SeasonNumber),
		intPart(ids.EpisodeNumber),
	}
}

func intPart(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func missingRequiredExternalIDs(sub Submitter, ids ExternalIDs) []string {
	reqProvider, ok := sub.(SubmissionRequirementProvider)
	if !ok {
		return nil
	}
	reqs := reqProvider.SubmissionRequirements()
	if len(reqs.RequiredExternalIDs) == 0 {
		return nil
	}
	values := ids.AsRequestMap()
	var missing []string
	for _, key := range reqs.RequiredExternalIDs {
		if strings.TrimSpace(values[key]) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}
