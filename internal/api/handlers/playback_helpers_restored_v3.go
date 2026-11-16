// Restored playback helpers.

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/noderouting"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

func findCarryCandidateSessionV3(sessions []*playback.Session, profileID string, fileID int, clientIP, userAgent string) *playback.Session {
	var previous *playback.Session
	matches := 0
	for _, session := range sessions {
		if session == nil || session.ProfileID != profileID {
			continue
		}
		if session.MediaFileID != fileID && session.RequestedMediaFileID != fileID {
			continue
		}
		if clientIP != "" && session.ClientIP != "" && session.ClientIP != clientIP {
			continue
		}
		if userAgent != "" && session.ClientUserAgent != "" && session.ClientUserAgent != userAgent {
			continue
		}
		matches++
		if previous == nil || session.LastActivityAt.After(previous.LastActivityAt) {
			previous = session
		}
	}
	if matches != 1 {
		// Two live sessions from the same client identity are ambiguous (two
		// tabs, or devices behind one NAT); inheriting either one's pick can
		// silently change the other's audio. The client must replay its
		// selection or name the predecessor attempt explicitly.
		return nil
	}
	return previous
}

// remapCarriedAudioIndexV3 re-identifies an explicit audio selection on the
// target inventory. When the inventories are the same layout the ordinal is
// still valid and is reused directly; otherwise the container index is not an
// identity across files, so the strict signature/facts remap decides. Duplicate
// signatures remain ambiguous (-1) and report unavailable.

func remapCarriedAudioIndexV3(previous []models.AudioTrack, selectedIndex int, file *models.MediaFile) int {
	if file == nil {
		return -1
	}
	if playback.AudioLayoutsEqual(previous, file.AudioTracks) &&
		selectedIndex >= 0 && selectedIndex < len(file.AudioTracks) {
		return selectedIndex
	}
	return playback.RemapAudioSelectionIndexBySignature(previous, selectedIndex, file.AudioTracks)
}

func audioTracksFromInventoryV3(items []playback.AudioInventoryItemV3) []models.AudioTrack {
	if len(items) == 0 {
		return nil
	}
	tracks := make([]models.AudioTrack, len(items))
	for i, item := range items {
		tracks[i] = models.AudioTrack{
			Index:         item.Index,
			Title:         item.Title,
			EmbeddedTitle: item.EmbeddedTitle,
			Language:      item.Language,
			Languages:     item.Languages,
			Codec:         item.Codec,
			Layout:        item.Layout,
			Channels:      item.Channels,
		}
	}
	return tracks
}

type candidateEvaluationV3 struct {
	start           playback.StartRequestV3
	file            *models.MediaFile
	result          playback.PlannerResultV3
	frozenRecipe    playback.ExecutableRecipeV3
	transport       preparedTransportV3
	reservationHeld bool
	toneMapErr      error
}

type subtitleTrackIdentityV3 struct {
	source          string
	language        string
	codec           string
	forced          bool
	hearingImpaired bool
}

func sameSubtitleInventoryV3(a, b *models.MediaFile) bool {
	return playback.SubtitleLayoutsEqualIncludingExternal(
		a.SubtitleTracks, a.ExternalSubtitles, b.SubtitleTracks, b.ExternalSubtitles)
}

// sameMediaInventoryV3 reports whether two files carry identical audio and
// subtitle inventories. It is the "did the effective tracks actually move"
// predicate for a replan: a same-row candidate rotation keeps the file id but
// replaces the probed tracks, so id equality is not a sufficient no-op guard.

func appendWarningOnceV3(warnings []playback.DegradationWarningV3, warning playback.DegradationWarningV3) []playback.DegradationWarningV3 {
	if slices.ContainsFunc(warnings, func(existing playback.DegradationWarningV3) bool { return existing.Code == warning.Code }) {
		return warnings
	}
	return append(warnings, warning)
}

func virtualPinnedProfileRowV3(session *playback.Session, row *models.MediaFile) bool {
	if session == nil || row == nil {
		return false
	}
	pinned := strings.TrimSpace(session.VirtualSourceURI)
	rowPath := strings.TrimSpace(row.FilePath)
	if pinned == "" || rowPath == "" {
		return false
	}
	// The session pin must be the concrete candidate; the loaded row must be its
	// provider-neutral profile, not another concrete pick. A different concrete
	// result is a release substitution candidate and must keep re-resolving.
	if virtualResultCandidateID(pinned) == "" || virtualResultCandidateID(rowPath) != "" {
		return false
	}
	return sameVirtualProfileScope(rowPath, pinned)
}

func sessionHasVirtualProvenanceV3(session *playback.Session) bool {
	if session == nil {
		return false
	}
	return session.VirtualSubtitleEvidenceSet ||
		strings.TrimSpace(session.VirtualSubtitleEvidenceURI) != "" ||
		session.VirtualSubtitleEvidenceFileID > 0
}

func sessionProvenanceMatchesPinnedRowV3(ctx context.Context, session *playback.Session, row *models.MediaFile, fileResolver FilePathResolver) bool {
	if !sessionHasVirtualProvenanceV3(session) || row == nil {
		return false
	}
	bound := *row
	bound.FilePath = session.VirtualSourceURI
	return virtualEvidenceMatchesBoundFile(ctx, &bound, session, fileResolver)
}

