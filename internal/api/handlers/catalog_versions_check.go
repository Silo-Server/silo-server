package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/Silo-Server/silo-server/internal/access"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/catalog"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

const (
	// maxVersionCheckFiles caps the batch so a single request cannot fan out
	// unbounded provider work.
	maxVersionCheckFiles = 40
	// versionCheckConcurrency bounds simultaneous provider resolutions.
	versionCheckConcurrency = 4
	// versionCheckPerFileBudget bounds one file's provider resolution.
	versionCheckPerFileBudget = 1 * time.Second
	// versionCheckOverallBudget bounds the whole batch.
	versionCheckOverallBudget = 2 * time.Second
	// versionCheckWriteBudget bounds one post-verdict write: a stamp, a
	// recovery clear, the post-write reload, or a rematch adoption. These run
	// after the request's own deadline has passed, so they must carry their
	// own bound instead of an unbounded background context.
	versionCheckWriteBudget = 500 * time.Millisecond
	// versionCheckPostBudget bounds all post-processing for the batch. It is
	// one absolute deadline rather than a per-row allowance, so a blocked
	// database cannot extend the handler proportionally to the batch size.
	versionCheckPostBudget = 1 * time.Second
	// versionCheckPreprobeBudget bounds the single-file inventory mode's
	// synchronous inventory enumeration. It is separate from the liveness
	// budgets above because that mode is the only one that waits for provider
	// bytes: the detached selection chain runs the same probe under its own
	// 90 s chain budget, and a client that opted in is better served by
	// declared metadata after 5 s than by an unbounded wait.
	versionCheckPreprobeBudget = 5 * time.Second
)

// The single-file inventory mode reports how much of a version's track
// inventory is evidence rather than registration metadata. These values are
// part of the check's wire contract.
const (
	// VersionInventoryDeclared means the row carries provider-declared hints
	// only; nothing has enumerated its bytes.
	VersionInventoryDeclared = "declared"
	// VersionInventoryPending means an inventory probe ran for this read but
	// no committed evidence reached the row yet, so the tracks below are still
	// the declared placeholders.
	VersionInventoryPending = "pending"
	// VersionInventoryVerified means a probe enumerated the row and its
	// evidence is stamped on it.
	VersionInventoryVerified = "verified"
)

// versionCheckDetached returns a context for post-verdict work that must
// survive client cancellation but must not run unbounded. When the parent context
// carries a deadline (such as versionCheckOverallBudget or versionCheckPostBudget),
// that absolute deadline is strictly honored so all writes together cannot
// exceed the budget.
func versionCheckDetached(ctx context.Context) (context.Context, context.CancelFunc) {
	detached := context.WithoutCancel(ctx)
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		timeout := versionCheckWriteBudget
		if remaining < timeout {
			timeout = remaining
		}
		if timeout <= 0 {
			cancelledCtx, cancel := context.WithCancel(detached)
			cancel()
			return cancelledCtx, cancel
		}
		return context.WithTimeout(detached, timeout)
	}
	return context.WithTimeout(detached, versionCheckWriteBudget)
}

type versionCheckRequest struct {
	FileIDs []int `json:"file_ids"`
	// Preprobe asks for the single file's enumerated track inventory in the
	// same answer. The v1 route is frozen and stays liveness-only.
	Preprobe bool `json:"preprobe,omitempty"`
}

// VersionCheckResult is one file's liveness verdict. It is the shared seam
// answer behind the v1 route and the v2 checkCatalogVersions operation.
type VersionCheckResult struct {
	FileID    int  `json:"file_id"`
	Available bool `json:"available"`
	// Failed is true when the row carries a durable failed verdict (either
	// from before this round or stamped by it). Unlike available=false,
	// which can mean a transient outage, failed means the release is
	// confirmed unservable: menus remove the row instead of offering a
	// retry-able pick.
	Failed bool `json:"failed,omitempty"`
	// SourceMismatch reports the byte verdict of a recent probe that rejected
	// the provider's bytes as a different release than the row declares. It is
	// additive and transient: it is present only while the process still holds
	// the short wrong-source marker (no database column). A mismatch never
	// means the release is dead, so it never travels with failed and never
	// changes available.
	SourceMismatch *VersionCheckSourceMismatch `json:"source_mismatch,omitempty"`
	// Inventory is present only in the single-file inventory mode
	// (CheckVersionsWithInventory). It carries the row's enumerated tracks and
	// how much of them is probe evidence rather than declared metadata. It is a
	// pointer so the batched answer keeps its exact previous shape and value
	// semantics.
	Inventory *VersionCheckInventory `json:"inventory,omitempty"`
}

