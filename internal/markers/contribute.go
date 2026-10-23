package markers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

// OutcomeStatus values for a contribution attempt, in addition to the provider
// SubmissionStatus* values.
const (
	OutcomeStatusConflict    = "conflict"
	OutcomeStatusError       = "error"
	OutcomeStatusInvalid     = "invalid"
	OutcomeStatusRateLimited = "rate_limited"
	OutcomeStatusSkipped     = "skipped"
	contributionStatusClaim  = "submitting"
	contributionSubmitLimit  = 2 * time.Minute
	contributionClaimLease   = 15 * time.Minute
	contributionRecordLimit  = 5 * time.Second
	// contributionInvalidRecheck is how long a target the provider refused as
	// invalid stays claimed before one more attempt. The refusal usually
	// reflects the provider's catalog (a season TMDB does not list yet), which
	// can change without any local edit.
	contributionInvalidRecheck = 30 * 24 * time.Hour
)

// contributionInFlightReason is the skip reason for a target another worker's
// submission holds; a WaitInFlight run tries that claim again every
// contributionInFlightPoll.
const (
	contributionInFlightReason = "another submission for this item is in flight"
	contributionInFlightPoll   = 500 * time.Millisecond
)

// ContributeOptions scopes a contribution run.
type ContributeOptions struct {
	// Provider restricts the run to a single provider id ("" = all enabled).
	Provider string
	// Segments restricts the run to specific kinds (nil = every present segment).
	Segments []MarkerKind
	// Auto marks a background run: a segment is only contributed when the
	// provider has contribute_auto_local enabled and the segment is a local
	// (scanner) intro or credits at or above the provider's confidence
	// threshold.
	Auto bool
	// Confirmed maps a kind to the online provider whose marker a manual save
	// kept (see ConfirmedProvider). The manual marker repeats that provider's
	// data, so it goes to that provider alone, as a human check of its marker.
	Confirmed map[MarkerKind]string
	// WaitInFlight makes a provider that supports amend wait, until ctx ends,
	// for another worker's in-flight submission of a different marker for the
	// same target, then send this one. The waits come after every other
	// provider and segment. A manual save needs it: nothing offers a manual
	// marker again, so skipping it would leave the provider with the earlier
	// one.
	WaitInFlight bool
}

// ContributionOutcome is the per (provider, segment) result of a run.
type ContributionOutcome struct {
	Provider     string
	Segment      MarkerKind
	Status       string // pending | accepted | rejected | conflict | invalid | error | rate_limited | skipped
	SubmissionID string
	Reason       string // skip reason or error message
	RetryAfter   time.Duration
}

// providerConfigReader is the read surface ContributionService needs from the
// provider config store (satisfied by *ProviderConfigStore). Load reads the
// stored rows, so a node that missed the change event for a sharing opt-out
// saved elsewhere does not share on its cached flags.
type providerConfigReader interface {
	Load(ctx context.Context) (map[string]ProviderConfig, error)
}

// contributionRecorder is the audit surface ContributionService needs
// (satisfied by *ContributionStore).
type contributionRecorder interface {
	Claim(ctx context.Context, row ContributionRow, staleAfter time.Duration) (ContributionClaim, bool, error)
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
	provider   *string
	confidence *float64
	algorithm  *string
}

func fileSegments(file *models.MediaFile) []segmentData {
	return []segmentData{
		{MarkerKindIntro, models.MarkerSegmentIntro, file.IntroStart, file.IntroEnd, file.IntroMarkersSource, file.IntroMarkersProvider, file.IntroMarkersConfidence, file.IntroMarkersAlgorithm},
		{MarkerKindCredits, models.MarkerSegmentCredits, file.CreditsStart, file.CreditsEnd, file.CreditsMarkersSource, file.CreditsMarkersProvider, file.CreditsMarkersConfidence, file.CreditsMarkersAlgorithm},
		{MarkerKindRecap, models.MarkerSegmentRecap, file.RecapStart, file.RecapEnd, file.RecapMarkersSource, file.RecapMarkersProvider, file.RecapMarkersConfidence, file.RecapMarkersAlgorithm},
		{MarkerKindPreview, models.MarkerSegmentPreview, file.PreviewStart, file.PreviewEnd, file.PreviewMarkersSource, file.PreviewMarkersProvider, file.PreviewMarkersConfidence, file.PreviewMarkersAlgorithm},
	}
}

