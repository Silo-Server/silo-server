package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/logredact"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

// errVirtualProviderUnavailable marks a resolve that failed because the
// provider's listing was transiently unavailable (empty answer, request
// failure, or 5XX) for a session-bound candidate the catalog still trusts. It
// is deliberately a retryable dependency condition, not a verdict about the
// release: the serve layer answers 503 provider_unavailable so hls.js keeps
// retrying the same pinned release instead of rotating to a sibling.
var errVirtualProviderUnavailable = errors.New("virtual provider listing temporarily unavailable")

// providerUnavailableReasonV3 is the retryable transport/terminal reason a
// classified provider outage surfaces under. It matches the serve layer's
// machine-readable code (stream.go writes the same string as a 503), so a
// v3 start and a byte serve describe the same transient dependence on the
// provider's listing.
const providerUnavailableReasonV3 = "provider_unavailable"

// virtualProviderOutageBackoff is the bounded wait schedule for a transient
// provider-listing failure on a session-bound candidate the catalog still
// trusts. Two retries add at most ~3s of latency, well under the serve and
// transport-startup budgets, and every wait observes request cancellation.
var virtualProviderOutageBackoff = []time.Duration{1 * time.Second, 2 * time.Second}

// virtualProviderListingOutage reports whether a virtual resolve failure is a
// transient provider-listing outage rather than a verdict about the release.
// Only the causes a live provider outage produces are named:
//   - resolver.ErrProviderUnavailable: the provider request failed or answered
//     5XX;
//   - ErrPersistedCandidateTrusted: the trusted persisted same-identity
//     candidate was absent from an empty/partial answer (the resolver refuses
//     to substitute, which is exactly the case that should be retried, not
//     rotated);
//   - ErrSessionBoundCandidateAbsent: a pinned candidate absent from the
//     current listing. Providers renumber their per-listing result ids, so an
//     absence can be a listing artifact rather than a dead release; a bounded
//     forced relist can re-identify the same release under its new id;
//   - the "no streams available from provider" message: an empty answer that
//     carried no pinned-candidate refusal (e.g. a neutral re-list).
//
// A genuine different-release substitution or an untrusted dead pin never
// reaches here because callers gate the retry on the trust predicate below. A
// genuine rotation verdict (an excluded, indicted pin) surfaces as a different
// error because substitution was allowed and the resolver substituted a
// sibling, so it is never turned into a retry loop.
func virtualProviderListingOutage(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, virtuallibrary.ErrTitleWithoutStreams) {
		return true
	}
	if errors.Is(err, resolver.ErrProviderUnavailable) {
		return true
	}
	if errors.Is(err, virtuallibrary.ErrPersistedCandidateTrusted) {
		return true
	}
	if errors.Is(err, virtuallibrary.ErrSessionBoundCandidateAbsent) {
		return true
	}
	return strings.Contains(err.Error(), "no streams available from provider")
}

// virtualAnchorProviderOutage reports whether a failed session-bound anchor
// resolve is a transient provider-listing outage that should surface as the
// retryable provider_unavailable rather than a transcode_start_failed verdict.
// It is deliberately narrower than virtualProviderListingOutage: an absent
// session pin (ErrSessionBoundCandidateAbsent) or a candidate the catalog
// marked failed is a verdict about the release, and the start path must still
// walk its alternates for it. Only the causes a live provider outage produces —
// the provider request failed/5XX, a trusted pin absent from an empty answer,
// or an empty listing — are classified here.
//
// Unlike classifyVirtualProviderOutage this does not gate on the durable-
// identity trust predicate: the remux seek anchor is the first live provider
// listing for a plan already bound by the probe-evidence P0 fast path, so a row
// without durable provider identity (which the evidence-gated outage retry
// cannot cover) must still surface the retryable dependency failure.
func virtualAnchorProviderOutage(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, virtuallibrary.ErrSessionBoundCandidateAbsent) || errors.Is(err, ErrVirtualCandidateMarkedFailed) {
		return false
	}
	return virtualProviderListingOutage(err)
}