func (h *PlaybackHandler) frozenSeekCarrierV3(ctx context.Context, session *playback.Session, record *playback.AttemptRecordV3, row *models.MediaFile) (*models.MediaFile, error) {
	if session == nil || record == nil || row == nil {
		return nil, errFrozenSeekEvidenceUnavailable
	}
	candidate := strings.TrimSpace(record.CurrentPlan.EffectiveVirtualURI)
	if candidate == "" {
		return nil, errFrozenSeekEvidenceUnavailable
	}
	rowNamesCandidate := sameVirtualCandidate(row.FilePath, candidate)
	// Evaluate the session's evidence exactly as the binder does, but against
	// the frozen candidate rather than the session's (possibly moved) pin.
	bound := *row
	bound.FilePath = candidate
	sessionEvidence := virtualEvidenceMatchesBoundFile(ctx, &bound, session, h.fileResolver)
	if !rowNamesCandidate && !sessionEvidence {
		return nil, errFrozenSeekEvidenceUnavailable
	}
	owner := 0
	if sameVirtualCandidate(session.VirtualSourceURI, candidate) {
		owner = session.VirtualSourceOwnerInstallationID
	}
	if owner <= 0 && sessionEvidence {
		owner = session.VirtualSubtitleEvidenceOwnerInstallationID
	}
	if owner <= 0 && rowNamesCandidate {
		owner = row.VirtualOwnerInstallationID
	}
	if owner <= 0 {
		return nil, errFrozenSeekEvidenceUnavailable
	}
	carrier := *row
	carrier.FilePath = candidate
	carrier.VirtualOwnerInstallationID = owner
	if !rowNamesCandidate {
		// The row describes another release: its inventory and probe stamp are
		// not evidence for the frozen candidate. Carry the session's retained
		// inventory for the candidate and drop the row's stamp so the state
		// writer cannot stamp the foreign inventory as verified under the
		// candidate's URI.
		carrier.AudioTracks = session.VirtualAudioTracks
		carrier.SubtitleTracks = session.VirtualSubtitleTracks
		carrier.ExternalSubtitles = session.VirtualExternalSubtitles
		carrier.ProbeUpdatedAt = nil
	}
	return carryValidatedVirtualVideoEvidenceV3(&carrier, record.CurrentPlan.Source), nil
}

func carryValidatedVirtualVideoEvidenceV3(file *models.MediaFile, source playback.SourceDescriptorV3) *models.MediaFile {
	if file == nil || !isVirtualPlaybackFile(file) {
		return file
	}
	if completeVirtualVideoEvidenceV3(file) {
		return file
	}
	if source.MediaFileID != file.ID || source.VideoCodec == "" ||
		source.BitDepth <= 0 || source.Width <= 0 || source.Height <= 0 ||
		source.FrameRate <= 0 || source.BitrateKbps <= 0 {
		return file
	}
	// Never mix a row's declared codec with descriptor evidence for a different
	// one. A row that declares h264 while the running plan validated hevc must
	// not receive the descriptor's geometry: that is franken-evidence the
	// planner would route against. Agreement uses the planner's own
	// normalization, so "h265"/"hevc" and "h264"/"avc" compare equal.
	if !virtualCodecAgreesV3(file, source.VideoCodec) {
		return file
	}
	carried := *file
	// Clone the video-track slice before the in-place top-up below. A shallow
	// struct copy shares the backing array, so writing carried.VideoTracks[0]
	// would mutate the caller's inventory and race any other reader of it.
	carried.VideoTracks = append([]models.VideoTrack(nil), file.VideoTracks...)
	if carried.CodecVideo == "" {
		carried.CodecVideo = source.VideoCodec
	}
	if carried.Container == "" || strings.EqualFold(carried.Container, "virtual") {
		if source.Container != "" {
			carried.Container = source.Container
		}
	}
	if carried.Bitrate == 0 {
		// MediaFile.Bitrate is the probe's kbps figure (scanner/probe.go stores
		// ffprobe bit_rate/1000), the same unit as
		// SourceDescriptorV3.BitrateKbps, so the assignment is unit-preserving.
		carried.Bitrate = source.BitrateKbps
	}
	if source.DynamicRange == playback.DynamicRangeHDRUnknownV3 {
		// Unknown HDR carries no classifying range text: tonemap's classifier
		// promotes any "hdr"-bearing VideoRangeType to HDR10, so the unknown
		// grade survives only through the file-level HDR flag with no range text
		// to classify. Setting it keeps the carried row's descriptor equal to
		// the running plan's.
		carried.HDR = true
	}
	if len(carried.VideoTracks) == 0 {
		carried.VideoTracks = []models.VideoTrack{virtualVideoTrackFromDescriptorV3(source)}
		return &carried
	}
	// The existing-track path fills the exact same field set the empty-track
	// reconstruction does — both call fillVirtualVideoTrackFromDescriptorV3 —
	// so a partial row is never left reading copy-safe on a source the running
	// plan proved is not.
	fillVirtualVideoTrackFromDescriptorV3(&carried.VideoTracks[0], source)
	return &carried
}

func virtualCandidateRotationRecordedV3(ctx context.Context, session *playback.Session, effectiveFile *models.MediaFile, fileResolver FilePathResolver) bool {
	if session == nil || effectiveFile == nil || !isVirtualPlaybackFile(effectiveFile) {
		return false
	}
	if session.VirtualSourceRevision != "" || session.VirtualSourceURI == "" {
		return false
	}
	return session.VirtualSubtitleEvidenceSet && !virtualEvidenceMatchesBoundFile(ctx, effectiveFile, session, fileResolver)
}

var errFrozenSeekEvidenceUnavailable = errors.New("the frozen seek candidate's own evidence is unavailable")