// manualConfirmTolerance is how far, in seconds, each bound of a manual save
// may move from a provider's marker and still count as keeping it.
const manualConfirmTolerance = 1.0

// ConfirmedProvider returns the online provider whose marker of kind a manual
// save of start-end keeps: before, the file as it was before the save, held
// that provider's marker, and neither bound moved by more than a second. It
// returns "" otherwise.
func ConfirmedProvider(before *models.MediaFile, kind MarkerKind, start, end float64) string {
	if before == nil {
		return ""
	}
	for _, seg := range fileSegments(before) {
		if seg.kind != kind {
			continue
		}
		if seg.start == nil || seg.end == nil || seg.source == nil || seg.provider == nil ||
			(*seg.source != models.MarkerSourceOnline && *seg.source != models.MarkerSourcePlugin) {
			return ""
		}
		if math.Abs(*seg.start-start) > manualConfirmTolerance || math.Abs(*seg.end-end) > manualConfirmTolerance {
			return ""
		}
		return *seg.provider
	}
	return ""
}

// AutoContributedKinds lists the local segment kinds automatic contribution
// shares, in the order a run shares them. The scanner detects intros and
// credits; intros go first so a backlog of credits never holds back a new
// episode's intro when a provider's daily limit ends the run.
func AutoContributedKinds() []MarkerKind {
	return []MarkerKind{MarkerKindIntro, MarkerKindCredits}
}