// VersionCheckInventory is the track inventory the versions check's single-file
// mode returns for the requested row.
type VersionCheckInventory struct {
	// Provenance says whether the tracks below are probe evidence (verified),
	// an attempted probe with no committed evidence yet (pending), or the
	// row's declared registration metadata (declared).
	Provenance string `json:"inventory_provenance"`
	// ProbedAt is when a probe last enumerated this row's real stream table.
	ProbedAt *time.Time `json:"probed_at,omitempty"`
	// The track lists are the same projections the version rows carry, so a
	// client can apply them without reshaping.
	VideoTracks    []models.VideoTrack            `json:"video_tracks,omitempty"`
	AudioTracks    []models.AudioTrack            `json:"audio_tracks,omitempty"`
	SubtitleTracks []catalog.VersionSubtitleTrack `json:"subtitle_tracks,omitempty"`
}

// VersionCheckSourceMismatch carries the served-vs-declared byte sizes of a
// rejected probe so the version list can render a distinct source-mismatch
// state instead of the generic unprobed/retry presentation.
type VersionCheckSourceMismatch struct {
	ServedBytes   int64 `json:"served_bytes"`
	DeclaredBytes int64 `json:"declared_bytes"`
}

type versionCheckResponse struct {
	Results []VersionCheckResult `json:"results"`
}

// versionCheckOutcome is one file's liveness verdict before stamps are
// applied. Resolution and classification run concurrently per file, but
// stamps apply only after the whole batch is judged (see
// gateVersionCheckStamps): one volatile listing must never mass-tag
// versions as dead.
type versionCheckOutcome struct {
	fileID int
	// file is the resolved row, carried so the deferred stamp can fence on
	// its identity and observed verdict. Nil when the file was not found.
	file *models.MediaFile
	// available is the per-file answer: durable state when the round is
	// ambiguous or gated, live resolution otherwise.
	available bool
	// stamp is true when this round found the pin dead.
	stamp bool
	// durableAlive mirrors the row's pre-round failed_at (true when the row
	// carried no verdict): the answer to report when stamping is gated off.
	durableAlive bool
	// checked is true when the file got a provider resolution attempt, the
	// only outcomes that speak to listing health. Denied, unknown, local,
	// and unwired files report without resolving and stay out of the gate
	// denominator.
	checked bool
	// sourceMismatch carries a recent rejected probe's byte verdict for this
	// row. It is advisory only: it never sets stamp, never changes available,
	// and never marks the row failed.
	sourceMismatch *VersionCheckSourceMismatch
}

const (
	// minVersionCheckStampQuorum is the minimum dead-pin count that can trip
	// the batch gate. Below it every stamp applies: a lone genuinely-dead
	// pin (the common single-file check) must still be recorded.
	minVersionCheckStampQuorum = 4
	// versionCheckStampDisagreementRate is the dead-pin fraction above which
	// the listing itself is distrusted instead of the pins. Past it the
	// round reports durable state and stamps nothing: a renumber storm or a
	// degraded partial listing disagrees with everything at once, and that
	// pattern means the observation is bad, not forty releases at once.
	// The comparison is strictly past the rate: an exact-half split still
	// stamps, deciding ties in favor of recording genuinely-dead pins
	// rather than protecting against a volatile listing.
	versionCheckStampDisagreementRate = 0.5
)

// gateVersionCheckStamps decides which outcomes may stamp. It returns the
// file IDs to stamp; availability in the returned outcomes already reflects
// the decision (durable state when gated, live resolution otherwise).
func gateVersionCheckStamps(outcomes []versionCheckOutcome) ([]versionCheckOutcome, []int) {
	dead, checked := 0, 0
	for _, outcome := range outcomes {
		if !outcome.checked {
			continue
		}
		checked++
		if outcome.stamp {
			dead++
		}
	}
	gated := checked > 0 && dead >= minVersionCheckStampQuorum &&
		float64(dead)/float64(checked) > versionCheckStampDisagreementRate
	final := make([]versionCheckOutcome, 0, len(outcomes))
	var toStamp []int
	for _, outcome := range outcomes {
		if gated && outcome.stamp {
			// Listing untrustworthy this round: report the durable signal
			// and stamp nothing.
			outcome.available = outcome.durableAlive
			outcome.stamp = false
		}
		if outcome.stamp {
			toStamp = append(toStamp, outcome.fileID)
		}
		final = append(final, outcome)
	}
	return final, toStamp
}

// HandleCheckVersions implements POST /catalog/versions/check: a batched
// liveness probe for the media page's version list. Each file is tested
// cheaply — virtual rows resolve their pinned ?result= candidate through the
// provider (no media transfer), local rows are read from missing_since — and
// the durable failed_at signal is stamped accordingly. Unknown or deleted
// file IDs are reported as available=false.
func (h *CatalogResourceHandler) HandleCheckVersions(w http.ResponseWriter, r *http.Request) {
	var req versionCheckRequest
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.FileIDs) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "At least one file ID is required")
		return
	}
	if len(req.FileIDs) > maxVersionCheckFiles {
		writeError(w, http.StatusRequestEntityTooLarge, "too_large", "At most 40 file IDs are allowed per request")
		return
	}

	// The frozen v1 route stays the batched liveness check: preprobe is a v2
	// inventory mode and is ignored here rather than answered without inventory.
	writeJSON(w, http.StatusOK, versionCheckResponse{Results: h.CheckVersions(r.Context(), req.FileIDs)})
}