func virtualCodecAgreesV3(file *models.MediaFile, sourceCodec string) bool {
	if file == nil || strings.TrimSpace(sourceCodec) == "" {
		return false
	}
	if declared := strings.TrimSpace(file.CodecVideo); declared != "" &&
		!playback.CodecsAgreeV3(declared, sourceCodec) {
		return false
	}
	if len(file.VideoTracks) > 0 {
		if declared := strings.TrimSpace(file.VideoTracks[0].Codec); declared != "" &&
			!playback.CodecsAgreeV3(declared, sourceCodec) {
			return false
		}
	}
	return true
}

// virtualVideoTrackFromDescriptorV3 reconstructs the primary video track the
// running plan validated from its source descriptor. It builds an empty track
// and fills it through fillVirtualVideoTrackFromDescriptorV3, so the
// reconstruction and the in-place top-up of a partial row cover exactly the
// same field set: the fields the planner reads for route selection (codec,
// profile, level, bit depth, dimensions, frame rate, bitrate, color range, and
// the dynamic-range classification via the range-type text, with the DV profile
// carried explicitly) plus the aggregated copy-safety verdict. The session's DV
// pin is applied after the carry and re-overwrites the profile for a pinned
// row, so the two never disagree.
func virtualVideoTrackFromDescriptorV3(source playback.SourceDescriptorV3) models.VideoTrack {
	var track models.VideoTrack
	fillVirtualVideoTrackFromDescriptorV3(&track, source)
	return track
}

// virtualVideoRangeTypeFromDescriptorV3 maps a normalized descriptor grade back
// to the probe's range-type token, the spelling the planner's classifier reads.
// Unknown HDR (and any unrecognized grade) stays unclassifiable; the file-level
// HDR flag carries that grade.
func virtualVideoRangeTypeFromDescriptorV3(source playback.SourceDescriptorV3) string {
	switch source.DynamicRange {
	case playback.DynamicRangeDolbyVisionV3:
		return videoRangeDOVI
	case playback.DynamicRangeHDR10PlusV3:
		return "HDR10Plus"
	case playback.DynamicRangeHLGV3:
		return videoRangeHLG
	case playback.DynamicRangeHDR10V3:
		return videoRangeHDR10
	case playback.DynamicRangeSDRV3:
		return videoRangeSDR
	default:
		return ""
	}
}

// fillVirtualVideoTrackFromDescriptorV3 top-fills every gap in track from the
// descriptor the running plan validated. It is the single field list shared by
// the empty-track reconstruction and the existing-track top-up: a field the
// planner reads is either filled from the descriptor here or deliberately
// absent, never filled by one caller but not the other.
//
// A field that already carries a row value is left as-is; the carry only tops
// up the unprobed metadata a fresh listing row dropped and never overwrites
// evidence the row already has. The one deliberate exception is VideoCopyUnsafe,
// an aggregated runtime verdict that must propagate whenever the running plan
// proved it: reading it false would let a route the plan already disqualified as
// copy-unsafe look copy-safe.
func fillVirtualVideoTrackFromDescriptorV3(track *models.VideoTrack, source playback.SourceDescriptorV3) {
	if track == nil {
		return
	}
	if track.Codec == "" {
		track.Codec = source.VideoCodec
	}
	if track.Profile == "" {
		track.Profile = source.VideoProfile
	}
	if track.Level == 0 {
		track.Level = source.VideoLevel
	}
	if track.BitDepth == 0 {
		track.BitDepth = source.BitDepth
	}
	if track.Width == 0 {
		track.Width = source.Width
	}
	if track.Height == 0 {
		track.Height = source.Height
	}
	if virtualFrameRateV3(track.FrameRate) <= 0 {
		track.FrameRate = strconv.FormatFloat(source.FrameRate, 'f', -1, 64)
	}
	if track.Bitrate == 0 {
		// VideoTrack.Bitrate is the probe's kbps figure (scanner/probe.go
		// stores ffprobe bit_rate/1000), the same unit as
		// SourceDescriptorV3.BitrateKbps, so the assignment is unit-preserving.
		track.Bitrate = source.BitrateKbps
	}
	if strings.TrimSpace(track.ColorRange) == "" {
		track.ColorRange = source.ColorRange
	}
	if !track.HDR10Plus {
		track.HDR10Plus = source.HDR10Plus
	}
	if strings.TrimSpace(track.VideoRangeType) == "" {
		track.VideoRangeType = virtualVideoRangeTypeFromDescriptorV3(source)
	}
	// A DV base-layer provenance without a profile is not a DV classification
	// the route resolver can trust, so the whole DV block is gated on the
	// descriptor naming a profile — the same fail-closed posture the previous
	// reconstruction used. Within that, each sub-field is a gap fill.
	if source.DVProfile > 0 { //nolint:nestif // fillVirtualVideoTrackFromDescriptorV3 exceeds Vio's nestif complexity-4 limit; refactor tracked separately
		if track.DVProfile == 0 {
			track.DVProfile = source.DVProfile
		}
		if track.DVLevel == 0 {
			track.DVLevel = source.DVLevel
		}
		if track.DVBLCompatID == 0 && !track.DVBLCompatIDPresent {
			// Zero is a meaningful compatibility id — tonemap deliberately
			// returns no safe base for it — so a row that already knows it is
			// zero keeps it. Only a missing field may be filled: matching codecs
			// alone does not establish that the row's profile and compatibility
			// agree with the descriptor's.
			track.DVBLCompatID = source.DVBLCompatID
		}
		// The descriptor carries a single aggregated base-layer provenance
		// flag. Reproduce it only when it was proven; an unproven profile keeps
		// the three presence booleans false so the route resolution fails
		// closed instead of inventing a compatible base layer.
		if source.DVBaseLayerProven {
			track.DVConfigPresent = true
			track.DVBLCompatIDPresent = true
			track.DVBLPresent = true
		}
	}
	if strings.TrimSpace(track.DVEnhancementLayer) == "" {
		if layer := strings.TrimSpace(string(source.DVEnhancementLayer)); layer != "" {
			track.DVEnhancementLayer = layer
		}
	}
	if source.VideoCopyUnsafe {
		track.VideoCopyUnsafe = true
	}
}

