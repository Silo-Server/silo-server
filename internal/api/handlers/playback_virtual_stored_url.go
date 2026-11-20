package handlers

import (
	"context"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
)

// virtualStoredURLState reports what a virtual candidate row's persisted
// resolved_url means for a session-bound re-resolve.
type virtualStoredURLState int

const (
	// virtualStoredURLMissing means the row does not verifiably own the
	// requested candidate, or carries no stored URL. The caller keeps the
	// existing list-and-resolve behavior.
	virtualStoredURLMissing virtualStoredURLState = iota
	// virtualStoredURLUsable means the row's own stored URL is present,
	// unexpired, and passes the resolver's URL validator. The caller serves
	// it without contacting the provider.
	virtualStoredURLUsable
	// virtualStoredURLExpiredWithinWindow means the row owns the requested
	// candidate and carries a stored URL whose expiry has passed, but the row
	// is still inside the configured candidate store window. The URL is never
	// served past its own expiry; the state tells the caller to keep preferring
	// the persisted same-identity candidate (re-resolve and refresh it) instead
	// of dropping the pin or substituting a sibling. It is only reachable when
	// the window is enabled.
	virtualStoredURLExpiredWithinWindow
	// virtualStoredURLExpired means the row owns the requested candidate and
	// carries a stored URL whose expiry has passed and the row is outside the
	// window (or the window is disabled). The caller lists and resolves as
	// before, then refreshes the stored value through the existing Phase-1
	// write path.
	virtualStoredURLExpired
)

// virtualCandidateWithinStoreWindow reports whether a virtual candidate row is
// still inside the configured trust window. A zero or negative window disables
// it, so the caller keeps the pre-window behavior. The row's updated_at is the
// same timestamp the retention sweep evaluates: it is refreshed every time the
// provider re-lists the candidate (and by a stored-URL refresh/adoption), so
// the window measures time since the candidate was last listed or resolved.
func virtualCandidateWithinStoreWindow(row *models.MediaFile, now time.Time, window time.Duration) bool {
	if row == nil || window <= 0 || row.UpdatedAt.IsZero() {
		return false
	}
	return !now.After(row.UpdatedAt.Add(window))
}

// evaluateStoredVirtualURLCandidate decides whether a catalog row's persisted
// resolved_url may be served for the exact requested candidate.
//
// Same-row enforcement: the caller looks the row up by the requested
// candidate's file_path (exact match) and this function re-checks it with
// sameVirtualReleaseIdentity, which is strict about concrete identity — a row
// carrying the provider-neutral path (no ?result= pick) does NOT satisfy a
// pinned request. A stored URL therefore always belongs to the candidate being
// resolved; it is the pinned release itself, never a substitute, so the
// session-binding guard is satisfied by construction.
//
// The list-time URL is persisted before URL validation (Phase 1 stores the
// provider's raw URL), so it is re-validated here through
// virtuallibrary.ValidateProviderStreamURL — the same validator the resolver
// applies to a fresh listing. A URL that fails validation is never fetched;
// the caller falls back to listing.
// trustWindow is the configured candidate store window. Zero disables it and
// restores the pre-window behavior. It never extends a signed URL's life: an
// expired URL is refused regardless, and the window only decides whether the
// caller may keep preferring the persisted same-identity candidate.
func evaluateStoredVirtualURLCandidate(
	ctx context.Context,
	candidateURI string,
	row *models.MediaFile,
	allowInsecure bool,
	now time.Time,
	trustWindow time.Duration,
) (ResolvedVirtualMedia, virtualStoredURLState) {
	candidateID := virtualResultCandidateID(candidateURI)
	if candidateID == "" || row == nil || row.ID <= 0 {
		return ResolvedVirtualMedia{}, virtualStoredURLMissing
	}
	// The row must own this concrete release. The exact-path lookup already
	// guarantees row.FilePath == candidateURI, but re-checking the release
	// identity keeps the guarantee explicit and independent of the lookup.
	if !sameVirtualReleaseIdentity(row.FilePath, candidateURI) {
		return ResolvedVirtualMedia{}, virtualStoredURLMissing
	}
	storedURL := strings.TrimSpace(row.ResolvedURL)
	if storedURL == "" {
		return ResolvedVirtualMedia{}, virtualStoredURLMissing
	}
	// A nil expiry is usable: the provider declared no lifetime. A non-nil
	// expiry at or before now is expired and must be refreshed by a resolve.
	// Inside the trust window the caller keeps preferring this same-identity
	// candidate; outside it (or with the window disabled) today's behavior is
	// unchanged. The URL is never served here in either case.
	if row.ResolvedURLExpiresAt != nil && !now.Before(*row.ResolvedURLExpiresAt) {
		if virtualCandidateWithinStoreWindow(row, now, trustWindow) {
			return ResolvedVirtualMedia{}, virtualStoredURLExpiredWithinWindow
		}
		return ResolvedVirtualMedia{}, virtualStoredURLExpired
	}
	validated, err := virtuallibrary.ValidateProviderStreamURL(ctx, storedURL, allowInsecure)
	if err != nil {
		// Refuse an unvalidated URL, but fall through to the provider list
		// rather than failing the resolve: the stored value is a cache, not
		// the source of truth.
		return ResolvedVirtualMedia{}, virtualStoredURLMissing
	}
	var expiresAt time.Time
	if row.ResolvedURLExpiresAt != nil {
		expiresAt = *row.ResolvedURLExpiresAt
	}
	return ResolvedVirtualMedia{
		URL:         validated,
		URI:         candidateURI,
		CandidateID: candidateID,
		OwnerID:     row.VirtualOwnerInstallationID,
		// The stored URL was recorded with the headers the relay forwards
		// (Referer/Origin/User-Agent). Serving the URL without them would break
		// a provider that authenticates by header instead of a URL token, so
		// they travel with the URL as a unit.
		RequestHeaders: cloneHeaderMap(row.ProviderRequestHeaders),
		ExpiresAt:      expiresAt,
	}, virtualStoredURLUsable
}