// CheckVersions is the liveness probe behind the v1 route and the v2
// checkCatalogVersions operation. Callers validate the batch (one to
// maxVersionCheckFiles IDs) before calling; the seam applies the per-file
// concurrency, timeout and stamping rules and never fails the whole batch on
// one file.
func (h *CatalogResourceHandler) CheckVersions(ctx context.Context, fileIDs []int) []VersionCheckResult {
	ctx, cancel := context.WithTimeout(ctx, versionCheckOverallBudget)
	defer cancel()

	outcomes := make([]versionCheckOutcome, 0, len(fileIDs))
	var mu sync.Mutex
	eg, egCtx := errgroup.WithContext(ctx)
	eg.SetLimit(versionCheckConcurrency)
	for _, id := range fileIDs {
		if id <= 0 {
			continue
		}
		id := id
		eg.Go(func() error {
			outcome := h.checkVersion(egCtx, id)
			mu.Lock()
			outcomes = append(outcomes, outcome)
			mu.Unlock()
			return nil
		})
	}
	// Per-file failures are folded into the availability verdict; the batch
	// itself never fails on one file.
	_ = eg.Wait()

	// All post-processing shares one absolute deadline. A blocked write must
	// not extend the handler per row, so work that does not fit is logged and
	// reported unconfirmed rather than assumed to have landed.
	postCtx, postCancel := context.WithTimeout(context.WithoutCancel(ctx), versionCheckPostBudget)
	defer postCancel()

	// Stamps apply only after the whole batch is judged: one volatile
	// listing disagreeing with everything must not mass-tag versions.
	final, toStamp := gateVersionCheckStamps(outcomes)
	stampByID := make(map[int]struct{}, len(toStamp))
	for _, id := range toStamp {
		stampByID[id] = struct{}{}
	}
	results := make([]VersionCheckResult, 0, len(final))
	for _, outcome := range final {
		stamped := false
		if _, ok := stampByID[outcome.fileID]; ok {
			// A nil error from the fenced write is not proof it landed (the
			// repository no-ops on a delivery-grace skip or a rotated row), so
			// report a new failure only for the confirmed post-write state.
			if postCtx.Err() != nil {
				slog.WarnContext(postCtx, "version check skipped a failed-version stamp: post-processing budget exhausted", "component", "api", "file_id", outcome.fileID)
			} else {
				stamped = h.stampVirtualCandidateFailed(postCtx, outcome.fileID, outcome.file)
			}
		}
		results = append(results, VersionCheckResult{
			FileID:         outcome.fileID,
			Available:      outcome.available,
			Failed:         versionCheckFailed(outcome, stamped),
			SourceMismatch: outcome.sourceMismatch,
		})
	}
	return results
}

// CheckVersionsWithInventory is the versions check's single-file inventory
// mode. It answers the ordinary liveness verdict first — the budgets, stamping
// and authorization above are unchanged — and then, for the one requested row,
// enumerates its track inventory synchronously under
// versionCheckPreprobeBudget so the answer can carry probe evidence instead of
// the registration placeholders a version row starts with.
//
// The mode is additive and never fails a read: an inaccessible or unknown ID
// answers exactly like the liveness check does, an unwired preprobe seam or a
// probe that does not commit reports declared/pending metadata with the row's
// current tracks, and the batch form keeps its liveness-only shape. Callers pass
// one validated file ID (the v2 adapter enforces that).
func (h *CatalogResourceHandler) CheckVersionsWithInventory(ctx context.Context, fileIDs []int) []VersionCheckResult {
	results := h.CheckVersions(ctx, fileIDs)
	if len(results) != 1 {
		return results
	}
	if h == nil || h.FileResolver == nil {
		return results
	}
	result := &results[0]
	if result.FileID <= 0 {
		return results
	}
	file, err := h.FileResolver.GetByID(ctx, result.FileID)
	if err != nil || file == nil || !h.fileAccessible(ctx, file) {
		return results
	}
	status := VirtualPreprobeDeclined
	// A row with no track inventory has something left to enumerate even when
	// it carries a probe stamp: an evidence write that landed no tracks (or
	// whose tracks were invalidated later) leaves exactly that shape, and
	// treating the stamp alone as the answer froze those rows — production's
	// four stamped-empty rows were never re-probed by the surface the pre-play
	// bar asks through. A dead pin is still not worth a synchronous provider
	// read.
	if isVirtualPlaybackFile(file) &&
		(file.ProbeUpdatedAt == nil || !virtualRowCarriesProbeInventory(file)) &&
		file.FailedAt == nil && h.items != nil {
		probeCtx, cancel := context.WithTimeout(ctx, versionCheckPreprobeBudget)
		status = h.items.preprobeSelectedVirtualFile(probeCtx, file.ContentID, file.ID, h.versionCheckAccessFilter(ctx))
		cancel()
		if refreshed, reloadErr := h.reloadVirtualFileAfterWrite(ctx, file.ID); reloadErr == nil && refreshed != nil {
			file = refreshed
		}
	}
	result.Inventory = &VersionCheckInventory{
		Provenance:     versionCheckInventoryProvenance(file, status),
		ProbedAt:       file.ProbeUpdatedAt,
		VideoTracks:    append([]models.VideoTrack(nil), file.VideoTracks...),
		AudioTracks:    append([]models.AudioTrack(nil), file.AudioTracks...),
		SubtitleTracks: catalog.VersionSubtitleTracks(file),
	}
	return results
}