func virtualReplanRotationExhaustedV3(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errVirtualRotationExhaustedV3) {
		return true
	}
	if errors.Is(err, resolver.ErrProviderUnavailable) {
		return true
	}
	if isVirtualCandidateDeadError(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no streams available from provider") ||
		strings.Contains(msg, "provider unavailable")
}

func classifyVirtualReplanExhaustionV3(initialVirtualErr error, candidateErrs []*candidateV3Error) *transportErrorV3 { //nolint:funlen,gocyclo // classifyVirtualReplanExhaustionV3 exceeds Vio's 100-line and complexity-30 limits; refactor tracked separately
	var joinedErr error
	if initialVirtualErr != nil {
		joinedErr = initialVirtualErr
	}
	var highestCandidate *candidateV3Error
	highestPriority := -1
	for _, ce := range candidateErrs {
		if ce != nil {
			if ce.Err != nil {
				joinedErr = errors.Join(joinedErr, ce.Err)
			} else if ce.Message != "" {
				joinedErr = errors.Join(joinedErr, errors.New(ce.Message))
			}
			p := candidateStagePriority(ce.Stage)
			if p > highestPriority {
				highestPriority = p
				highestCandidate = ce
			}
		}
	}

	if errors.Is(initialVirtualErr, context.DeadlineExceeded) && (highestCandidate == nil || highestCandidate.Stage == candidateStageResolve) {
		return &transportErrorV3{
			reason:    replanVirtualInputTimeoutReasonV3,
			message:   replanVirtualInputTimeoutMessageV3,
			retryable: true,
			cause:     joinedErr,
		}
	}
	for _, ce := range candidateErrs {
		if ce != nil && ce.Stage == candidateStageResolve && errors.Is(ce.Err, context.DeadlineExceeded) {
			if highestCandidate == nil || highestCandidate.Stage == candidateStageResolve {
				return &transportErrorV3{
					reason:    replanVirtualInputTimeoutReasonV3,
					message:   replanVirtualInputTimeoutMessageV3,
					retryable: true,
					cause:     joinedErr,
				}
			}
		}
	}

	// Route-capacity exhaustion is an honest, retryable verdict: the selected
	// workload has no route capacity right now, not a broken plan. Surface it
	// even when another candidate failed at a higher-priority stage first (for
	// example a canceled virtual re-resolve), because the client's correct
	// response is to retry. Without this the final reason can be a masking
	// transport error and the replan handler answers 500 internal_error
	// instead of a retryable 503, which stranded the HLS fallback in the field.
	for _, ce := range candidateErrs {
		if ce != nil && ce.TransportErr != nil &&
			ce.TransportErr.reason == string(noderouting.OutcomeCapacityUnavailable) {
			return &transportErrorV3{
				reason:    string(noderouting.OutcomeCapacityUnavailable),
				message:   "The playback route capacity is temporarily unavailable; retry shortly",
				retryable: true,
				cause:     joinedErr,
			}
		}
	}

	// A limit-provider blip is not a capacity limit. Replacement admission
	// fails open on it, but if it still surfaces (for example through a later
	// transcode-permission lookup) classify it as the transient dependency
	// failure it is instead of reporting capacity_unavailable or
	// transcoding_disabled.
	if highestCandidate != nil &&
		(highestCandidate.Stage == candidateStageAdmission || highestCandidate.Stage == candidateStageTranscodePerm) &&
		errors.Is(highestCandidate.Err, playback.ErrLimitProviderUnavailable) {
		return &transportErrorV3{
			reason:    "limit_provider_unavailable",
			message:   "Playback limits could not be checked. Please retry.",
			retryable: true,
			cause:     joinedErr,
		}
	}

	if highestCandidate != nil {
		if highestCandidate.TransportErr != nil {
			cloned := *highestCandidate.TransportErr
			cloned.cause = joinedErr
			return &cloned
		}
		switch highestCandidate.Stage {
		case candidateStageTransport:
			reason := highestCandidate.Reason
			if reason == "" {
				reason = transcodeStartFailedReasonV3
			}
			return &transportErrorV3{
				reason:    reason,
				message:   "Playback transport failed for all alternate media versions.",
				retryable: true,
				cause:     joinedErr,
			}
		case candidateStageSubtitleArtifact:
			return subtitleArtifactErrorV3("Failed to prepare the selected subtitle artifact.", joinedErr)
		case candidateStageSubtitleRemap:
			// A subtitle remap miss is a subtitle-local outcome. Reporting it
			// as virtual_source_unavailable would let the generic candidate
			// machinery treat a track that every compatible version lacks as
			// a failed media source. Name the subtitle instead so the client
			// can drop or re-pick it without a release swap.
			return subtitleArtifactErrorV3("The selected subtitle is unavailable in every compatible media version.", joinedErr)
		case candidateStageAudioRemap:
			// An audio remap miss is an audio-local outcome. The candidate's
			// audio inventory is what failed to bind, not its video stream.
			// Reporting virtual_source_unavailable would let the generic
			// candidate machinery indict the release for a track the file
			// lacks or the client sent malformed. Name the audio so the client
			// can pick another track without a release swap.
			return &transportErrorV3{
				reason:    trackUnavailableReasonV3,
				message:   "The selected audio track is unavailable in every compatible media version.",
				retryable: false,
				cause:     joinedErr,
			}
		case candidateStageTranscodePerm:
			return &transportErrorV3{
				reason:    transcodingDisabledCode,
				message:   "The required server adaptation is disabled for this user.",
				retryable: false,
				cause:     joinedErr,
			}
		case candidateStageAdmission:
			return &transportErrorV3{
				reason:    "capacity_unavailable",
				message:   "No playback capacity is available for alternate versions.",
				retryable: true,
				cause:     joinedErr,
			}
		}
	}

	return &transportErrorV3{
		reason:    virtualSourceUnavailableReasonV3,
		message:   "The virtual source could not be refreshed for playback.",
		retryable: true,
		cause:     joinedErr,
	}
}

