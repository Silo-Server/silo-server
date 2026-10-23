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

// contributionInFlightReason is the skip reason for an amend target another
// worker's submission holds; the daily task sends the marker once that
// submission settles. contributionSupersededReason is the skip reason for a
// marker a later save replaced.
const (
	contributionInFlightReason   = "another submission for this item is in flight"
	contributionSupersededReason = "a later save replaced this marker"
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
	// Corrections marks the daily task's resend of manual markers: only a
	// manual marker is contributed, only to a provider that supports amend,
	// and only as a correction of an earlier submission for the file's segment
	// (see ContributionRow.Correction).
	Corrections bool
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
	confidence *float64
	algorithm  *string
}

func fileSegments(file *models.MediaFile) []segmentData {
	return []segmentData{
		{MarkerKindIntro, models.MarkerSegmentIntro, file.IntroStart, file.IntroEnd, file.IntroMarkersSource, file.IntroMarkersConfidence, file.IntroMarkersAlgorithm},
		{MarkerKindCredits, models.MarkerSegmentCredits, file.CreditsStart, file.CreditsEnd, file.CreditsMarkersSource, file.CreditsMarkersConfidence, file.CreditsMarkersAlgorithm},
		{MarkerKindRecap, models.MarkerSegmentRecap, file.RecapStart, file.RecapEnd, file.RecapMarkersSource, file.RecapMarkersConfidence, file.RecapMarkersAlgorithm},
		{MarkerKindPreview, models.MarkerSegmentPreview, file.PreviewStart, file.PreviewEnd, file.PreviewMarkersSource, file.PreviewMarkersConfidence, file.PreviewMarkersAlgorithm},
	}
}

// Manual marker algorithms. A manual save stores ManualWithheldAlgorithm
// instead of ManualAlgorithm for a kind that may repeat a provider's marker
// (KeepsProviderMarker). No contribution path ever sends such a marker, to any
// provider; everything else treats the two alike.
const (
	ManualAlgorithm         = "manual:v1"
	ManualWithheldAlgorithm = "manual:withheld:v1"
)

// manualKeepTolerance is how far, in seconds, each bound of a manual save may
// move from a marker and still count as keeping it.
const manualKeepTolerance = 1.0

// KeepsProviderMarker reports whether a manual save of kind from start to end
// keeps a provider's marker: shown, the file as the user saw it before the
// save, held an online or plugin marker of kind, or a manual one an earlier
// save withheld, and neither bound moved by more than a second.
func KeepsProviderMarker(shown *models.MediaFile, kind MarkerKind, start, end float64) bool {
	if shown == nil {
		return false
	}
	for _, seg := range fileSegments(shown) {
		if seg.kind != kind || seg.start == nil || seg.end == nil || seg.source == nil {
			continue
		}
		fromProvider := *seg.source == models.MarkerSourceOnline || *seg.source == models.MarkerSourcePlugin ||
			(seg.algorithm != nil && *seg.algorithm == ManualWithheldAlgorithm)
		return fromProvider && math.Abs(*seg.start-start) <= manualKeepTolerance && math.Abs(*seg.end-end) <= manualKeepTolerance
	}
	return false
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

// AmendContributors lists the registered submitters with contribution enabled
// that support amend. The daily task resends manual corrections to them
// whether or not they share local markers automatically. It reads the stored
// provider config; a failed read lists none.
func (s *ContributionService) AmendContributors(ctx context.Context) ([]string, error) {
	if s == nil || s.config == nil {
		return nil, nil
	}
	configs, err := s.config.Load(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, sub := range s.submitters() {
		if cfg, ok := configs[sub.ID()]; ok && cfg.ContributeEnabled && submissionRequirements(sub).SupportsAmend {
			out = append(out, sub.ID())
		}
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
			if outcome, attempted := s.contributeSegment(ctx, sub, providerID, cfg, file, ids, seg, opts); attempted {
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
	// Nor a manual marker that may repeat a provider's.
	if seg.algorithm != nil && *seg.algorithm == ManualWithheldAlgorithm {
		return ContributionOutcome{}, false
	}
	if opts.Corrections && (source != models.MarkerSourceManual || !submissionRequirements(sub).SupportsAmend) {
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

	row := newContributionRow(sub, file, ids, seg, source)
	row.Correction = opts.Corrections
	claim, claimed, err := s.store.Claim(ctx, row, contributionClaimLease)
	if errors.Is(err, errContributionNotCorrection) {
		return ContributionOutcome{}, false
	}
	if errors.Is(err, errContributionInFlight) {
		return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusSkipped, Reason: contributionInFlightReason}, true
	}
	if errors.Is(err, errContributionSuperseded) {
		return ContributionOutcome{Provider: providerID, Segment: seg.kind, Status: OutcomeStatusSkipped, Reason: contributionSupersededReason}, true
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
		Algorithm:          row.Algorithm,
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

// newContributionRow is the claim row for submitting seg's marker, from
// source, to sub. An amend provider replaces this file's earlier submission,
// so the file is its own target and a new detection version is a change worth
// sending. CandidateLocalMarkerFiles derives the same hash and key in SQL.
func newContributionRow(sub Submitter, file *models.MediaFile, ids ExternalIDs, seg segmentData, source string) ContributionRow {
	reqs := submissionRequirements(sub)
	startMs := int64(*seg.start * 1000)
	endMs := int64(*seg.end * 1000)
	durMs := int64(file.Duration) * 1000
	algorithm := ""
	if seg.algorithm != nil {
		algorithm = *seg.algorithm
	}
	hashParts := contributionTargetParts(ids)
	targetKey := contributionTargetKey(ids, reqs.RequiredExternalIDs)
	if reqs.SupportsAmend {
		hashParts = append(hashParts, source, algorithm)
		targetKey += "|file:" + strconv.Itoa(file.ID)
	}
	return ContributionRow{
		MediaFileID:      file.ID,
		Provider:         sub.ID(),
		SegmentKind:      seg.name,
		Source:           source,
		SubmittedStartMs: &startMs,
		SubmittedEndMs:   &endMs,
		VideoDurationMs:  &durMs,
		ContentHash:      ContentHash(seg.name, &startMs, &endMs, &durMs, hashParts...),
		TargetKey:        targetKey,
		Status:           contributionStatusClaim,
		Amend:            reqs.SupportsAmend,
		Algorithm:        algorithm,
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