// versionCheckInventoryProvenance classifies the inventory a check result
// carries. The row's own probe stamp outranks what this read attempted: a stamp
// is durable evidence from any probe, while an attempt that did not commit
// leaves the row on declared metadata.
func versionCheckInventoryProvenance(file *models.MediaFile, status VirtualPreprobeStatus) string {
	if file != nil && file.ProbeUpdatedAt != nil {
		return VersionInventoryVerified
	}
	if status == VirtualPreprobeVerified || status == VirtualPreprobePending {
		return VersionInventoryPending
	}
	return VersionInventoryDeclared
}

// checkVersion tests one media file's liveness and returns whether it is
// available. The file is authorized for the requesting profile BEFORE any
// resolution: inaccessible IDs are indistinguishable from unknown IDs
// (available=false, no distinguishing error), so a restricted profile cannot
// probe arbitrary files or trigger provider work for them. Virtual rows
// resolve the pinned candidate through the provider and stamp failed_at on a
// confirmed dead pin; local rows are read from missing_since with no probe.
// Ambiguous provider errors (timeout, network, resolver not configured) leave
// the stamp unchanged and report the row's current computed availability, so
// a provider outage cannot mass-tag versions as dead.
func (h *CatalogResourceHandler) checkVersion(ctx context.Context, fileID int) versionCheckOutcome {
	outcome := versionCheckOutcome{fileID: fileID}
	if h == nil || h.FileResolver == nil {
		return outcome
	}
	file, err := h.FileResolver.GetByID(ctx, fileID)
	if err != nil || file == nil {
		return outcome
	}
	if !h.fileAccessible(ctx, file) {
		// Denied by the profile's catalog/library access policy: report the
		// same shape as an unknown ID and never resolve or stamp. The row is
		// deliberately not attached to the outcome, so its durable failed_at
		// stays indistinguishable from a nonexistent ID's.
		return outcome
	}
	outcome.file = file
	outcome.durableAlive = file.FailedAt == nil
	outcome.sourceMismatch = versionCheckSourceMismatchFor(file)
	if !isVirtualPlaybackFile(file) {
		outcome.available = file.MissingSince == nil
		return outcome
	}
	// A probed row already carries real evidence for its pinned bytes. Version
	// liveness is a navigation surface, not a second probe: resolving every
	// warm row here fan-outs one provider call per version and used to take
	// whole seconds for a season page. Durable failed_at rows still resolve so
	// a recovered pin can clear its verdict.
	if file.ProbeUpdatedAt != nil && file.FailedAt == nil {
		outcome.available = true
		return outcome
	}
	if h.VirtualResolver == nil {
		// No provider resolver wired (playback disabled): report the durable
		// stamp only, never stamp anything.
		outcome.available = outcome.durableAlive
		return outcome
	}

	perFileCtx, cancel := context.WithTimeout(ctx, versionCheckPerFileBudget)
	defer cancel()
	// A strict availability check of the requested pin, not a fresh selection:
	// declare it session-bound so a profile-removed pin reports ambiguous
	// (do not stamp) instead of being substituted by a different candidate.
	perFileCtx = withVirtualSessionBindingV3(perFileCtx, true)
	// Thread the row's durable identity so a provider that renumbers result ids
	// between listings re-identifies the same release instead of being read as
	// a dead pin. Without this a volatile listing stamped every pinned row
	// dead, which is the incident this check guards against. A legacy row with
	// no durable identity is left untouched and stays ambiguous.
	perFileCtx = virtualResolveContextWithPersistedIdentity(perFileCtx, file)
	requestedCandidateID := virtualResultCandidateID(file.FilePath)
	resolved, err := h.VirtualResolver.ResolveVirtualMediaDetailed(
		perFileCtx, file.FilePath, file.VirtualOwnerInstallationID,
		apimw.GetUserID(ctx), apimw.GetProfileID(ctx), true, nil, requestedCandidateID,
	)
	if err == nil {
		// Strict check semantics, not playback fallback semantics: with
		// forceRefresh=true the selection code does not substitute candidates[0]
		// for an absent pin. A successful resolution counts when it named the
		// requested candidate, or when the durable identity proves it is the
		// same release under a renumbered result id (IdentityRematched).
		outcome.checked = true
		if resolvedIdentityMatches(resolved, requestedCandidateID) || resolvedMatchesPersistedIdentity(resolved, file) {
			// A healthy liveness observation for the same identity clears a
			// stale failed_at so the auto-pick considers the release again.
			// The response carries the confirmed post-clear row, so a recovered
			// version is not still reported (and hidden) as failed.
			outcome.file = h.clearVirtualCandidateIfFailed(ctx, fileID, file)
			if resolved.IdentityRematched {
				// Adopt the new ?result= identity through the same CAS-fenced
				// Phase-1 write the playback path uses, so the row moves with
				// the provider's renumbering instead of being rematched on
				// every listing.
				adoptCtx, adoptCancel := versionCheckDetached(ctx)
				adoptRematchedVirtualResolution(
					adoptCtx, file, resolved,
					h.VirtualFileMetadataSaver, h.VirtualFileSaver,
				)
				adoptCancel()
			}
			outcome.available = true
			return outcome
		}
		if requestedCandidateID == "" {
			// The row carries no concrete pin (profile-neutral row): any
			// resolution of its identity is listing evidence. Report live.
			outcome.file = h.clearVirtualCandidateIfFailed(ctx, fileID, file)
			outcome.available = true
			return outcome
		}
		// The provider answered with a different candidate and the row's
		// durable identity does not match it: the requested release is
		// genuinely gone. Record the stamp for the batch gate; a row with
		// no durable identity is indistinguishable from a renumbered
		// listing, so it is left alone (ambiguous) rather than mass-stamped.
		if _, hasIdentity := persistedVirtualIdentity(file); hasIdentity {
			outcome.stamp = true
		}
		outcome.available = false
		return outcome
	}
	if errors.Is(err, virtuallibrary.ErrTitleWithoutStreams) {
		// The title is consecutively empty across more than one provider
		// cache entry: there is nothing to resolve, so report the row
		// unavailable without visiting the provider again. Never a stamp:
		// this is a title-wide outage verdict, not evidence about the pin,
		// and stamping it would brick the row for a day on a transient
		// provider gap.
		outcome.checked = true
		outcome.available = false
		return outcome
	}
	if isVirtualProviderListingTemporaryError(err) {
		// Transport-temporary: the provider's listing request failed, timed
		// out, or answered 5xx. That is availability-shaped and says nothing
		// about the pinned release, so this must be classified BEFORE the
		// string-based confirmed-dead classifier and before any
		// identity-grounded absent-pin branch. A joined error can carry both a
		// transient listing cause and a "no matching candidate" fallback text
		// (or an absent-pin sentinel); letting either be read as a verdict is
		// what let a provider flap indict a pin the liveness check then
		// reported dead.
		outcome.checked = true
		outcome.available = outcome.durableAlive
		return outcome
	}
	if isVirtualCandidateDeadError(err) {
		// Confirmed dead pin: the provider listed but the pinned candidate is
		// gone or unusable. Record the stamp for the batch gate; the write
		// itself is fenced the same way, so a candidate rotated while
		// resolution was in flight is never mis-marked.
		outcome.checked = true
		outcome.stamp = true
		outcome.available = false
		return outcome
	}
	if _, hasIdentity := persistedVirtualIdentity(file); hasIdentity &&
		errors.Is(err, virtuallibrary.ErrSessionBoundCandidateAbsent) {
		// The provider listed, the row carries a durable identity, and the
		// resolver still refused because no listed candidate re-identified the
		// same release. That is a confirmed absence, not a renumbered listing:
		// record the stamp for the batch gate. Without identity the sentinel
		// is ambiguous and is left alone below.
		outcome.checked = true
		outcome.stamp = true
		outcome.available = false
		return outcome
	}
	// Ambiguous (provider down, timeout, identity-less absence): do not stamp.
	// Report the current durable signal so an outage cannot mass-tag versions.
	outcome.checked = true
	outcome.available = outcome.durableAlive
	return outcome
}