// virtualCandidateTrustWindow reports the configured candidate store window.
// Zero (the default when the callback is unwired) keeps the pre-window
// behavior.
func (h *PlaybackHandler) virtualCandidateTrustWindow() time.Duration {
	if h == nil || h.VirtualCandidateTrustWindow == nil {
		return 0
	}
	return h.VirtualCandidateTrustWindow()
}

// virtualStoredURLTrustWindow is the StreamHandler counterpart of the playback
// handler's trust-window reader.
func (h *StreamHandler) virtualStoredURLTrustWindow() time.Duration {
	if h == nil || h.VirtualCandidateTrustWindow == nil {
		return 0
	}
	return h.VirtualCandidateTrustWindow()
}

// storedVirtualURLAllowInsecure evaluates the allow_private_streams opt-in for
// a row, mirroring the resolver's decision for provider URLs. virtual_library.
// allow_private_streams is a server-wide setting, so the owner only selects
// the callback; a nil callback keeps the strict SSRF policy.
func (h *PlaybackHandler) storedVirtualURLAllowInsecure(row *models.MediaFile, ownerInstallationID int) bool {
	if h == nil || h.AllowPrivateStreams == nil || row == nil {
		return false
	}
	return h.AllowPrivateStreams(effectiveVirtualOwner(row.VirtualOwnerInstallationID, ownerInstallationID))
}

// storedVirtualURLAllowInsecure is the StreamHandler counterpart of the playback
// handler's allow_private_streams reader.
func (h *StreamHandler) storedVirtualURLAllowInsecure(row *models.MediaFile, ownerInstallationID int) bool {
	if h == nil || h.AllowPrivateStreams == nil || row == nil {
		return false
	}
	return h.AllowPrivateStreams(effectiveVirtualOwner(row.VirtualOwnerInstallationID, ownerInstallationID))
}

// persistedVirtualIdentity snapshots a catalog row's durable provider identity
// for the same-release re-match. It reports false for a legacy row whose
// identity columns are all NULL, so those keep today's dead-pin behavior with
// no re-match attempt.
func persistedVirtualIdentity(row *models.MediaFile) (virtuallibrary.PersistedCandidateIdentity, bool) {
	if row == nil {
		return virtuallibrary.PersistedCandidateIdentity{}, false
	}
	identity := virtuallibrary.PersistedCandidateIdentity{
		VideoHash:   row.ProviderVideoHash,
		GUID:        row.ProviderGUID,
		ReleaseName: row.ProviderReleaseName,
		ReleaseSize: row.ProviderReleaseSize,
	}
	if !identity.HasDurableIdentity() {
		return virtuallibrary.PersistedCandidateIdentity{}, false
	}
	return identity, true
}