func sameCandidateFailureDescriptorCarryV3(operation playback.ReplanOperationV3, session *playback.Session, record *playback.AttemptRecordV3, pinnedRowProvenance bool, resolved resolvedVirtualPlaybackSource, decodeRotation bool) bool {
	if !failureRecoveryOperationV3(operation) || session == nil || record == nil || resolved.File == nil {
		return false
	}
	if decodeRotation || !pinnedRowProvenance {
		return false
	}
	planURI := strings.TrimSpace(record.CurrentPlan.EffectiveVirtualURI)
	resolvedURI := strings.TrimSpace(resolved.URI)
	if planURI == "" || resolvedURI == "" {
		return false
	}
	// The running plan must still name the session's pin, and the resolver must
	// have returned that same candidate. Session provenance proves the retained
	// evidence describes the pinned row; it does not by itself prove the plan's
	// descriptor is for the same bytes, so both candidate checks are required.
	if !sameVirtualCandidate(session.VirtualSourceURI, planURI) ||
		!sameVirtualCandidate(resolvedURI, planURI) {
		return false
	}
	// An installation change is a different provider binding even when the
	// provider-neutral path is unchanged.
	return resolved.OwnerID > 0 && resolved.OwnerID == session.VirtualSourceOwnerInstallationID
}

// virtualCodecAgreesV3 reports whether every video-codec spelling the row
// already declares agrees with the descriptor's codec under the planner's
// normalization. An absent declaration is a gap and agrees by construction.
type candidateFailureStage string

const (
	candidateStageResolve          candidateFailureStage = "resolve"
	candidateStageAudioRemap       candidateFailureStage = "audio_remap"
	candidateStageSubtitleRemap    candidateFailureStage = "subtitle_remap"
	candidateStagePreflight        candidateFailureStage = "preflight"
	candidateStageNormalize        candidateFailureStage = "normalize"
	candidateStagePlan             candidateFailureStage = "plan"
	candidateStageAdmission        candidateFailureStage = "admission"
	candidateStageTranscodePerm    candidateFailureStage = "transcode_permission"
	candidateStageTransport        candidateFailureStage = "transport"
	candidateStageRecipe           candidateFailureStage = "recipe"
	candidateStageSubtitleArtifact candidateFailureStage = "subtitle_artifact"
)

type candidateV3Error struct {
	Stage          candidateFailureStage
	Reason         string
	Message        string
	TerminalReason string
	TransportErr   *transportErrorV3
	Err            error
}

func candidateStagePriority(s candidateFailureStage) int {
	switch s {
	case candidateStageSubtitleArtifact:
		return 10
	case candidateStageRecipe:
		return 9
	case candidateStageTransport:
		return 8
	case candidateStageTranscodePerm:
		return 7
	case candidateStageAdmission:
		return 6
	case candidateStagePlan:
		return 5
	case candidateStageNormalize:
		return 4
	case candidateStagePreflight:
		return 3
	case candidateStageAudioRemap, candidateStageSubtitleRemap:
		return 2
	case candidateStageResolve:
		return 1
	default:
		return 0
	}
}

func (e *candidateV3Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Err != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Stage, e.Message, logredact.SanitizeURLError(e.Err))
	}
	return fmt.Sprintf("[%s] %s", e.Stage, e.Message)
}

func (e *candidateV3Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// candidateSubtitleLocalFailureV3 reports whether a candidate was rejected for
// a subtitle reason rather than a video/transport one. Such a candidate is held
// as the subtitle-degrade target: dropping the subtitle and playing its video
// changes the release less than skipping it for a sibling that also cannot
// honor the selection. A video/transport rejection is deliberately excluded so
// the genuine failover paths are untouched.
func candidateSubtitleLocalFailureV3(err *candidateV3Error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errSubtitleUnavailableInTargetV3) {
		return true
	}
	return err.Stage == candidateStageSubtitleArtifact
}

// virtualReplanRotationExhaustedV3 reports whether a failed virtual replan
// rehydration has already proved that no provider candidate can serve the
// session's release. The rotation retry marks its own failure, and a
// provider-wide outage or a candidate walk that produced no usable stream
// carries the same meaning. A release verdict (an absent pin, a marked-failed
// candidate) is deliberately not exhaustion: rotation may have found a
// genuinely different release and refused to substitute it, which leaves the
// sibling-edition walk as the correct recovery for an auto selection.