// versionCheckSourceMismatchFor reads the process-wide wrong-source verdict a
// playback probe recorded for this row. A mismatch is not a liveness verdict:
// the release is fine, only the bytes the provider served were wrong, so the
// caller reports it alongside available/failed without touching either. The
// signal lives only in the in-memory probe damper (keyed exactly like the
// playback probe cache), so it is intentionally absent after a restart or on a
// node that did not run the probe; the version list then keeps its generic
// unprobed presentation instead of inventing a mismatch.
func versionCheckSourceMismatchFor(file *models.MediaFile) *VersionCheckSourceMismatch {
	if file == nil || !isVirtualPlaybackFile(file) {
		return nil
	}
	served, declared, ok := virtualProbeFailures.sizeMismatchVerdict(
		virtualProbeFailureKey(file.FilePath, file.VirtualOwnerInstallationID),
	)
	if !ok {
		return nil
	}
	return &VersionCheckSourceMismatch{ServedBytes: served, DeclaredBytes: declared}
}

// VersionSourceMismatch projects the same process-local wrong-source verdict
// the versions check endpoint reports onto the catalog detail projection. It
// is the single verdict source for both surfaces: the playback probe damper
// decides, and this adapter only shapes it. A row with no recent verdict, or a
// local row, returns nil so the detail field stays absent.
func VersionSourceMismatch(file *models.MediaFile) *catalog.VersionSourceMismatch {
	verdict := versionCheckSourceMismatchFor(file)
	if verdict == nil {
		return nil
	}
	return &catalog.VersionSourceMismatch{ServedBytes: verdict.ServedBytes, DeclaredBytes: verdict.DeclaredBytes}
}