// resolvedMatchesPersistedIdentity reports whether a resolved candidate is the
// same release as the catalog row, judged by the durable identity tiers in the
// same precedence the deduplication chain uses (video hash, then source GUID,
// then normalized release name plus exact size). It is the read-side companion
// of resolver.PersistedDedupKey: a row or a resolution with no usable tier can
// never match, so an identity-less row is never treated as confirmed by a
// merely-rotated listing.
func resolvedMatchesPersistedIdentity(resolved ResolvedVirtualMedia, row *models.MediaFile) bool {
	identity, ok := persistedVirtualIdentity(row)
	if !ok {
		return false
	}
	want := resolver.PersistedDedupKey(identity.VideoHash, identity.GUID, identity.ReleaseName, identity.ReleaseSize)
	got := resolver.PersistedDedupKey(resolved.ProviderVideoHash, resolved.ProviderGUID, resolved.ProviderReleaseName, resolved.ProviderReleaseSize)
	return want != "" && want == got
}

// virtualResolveContextWithPersistedIdentity threads the row's durable identity
// into a resolve so the resolver can re-identify the same release when the
// provider renumbers result ids. A row with no durable identity is left
// untouched, so a legacy row resolves exactly as before.
func virtualResolveContextWithPersistedIdentity(ctx context.Context, row *models.MediaFile) context.Context {
	identity, ok := persistedVirtualIdentity(row)
	if !ok {
		return ctx
	}
	return virtuallibrary.WithPersistedCandidateIdentity(ctx, identity)
}

// owningSessionBoundCandidateRow returns the catalog row that owns the exact
// concrete candidate path named by file, when the handler can look it up and it
// is a different row than the one in hand.
//
// A session binds to a concrete ?result= URI while the requested catalog row can
// remain the provider-neutral placeholder the session was opened through, so the
// row in hand may carry no durable identity or stored URL even though the bound
// candidate's own row does. This is the lookup that recovers that evidence from
// the row that actually owned it at ingest time.
//
// Ownership is asserted on the exact path plus a non-empty matching content id
// and episode, never on a path collision alone: a provider renumbers result ids
// per listing, so binding to another episode's or another content's row would
// anchor the session on bytes the viewer never selected. A missing content id
// proves nothing and is not a match. A lookup that cannot run, a not-found, or
// an incomplete row yields nil, so the caller keeps the row in hand unchanged.
func (h *PlaybackHandler) owningSessionBoundCandidateRow(ctx context.Context, file *models.MediaFile) *models.MediaFile {
	if h == nil || file == nil {
		return nil
	}
	candidateURI := strings.TrimSpace(file.FilePath)
	if virtualResultCandidateID(candidateURI) == "" {
		// The provider-neutral placeholder path names no concrete candidate.
		return nil
	}
	var row *models.MediaFile
	if h.VirtualFileLookup != nil {
		found, err := h.VirtualFileLookup(ctx, candidateURI)
		if err != nil || found == nil {
			return nil
		}
		row = found
	} else {
		found, capable, err := h.virtualPathLookupRow(ctx, candidateURI)
		if !capable || err != nil || found == nil {
			return nil
		}
		row = found
	}
	if row.ID <= 0 || row.ID == file.ID {
		return nil
	}
	if strings.TrimSpace(row.FilePath) != candidateURI {
		return nil
	}
	// Ownership is proven by the concrete path plus this content and episode. An
	// empty content id proves nothing, so it must not let a path collision stand
	// in for ownership.
	if file.ContentID == "" || row.ContentID != file.ContentID {
		return nil
	}
	if row.EpisodeID != file.EpisodeID {
		return nil
	}
	return row
}

// persistedIdentityCompatible reports whether two durable identities describe
// the same release under the resolver's precedence (video hash, then source
// GUID, then normalized release name plus exact size). Two rows with
// incompatible identities describe different releases, so their evidence must
// never be combined.
func persistedIdentityCompatible(a, b virtuallibrary.PersistedCandidateIdentity) bool {
	return resolver.PersistedDedupKey(a.VideoHash, a.GUID, a.ReleaseName, a.ReleaseSize) ==
		resolver.PersistedDedupKey(b.VideoHash, b.GUID, b.ReleaseName, b.ReleaseSize)
}

