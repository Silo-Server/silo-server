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
// payload the file's current state no longer gives: a later save or
// detection replaced the marker, or the file's duration or IDs changed.
const (
	contributionInFlightReason   = "another submission for this item is in flight"
	contributionSupersededReason = "the file changed since this marker was read"
)

// ContributeOptions scopes a contribution run.
type ContributeOptions struct {
	// Provider restricts the run to a single provider id ("" = all enabled).
	Provider string
	// Segments restricts the run to specific kinds (nil = every present
	// segment and manual clear).
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
// save withheld, and neither bound moved by more than a second from any of
// its occurrences (models.EffectiveMarkerSegments).
func KeepsProviderMarker(shown *models.MediaFile, kind MarkerKind, start, end float64) bool {
	if shown == nil {
		return false
	}
	for _, seg := range fileSegments(shown) {
		if seg.kind != kind || seg.source == nil {
			continue
		}
		fromProvider := *seg.source == models.MarkerSourceOnline || *seg.source == models.MarkerSourcePlugin ||
			(seg.algorithm != nil && *seg.algorithm == ManualWithheldAlgorithm)
		return fromProvider && slices.ContainsFunc(models.EffectiveMarkerSegments(shown), func(o models.MarkerSegment) bool {
			return o.Kind == seg.name && math.Abs(o.StartSeconds-start) <= manualKeepTolerance && math.Abs(o.EndSeconds-end) <= manualKeepTolerance
		})
	}
	return false
}

// HoldsSharedManualMarker reports whether file holds a manual marker of kind
// with algorithm ManualAlgorithm. No online or plugin marker replaces it
// (CanWriteMarkerUpdate), so a player is shown the stored kind whatever an
// on-demand lookup returns. A withheld marker or a manual clear does not
// count: a player that loaded the file before the save that wrote one may
// still show a provider's marker. With on-demand lookups every save over
// anything else is withheld, so the kind can only hold a ManualAlgorithm
// marker now if it held one when any open editor loaded the file.
func HoldsSharedManualMarker(file *models.MediaFile, kind MarkerKind) bool {
	if file == nil {
		return false
	}
	for _, seg := range fileSegments(file) {
		if seg.kind == kind {
			return seg.start != nil && seg.end != nil && seg.source != nil && *seg.source == models.MarkerSourceManual &&
				seg.algorithm != nil && *seg.algorithm == ManualAlgorithm
		}
	}
	return false
}

// KeepsProviderAbsence reports whether a manual clear of kind repeats a
// provider's answer that the file has none of it: shown, the file as the user
// saw it before the clear, held an online or plugin tombstone of kind, or a
// manual clear an earlier save withheld. A manual save stores
// ManualWithheldAlgorithm on such a clear, as on a marker KeepsProviderMarker
// matches.
func KeepsProviderAbsence(shown *models.MediaFile, kind MarkerKind) bool {
	if shown == nil {
		return false
	}
	for _, seg := range fileSegments(shown) {
		if seg.kind != kind || seg.start != nil || seg.end != nil || seg.source == nil {
			continue
		}
		return *seg.source == models.MarkerSourceOnline || *seg.source == models.MarkerSourcePlugin ||
			(seg.algorithm != nil && *seg.algorithm == ManualWithheldAlgorithm)
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
func (s *ContributionService) AmendContributors(ctx context.Context) ([]AmendContributor, error) {
	if s == nil || s.config == nil {
		return nil, nil
	}
	configs, err := s.config.Load(ctx)
	if err != nil {
		return nil, err
	}
	var out []AmendContributor
	for _, sub := range s.submitters() {
		cfg, ok := configs[sub.ID()]
		if !ok || !cfg.ContributeEnabled {
			continue
		}
		if reqs := submissionRequirements(sub); reqs.SupportsAmend {
			out = append(out, AmendContributor{Provider: sub.ID(), SupportsAbsent: reqs.SupportsAbsent})
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
	source := ""
	if seg.source != nil {
		source = *seg.source
	}
	// A manual clear says the item has no segment of the kind, and goes to
	// providers that accept that. A detector that found nothing is no
	// evidence of absence, and stores no clear.
	absent := seg.start == nil && seg.end == nil && source == models.MarkerSourceManual
	reqs := submissionRequirements(sub)
	if absent && !reqs.SupportsAbsent {
		return ContributionOutcome{}, false
	}
	if !absent && (seg.start == nil || seg.end == nil) {
		return ContributionOutcome{}, false
	}
	// Only ever contribute our own data; never round-trip online markers.
	if source != models.MarkerSourceScanner && source != models.MarkerSourceManual {
		return ContributionOutcome{}, false
	}
	// Nor a manual marker that may repeat a provider's.
	if seg.algorithm != nil && *seg.algorithm == ManualWithheldAlgorithm {
		return ContributionOutcome{}, false
	}
	if opts.Corrections && (source != models.MarkerSourceManual || !reqs.SupportsAmend) {
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

	algorithm, confidence := row.Algorithm, 0.0
	if seg.confidence != nil {
		confidence = *seg.confidence
	}
	if absent {
		// A clear saved before clears carried provenance has none.
		algorithm, confidence = ManualAlgorithm, 1
	}
	req := SubmissionRequest{
		Kind:               ids.Kind,
		ExternalIDs:        ids.AsRequestMap(),
		SeasonNumber:       ids.SeasonNumber,
		EpisodeNumber:      ids.EpisodeNumber,
		EpisodeExternalIDs: ids.Episode.AsRequestMap(),
		EpisodeNumbering:   ids.EpisodeNumbering,
		EpisodeTitle:       ids.EpisodeTitle,
		Segment:            seg.kind,
		Duration:           time.Duration(file.Duration) * time.Second,
		FileHash:           file.FileHash,
		FileSize:           file.FileSize,
		Source:             source,
		Algorithm:          algorithm,
		Confidence:         confidence,
		Absent:             absent,
	}
	if !absent {
		req.Start = new(time.Duration(*seg.start * float64(time.Second)))
		req.End = new(time.Duration(*seg.end * float64(time.Second)))
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
// so the file alone is its target (amendTargetKey), and its hash covers
// everything the request identifies the episode and the file's bytes by, a
// new detection version, and a rescored confidence, each a change worth
// sending. currentAmendHashSQL
// derives the same amend hash in SQL, and CandidateLocalMarkerFiles the same
// keys.
func newContributionRow(sub Submitter, file *models.MediaFile, ids ExternalIDs, seg segmentData, source string) ContributionRow {
	reqs := submissionRequirements(sub)
	// A manual clear carries no times.
	var startMs, endMs *int64
	if seg.start != nil && seg.end != nil {
		startMs, endMs = new(int64(*seg.start*1000)), new(int64(*seg.end*1000))
	}
	durMs := int64(file.Duration) * 1000
	algorithm := ""
	if seg.algorithm != nil {
		algorithm = *seg.algorithm
	}
	hashParts := contributionTargetParts(ids)
	targetKey := contributionTargetKey(ids, reqs.RequiredExternalIDs)
	if reqs.SupportsAmend {
		hashParts = append(hashParts,
			ids.Episode.TmdbID, ids.Episode.ImdbID, ids.Episode.TvdbID, ids.EpisodeNumbering,
			file.FileHash, strconv.FormatInt(file.FileSize, 10),
			source, algorithm, confidenceHashPart(seg.confidence))
		targetKey = amendTargetKey(file.ID)
	}
	return ContributionRow{
		MediaFileID:      file.ID,
		Provider:         sub.ID(),
		SegmentKind:      seg.name,
		Source:           source,
		SubmittedStartMs: startMs,
		SubmittedEndMs:   endMs,
		VideoDurationMs:  &durMs,
		ContentHash:      ContentHash(seg.name, startMs, endMs, &durMs, hashParts...),
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

// amendTargetKey is the target of every claim an amend provider holds for a
// file's segment. It names no external ID, so a claim taken before a metadata
// change still holds the target and serializes the next submission behind it.
func amendTargetKey(fileID int) string {
	return "file:" + strconv.Itoa(fileID)
}

// contributionTargetKey names the provider-side item a segment is submitted
// against, independent of the submitted times. Most providers accept one
// submission per account for each item and segment and offer no way to amend
// it, so an active claim on this key blocks every later payload for the same
// item. A provider that supports amend uses amendTargetKey instead.
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