// versionCheckFailed reports whether a checked row carries a durable failed
// verdict: either it entered the round already failed, or this round verifiably
// stamped one. Title-empty and ambiguous outcomes never set it: only an
// identity-grounded dead pin counts, so menus remove genuinely dead releases
// without hiding rows that may recover on a fresh listing. The outcome's file
// is the confirmed post-write state, so a row this round recovered reports
// failed=false.
func versionCheckFailed(outcome versionCheckOutcome, stamped bool) bool {
	if stamped {
		return true
	}
	return outcome.file != nil && outcome.file.FailedAt != nil
}

// stampVirtualCandidateFailed applies the fenced failed_at verdict the check
// computed. It is a no-op when no stamp callback is wired or the row already
// carries a verdict, and the fenced write (expectedFilePath + observedFailedAt)
// rejects a row that rotated or gained a newer verdict while resolution was in
// flight.
//
// A nil error alone does not prove the write landed: the repository also
// returns nil for a fenced-out write and for a delivery-grace skip (see
// FileRepository.MarkVirtualCandidateFailed), so a healthy or concurrently
// replaced row would be mis-reported as durably failed. The row is re-read and
// the call reports true — and publishes the versions_updated event, so every
// client drops the row from the version list immediately — only when the
// confirmed post-write state carries this identity's verdict.
func (h *CatalogResourceHandler) stampVirtualCandidateFailed(ctx context.Context, fileID int, file *models.MediaFile) bool {
	if h == nil || h.MarkVirtualFailed == nil || file == nil || file.FailedAt != nil {
		return false
	}
	stampCtx, cancel := versionCheckDetached(ctx)
	defer cancel()
	if err := h.MarkVirtualFailed(stampCtx, fileID, file.FilePath, file.FailedAt); err != nil {
		slog.WarnContext(ctx, "version check could not stamp a failed version", "component", "api", "file_id", fileID, "error", err)
		return false
	}
	confirmed, err := h.reloadVirtualFileAfterWrite(stampCtx, fileID)
	if err != nil {
		slog.WarnContext(ctx, "version check could not confirm a failed-version stamp", "component", "api", "file_id", fileID, "error", err)
		return false
	}
	if confirmed == nil || confirmed.FilePath != file.FilePath || confirmed.FailedAt == nil {
		return false
	}
	h.publishVirtualCandidateFailed(ctx, file)
	return true
}

// reloadVirtualFileAfterWrite re-reads the row a fenced health write touched so
// the caller can report and publish the persisted state instead of the write
// attempt. It returns nil when no resolver is wired or the row vanished.
func (h *CatalogResourceHandler) reloadVirtualFileAfterWrite(ctx context.Context, fileID int) (*models.MediaFile, error) {
	if h == nil || h.FileResolver == nil {
		return nil, nil
	}
	return h.FileResolver.GetByID(ctx, fileID)
}

// publishVirtualCandidateFailed announces that a freshly stamped row is gone
// from the version list, through the shared event authority the Refresh List
// executor and the first-playback sink use.
//
// The publish is detached from request cancellation for the same reason the
// stamp is: the row is already committed when the client's request ends. The
// scope mirrors what the refresh path publishes for the same row: an episode
// virtual row carries content_id = series id and episode_id = episode id while
// the refresh request names the episode, so both ids are announced (each is
// cheap and the helper is nil-safe).
func (h *CatalogResourceHandler) publishVirtualCandidateFailed(ctx context.Context, file *models.MediaFile) {
	if h == nil || h.Events == nil || file == nil {
		return
	}
	publishCtx, cancel := versionCheckDetached(ctx)
	defer cancel()
	hub := h.Events.EventsHub()
	publishVirtualVersionsUpdatedEvent(publishCtx, nil, hub, file.ContentID)
	if file.EpisodeID != "" && file.EpisodeID != file.ContentID {
		publishVirtualVersionsUpdatedEvent(publishCtx, nil, hub, file.EpisodeID)
	}
}