// enrichSessionBoundCandidateIdentity overlays the durable provider identity and
// stored-URL evidence of the catalog row that owns file's concrete candidate
// path onto a copy of file, wherever file carries none of its own.
//
// This is what lets a session-bound re-resolve survive the provider re-listing
// the same release under a new ?result= token: the session anchor row carries
// the release name and size the resolver needs to re-identify the renumbered
// candidate, and the owning row's stored URL lets a still-live token short-
// circuit the provider listing entirely. Without it the resolve threads an empty
// identity and the resolver refuses the absent pin as unprovable (the observed
// has_identity=false refusal).
//
// The overlay is evidence-coherent, not field-by-field. The owning row's
// durable identity and its transport evidence (the stored URL, its expiry and
// its request headers, which travel with the URL as a unit) are imported from
// the same row, so the release the resolver re-matches and the bytes the caller
// serves always share a provenance. Contradictory identities are refused
// outright: if both rows carry a durable identity and they disagree, they
// describe different releases, so neither row's evidence is combined with the
// other's and the caller's row is returned unchanged. An identity is never
// attached to a stored URL of unknown provenance, and a stored URL is never
// imported without a matching identity when the anchor already carries one. The
// owning row's updated_at (which the trust window measures) is copied only
// together with its URL, so a row whose transport evidence was not imported
// cannot advance the trust clock on the anchor's older evidence.
//
// The exact-path, content and episode ownership check makes the copied evidence
// the bound candidate's own, so no release swap is possible. Track inventory,
// verdicts, ownership and every other field stay exactly as the caller's row
// carried them.
//
// A row that already carries both identity and a stored URL is returned
// unchanged with no lookup, so the common warm path pays nothing.
func (h *PlaybackHandler) enrichSessionBoundCandidateIdentity(ctx context.Context, file *models.MediaFile) *models.MediaFile {
	if h == nil || file == nil {
		return file
	}
	anchorIdentity, anchorHasIdentity := persistedVirtualIdentity(file)
	anchorHasURL := strings.TrimSpace(file.ResolvedURL) != ""
	needsIdentity := !anchorHasIdentity
	needsURL := !anchorHasURL
	if !needsIdentity && !needsURL {
		return file
	}
	row := h.owningSessionBoundCandidateRow(ctx, file)
	if row == nil {
		return file
	}
	rowIdentity, rowHasIdentity := persistedVirtualIdentity(row)
	rowHasURL := strings.TrimSpace(row.ResolvedURL) != ""

	// Contradictory identities: the two rows describe different releases. Never
	// mix one row's identity with the other's transport evidence.
	if anchorHasIdentity && rowHasIdentity && !persistedIdentityCompatible(anchorIdentity, rowIdentity) {
		return file
	}

	importIdentity := needsIdentity && rowHasIdentity
	importURL := needsURL && rowHasURL

	// An anchor that already carries a stored URL without a durable identity
	// cannot prove that the owning row describes the same historical release.
	// On token reuse the owning row may describe a different release under the
	// same result token. Preserve the anchor's own transport and refuse to adopt
	// the owning row's identity or URL.
	if anchorHasURL && !anchorHasIdentity {
		importIdentity = false
		importURL = false
	}
	// The converse: a URL imported while the anchor keeps an identity the owning
	// row does not supply has no proven provenance.
	if importURL && anchorHasIdentity && !rowHasIdentity {
		importURL = false
	}
	if !importIdentity && !importURL {
		return file
	}

	enriched := *file
	if importIdentity {
		enriched.ProviderVideoHash = row.ProviderVideoHash
		enriched.ProviderGUID = row.ProviderGUID
		enriched.ProviderReleaseName = row.ProviderReleaseName
		enriched.ProviderReleaseSize = row.ProviderReleaseSize
	}
	if importURL {
		enriched.ResolvedURL = row.ResolvedURL
		enriched.ResolvedURLExpiresAt = row.ResolvedURLExpiresAt
		enriched.ProviderRequestHeaders = cloneHeaderMap(row.ProviderRequestHeaders)
		// Freshness provenance travels with the transport evidence: only the
		// row that supplied the URL may advance the trust-window clock. A zero
		// timestamp on the supplier is copied unconditionally and disables trust.
		enriched.UpdatedAt = row.UpdatedAt
	}
	return &enriched
}

// persistedVirtualCandidateTrusted reports whether the row carries a durable
// identity and is inside the trust window. It is the start-path predicate for
// preferring the persisted candidate over a re-list substitution.
func (h *PlaybackHandler) persistedVirtualCandidateTrusted(row *models.MediaFile, now time.Time) bool {
	if _, ok := persistedVirtualIdentity(row); !ok {
		return false
	}
	return virtualCandidateWithinStoreWindow(row, now, h.virtualCandidateTrustWindow())
}