// classifyVirtualAnchorProviderOutage wraps an anchor's transient provider
// outage so the start surfaces the retryable provider_unavailable instead of
// transcode_start_failed. It is a no-op for a release verdict (an absent pin or
// a marked-failed candidate) or a non-outage error, so the caller's alternate
// walk is preserved.
func classifyVirtualAnchorProviderOutage(err error) error {
	if !virtualAnchorProviderOutage(err) {
		return err
	}
	return fmt.Errorf("%w: %w", errVirtualProviderUnavailable, err)
}

// virtualProviderOutageRetries returns how many bounded forced relists a
// retryable provider-listing failure gets. A pinned id that is merely absent
// from the listing gets exactly one: providers renumber result ids per listing,
// so a single forced relist can re-identify the same release under its new id,
// while waiting out the full schedule would only delay a genuinely dead pin. A
// transient provider outage (5XX, empty listing) keeps the full backoff
// schedule.
func virtualProviderOutageRetries(err error) int {
	if errors.Is(err, virtuallibrary.ErrSessionBoundCandidateAbsent) {
		return min(1, len(virtualProviderOutageBackoff))
	}
	return len(virtualProviderOutageBackoff)
}

// virtualCandidateTrustedForOutageRetry reports whether a catalog row is the
// session-bound candidate a transient provider outage should be retried for:
// it carries durable provider identity plus transport evidence (a stored
// resolved URL or a recorded delivery) and is inside the candidate store
// window. A row with no evidence, no identity, or a lapsed window keeps the
// pre-existing behavior: the outage is reported, not retried.
func (h *PlaybackHandler) virtualCandidateTrustedForOutageRetry(row *models.MediaFile) bool {
	if h == nil {
		return false
	}
	return virtualCandidateTrustedForOutageRetry(row, h.virtualCandidateTrustWindow())
}

// virtualCandidateTrustedForOutageRetry is the StreamHandler counterpart of the
// playback handler's trust predicate. The window is read from the handler so
// both share one implementation.
func (h *StreamHandler) virtualCandidateTrustedForOutageRetry(row *models.MediaFile) bool {
	if h == nil {
		return false
	}
	return virtualCandidateTrustedForOutageRetry(row, h.virtualStoredURLTrustWindow())
}

func virtualCandidateTrustedForOutageRetry(row *models.MediaFile, window time.Duration) bool {
	if row == nil || window <= 0 {
		return false
	}
	if _, ok := persistedVirtualIdentity(row); !ok {
		return false
	}
	if strings.TrimSpace(row.ResolvedURL) == "" && row.LastDeliveredAt == nil {
		return false
	}
	return virtualCandidateWithinStoreWindow(row, time.Now(), window)
}

// virtualPendingHoldCap bounds one wait for an in-flight AltMount import
// (SABnzbd queue, not history). It matches the observed 1–10s fetch window:
// long enough for a progressing download to flip to completed, short enough
// that a stuck slot cannot pin playback.
const virtualPendingHoldCap = 10 * time.Second

// virtualPendingHoldReserve is the room the hold leaves for the retry
// resolve after the pause: one probe-class attempt plus scheduling margin.
// A caller with less than reserve plus a minimal pause degrades immediately
// instead of waiting away the retry budget.
const virtualPendingHoldReserve = 15 * time.Second

// virtualPendingLoopPause spaces startup-loop retries of a pending release
// so a lapsed input hold (low remaining budget) cannot spin fresh provider
// listings with no sleep.
const virtualPendingLoopPause = 1 * time.Second

// waitVirtualPendingHold pauses for an in-flight import, bounded by the hold
// cap and the caller's remaining budget. It reports whether the caller should
// re-list: false when the pause would consume the room for the retry resolve,
// in which case the caller degrades to the pending error immediately.
func waitVirtualPendingHold(ctx context.Context, virtualURI string, fileID int) bool {
	deadline, ok := ctx.Deadline()
	wait := virtualPendingHoldCap
	if ok {
		remaining := time.Until(deadline)
		wait = min(virtualPendingHoldCap, remaining-virtualPendingHoldReserve)
	}
	if wait <= 0 {
		return false
	}
	// The hold is the longest silent leg of a pending-provider start: the
	// resolver has already listed, so nothing logs again until the import
	// flips or the hold lapses. Name the release and the hold cap before
	// sleeping, and warn once when the full cap is consumed.
	waitLog := startVirtualResolveWaitLog(ctx, "fetch", "provider_import_hold", "", fileID, virtualURI,
		virtualPendingHoldCap, virtualPendingHoldCap)
	held := sleepWithContext(ctx, wait)
	waitLog.finish(ctx.Err())
	return held
}