func (h *PlaybackHandler) evaluateReplanCandidateV3(
	r *http.Request,
	session *playback.Session,
	record *playback.AttemptRecordV3,
	req playback.ReplanRequestV3,
	baseStart playback.StartRequestV3,
	sourceFile *models.MediaFile,
	candidateAlternate *models.MediaFile,
	plannerRequestedFile *models.MediaFile,
	plannerSettings playback.PlannerSettingsV3,
	plannerSettingsErr error,
	attemptedKeys []string,
	lowerVersion *playback.LowerVersionV3,
) (*candidateEvaluationV3, *candidateV3Error) {
	if candidateAlternate == nil {
		return nil, &candidateV3Error{Stage: candidateStageResolve, Message: "nil candidate alternate"}
	}
	candidateFile, candidateProvenance, err := h.prepareVirtualAlternateFileV3(r, candidateAlternate, record.ProfileID, false)
	if err != nil || candidateFile == nil {
		if err == nil {
			err = errors.New("candidate alternate unavailable")
		}
		return nil, &candidateV3Error{Stage: candidateStageResolve, Message: fmt.Sprintf("candidate %d resolution failed", candidateAlternate.ID), Err: err}
	}
	return h.evaluatePreparedReplanCandidateV3(r, session, record, req, baseStart, sourceFile, candidateFile, candidateProvenance, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, false, lowerVersion)
}