// virtualStoredURLNeedsSignedRefresh reports whether the row's stored URL is a
// signed URL whose lifetime has already lapsed. A nil expiry is usable (never
// needs a refresh) and a future expiry is not yet due. Only this case justifies
// relisting to refresh the URL within the trust window.
func virtualStoredURLNeedsSignedRefresh(row *models.MediaFile, now time.Time) bool {
	return row != nil && row.ResolvedURLExpiresAt != nil && !now.Before(*row.ResolvedURLExpiresAt)
}

// virtualResolvedURLExpired reports whether a resolved virtual candidate's
// stored URL has already lapsed. A zero expiry means the provider declared no
// lifetime, so the URL never needs a pre-emptive refresh. It is the
// resolved-URL counterpart of virtualStoredURLNeedsSignedRefresh: the row check
// gates a relist, while this gate lets a probe or seek renew the URL in hand
// before it spends its budget on a dead token.
func virtualResolvedURLExpired(expiresAt time.Time, now time.Time) bool {
	return !expiresAt.IsZero() && !now.Before(expiresAt)
}

// virtualResolveContextWithPersistedTrust threads the row's durable identity
// and, when the row is inside the trust window and carries evidence that can
// back a re-serve, marks the resolve as allowed to keep trusting that persisted
// candidate even when the provider's current list omits it.
//
// A durable identity can back a re-serve: the resolver re-identifies the same
// release under a new result id, and a stored URL or a recorded delivery is its
// transport proof. Without an identity there is no re-match, so the only usable
// support is a stored URL that can actually be served now — non-empty,
// unexpired, and structurally/SSRF-valid under the same policy recovery applies.
// A delivery stamp alone proves bytes flowed once, not that a URL can be
// served, and an expired, empty, or policy-rejected URL cannot be served, so
// neither claims resumability. Trust alone never substitutes a sibling: a
// delisted pin is refused with the trust sentinel and the handler re-serves its
// stored URL via recoverTrustedStoredVirtualURL, or fails boundedly. A row with
// no resumable evidence keeps the pre-window refusal/rotation behavior. A zero
// window likewise leaves the context untouched.
func virtualResolveContextWithPersistedTrust(ctx context.Context, row *models.MediaFile, now time.Time, window time.Duration, allowInsecure ...bool) context.Context {
	ctx = virtualResolveContextWithPersistedIdentity(ctx, row)
	if row == nil {
		return ctx
	}
	// A caller that dropped the trust for a delisted automatic selection must
	// not have it re-established here: the degraded retry exists precisely to
	// let the resolver serve a live sibling.
	if virtuallibrary.PersistedCandidateTrustSuppressed(ctx) {
		return ctx
	}
	insecure := false
	if len(allowInsecure) > 0 {
		insecure = allowInsecure[0]
	}
	if !virtualPersistedCandidateHasResumableEvidence(ctx, row, now, insecure) {
		return ctx
	}
	if virtualCandidateWithinStoreWindow(row, now, window) {
		ctx = virtuallibrary.WithPersistedCandidateTrust(ctx, true)
	}
	return ctx
}

// virtualPersistedCandidateHasResumableEvidence reports whether a row's
// persisted state can back a trusted re-serve. A durable identity does (the
// resolver re-identifies the same release by identity, with a stored URL or a
// recorded delivery as the transport proof; an identity-backed row may retain
// trust through unusable transport because the provider listing re-resolves it).
// Without an identity there is no re-match, so the only usable support is a
// stored URL that can actually be served now: present, unexpired, and passing
// the same stream URL validation and private-stream policy that recovery applies.
// A delivery stamp alone and an expired, empty, malformed, or policy-rejected
// URL do not claim resumability.
func virtualPersistedCandidateHasResumableEvidence(ctx context.Context, row *models.MediaFile, now time.Time, allowInsecure bool) bool {
	if row == nil {
		return false
	}
	if _, ok := persistedVirtualIdentity(row); ok {
		return strings.TrimSpace(row.ResolvedURL) != "" || row.LastDeliveredAt != nil
	}
	storedURL := strings.TrimSpace(row.ResolvedURL)
	if storedURL == "" {
		return false
	}
	if virtualStoredURLNeedsSignedRefresh(row, now) {
		return false
	}
	if _, err := virtuallibrary.ValidateProviderStreamURL(ctx, storedURL, allowInsecure); err != nil {
		return false
	}
	return true
}