// clearVirtualCandidateIfFailed applies the recovery half of a healthy liveness
// observation: a same-identity successful resolution clears a stale failed_at
// so the release is eligible again. The clear is fenced on the same identity
// and observed verdict as the stamp, so a concurrent rotation or newer failure
// survives. It is a no-op when the row carries no verdict or no clear callback
// is wired.
//
// The returned row is the confirmed post-write state when the clear verifiably
// landed: the write returns nil even for a fenced-out no-op, so the row is
// re-read and replaces the caller's snapshot only when it still names the same
// identity without a verdict. Otherwise the original snapshot is returned and
// the response keeps reporting its durable failure.
func (h *CatalogResourceHandler) clearVirtualCandidateIfFailed(ctx context.Context, fileID int, file *models.MediaFile) *models.MediaFile {
	if h == nil || h.ClearVirtualFailed == nil || file == nil || file.FailedAt == nil {
		return file
	}
	clearCtx, cancel := versionCheckDetached(ctx)
	defer cancel()
	if err := h.ClearVirtualFailed(clearCtx, fileID, file.FilePath, file.FailedAt); err != nil {
		slog.WarnContext(ctx, "version check could not clear a recovered version", "component", "api", "file_id", fileID, "error", err)
		return file
	}
	confirmed, err := h.reloadVirtualFileAfterWrite(clearCtx, fileID)
	if err != nil {
		slog.WarnContext(ctx, "version check could not confirm a version recovery clear", "component", "api", "file_id", fileID, "error", err)
		return file
	}
	if confirmed == nil || confirmed.FilePath != file.FilePath || confirmed.FailedAt != nil {
		return file
	}
	return confirmed
}

// resolvedIdentityMatches reports whether the resolver's answer named the
// requested candidate: either the returned CandidateID is the requested
// result= value, or the returned URI carries it. Substituted candidates are
// never treated as recovery evidence for the requested pin.
func resolvedIdentityMatches(resolved ResolvedVirtualMedia, requestedCandidateID string) bool {
	if resolved.CandidateID == requestedCandidateID {
		return true
	}
	if parsed, err := url.Parse(resolved.URI); err == nil {
		if strings.TrimSpace(parsed.Query().Get("result")) == requestedCandidateID {
			return true
		}
	}
	return false
}

// fileAccessible applies the requesting profile's catalog/library access
// policy to a media file, mirroring the playback handler's loadAuthorizedFile
// authorization: episodes authorize through their parent series, extras
// through their parent item, and plain files through their own content ID,
// followed by the file-level library/quality predicate. Any failure — missing
// lookup dependencies, an inaccessible parent, or a file outside the allowed
// libraries — denies the file exactly like an unknown ID.
func (h *CatalogResourceHandler) fileAccessible(ctx context.Context, file *models.MediaFile) bool {
	if h == nil || h.ItemAccess == nil {
		return false
	}
	filter := h.versionCheckAccessFilter(ctx)
	switch {
	case file.EpisodeID != "":
		if h.EpisodeLookup == nil {
			return false
		}
		episode, err := h.EpisodeLookup.GetByID(ctx, file.EpisodeID)
		if err != nil || episode == nil {
			return false
		}
		if err := h.ItemAccess.EnsureAccessible(ctx, episode.SeriesID, filter); err != nil {
			return false
		}
	case file.ContentID != "":
		if err := h.ItemAccess.EnsureAccessible(ctx, file.ContentID, filter); err != nil {
			return false
		}
	case file.ExtraID != "":
		if h.ExtraLookup == nil {
			return false
		}
		extra, err := h.ExtraLookup.GetByID(ctx, file.ExtraID)
		if err != nil || extra == nil {
			return false
		}
		if err := h.ItemAccess.EnsureAccessible(ctx, extra.ParentID, filter); err != nil {
			return false
		}
	default:
		return false
	}
	return catalog.FileAllowedByAccess(file, filter)
}