func autoContributedKind(kind MarkerKind) bool {
	return slices.Contains(AutoContributedKinds(), kind)
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

// AutoContributors lists the registered submitters that share local markers
// automatically, with the settings ContributeFile applies to an automatic run.
// It reads the stored provider config; a failed read lists none.
func (s *ContributionService) AutoContributors(ctx context.Context) ([]AutoContributor, error) {
	if s == nil || s.config == nil {
		return nil, nil
	}
	configs, err := s.config.Load(ctx)
	if err != nil {
		return nil, err
	}
	var out []AutoContributor
	for _, sub := range s.submitters() {
		cfg, ok := configs[sub.ID()]
		if !ok || !cfg.ContributeEnabled || !cfg.ContributeAutoLocal {
			continue
		}
		reqs := submissionRequirements(sub)
		out = append(out, AutoContributor{
			Provider:            sub.ID(),
			MinConfidence:       cfg.ContributeMinConfidence,
			RequiredExternalIDs: reqs.RequiredExternalIDs,
			SupportsAmend:       reqs.SupportsAmend,
		})
	}
	return out, nil
}

// ContributeFile submits the file's eligible markers and returns one outcome
// per (provider, segment) attempted (including explicit skips). It reads the
// stored provider config for each call, so an opt-out saved on another node
// applies to the next save or file even if this node missed its change event;
// a failed read shares nothing.
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
	configs, err := s.config.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load marker sharing settings: %w", err)
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

	// A target held by an in-flight submission is waited out only after every
	// other provider and segment has had its turn, so one slow submission does
	// not use up the run's time.
	type heldTarget struct {
		index int
		sub   Submitter
		cfg   ProviderConfig
		seg   segmentData
	}
	var held []heldTarget
	first := opts
	first.WaitInFlight = false
	var outcomes []ContributionOutcome
	for _, sub := range s.submitters() {
		providerID := sub.ID()
		if opts.Provider != "" && opts.Provider != providerID {
			continue
		}
		cfg, ok := configs[providerID]
		if !ok || !cfg.ContributeEnabled {
			continue
		}
		for _, seg := range fileSegments(file) {
			if !want(seg.kind) {
				continue
			}
			if outcome, attempted := s.contributeSegment(ctx, sub, providerID, cfg, file, ids, seg, first); attempted {
				if opts.WaitInFlight && outcome.Reason == contributionInFlightReason {
					held = append(held, heldTarget{len(outcomes), sub, cfg, seg})
				}
				outcomes = append(outcomes, outcome)
				if outcome.Status == OutcomeStatusRateLimited {
					return outcomes, nil
				}
			}
		}
	}
	for _, h := range held {
		outcome, _ := s.contributeSegment(ctx, h.sub, h.sub.ID(), h.cfg, file, ids, h.seg, opts)
		outcomes[h.index] = outcome
		if outcome.Status == OutcomeStatusRateLimited {
			return outcomes, nil
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
	opts ContributeOptions,
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
	// Nor another provider's marker that a manual save only kept.
	if confirmed := opts.Confirmed[seg.kind]; source == models.MarkerSourceManual && confirmed != "" && confirmed != providerID {
		return ContributionOutcome{}, false
	}
	if opts.Auto {
		if !cfg.ContributeAutoLocal || source != models.MarkerSourceScanner || !autoContributedKind(seg.kind) {
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
	// Providers key episodes by show plus season and episode number, so an
	// unnumbered episode has no submittable target. Season 0 holds specials.
	if ids.Kind == ItemKindEpisode && (ids.SeasonNumber < 0 || ids.EpisodeNumber <= 0) {
		return ContributionOutcome{
			Provider: providerID,
			Segment:  seg.kind,
			Status:   OutcomeStatusSkipped,
			Reason:   "season and episode numbers required",
		}, true
	}

	startMs := int64(*seg.start * 1000)
	endMs := int64(*seg.end * 1000)
	durMs := int64(file.Duration) * 1000
	reqs := submissionRequirements(sub)
	algorithm := ""
	if seg.algorithm != nil {
		algorithm = *seg.algorithm
	}
	// An amend provider replaces this file's earlier submission, so the file
	// is its own target and a new detection version is a change worth sending.
	// CandidateLocalMarkerFiles derives the same hash and key in SQL.
	hashParts := contributionTargetParts(ids)
	targetKey := contributionTargetKey(ids, reqs.RequiredExternalIDs)
	if reqs.SupportsAmend {
		hashParts = append(hashParts, source, algorithm)
		targetKey += "|file:" + strconv.Itoa(file.ID)
	}
	hash := ContentHash(seg.name, &startMs, &endMs, &durMs, hashParts...)

	row := ContributionRow{
		MediaFileID:      file.ID,
		Provider:         providerID,
		SegmentKind:      seg.name,
		Source:           source,
		SubmittedStartMs: &startMs,
		SubmittedEndMs:   &endMs,
		VideoDurationMs:  &durMs,
		ContentHash:      hash,
		TargetKey:        targetKey,
		Status:           contributionStatusClaim,
		Amend:            reqs.SupportsAmend,
	}
	claim, claimed, err := s.claim(ctx, row, opts.WaitInFlight)
	if errors.Is(err, errContributionInFlight) {
		return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusSkipped, Reason: contributionInFlightReason}, true
	}
	if err != nil {
		return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusError, Reason: err.Error()}, true
	}
	if !claimed {
		return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusSkipped, Reason: "already submitted for this item"}, true
	}
	row.ID = claim.ID
	row.ClaimToken = claim.Token

	startDur := time.Duration(*seg.start * float64(time.Second))
	endDur := time.Duration(*seg.end * float64(time.Second))
	req := SubmissionRequest{
		Kind:               ids.Kind,
		ExternalIDs:        ids.AsRequestMap(),
		SeasonNumber:       ids.SeasonNumber,
		EpisodeNumber:      ids.EpisodeNumber,
		EpisodeExternalIDs: ids.Episode.AsRequestMap(),
		EpisodeNumbering:   ids.EpisodeNumbering,
		EpisodeTitle:       ids.EpisodeTitle,
		Segment:            seg.kind,
		Start:              &startDur,
		End:                &endDur,
		Duration:           time.Duration(file.Duration) * time.Second,
		FileHash:           file.FileHash,
		FileSize:           file.FileSize,
		Source:             source,
		Algorithm:          algorithm,
	}
	if seg.confidence != nil {
		req.Confidence = *seg.confidence
	}

	submitCtx, cancel := context.WithTimeout(ctx, contributionSubmitLimit)
	result, err := sub.SubmitMarker(submitCtx, req)
	cancel()
	if err != nil {
		msg := err.Error()
		row.Error = &msg
		var conflict *SubmissionConflictError
		var invalid *SubmissionInvalidError
		switch {
		case errors.As(err, &conflict) && conflict != nil:
			row.Status = OutcomeStatusConflict
			if conflict.HTTPStatus > 0 {
				status := conflict.HTTPStatus
				row.HTTPStatus = &status
			}
		case errors.As(err, &invalid) && invalid != nil:
			row.Status = OutcomeStatusInvalid
			if invalid.HTTPStatus > 0 {
				status := invalid.HTTPStatus
				row.HTTPStatus = &status
			}
		default:
			row.Status = OutcomeStatusError
		}
		s.recordContribution(ctx, row)
		if row.Status == OutcomeStatusConflict || row.Status == OutcomeStatusInvalid {
			return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: row.Status, Reason: msg}, true
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
	s.recordContribution(ctx, row)
	return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: row.Status, SubmissionID: result.ID}, true
}

// claim claims row's target. With wait set, it tries again while another
// worker's submission for an amend target is in flight, until ctx ends.
func (s *ContributionService) claim(ctx context.Context, row ContributionRow, wait bool) (ContributionClaim, bool, error) {
	for {
		claim, claimed, err := s.store.Claim(ctx, row, contributionClaimLease)
		if !wait || !errors.Is(err, errContributionInFlight) {
			return claim, claimed, err
		}
		select {
		case <-ctx.Done():
			return claim, claimed, err
		case <-time.After(contributionInFlightPoll):
		}
	}
}

func (s *ContributionService) recordContribution(ctx context.Context, row ContributionRow) {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), contributionRecordLimit)
	defer cancel()
	if err := s.store.Record(recordCtx, row); err != nil {
		s.logger.WarnContext(recordCtx, "record contribution failed", "file_id", row.MediaFileID, "provider", row.Provider, "segment", row.SegmentKind, "status", row.Status, "error", err)
	}
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