// recoverTrustedStoredVirtualURL is the absent-pin companion of the trust gate:
// when the resolver reports the pinned candidate absent from the provider list
// but the request was trusted, it re-serves the row's own validated stored URL
// so a trusted-but-pinless session recovers the same release without a provider
// round-trip. Same-release is enforced by sameVirtualReleaseIdentity and by
// evaluateStoredVirtualURLCandidate's exact-path ownership check, so this never
// substitutes a sibling. It reports false when the row carries no usable URL;
// the caller then fails boundedly with the original sentinel.
func (h *PlaybackHandler) recoverTrustedStoredVirtualURL(
	ctx context.Context,
	file *models.MediaFile,
	pinnedURI string,
	ownerInstallationID int,
) (ResolvedVirtualMedia, bool) {
	if h == nil || file == nil || !sameVirtualReleaseIdentity(file.FilePath, pinnedURI) {
		return ResolvedVirtualMedia{}, false
	}
	usable, state := evaluateStoredVirtualURLCandidate(
		ctx, pinnedURI, file,
		h.storedVirtualURLAllowInsecure(file, ownerInstallationID), time.Now(),
		h.virtualCandidateTrustWindow(),
	)
	if state != virtualStoredURLUsable || strings.TrimSpace(usable.URL) == "" {
		return ResolvedVirtualMedia{}, false
	}
	return usable, true
}

// adoptRematchedVirtualResolution adopts the new result= identity the resolver
// re-matched for the same release. It reuses the Phase-1 metadata write with
// AdoptPath set, so rewriting file_path and persisting the resolution's URL,
// headers and durable identity are one CAS-fenced statement: a row that rotated
// underneath performs no write, and a live failed_at verdict still blocks the
// adoption (the saver's fence is not bypassed). The write is best-effort: the
// resolved URL is already in hand and serving it does not depend on the
// adoption landing.
func adoptRematchedVirtualResolution(
	ctx context.Context,
	row *models.MediaFile,
	resolved ResolvedVirtualMedia,
	metaSaver VirtualFileMetadataSaver,
	saver VirtualFileSaver,
) {
	if row == nil || row.ID <= 0 || (metaSaver == nil && saver == nil) {
		return
	}
	adoptPath := strings.TrimSpace(resolved.URI)
	if adoptPath == "" || adoptPath == row.FilePath || virtualResultCandidateID(adoptPath) == "" {
		return
	}
	var expiresAt *time.Time
	if !resolved.ExpiresAt.IsZero() {
		expires := resolved.ExpiresAt
		expiresAt = &expires
	}
	args := models.VirtualFilePersistArgs{
		FileID:           row.ID,
		ExpectedFilePath: row.FilePath,
		VideoTracks:      marshalTracksJSON(sanitizeTrackSlice(row.VideoTracks)),
		// A rematch binds the row to a new ?result= identity. Even when the
		// durable identity proves the same release, the concrete file behind
		// the new id is not guaranteed to carry the old inventory (the
		// name+size identity tier collapses distinct muxes), and the stored
		// probe evidence describes the previous id. Replace the audio inventory
		// with the matched candidate's declared languages and drop the old
		// subtitle inventory; the probe stamp is cleared below so the next
		// start probes the real bytes and converges the row. Carrying the old
		// release's labels here is what made the menu lie about the streams.
		AudioTracks:    marshalTracksJSON(sanitizeTrackSlice(declaredVirtualAudioTracks(resolved.CodecAudio, resolved.AudioLanguages, row.CodecAudio))),
		SubtitleTracks: marshalTracksJSON(sanitizeTrackSlice([]models.SubtitleTrack{})),
		Resolution:     row.Resolution,
		CodecVideo:     row.CodecVideo,
		CodecAudio:     row.CodecAudio,
		Container:      row.Container,
		HDR:            row.HDR,
		Bitrate:        row.Bitrate,
		Duration:       row.Duration,
		UpdatedAt:      row.UpdatedAt,
		ProbeUpdatedAt: row.ProbeUpdatedAt,
		// The identity tiers above describe the release being left; the adopted
		// candidate's identity is compared against them to keep a proven
		// same-release rematch on preserve-on-omission.
		ExpectedProviderVideoHash:   row.ProviderVideoHash,
		ExpectedProviderGUID:        row.ProviderGUID,
		ExpectedProviderReleaseName: row.ProviderReleaseName,
		ExpectedProviderReleaseSize: row.ProviderReleaseSize,
		OwnerID:                     row.VirtualOwnerInstallationID,
		LibraryID:                   row.MediaFolderID,
		AdoptPath:                   adoptPath,
		RequireAdopt:                true,
		// The resolver certified this candidate as the same release under a new
		// result id, so a fence refusal is not stale-pin evidence. Without this
		// flag a duplicate row owning the new id counted every rematch as a
		// refusal (production: 80 in 25 minutes) and stamped the healthy row's
		// terminal failed verdict, which then vetoed its own evidence writes.
		SameReleaseRematch: true,
		// The old probe stamp referenced the previous result= identity, so it
		// no longer describes the row; clear it so the next start re-probes
		// instead of trusting the declared placeholder inventory.
		ClearProbe:             true,
		ResolvedURL:            strings.TrimSpace(resolved.URL),
		ResolvedURLExpiresAt:   expiresAt,
		ProviderVideoHash:      resolved.ProviderVideoHash,
		ProviderGUID:           resolved.ProviderGUID,
		ProviderReleaseName:    resolved.ProviderReleaseName,
		ProviderReleaseSize:    resolved.ProviderReleaseSize,
		ProviderRequestHeaders: cloneHeaderMap(resolved.RequestHeaders),
	}
	adoptCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if metaSaver != nil {
		_, _ = metaSaver(adoptCtx, args)
		return
	}
	_, _ = saver(adoptCtx, args)
}