// isVirtualProviderListingTemporaryError reports whether a resolution failure
// is transport-temporary / availability-shaped rather than a verdict about the
// pinned release: the provider listing could not be produced (request failed,
// timed out, or answered 5xx), the title is fail-fast marked as having no
// provider streams, the release is still being fetched, or the trusted
// persisted candidate was absent from a listing the resolver refused to
// substitute from. None of these may indict a pin; the caller reports durable
// state instead.
//
// This is deliberately checked BEFORE the string-based
// isVirtualCandidateDeadError: a joined error can carry both a transient
// listing cause and a "no matching candidate" fallback text, and the transient
// cause must win. Every typed sentinel from internal/virtuallibrary and
// internal/virtuallibrary/resolver is matched with errors.Is so a rename of the
// human text cannot silently re-open the indictment; the provider-neutral
// strings below cover a wrapped RPC failure the core path does not type.
func isVirtualProviderListingTemporaryError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, resolver.ErrProviderUnavailable) ||
		errors.Is(err, virtuallibrary.ErrTitleWithoutStreams) ||
		errors.Is(err, virtuallibrary.ErrProviderPending) ||
		errors.Is(err, virtuallibrary.ErrPersistedCandidateTrusted) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "request failed") ||
		strings.Contains(msg, "resolver is not installed") ||
		strings.Contains(msg, "load owning virtual stream provider") ||
		strings.Contains(msg, "no streams available") ||
		strings.Contains(msg, "provider unavailable") ||
		strings.Contains(msg, "failed recently") ||
		strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "service unavailable") ||
		strings.Contains(msg, "temporarily unavailable") ||
		containsProvider5xxStatus(msg)
}

// containsProvider5xxStatus reports whether an error message names an HTTP 5xx
// provider status ("... returned status 503", "... HTTP 503"). A listing that
// failed with a raw 5xx is transport-temporary even when the surrounding text
// also carries a fallback "no matching candidate", so the dead-pin classifier
// must defer to it.
func containsProvider5xxStatus(msg string) bool {
	if strings.Contains(msg, " 5xx") {
		return true
	}
	for _, marker := range []string{"status 5", "http 5"} {
		idx := strings.Index(msg, marker)
		if idx < 0 {
			continue
		}
		rest := msg[idx+len(marker):]
		if len(rest) >= 2 && rest[0] >= '0' && rest[0] <= '9' && rest[1] >= '0' && rest[1] <= '9' {
			return true
		}
	}
	return false
}

// isVirtualCandidateDeadError classifies a resolution failure as a confirmed
// dead pin: the provider answered, listed candidates, and the pinned candidate
// is no longer among them or is unusable. Provider-down/timeout errors do not
// match and are treated as ambiguous. The strings are the provider-neutral
// error texts the plugin service produces (see
// internal/plugins/virtual_playback.go). A joined error that also carries a
// provider RPC failure ("request failed") is ambiguous even when a fallback
// provider reported no matching candidate: the owner provider that owns the pin
// may simply be down. A transport-temporary cause always wins over the
// dead-pin strings, so a listing failure joined with a "no matching candidate"
// fallback text is never read as a verdict (see
// isVirtualProviderListingTemporaryError).
//
// An EMPTY listing ("no streams available from provider") is deliberately NOT
// classified as dead. A zero-count answer is a provider hiccup that proves
// nothing about any specific release: the provider intermittently returns an
// empty listing for a title whose releases are all still offered. Stamping pins
// on it is how a 2.6 s burst marked 50 of 57 rows failed in the incident.
func isVirtualCandidateDeadError(err error) bool {
	if err == nil || isVirtualProviderListingTemporaryError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no matching candidate") ||
		strings.Contains(msg, "no usable stream")
}

// versionCheckAccessFilter builds the filter the check authorizes and probes
// under. The liveness fan-out and the single-file inventory mode share it, so a
// preprobe never runs with a wider policy than the verdict that admitted it.
//
// The access-scope extractors below mirror requestAccessFilter's mapping of the
// resolved access scope onto a catalog.AccessFilter, but take a context instead
// of an *http.Request because the liveness check fans out per file through an
// errgroup. A missing scope yields the unrestricted zero values, exactly like
// requestAccessFilter.
func (h *CatalogResourceHandler) versionCheckAccessFilter(ctx context.Context) catalog.AccessFilter {
	filter := catalog.AccessFilter{
		AllowedLibraryIDs:  accessScopeAllowedLibraryIDs(ctx),
		DisabledLibraryIDs: accessScopeDisabledLibraryIDs(ctx),
		MaxPlaybackQuality: accessScopeMaxPlaybackQuality(ctx),
		UserID:             apimw.GetUserID(ctx),
		ProfileID:          apimw.GetProfileID(ctx),
	}
	if scope, ok := access.GetScope(ctx); ok {
		filter.MaturityLimits = scope.MaturityLimits
	}
	return filter
}

func accessScopeAllowedLibraryIDs(ctx context.Context) []int {
	if scope, ok := access.GetScope(ctx); ok {
		return scope.AllowedLibraryIDs
	}
	return nil
}

func accessScopeDisabledLibraryIDs(ctx context.Context) []int {
	if scope, ok := access.GetScope(ctx); ok {
		return scope.DisabledLibraryIDs
	}
	return nil
}

func accessScopeMaxPlaybackQuality(ctx context.Context) string {
	if scope, ok := access.GetScope(ctx); ok {
		return scope.MaxPlaybackQuality
	}
	return ""
}