func (h *PlaybackHandler) evaluatePreparedReplanCandidateV3( //nolint:funlen // evaluatePreparedReplanCandidateV3 exceeds Vio's 100-line limit; refactor tracked separately
	r *http.Request,
	session *playback.Session,
	record *playback.AttemptRecordV3,
	req playback.ReplanRequestV3,
	baseStart playback.StartRequestV3,
	sourceFile *models.MediaFile,
	candidateFile *models.MediaFile,
	candidateProvenance ProbeProvenance,
	plannerRequestedFile *models.MediaFile,
	plannerSettings playback.PlannerSettingsV3,
	plannerSettingsErr error,
	attemptedKeys []string,
	planOnly bool,
	lowerVersion *playback.LowerVersionV3,
) (*candidateEvaluationV3, *candidateV3Error) {
	candidateStart := baseStart
	// A carried selection was minted against the plan-time inventory. Use the
	// session's captured evidence as the remap source for a virtual session:
	// the loaded candidate row may have been re-probed in place (same id,
	// different release), in which case source==target by id but the ordinal
	// still names the previous release's track.
	remapSource := h.replanRemapSourceV3(r.Context(), sourceFile, session, nil).file
	if err := remapAudioSelectionV3(remapSource, candidateFile, &candidateStart); err != nil {
		return nil, &candidateV3Error{Stage: candidateStageAudioRemap, Message: fmt.Sprintf("candidate %d audio remap failed", candidateFile.ID), Err: err}
	}
	candidateAudioIndex, _, err := resolveV3AudioIndex(candidateFile, candidateStart.AudioTrackID, candidateStart.AudioTrackIndex)
	if err != nil {
		return nil, &candidateV3Error{Stage: candidateStageAudioRemap, Message: fmt.Sprintf("candidate %d audio track resolve failed", candidateFile.ID), Err: err}
	}
	var candidateResult playback.PlannerResultV3
	var candidateToneMapErr error
	// A carried selection already naming this candidate was minted against
	// this file's inventory: remap from the candidate itself rather than
	// from the nominal source, whose inventory may describe a different
	// edition (e.g. a speculative detour that failed). Restricted to
	// non-virtual files, where a stable row id means a stable release;
	// virtual rows rotate releases under a fixed id, so they keep the
	// evidence-anchored remap above.
	subtitleRemapSource := remapSource
	if subtitleRemapSource == nil {
		subtitleRemapSource = candidateFile
	}
	if selFileID, selKind, _, selOK := playback.ParseTrackIDV3(candidateStart.SubtitleTrackID); selOK && selKind == subtitleTrackKindV3 && selFileID == candidateFile.ID && !isVirtualPlaybackFile(candidateFile) {
		subtitleRemapSource = candidateFile
	}
	if _, err := h.remapSubtitleSelectionV3(r.Context(), subtitleRemapSource, candidateFile, &candidateStart); err != nil {
		candidateResult = playback.PlannerResultV3{Terminal: &playback.TerminalV3{
			Reason:    terminalSubtitleUnavailableInVersionV3,
			Message:   "The selected subtitle track is unavailable in the fallback media version.",
			Retryable: false,
		}}
		return &candidateEvaluationV3{
			file:       candidateFile,
			start:      candidateStart,
			result:     candidateResult,
			toneMapErr: candidateToneMapErr,
		}, &candidateV3Error{Stage: candidateStageSubtitleRemap, TerminalReason: terminalSubtitleUnavailableInVersionV3, Message: fmt.Sprintf("candidate %d subtitle remap failed", candidateFile.ID), Err: err}
	} else {
		candidateStart.FileID = candidateFile.ID
		if _, err := candidateStart.NormalizeAndValidate(); err != nil {
			return nil, &candidateV3Error{Stage: candidateStageNormalize, Message: fmt.Sprintf("candidate %d normalize failed", candidateFile.ID), Err: err}
		}
		if err := preflightPlaybackFile(r.Context(), candidateFile, h.MissingMarker, h.EventsHub); err != nil {
			return nil, &candidateV3Error{Stage: candidateStagePreflight, Message: fmt.Sprintf("candidate %d preflight failed", candidateFile.ID), Err: err}
		}
		candidateResult, candidateToneMapErr = h.planPlaybackWithCapabilitiesV3(r.Context(), playback.PlannerInputV3{
			Request: candidateStart, RequestedFile: plannerRequestedFile, EffectiveFile: candidateFile,
			LowerVersion:    lowerVersionForFileV3(lowerVersion, candidateFile),
			AudioTrackIndex: candidateAudioIndex, Settings: plannerSettings, Registry: h.transformationRegistryV3(r.Context()),
			DVRPUStrippable: h.lazyDVRPUStrippableV3(r.Context(), candidateFile), Now: time.Now(),
			AttemptedKeys: attemptedKeys, AdditionalSubtitles: h.downloadedSubtitleInventoryV3(r.Context(), candidateFile),
			InventoryProvenance: string(candidateProvenance),
		})
	}
	clampPlannerTargetResolution(&candidateResult, candidateFile)
	candidateResult = retryIncompleteToneMapPlanningV3(candidateResult, candidateToneMapErr)
	candidateResult = retryIncompletePlaybackSettingsV3(candidateResult, plannerSettingsErr)
	if candidateResult.Terminal != nil {
		return &candidateEvaluationV3{
			file:       candidateFile,
			start:      candidateStart,
			result:     candidateResult,
			toneMapErr: candidateToneMapErr,
		}, &candidateV3Error{Stage: candidateStagePlan, TerminalReason: candidateResult.Terminal.Reason, Message: fmt.Sprintf("candidate %d planner terminal %s: %s", candidateFile.ID, candidateResult.Terminal.Reason, candidateResult.Terminal.Message)}
	}

	candidateResult.Plan.SessionID = session.ID
	var candidateReservationHeld bool
	if checker, ok := h.sessionMgr.(replacementAdmissionCheckerV3); ok {
		if err := checker.CheckReplacementAllowed(r.Context(), session.ID, candidateResult.PlayMethod, candidateResult.TranscodeAudio); err != nil {
			return nil, &candidateV3Error{Stage: candidateStageAdmission, Message: fmt.Sprintf("candidate %d replacement admission denied", candidateFile.ID), Err: err}
		}
		_, candidateReservationHeld = h.sessionMgr.(replacementReservationCancellerV3)
	}
	cancelCandidateReservation := func() {
		if candidateReservationHeld {
			if canceller, ok := h.sessionMgr.(replacementReservationCancellerV3); ok {
				canceller.CancelReplacementReservation(session.ID)
			}
			candidateReservationHeld = false
		}
	}

	if checker, ok := h.sessionMgr.(transcodePermissionChecker); ok && (candidateResult.PlayMethod == playback.PlayTranscode || candidateResult.TranscodeAudio) {
		if err := checker.CheckTranscodingAllowed(r.Context(), session.UserID, candidateResult.PlayMethod == playback.PlayTranscode); err != nil {
			cancelCandidateReservation()
			return nil, &candidateV3Error{Stage: candidateStageTranscodePerm, Message: fmt.Sprintf("candidate %d transcoding disallowed", candidateFile.ID), Err: err}
		}
	}

	if planOnly {
		// A same-release subtitle drop leaves the A/V recipe byte-identical;
		// the caller reuses the active transport, so preparing a probe one
		// would only spawn and wait on a second FFmpeg.
		return &candidateEvaluationV3{
			file:       candidateFile,
			start:      candidateStart,
			result:     candidateResult,
			toneMapErr: candidateToneMapErr,
		}, nil
	}
	candTransport, transportErr := h.prepareTransportV3(r, session, candidateFile, candidateResult, headerAuthenticatedMediaV3(req.ClientFeatures))
	if transportErr != nil {
		cancelCandidateReservation()
		errMsg := transportErr.message
		if transportErr.cause != nil {
			errMsg = fmt.Sprintf("%s (%v)", transportErr.message, logredact.SanitizeURLError(transportErr.cause))
		}
		return nil, &candidateV3Error{Stage: candidateStageTransport, Reason: transportErr.reason, Message: fmt.Sprintf("candidate %d transport preparation failed (%s): %s", candidateFile.ID, transportErr.reason, errMsg), TransportErr: transportErr, Err: transportErr.cause}
	}

	applyTransportToneMapModeV3(&candidateResult, candTransport)
	candidateFrozenRecipe, frozenErr := h.freezeExecutableRecipeV3(r.Context(), candidateFile, candidateResult)
	if frozenErr != nil {
		candTransport.rollback()
		cancelCandidateReservation()
		return nil, &candidateV3Error{Stage: candidateStageRecipe, Message: fmt.Sprintf("candidate %d freeze recipe failed", candidateFile.ID), Err: frozenErr}
	}

	candidateResult.Plan.Stream.URL = candTransport.url
	if err := h.attachSubtitleArtifactV3(r.Context(), session.ID, candidateFile, candidateResult.Plan, candidateResult.SubtitleTrackIndex, &candidateFrozenRecipe, req.ClientFeatures); err != nil {
		candTransport.rollback()
		cancelCandidateReservation()
		return nil, &candidateV3Error{Stage: candidateStageSubtitleArtifact, Message: fmt.Sprintf("candidate %d attach subtitle artifact failed", candidateFile.ID), Err: err}
	}

	return &candidateEvaluationV3{
		start:           candidateStart,
		file:            candidateFile,
		result:          candidateResult,
		frozenRecipe:    candidateFrozenRecipe,
		transport:       candTransport,
		reservationHeld: candidateReservationHeld,
		toneMapErr:      candidateToneMapErr,
	}, nil
}