// lookupStoredVirtualURLCandidate is the serve-layer counterpart: the stream
// handler holds the row it loaded but that row's file_path may have been bound
// to the session's virtual source, and its stored URL belongs to the original
// row. Re-reading the exact candidate path keeps the same-row guarantee.
func (h *StreamHandler) lookupStoredVirtualURLCandidate(
	ctx context.Context,
	candidateURI string,
	ownerInstallationID int,
) (ResolvedVirtualMedia, *models.MediaFile, virtualStoredURLState) {
	if h == nil || virtualResultCandidateID(candidateURI) == "" {
		return ResolvedVirtualMedia{}, nil, virtualStoredURLMissing
	}
	pathResolver, ok := h.fileResolver.(interface {
		GetByPath(context.Context, string) (*models.MediaFile, error)
	})
	if !ok {
		return ResolvedVirtualMedia{}, nil, virtualStoredURLMissing
	}
	row, err := pathResolver.GetByPath(ctx, candidateURI)
	if err != nil || row == nil {
		return ResolvedVirtualMedia{}, nil, virtualStoredURLMissing
	}
	allowInsecure := false
	if h.AllowPrivateStreams != nil {
		allowInsecure = h.AllowPrivateStreams(effectiveVirtualOwner(row.VirtualOwnerInstallationID, ownerInstallationID))
	}
	usable, state := evaluateStoredVirtualURLCandidate(
		ctx, candidateURI, row, allowInsecure, time.Now(), h.virtualStoredURLTrustWindow(),
	)
	if state == virtualStoredURLMissing {
		return ResolvedVirtualMedia{}, nil, virtualStoredURLMissing
	}
	return usable, row, state
}

// refreshStoredVirtualResolution records a successful provider resolution on
// the candidate's own row through the existing Phase-1 saver. It never adds a
// write path: the saver writes resolved_url/resolved_url_expires_at and the
// durable identity only when the resolution carried them, and the row's
// current metadata is passed through unchanged so the write cannot clobber
// track inventory. The write is fenced on the row snapshot (CAS), so a row
// that rotated underneath is left untouched.
//
// refreshStoredVirtualResolutionResult reports what a stored-URL persistence
// attempt did. Callers that count "refreshed rows" must only count Persisted;
// a skipped resolution (sibling, empty URL, unwired saver) is neither a
// success nor a failure and must not trip the failure cooldown.
type refreshStoredVirtualResolutionResult int

const (
	// refreshStoredVirtualResolutionSkipped means no write was attempted: the
	// resolution did not belong to this row (sibling, empty URL) or no saver
	// is wired. The row is left untouched and the pass moves on.
	refreshStoredVirtualResolutionSkipped refreshStoredVirtualResolutionResult = iota
	// refreshStoredVirtualResolutionPersisted means the CAS-fenced write ran.
	// consult the returned update result for whether the row actually moved:
	// RowsAffected 0 means a concurrent write won the race, not a failure.
	refreshStoredVirtualResolutionPersisted
)