// contributionTargetKey names the provider-side item a segment is submitted
// against, independent of the submitted times. Most providers accept one
// submission per account for each item and segment and offer no way to amend
// it, so an active claim on this key blocks every later payload for the same
// item. contributeSegment appends the file for a provider that supports amend.
//
// The item is identified by the first external ID the provider keys
// submissions by, or else by TMDB, TVDB, then IMDb. The candidate query
// derives the same key in SQL; the migration that introduced the column
// derived only the second form.
func contributionTargetKey(ids ExternalIDs, keyedBy []string) string {
	id := ""
	values := ids.AsRequestMap()
	for _, key := range append(append([]string(nil), keyedBy...), ExternalIDKeyTMDB, ExternalIDKeyTVDB, ExternalIDKeyIMDB) {
		if value := strings.TrimSpace(values[key]); value != "" {
			id = key + ":" + value
			break
		}
	}
	return strings.Join([]string{itemTypeName(ids.Kind), id, strconv.Itoa(ids.SeasonNumber), strconv.Itoa(ids.EpisodeNumber)}, "|")
}

// submissionRequirements returns what the provider declares, or nothing for a
// provider without requirements. Its required IDs are the ones it keys
// submissions by.
func submissionRequirements(sub Submitter) SubmissionRequirements {
	provider, ok := sub.(SubmissionRequirementProvider)
	if !ok {
		return SubmissionRequirements{}
	}
	return provider.SubmissionRequirements()
}

func intPart(v int) string {
	if v == 0 {
		return ""
	}
	return strconv.Itoa(v)
}

func missingRequiredExternalIDs(sub Submitter, ids ExternalIDs) []string {
	required := submissionRequirements(sub).RequiredExternalIDs
	if len(required) == 0 {
		return nil
	}
	values := ids.AsRequestMap()
	var missing []string
	for _, key := range required {
		if strings.TrimSpace(values[key]) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}