func (h *PlaybackHandler) evaluateSubtitleDegradedCandidateV3(
	r *http.Request,
	session *playback.Session,
	record *playback.AttemptRecordV3,
	req playback.ReplanRequestV3,
	baseStart playback.StartRequestV3,
	sourceFile *models.MediaFile,
	candidateAlternate *models.MediaFile,
	plannerRequestedFile *models.MediaFile,
	plannerSettings playback.PlannerSettingsV3,
	plannerSettingsErr error,
	attemptedKeys []string,
	lowerVersion *playback.LowerVersionV3,
) (*candidateEvaluationV3, *candidateV3Error) {
	degradedStart := subtitleDegradedStartV3(baseStart)
	eval, evalErr := h.evaluateReplanCandidateV3(r, session, record, req, degradedStart, sourceFile, candidateAlternate, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, lowerVersion)
	if eval != nil {
		annotateSubtitleDroppedV3(&eval.result)
	}
	return eval, evalErr
}

func (h *PlaybackHandler) evaluateSubtitleDegradeInPlaceV3(
	r *http.Request,
	session *playback.Session,
	record *playback.AttemptRecordV3,
	req playback.ReplanRequestV3,
	baseStart playback.StartRequestV3,
	file *models.MediaFile,
	provenance ProbeProvenance,
	plannerRequestedFile *models.MediaFile,
	plannerSettings playback.PlannerSettingsV3,
	plannerSettingsErr error,
	attemptedKeys []string,
) (*candidateEvaluationV3, *candidateV3Error) {
	if file == nil {
		return nil, &candidateV3Error{Stage: candidateStageResolve, Message: "nil same-release subtitle degrade target"}
	}
	degradedStart := subtitleDegradedStartV3(baseStart)
	eval, evalErr := h.evaluatePreparedReplanCandidateV3(r, session, record, req, degradedStart, file, file, provenance, plannerRequestedFile, plannerSettings, plannerSettingsErr, attemptedKeys, true, nil)
	if evalErr != nil || eval == nil || eval.result.Terminal != nil || eval.result.Plan == nil {
		return eval, evalErr
	}
	if eval.result.Plan.Delivery == playback.DeliveryRemuxProgressiveV3 {
		// The same-release plan may be a progressive remux the API cannot run.
		// Escalate it before adopting it; a failed escalation means this
		// release cannot play without the subtitle either, so the caller can
		// try a playable sibling.
		audioIndex, _, indexErr := resolveV3AudioIndex(file, eval.start.AudioTrackID, eval.start.AudioTrackIndex)
		if indexErr != nil {
			return nil, &candidateV3Error{Stage: candidateStageAudioRemap, Message: "subtitle degrade audio track resolve failed", Err: indexErr}
		}
		input := h.plannerInputV3(r.Context(), eval.start, plannerRequestedFile, file, audioIndex, attemptedKeys, provenance)
		escalated, escalateErr := h.escalateRefusedProgressiveRemuxV3(
			r.Context(), headerAuthenticatedMediaV3(eval.start.ClientFeatures),
			func() playback.PlannerInputV3 { return input }, eval.result)
		if escalateErr != nil {
			return nil, &candidateV3Error{Stage: candidateStageTransport, Reason: escalateErr.reason, Message: escalateErr.message, TransportErr: escalateErr, Err: escalateErr.cause}
		}
		eval.result = escalated
	}
	eval.result.Plan.DegradationWarnings = append(eval.result.Plan.DegradationWarnings, playback.SubtitleNotShownWarningV3())
	return eval, evalErr
}

func subtitleDegradedStartV3(baseStart playback.StartRequestV3) playback.StartRequestV3 {
	degradedStart := baseStart
	degradedStart.SubtitleTrackIndex = nil
	degradedStart.SubtitleTrackID = ""
	return degradedStart
}

func (h *PlaybackHandler) replanRemapSourceV3(ctx context.Context, effectiveFile *models.MediaFile, session *playback.Session, durable map[int]predecessorPlanInventoryV3) carriedInventorySourceV3 {
	if effectiveFile == nil {
		return carriedInventorySourceV3{}
	}
	// Remapping needs the inventory the carried ordinal was minted against,
	// not the inventory serving would accept. Captured evidence is that
	// inventory even when it names a different candidate or the session has no
	// live row id; anchor the re-key on the effective row so its evidence path
	// is used instead of the reloaded row's current tracks.
	rekeySession := session
	if session != nil && session.VirtualSubtitleEvidenceSet && isVirtualPlaybackFile(effectiveFile) {
		copied := *session
		copied.MediaFileID = effectiveFile.ID
		copied.VirtualSubtitleEvidenceFileID = effectiveFile.ID
		rekeySession = &copied
	}
	carry := h.rekeySourceFileWithInventoryV3(ctx, effectiveFile.ID, rekeySession, durable)
	if carry.file == nil {
		// No evidence and no catalog row to load: the loaded row is still the
		// only description available.
		carry.file = effectiveFile
	}
	return carry
}

func sameMediaInventoryV3(a, b *models.MediaFile) bool {
	if a == nil || b == nil {
		return a == b
	}
	return sameSubtitleInventoryV3(a, b) && playback.AudioLayoutsEqual(a.AudioTracks, b.AudioTracks)
}