// Only the requested candidate's row is refreshed. A substituted resolution
// names a different release and must not deposit its URL on this row.
//
// refreshStoredVirtualResolution keeps the legacy fire-and-forget contract for
// the serve paths (transport, stream): the URL is already in hand there, so
// persistence is best-effort cache hygiene and its outcome must not fail the
// request. The background refresher uses refreshStoredVirtualResolutionWithError
// so its success accounting and failure cooldown reflect real outcomes.
func refreshStoredVirtualResolution(
	ctx context.Context,
	row *models.MediaFile,
	resolved ResolvedVirtualMedia,
	metaSaver VirtualFileMetadataSaver,
	saver VirtualFileSaver,
) (refreshStoredVirtualResolutionResult, VirtualFileMetadataUpdateResult) {
	outcome, result, _ := refreshStoredVirtualResolutionWithError(ctx, row, resolved, metaSaver, saver)
	return outcome, result
}

// refreshStoredVirtualResolutionWithError is the accounting variant: same skip
// rules, but a persistence failure is returned so the caller can count no
// success and cool the row down.
func refreshStoredVirtualResolutionWithError(
	ctx context.Context,
	row *models.MediaFile,
	resolved ResolvedVirtualMedia,
	metaSaver VirtualFileMetadataSaver,
	saver VirtualFileSaver,
) (refreshStoredVirtualResolutionResult, VirtualFileMetadataUpdateResult, error) {
	if row == nil || row.ID <= 0 {
		return refreshStoredVirtualResolutionSkipped, VirtualFileMetadataUpdateResult{}, nil
	}
	if metaSaver == nil && saver == nil {
		return refreshStoredVirtualResolutionSkipped, VirtualFileMetadataUpdateResult{}, nil
	}
	if strings.TrimSpace(resolved.URL) == "" {
		return refreshStoredVirtualResolutionSkipped, VirtualFileMetadataUpdateResult{}, nil
	}
	candidateID := virtualResultCandidateID(row.FilePath)
	if candidateID == "" {
		return refreshStoredVirtualResolutionSkipped, VirtualFileMetadataUpdateResult{}, nil
	}
	resolvedID := resolved.CandidateID
	if resolvedID == "" {
		resolvedID = virtualResultCandidateID(resolved.URI)
	}
	if resolvedID != candidateID {
		// The resolver served a sibling. Its URL does not belong to this row.
		return refreshStoredVirtualResolutionSkipped, VirtualFileMetadataUpdateResult{}, nil
	}
	var expiresAt *time.Time
	if !resolved.ExpiresAt.IsZero() {
		expires := resolved.ExpiresAt
		expiresAt = &expires
	}
	args := models.VirtualFilePersistArgs{
		FileID:               row.ID,
		ExpectedFilePath:     row.FilePath,
		VideoTracks:          marshalTracksJSON(sanitizeTrackSlice(row.VideoTracks)),
		AudioTracks:          marshalTracksJSON(sanitizeTrackSlice(row.AudioTracks)),
		SubtitleTracks:       marshalTracksJSON(sanitizeTrackSlice(row.SubtitleTracks)),
		Resolution:           row.Resolution,
		CodecVideo:           row.CodecVideo,
		CodecAudio:           row.CodecAudio,
		Container:            row.Container,
		HDR:                  row.HDR,
		Bitrate:              row.Bitrate,
		Duration:             row.Duration,
		UpdatedAt:            row.UpdatedAt,
		ProbeUpdatedAt:       row.ProbeUpdatedAt,
		OwnerID:              row.VirtualOwnerInstallationID,
		LibraryID:            row.MediaFolderID,
		ResolvedURL:          strings.TrimSpace(resolved.URL),
		ResolvedURLExpiresAt: expiresAt,
		ProviderVideoHash:    resolved.ProviderVideoHash,
		ProviderGUID:         resolved.ProviderGUID,
		ProviderReleaseName:  resolved.ProviderReleaseName,
		ProviderReleaseSize:  resolved.ProviderReleaseSize,
		// The resolution's headers are stored with its URL; the saver preserves
		// a stored set when the resolution carries none.
		ProviderRequestHeaders: cloneHeaderMap(resolved.RequestHeaders),
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if metaSaver != nil {
		result, err := metaSaver(refreshCtx, args)
		if err != nil {
			return refreshStoredVirtualResolutionPersisted, VirtualFileMetadataUpdateResult{}, err
		}
		return refreshStoredVirtualResolutionPersisted, result, nil
	}
	rows, err := saver(refreshCtx, args)
	if err != nil {
		return refreshStoredVirtualResolutionPersisted, VirtualFileMetadataUpdateResult{}, err
	}
	return refreshStoredVirtualResolutionPersisted, VirtualFileMetadataUpdateResult{RowsAffected: rows, MetadataUpdated: rows > 0}, nil
}