// virtualServeResolveBudget bounds the one virtual candidate resolve a
// direct-play byte serve performs before it can open the relay. Planning has
// already spent its own, longer budget resolving and probing the candidate; a
// byte serve is on the media element's clock and cannot deliver a single byte
// until the resolve returns. One provider fetch is allotted
// backgroundRefreshTimeout (15s), so this bound leaves room for a listing that
// answers in time while refusing to hold the request through the whole outage
// retry schedule: in production that schedule spent a minute relisting an empty
// answer while the client waited, then aborted and recorded a fake 408. An
// expired budget is answered as the retryable provider_unavailable 503, so the
// player falls back or retries the route instead of hanging.
//
// It is a var so tests can shrink it and observe the bound.
var virtualServeResolveBudget = 20 * time.Second

// virtualServeResolveRetryAfterSeconds is the Retry-After a serve-budget
// expiry advertises. It matches the subtitle source's retry hint: long enough
// that a client backs off instead of re-opening the provider immediately, short
// enough that a recovered listing is not hidden.
const virtualServeResolveRetryAfterSeconds = 5

// resolveVirtualInputForServe resolves a virtual input for the direct-play byte
// serve under virtualServeResolveBudget. A budget expiry while the caller is
// still waiting is a provider-listing dependency failure, not a client
// cancellation: the caller's own context is live, so it is wrapped as
// errVirtualProviderUnavailable (retryable 503). A parent context that is done
// keeps the existing classification so a viewer who left is still recorded as
// a client cancellation.
func (h *StreamHandler) resolveVirtualInputForServe(ctx context.Context, file *models.MediaFile, userID int, profileID string) (ResolvedVirtualMedia, func(), error) {
	serveCtx, cancel := context.WithTimeout(ctx, virtualServeResolveBudget)
	defer cancel()
	resolved, cleanup, err := h.resolveVirtualInputURI(serveCtx, file, userID, profileID, false)
	if err == nil {
		return resolved, cleanup, nil
	}
	if cleanup != nil {
		cleanup()
	}
	if ctx.Err() != nil {
		return ResolvedVirtualMedia{}, nil, err
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(serveCtx.Err(), context.DeadlineExceeded) {
		return ResolvedVirtualMedia{}, nil, fmt.Errorf("%w: virtual serve resolve exceeded %s: %w", errVirtualProviderUnavailable, virtualServeResolveBudget, err)
	}
	return ResolvedVirtualMedia{}, nil, err
}

// retryVirtualProviderOutageResolve runs resolve, and while it fails with a
// transient provider-listing outage for a trusted session-bound row, retries
// with the bounded schedule. A canceled request stops immediately. Retry
// attempts are passed relist=true so the resolve closure forces a genuine
// re-list past the negative cache the outage just wrote, and marks the service
// resolve as an outage re-list. The stored-first resolve stays the first
// attempt, so a healthy persisted URL still wins with zero provider calls and a
// transient outage never swaps the viewer's release.
//
// The retry budget is error-shaped (see virtualProviderOutageRetries): a pinned
// id merely absent from the listing gets a single forced relist (a provider
// renumber is a listing artifact), while a transient provider outage keeps the
// full backoff schedule. A genuine rotation verdict never reaches this loop
// because the resolver substitutes a sibling instead of reporting the pin
// absent.
func retryVirtualProviderOutageResolve(
	ctx context.Context,
	trusted bool,
	resolve func(context.Context, bool) (ResolvedVirtualMedia, error),
	attrs ...any,
) (ResolvedVirtualMedia, error) {
	resolved, err := resolve(ctx, false)
	if !trusted {
		return resolved, err
	}
	for attempt := 0; err != nil && virtualProviderListingOutage(err) && attempt < virtualProviderOutageRetries(err); attempt++ {
		wait := virtualProviderOutageBackoff[attempt]
		// The fail-fast sentinel answered without contacting the provider, so
		// there is no spent round-trip to back off from: retry immediately and
		// let the forced relist (which bypasses the mark) prove recovery.
		if errors.Is(err, virtuallibrary.ErrTitleWithoutStreams) {
			wait = 0
		}
		// The pause plus the forced re-list between resolve rounds is otherwise
		// silent; observe the whole retry cycle once and name the attempt.
		cycleLog := startVirtualResolveWaitLog(ctx, "fetch", "provider_outage_retry", "", 0, "",
			wait, virtualProbeBudget, append(attrs, "attempt", attempt+1)...)
		waited := sleepWithContext(ctx, wait)
		if !waited {
			cycleLog.finish(ctx.Err())
			return resolved, err
		}
		slog.InfoContext(ctx, "virtual provider listing unavailable; retrying the session-bound candidate",
			"component", "api", "attempt", attempt+1, "retry_in_ms", wait.Milliseconds(), "error", logredact.SanitizeURLError(err))
		resolved, err = resolve(ctx, true)
		cycleLog.finish(err)
	}
	return resolved, err
}

// classifyVirtualProviderOutage wraps a final provider outage so the serve
// layer can answer 503 provider_unavailable instead of a permanent resolve
// error. It is a no-op for a non-outage error or an untrusted row.
func classifyVirtualProviderOutage(err error, trusted bool) error {
	if err == nil || !trusted || !virtualProviderListingOutage(err) {
		return err
	}
	return fmt.Errorf("%w: %w", errVirtualProviderUnavailable, err)
}

// transportStartFailureV3 classifies a local transport start failure. A
// virtual provider-listing outage on a release the catalog still trusts is a
// transient dependency failure, not a transcode fault: the transport start
// itself never ran FFmpeg, so reporting transcode_start_failed would tell the
// client the start is broken rather than that the provider should be retried.
// It maps to the retryable provider_unavailable reason the serve layer already
// answers 503 with, so a start and a byte serve agree. Every other cause keeps
// the caller's reason and message.
func transportStartFailureV3(cause error, fallback *transportErrorV3) *transportErrorV3 {
	if errors.Is(cause, errVirtualProviderUnavailable) {
		return &transportErrorV3{
			reason:    providerUnavailableReasonV3,
			message:   "The virtual source provider is temporarily unavailable.",
			retryable: true,
			cause:     cause,
		}
	}
	return fallback
}

// errVirtualSelectedReleaseCannotPlay marks a virtual start whose selected
// release could not be resolved and for which every substitute the fallback
// walk found would have contradicted the selection: a different runtime, or a
// poorer audio/subtitle menu. It is deliberately not a provider-outage sentinel:
// retrying does not make a track-losing substitute honest, so the terminal names
// the version list instead of serving different bytes.
var errVirtualSelectedReleaseCannotPlay = errors.New("the selected release cannot serve the selection")

// errVirtualSelectedReleaseUnverified marks a virtual start whose selected
// release was offered only bytes that did not establish its identity: the
// served size contradicted the declaration and the candidate's row belonged to
// a different release. It is retryable like an unavailable source, but it must
// not be answered with a substitute the viewer did not select.
var errVirtualSelectedReleaseUnverified = errors.New("the selected release was served by a different release")

// virtualVersionFallbackSubstitutionRejectionV3 names why a substitute the
// cold-start fallback walk resolved must not replace the selected release, or ""
// when the substitute is safe. It is the same policy #448 applies to a rotation,
// reused instead of a second one: a substitute must not contradict the item's
// runtime (duration_contradiction) or offer fewer audio or subtitle tracks than
// the selected row (track_loss). A substitute on the requested row itself, or a
// row with no measured evidence, is not rejected by absence of evidence.
func virtualVersionFallbackSubstitutionRejectionV3(expectedRuntimeMinutes int, selected, effective *models.MediaFile) string {
	if selected == nil || effective == nil || effective.ID <= 0 || effective.ID == selected.ID {
		return ""
	}
	switch {
	case !virtualRuntimePlausible(effective.Duration, expectedRuntimeMinutes):
		return "duration_contradiction"
	case virtualRotationWouldDropTracks(selected, effective, effective):
		return "track_loss"
	}
	return ""
}

// virtualStartUnresolvedTerminalV3 builds the honest terminal for a virtual
// start whose listing failed on every version the fallback walk could try. A
// provider request failure (the edge answered 5xx or the request timed out) is
// the retryable provider_unavailable dependency condition; an empty provider
// listing stays the retryable virtual_source_unavailable the start path already
// used, with a message that names the empty answer; a selected release whose
// every substitute would have lost the selection names the version list; every
// other cause keeps the generic resolve message so no verdict is fabricated from
// a failure the classifier cannot name.
func virtualStartUnresolvedTerminalV3(resolveErr error) playback.DecisionResponseV3 {
	switch {
	case errors.Is(resolveErr, errOfferedCandidateUnavailable):
		return playback.NewTerminalResponseV3("offered_release_unavailable", "The offered release is no longer available; refresh the version list and choose again.", true)
	case errors.Is(resolveErr, errVirtualSelectedReleaseCannotPlay):
		return playback.NewTerminalResponseV3(virtualSourceUnavailableReasonV3, "The selected version cannot serve its audio and subtitle tracks. Refresh the version list and choose another version.", true)
	case errors.Is(resolveErr, errVirtualSelectedReleaseUnverified):
		// The served bytes contradict the row the viewer picked. Report the
		// versions check's own vocabulary (source_mismatch) instead of the
		// generic unavailable, so the client can say "the provider served a
		// different release" rather than "this version is gone".
		return playback.NewTerminalResponseV3(sourceMismatchReasonV3, "The provider served a different release's bytes for this version. Refresh the version list and choose again.", true)
	case errors.Is(resolveErr, errEffectiveFileNotInVersionList):
		return playback.NewTerminalResponseV3(virtualSourceUnavailableReasonV3, "The selected version resolved to a release that is not in this item's version list. Refresh the version list and choose another version.", true)
	case errors.Is(resolveErr, errSelectedReleaseNotServableV3):
		// The provider answered with another release's candidate. The selected
		// version itself was never disproven, so this stays retryable and names
		// that version: a fresh start can resolve it again, and the viewer is
		// never told a different file's fate.
		return playback.NewTerminalResponseV3(virtualSourceUnavailableReasonV3, "The selected version could not be prepared from its own release. Try again, or choose another version.", true)
	case errors.Is(resolveErr, resolver.ErrProviderUnavailable):
		return playback.NewTerminalResponseV3(providerUnavailableReasonV3, "The virtual source provider is temporarily unavailable.", true)
	case strings.Contains(strings.ToLower(fmt.Sprint(resolveErr)), "no streams available"):
		return playback.NewTerminalResponseV3(virtualSourceUnavailableReasonV3, "The provider listed no streams for this title.", true)
	default:
		return playback.NewTerminalResponseV3(virtualSourceUnavailableReasonV3, "The virtual source could not be resolved for playback.", true)
	}
}

// virtualSubstitutionReasonV3 classifies why a fresh start resolved to a
// release other than the one the viewer requested, into the additive
// substitution_reason wire value. A transient provider-listing outage and a
// confirmed-dead pin are both listing failures; every other cause is left
// unknown rather than guessed from a message.
func virtualSubstitutionReasonV3(resolveErr error) string {
	switch {
	case errors.Is(resolveErr, ErrVirtualCandidateMarkedFailed):
		// The requested release is confirmed dead, not merely unreachable.
		return substitutionReasonDeadReleaseV3
	case virtualProviderListingOutage(resolveErr):
		return substitutionReasonListingFailedV3
	default:
		return substitutionReasonUnknownV3
	}
}

// sleepWithContext waits for d or returns false when ctx is canceled first.
func sleepWithContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
