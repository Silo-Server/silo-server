package virtuallibrary

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/remotestream"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/quality"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/resolver"
	"github.com/Silo-Server/silo-server/internal/virtuallibrary/stream"
)

// ResolvedVirtualStream is the core-resolved counterpart of the plugin's
// ResolvedVirtualStream: a validated stream URL plus candidate identity and
// request headers for playback. Provenance is always core.
type ResolvedVirtualStream struct {
	URL            string
	URI            string
	CandidateID    string
	RequestHeaders map[string]string
	ExpiresAt      time.Time
	// ProviderVideoHash, ProviderGUID, ProviderReleaseName and
	// ProviderReleaseSize are the resolved candidate's durable provider
	// identity, in the same tier order as the dedup key. They are additive
	// evidence for the persistence path: a later phase can re-match the row
	// after the provider rotates result ids. Any of them may be empty.
	ProviderVideoHash   string
	ProviderGUID        string
	ProviderReleaseName string
	ProviderReleaseSize int64
	// IdentityRematched is true when the requested pin's result id was absent
	// from the fresh listing but a listed candidate carried the row's durable
	// persisted identity. The returned candidate is then the same release
	// re-identified under a new provider result id, not a substitution: the
	// caller must adopt the new identity (including the new ?result= path)
	// rather than report a release swap.
	IdentityRematched bool
	// CodecAudio, AudioLanguages and SubtitleLanguages are the candidate's
	// provider-declared inventory. They are release metadata, not probe
	// evidence, and are carried so a caller that re-binds a row to this
	// candidate can seed a declared inventory while the probe catches up.
	CodecAudio        string
	AudioLanguages    []string
	SubtitleLanguages []string
}

// PersistedCandidateIdentity is the durable identity a virtual candidate row
// carries from persistence: the video hash, source GUID and normalized release
// name + size. It is the caller's snapshot of the catalog row, threaded into a
// resolve so a provider re-list that renumbered result ids can be recognized as
// the same release instead of a dead pin.
type PersistedCandidateIdentity struct {
	VideoHash   string
	GUID        string
	ReleaseName string
	ReleaseSize int64
}

// HasDurableIdentity reports whether any identity tier is present. A row with
// no durable identity (a legacy row) cannot be re-matched and keeps today's
// dead-pin behavior.
func (p PersistedCandidateIdentity) HasDurableIdentity() bool {
	return strings.TrimSpace(p.VideoHash) != "" ||
		strings.TrimSpace(p.GUID) != "" ||
		strings.TrimSpace(p.ReleaseName) != ""
}

type persistedCandidateIdentityContextKey struct{}

// WithPersistedCandidateIdentity threads the catalog row's durable identity
// into a resolve. It is a no-op for an identity with no usable tier, so a
// legacy row cannot accidentally enable re-matching.
func WithPersistedCandidateIdentity(ctx context.Context, identity PersistedCandidateIdentity) context.Context {
	if ctx == nil || !identity.HasDurableIdentity() {
		return ctx
	}
	return context.WithValue(ctx, persistedCandidateIdentityContextKey{}, identity)
}

// persistedCandidateIdentityFromContext returns the identity threaded by the
// caller, or false when none is present.
func persistedCandidateIdentityFromContext(ctx context.Context) (PersistedCandidateIdentity, bool) {
	if ctx == nil {
		return PersistedCandidateIdentity{}, false
	}
	identity, ok := ctx.Value(persistedCandidateIdentityContextKey{}).(PersistedCandidateIdentity)
	if !ok || !identity.HasDurableIdentity() {
		return PersistedCandidateIdentity{}, false
	}
	return identity, true
}

// PersistedCandidateIdentityFromContext is the exported read counterpart for
// handler tests that assert the durable identity reaches the resolver. The
// identity itself is server-internal and never client-visible.
func PersistedCandidateIdentityFromContext(ctx context.Context) (PersistedCandidateIdentity, bool) {
	return persistedCandidateIdentityFromContext(ctx)
}

type persistedCandidateTrustContextKey struct{}

// persistedCandidateTrustSuppressedContextKey marks a resolve whose caller has
// deliberately dropped the trust for a delisted automatic selection, so a seam
// that recomputes trust per candidate cannot put it back.
type persistedCandidateTrustSuppressedContextKey struct{}

// WithPersistedCandidateTrust marks a resolve as allowed to keep trusting the
// persisted candidate even when the provider's current list omits it. The caller
// sets it only when the catalog row is inside the configured candidate store
// window (see virtual_library.candidate_store_hours) and carries evidence that
// can back a re-serve, and the resolve is for a release the viewer is bound to
// (a session binding or an explicit version pick). It is paired with
// WithPersistedCandidateIdentity when the row has a durable identity so the
// resolver can re-identify the same release under a new result id. An
// identity-less row cannot be re-matched here, so the caller sets the flag only
// when the row's own stored URL can be served (present and unexpired) and the
// handler recovers it; a trusted resolve treats the pin like a session binding —
// rematch by identity when possible or refuse, never substitute.
func WithPersistedCandidateTrust(ctx context.Context, trusted bool) context.Context {
	if ctx == nil || !trusted {
		return ctx
	}
	return context.WithValue(ctx, persistedCandidateTrustContextKey{}, true)
}

// persistedCandidateTrustFromContext reports whether the caller declared the
// persisted candidate as trusted.
func persistedCandidateTrustFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	trusted, _ := ctx.Value(persistedCandidateTrustContextKey{}).(bool)
	return trusted
}

// WithoutPersistedCandidateTrust clears the trust flag on a resolve and keeps any
// inner seam from re-establishing it (the handler recomputes trust per candidate
// from the row, so a cleared flag alone would be re-threaded on the next
// attempt). It is the escape hatch for the one caller that may replace a trusted
// pin: an automatic selection whose candidate is gone from the listing and
// cannot be re-served from its own stored URL (see the replan rehydration). An
// explicit pick never clears it.
func WithoutPersistedCandidateTrust(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	ctx = context.WithValue(ctx, persistedCandidateTrustContextKey{}, false)
	return context.WithValue(ctx, persistedCandidateTrustSuppressedContextKey{}, true)
}

// persistedCandidateTrustSuppressed reports whether a resolve must not
// re-establish trust for the persisted candidate.
func persistedCandidateTrustSuppressed(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	suppressed, _ := ctx.Value(persistedCandidateTrustSuppressedContextKey{}).(bool)
	return suppressed
}

// PersistedCandidateTrusted reports whether a resolve was marked to keep
// trusting a persisted same-identity candidate inside the candidate store
// window. It is the read counterpart of WithPersistedCandidateTrust for callers
// in other packages (playback diagnostics and tests); the flag is
// server-internal and never client-visible.
func PersistedCandidateTrusted(ctx context.Context) bool {
	return persistedCandidateTrustFromContext(ctx)
}

// PersistedCandidateTrustSuppressed reports whether the caller dropped the trust
// for this resolve (see WithoutPersistedCandidateTrust). Seams that recompute
// trust per candidate read it so a degraded automatic selection stays rotatable.
func PersistedCandidateTrustSuppressed(ctx context.Context) bool {
	return persistedCandidateTrustSuppressed(ctx)
}

// providerOutageRelistContextKey marks a resolve as a retry of a transient
// provider-listing outage, which must re-list the provider rather than serve
// the empty answer the outage just cached.
type providerOutageRelistContextKey struct{}

// requestIDContextKey carries the transport request id into a resolve.
type requestIDContextKey struct{}

// WithRequestID threads the transport's request id into a resolve so a refusal
// log can be correlated with the edge request that caused it. The virtual
// library must not import the HTTP middleware (which owns chi's request-id
// key), so the caller passes the value it already read with chimw.GetReqID. An
// empty id leaves ctx untouched.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	requestID = strings.TrimSpace(requestID)
	if ctx == nil || requestID == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDContextKey{}, requestID)
}

// RequestIDFromContext returns the request id threaded by WithRequestID, or ""
// when none is present. It is exported so handler tests can pin the seam.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

// WithProviderOutageRelist marks ctx as a provider-outage retry. A forced
// resolve carrying this flag bypasses the fresh-serve floor so the retry asks
// the provider again instead of re-serving the negative entry the outage wrote.
func WithProviderOutageRelist(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, providerOutageRelistContextKey{}, true)
}

// providerOutageRelistFromContext reports whether this resolve is an outage
// retry that must bypass the fresh-serve floor.
func providerOutageRelistFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	relist, _ := ctx.Value(providerOutageRelistContextKey{}).(bool)
	return relist
}

// ProviderOutageRelistFromContext is the exported read counterpart of
// WithProviderOutageRelist, for callers in other packages (handler tests that
// pin the user-relink seam). It reports whether ctx was marked as an outage
// re-list.
func ProviderOutageRelistFromContext(ctx context.Context) bool {
	return providerOutageRelistFromContext(ctx)
}

// autoProfileFallbackContextKey marks a resolve whose quality profile was
// selected by the server or the client's automatic best-match logic rather than
// an explicit user version pick. A zero-match against an auto-picked profile
// degrades to the best-ranked candidate instead of hard-failing, because a
// listing the server's own recommendation cannot satisfy is a dead end the
// viewer never chose. An explicit pick keeps the refusal so the client can show
// the version list.
type autoProfileFallbackContextKey struct{}

// WithAutoProfileFallback declares that a zero-match quality profile may fall
// back to the best-ranked candidate. Absent means false, so a caller that does
// not participate keeps the conservative refusal.
func WithAutoProfileFallback(ctx context.Context, auto bool) context.Context {
	if ctx == nil || !auto {
		return ctx
	}
	return context.WithValue(ctx, autoProfileFallbackContextKey{}, true)
}

// autoProfileFallbackFromContext reports whether the caller declared the
// quality profile auto-picked and therefore fallback-eligible.
func autoProfileFallbackFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	auto, _ := ctx.Value(autoProfileFallbackContextKey{}).(bool)
	return auto
}

// PlaybackStream represents an available stream candidate formatted for
// playback selection in API handlers and Jellyfin compatibility.
type PlaybackStream struct {
	// PreserveProviderOrder prevents downstream pickers from re-ranking this listing.
	PreserveProviderOrder bool

	ID                  string
	Label               string
	URI                 string
	Resolution          string
	CodecVideo          string
	CodecAudio          string
	HasAtmos            bool
	QualityScore        int
	RequestHeaders      map[string]string
	ExpiresAt           time.Time
	HDR                 string
	SourceType          string
	FileSize            int64
	Container           string
	Bitrate             int
	FrameRate           string
	AudioLanguages      []string
	SubtitleLanguages   []string
	OwnerInstallationID int
	Visible             bool
	VisibilitySpecified bool
	AudioChannels       string
	Is10Bit             bool
	IsMultiAudio        bool
	VisualTags          []string
	AudioTags           []string
	// ProviderURL, ProviderVideoHash, ProviderGUID and ProviderReleaseName
	// carry the candidate's durable provider identity to the persistence path
	// (ReplaceVirtualCandidates), which stores them on the candidate row.
	// ProviderURL is not a client-facing field: it never leaves the server
	// process and must not be serialized to a response.
	ProviderURL         string
	ProviderVideoHash   string
	ProviderGUID        string
	ProviderReleaseName string
	// Rejected marks a candidate a configured custom format rejects. It is a
	// transient ranking signal, recomputed on every list; reject means
	// rank-last and last-resort selectable, never a hard drop.
	Rejected bool
	// MatchedFormats names the enabled, non-rejecting custom formats this
	// candidate matched under the listing's profile, ordered score-descending
	// then name-ascending. Display metadata for the version list, recomputed
	// on every list like QualityScore; nil when none matched.
	MatchedFormats []string
}

// ValidateProviderStreamURL checks structural syntax and enforces SSRF
// protection using the central remotestream policy. When allowPrivateStreams
// is set (the virtual_library.allow_private_streams opt-in) it permits
// private and local network destinations; otherwise, loopback, RFC 1918,
// link-local, and multicast addresses are rejected. It is the single
// validator the resolver applies to a fresh provider listing, exported so a
// serve-layer caller that re-uses a persisted provider URL re-validates it
// through exactly the same policy rather than a parallel copy that could
// drift.
func ValidateProviderStreamURL(ctx context.Context, raw string, allowPrivateStreams bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("empty provider stream URL")
	}
	if strings.ContainsAny(trimmed, "\x00\r\n") {
		return "", fmt.Errorf("provider stream URL contains control characters")
	}
	if allowPrivateStreams {
		parsed, err := remotestream.ValidateURLSyntaxAllowNonPublic(trimmed)
		if err != nil {
			return "", fmt.Errorf("invalid stream URL syntax: %w", err)
		}
		return parsed.String(), nil
	}
	validated, err := remotestream.ValidateURL(ctx, trimmed)
	if err != nil {
		return "", fmt.Errorf("stream URL rejected by SSRF policy: %w", err)
	}
	return validated.String(), nil
}

// validateStreamURL checks structural syntax and enforces SSRF protection
// using the central remotestream policy. When AllowPrivateStreams is enabled,
// it permits private and local network destinations; otherwise, loopback,
// RFC 1918, link-local, and multicast addresses are rejected.
func (s *Service) validateStreamURL(ctx context.Context, raw string) (string, error) {
	return ValidateProviderStreamURL(ctx, raw, s.cfg.AllowPrivateStreams)
}

// withResultKey appends the candidate identity as ?result=, mirroring the
// plugin's withVirtualResultKey.
func withResultKey(virtualPath, candID string) string {
	if candID == "" {
		return virtualPath
	}
	parsed, err := url.Parse(virtualPath)
	if err != nil {
		return virtualPath + "?result=" + candID
	}
	q := parsed.Query()
	q.Set("result", candID)
	parsed.RawQuery = q.Encode()
	return parsed.String()
}

// qualityProfileForPath returns the quality profile selected by a virtual
// URI's ?profile= label. It returns the zero profile when profiles are
// disabled or the label is unknown; ranking still applies custom formats with
// a zero profile, it just leaves the profile-specific tie-breaks inert.
func (s *Service) qualityProfileForPath(virtualPath string) quality.QualityProfile {
	if s == nil || !s.cfg.Quality.EnableProfiles {
		return quality.QualityProfile{}
	}
	parsed, err := url.Parse(virtualPath)
	if err != nil || parsed == nil {
		return quality.QualityProfile{}
	}
	label := strings.TrimSpace(parsed.Query().Get("profile"))
	if label == "" {
		return quality.QualityProfile{}
	}
	for _, p := range s.cfg.Quality.Profiles {
		if strings.EqualFold(strings.TrimSpace(p.Label), label) {
			return p
		}
	}
	return quality.QualityProfile{}
}

// Ranking source values reported by RankingForPath.
const (
	VirtualRankingSourceProfile = "profile"
	VirtualRankingSourceDefault = "default"
)

// VirtualRanking is the ranking the resolver applies to a virtual listing: the
// quality profile selected by the path's ?profile= label (empty when none
// applies) and the ordered sort criteria actually used. It is config-derived
// and read-only: resolving it performs no provider or database work.
type VirtualRanking struct {
	ProfileLabel string
	Source       string
	Criteria     []quality.SortCriterion
}

// RankingForPath resolves the ranking for a virtual path. A path whose
// ?profile= selector names a configured profile reports that profile's label
// and its own sort criteria; otherwise it reports the built-in default order.
// It is the read-only counterpart of the ranking applied during resolution.
func (s *Service) RankingForPath(virtualPath string) VirtualRanking {
	if s.cfg.Quality.TrustServerOrder() {
		return VirtualRanking{Source: VirtualRankingSourceDefault}
	}
	profile := s.qualityProfileForPath(virtualPath)
	ranking := VirtualRanking{
		Source:   VirtualRankingSourceDefault,
		Criteria: quality.EffectiveSortCriteria(profile),
	}
	if label := strings.TrimSpace(profile.Label); label != "" {
		ranking.ProfileLabel = label
		ranking.Source = VirtualRankingSourceProfile
	}
	return ranking
}

// rankCandidatesForVirtualPath is the single ranking step shared by
// ListStreams and ResolveDetailed. It scores custom formats, records the
// rejected verdict on each candidate, and orders accepted before rejected,
// then by score, resolution, source, language and OriginalIndex. It mutates
// the slice in place; the resolver hands out a private clone per call, so the
// caller owns it. The server-order preset bypasses scoring and sorting.
func (s *Service) rankCandidatesForVirtualPath(virtualPath string, candidates []stream.StreamCandidate) {
	if s.cfg.Quality.TrustServerOrder() {
		return
	}
	quality.SortCandidatesForProfile(candidates, s.qualityProfileForPath(virtualPath), s.cfg.Quality.CustomFormats)
}

// warnIfAllRejected logs once when every candidate in the set is rejected by
// custom formats. Reject is rank-last, last-resort selectable, never a hard
// drop, so the caller still uses the best rejected candidate; the Warn tells
// the operator that no accepted release was available.
func (s *Service) warnIfAllRejected(ctx context.Context, virtualPath string, candidates []stream.StreamCandidate) {
	if s == nil || s.logger == nil || len(candidates) == 0 {
		return
	}
	for _, c := range candidates {
		if !c.CustomFormatRejected {
			return
		}
	}
	profile := s.qualityProfileForPath(virtualPath)
	s.logger.WarnContext(ctx, "every virtual candidate is rejected by custom formats; using the best rejected stream",
		"profile", strings.TrimSpace(profile.Label),
		"candidates", len(candidates),
		"rejected_by", quality.RejectingFormatNames(candidates[0], s.cfg.Quality.CustomFormats))
}

// Resolve resolves a virtual path to a concrete stream URL.
func (s *Service) Resolve(ctx context.Context, virtualPath string) (string, error) {
	res, err := s.ResolveDetailed(ctx, virtualPath, false, nil, "", true)
	if err != nil {
		return "", err
	}
	return res.URL, nil
}

// Refresh resolves a virtual path with forceRefresh enabled.
func (s *Service) Refresh(ctx context.Context, virtualPath string) (string, error) {
	res, err := s.ResolveDetailed(ctx, virtualPath, true, nil, "", true)
	if err != nil {
		return "", err
	}
	return res.URL, nil
}

// ErrSessionBoundCandidateAbsent reports that a session-bound pinned candidate
// is absent from the provider's current list and no surviving keeper matched it,
// so the resolver refused to serve a sibling because candidate rotation was not
// requested. Callers that can recover by rotating (the serve layer and the
// failure-replan rehydration) distinguish this cause from a generic provider or
// resolve failure so a renumbered/dead anchor rotates without letting a display-
// driven fallback silently swap a live release.
var ErrSessionBoundCandidateAbsent = fmt.Errorf("session-bound candidate absent from provider list")

// ErrPersistedCandidateTrusted reports that a session-bound pin was absent from
// the provider's current list, but the request declared the persisted candidate
// as trusted (the catalog row carries resumable evidence and is still inside the
// candidate store window). The row may or may not carry a durable identity: an
// identity-less trusted row cannot be re-matched here, but the caller declared a
// usable stored URL (present and unexpired), so the handler recovers by
// re-serving that URL. It is the deliberate relaxation of the re-list-truth
// policy: callers must NOT rotate on it, because the persisted candidate is
// still the viewer's selection. It is distinct from
// ErrSessionBoundCandidateAbsent precisely so the serve layer and the
// failure-replan rehydration keep their rotation-on-absent behavior outside the
// window or for a row with no resumable evidence.
var ErrPersistedCandidateTrusted = fmt.Errorf("persisted virtual candidate is inside the trust window")

// ErrProviderPending reports that the only viable candidate is a release
// AltMount is actively fetching (SABnzbd queue, not history). It is neither
// dead (so callers must not rotate, indict, or prune on it) nor ready (so
// callers must hold briefly rather than serve). Callers that can wait (the
// serve layer startup loop) pause and re-list; callers that cannot treat it
// as an ordinary unresolvable release. It is distinct from
// ErrProviderUnavailable precisely so a flap backoff is never recorded for a
// release that is progressing.
var ErrProviderPending = fmt.Errorf("virtual playback provider release is still fetching")

// ResolveDetailed resolves a virtual path to a concrete stream URL through
// the core resolver, preserving full candidate identity and selection semantics:
//
//   - candidate IDs use stream.CandidateVariantID, exactly matching the plugin;
//   - a pinned resultID (from the URI ?result=) selects that candidate first;
//   - profile filtering applies if ?profile= is present and profiles are enabled;
//   - excludedCandidateIDs are skipped;
//   - preferredCandidateID is tried first;
//   - sessionBound declares whether the caller is resolving a release an
//     existing session is already serving. A quality profile is a selection
//     preference, not a gate: a session-bound candidate that still exists in
//     the provider list is served even when it fails the profile (the mismatch
//     is logged, never an error), because refusing would not prevent a release
//     swap and would only break playback. Only an indicted candidate (an
//     explicit exclusion, a collapsed-keeper exclusion, or a confirmed decode
//     rejection) is refused when substitution is disallowed. A fresh selection
//     (sessionBound false) whose probed pin is profile-removed, stale-failed,
//     or absent falls through to the best live candidate that satisfies the
//     profile and is re-pinned by the caller; when nothing satisfies the
//     profile and fallback is disallowed the caller gets the existing
//     no-stream-matches-profile error. A session-bound dead/absent pin is
//     refused when substitution is disallowed; otherwise a dead/absent pin
//     falls back;
//   - a pin whose multi-file variant dedup collapsed resolves to the surviving
//     keeper of that release (a file swap inside the release, never a release
//     swap), because dedup preserves exactly one candidate per release and
//     reports the dropped -> keeper map; the translated pin then overrides
//     rank and reject and respects exclusions, the profile filter and
//     allowCandidateSubstitution exactly like the original pin;
//   - a genuinely dead pin (absent with no keeper) still falls back when
//     substitution is allowed or the resolve is not pinned; a pinned dead pin
//     with substitution disallowed is refused, and when the caller also
//     declared the persisted candidate as trusted (WithPersistedCandidateTrust,
//     resumable evidence inside the candidate store window) the refusal carries
//     ErrPersistedCandidateTrusted instead of ErrSessionBoundCandidateAbsent so
//     callers do not rotate to a sibling.
//     A pinned resolve is a session binding or an explicitly selected
//     persisted candidate; outside the window an explicit pick carries no
//     trust and keeps the ordinary fallback;
//   - when substitution is refused and a preferredCandidateID names a
//     resolvable session release, the candidate actually served must belong to
//     that release: a present resultID for a different release is refused
//     rather than swapped in, while substitution allowed still lets the
//     explicit resultID win;
//   - every stream URL is validated against outbound SSRF.
//
// allowCandidateSubstitution gates the fallback that lets a pinned result= URI
// resolve to a *different* sibling candidate. It defaults to true so existing
// callers keep their behavior. A caller that excludes the pinned candidate
// without a verdict that indicts the release must pass false: a display-driven
// fallback (Dolby Vision to HDR10/HDR10+/DV8.1) changes the transformation on
// the same file and must never silently swap the release mid-stream. A pinned
// id that is merely absent from the provider list is a dead release and still
// falls back, so a genuinely unavailable provider recovers.
func (s *Service) ResolveDetailed(
	ctx context.Context,
	virtualPath string,
	forceRefresh bool,
	excludedCandidateIDs []string,
	preferredCandidateID string,
	sessionBound bool,
	allowCandidateSubstitution ...bool,
) (ResolvedVirtualStream, error) {
	allowSubstitution := true
	if len(allowCandidateSubstitution) > 0 {
		allowSubstitution = allowCandidateSubstitution[0]
	}
	if s == nil {
		return ResolvedVirtualStream{}, ErrVirtualLibraryUnavailable
	}
	// A forced refresh is an explicit request to re-list, so it must not be
	// short-circuited by the fail-fast mark: the mark is evidence from an
	// earlier listing, and this caller has deliberately asked for a new one.
	if !forceRefresh && s.emptyTitleMarked(virtualPath) {
		return ResolvedVirtualStream{}, fmt.Errorf("%w: title %s", ErrTitleWithoutStreams, neutralVirtualPathKey(virtualPath))
	}
	if s.Resolver == nil {
		return ResolvedVirtualStream{}, ErrVirtualLibraryUnavailable
	}

	parsedURI, parseErr := url.Parse(virtualPath)
	resultID := ""
	if parseErr == nil && parsedURI != nil {
		resultID = parsedURI.Query().Get("result")
	}

	var (
		candidates []stream.StreamCandidate
		keepers    map[string]string
		err        error
	)
	if forceRefresh {
		if providerOutageRelistFromContext(ctx) {
			candidates, keepers, _, _, err = s.Resolver.GetCandidatesFreshUnboundedWithKeepers(ctx, virtualPath)
		} else {
			candidates, keepers, _, _, err = s.Resolver.GetCandidatesFreshWithKeepers(ctx, virtualPath)
		}
	} else {
		candidates, keepers, _, _, err = s.Resolver.GetCandidatesWithKeepers(ctx, virtualPath)
	}
	if err != nil {
		return ResolvedVirtualStream{}, err
	}

	excluded := make(map[string]struct{}, len(excludedCandidateIDs))
	for _, id := range excludedCandidateIDs {
		excluded[id] = struct{}{}
	}

	if len(candidates) == 0 {
		s.noteEmptyTitleListing(virtualPath)
		return ResolvedVirtualStream{}, fmt.Errorf("no streams available from provider")
	}
	s.noteNonEmptyTitleListing(virtualPath)

	// Rank through the same helper ListStreams uses, so the version list and
	// the resolver agree on order and on the rejected verdict. Reject is
	// rank-last, last-resort selectable; an all-rejected set still resolves.
	s.rankCandidatesForVirtualPath(virtualPath, candidates)
	s.warnIfAllRejected(ctx, virtualPath, candidates)

	// A pin whose variant dedup collapsed is absent from the list, but its
	// release survives as a keeper. Translate it to that keeper before any pin
	// state is computed, so a collapsed pin behaves exactly like the real pin:
	// it overrides rank and reject, honors its exclusions and the profile
	// filter, and respects allowSubstitution=false. A genuinely dead pin (no
	// keeper) is left unchanged and still falls back as before.
	requestedResultID := resultID
	effectiveResultID := resultID
	if effectiveResultID != "" && !candidateIDPresent(candidates, effectiveResultID) {
		if keeperID := keepers[effectiveResultID]; keeperID != "" {
			effectiveResultID = keeperID
		}
	}
	effectivePreferredID := preferredCandidateID
	if effectivePreferredID != "" && !candidateIDPresent(candidates, effectivePreferredID) {
		if keeperID := keepers[effectivePreferredID]; keeperID != "" {
			effectivePreferredID = keeperID
		}
	}
	// Whether the session's pinned release, as resolved through the keeper map,
	// is present in the ranked set. Captured before the profile filter so a
	// session release the profile removes still blocks substitution below.
	sessionReleaseResolvable := preferredCandidateID != "" && candidateIDPresent(candidates, effectivePreferredID)

	profile := s.qualityProfileForPath(virtualPath)
	profileActive := strings.TrimSpace(profile.Label) != ""

	// pinnedRelease is the identity contract for a release the viewer is
	// already bound to: a live session serving it (sessionBound), or an
	// explicitly selected persisted candidate inside its trust window
	// (WithPersistedCandidateTrust, which the caller threads only for an
	// explicit pick or a session binding). Either must rematch by durable
	// identity or refuse; neither may silently substitute a sibling. Outside
	// the window an explicit pick carries no trust, so today's fallback
	// behavior is unchanged.
	pinnedRelease := sessionBound || persistedCandidateTrustFromContext(ctx)

	// A quality profile is a selection preference, not a gate on a release the
	// viewer is already bound to. When the pinned candidate still exists in the
	// provider list it is served even if it fails the profile: refusing would
	// not prevent a release swap (nothing is substituted) and would only break
	// playback. The mismatch is logged for diagnosis.
	sessionCandidatePresent := pinnedRelease && effectiveResultID != "" && candidateIDPresent(candidates, effectiveResultID)

	// Same-release re-identification. A pinned result id that is absent from a
	// fresh listing is not evidence the release is gone: providers renumber
	// result ids per listing. When the row carries a durable identity and a
	// listed candidate carries the same identity under the deduplication
	// chain's precedence, treat it as the same release re-identified: bind to
	// the new id and report IdentityRematched so the caller adopts it. A
	// genuinely different release shares no identity tier, so the dead-pin
	// refusal below still covers it. This only applies to a pinned resolve
	// (session-bound or an explicit persisted selection) that would otherwise
	// refuse a substitution; a rotation already authorizes the ordinary
	// fallback.
	identityRematched := false
	var rematchReport resolver.PersistedIdentityMatch
	if pinnedRelease && !allowSubstitution && effectiveResultID != "" && !sessionCandidatePresent {
		_, requestedExcludedEarly := excluded[requestedResultID]
		_, keeperExcludedEarly := excluded[effectiveResultID]
		if !requestedExcludedEarly && !keeperExcludedEarly {
			if identity, ok := persistedCandidateIdentityFromContext(ctx); ok {
				matched, found, report := resolver.MatchCandidateByPersistedIdentityReport(
					candidates, identity.VideoHash, identity.GUID, identity.ReleaseName, identity.ReleaseSize,
				)
				rematchReport = report
				if found {
					if matchedID := stream.CandidateVariantID(matched); matchedID != "" && matchedID != effectiveResultID {
						effectiveResultID = matchedID
						// The matched candidate is the same release as the
						// session's binding, now listed under a new id. Keep the
						// preferred id in step with it and re-mark the session
						// release resolvable: otherwise the dead-session guard
						// below sees only the old, now-absent id and rejects a
						// valid rematch. Substitution stays disabled — this
						// binds to the session's own release, not to a sibling.
						effectivePreferredID = matchedID
						sessionReleaseResolvable = true
						identityRematched = true
						// The matched candidate is the session-bound release, so
						// the profile filter must not remove it (same rule as a
						// directly present session pin).
						sessionCandidatePresent = true
					}
				}
			}
		}
	}

	if sessionCandidatePresent && profileActive {
		for _, c := range candidates {
			if stream.CandidateVariantID(c) != effectiveResultID {
				continue
			}
			if !quality.MatchProfile(c, profile) && s.logger != nil {
				s.logger.InfoContext(ctx, "session-bound virtual candidate does not satisfy the quality profile; serving the bound candidate",
					"candidate_id", effectiveResultID, "profile", strings.TrimSpace(profile.Label))
			}
			break
		}
	}

	// The profile filter selects among candidates. It must not remove a
	// session-bound candidate that still exists: the session binding wins over
	// the selection preference. For a fresh selection (or a session candidate
	// that is gone) it applies as before, and the no-stream-matches-profile
	// error stays for a selection that cannot be satisfied with fallback
	// disallowed.
	if profileActive && !sessionCandidatePresent {
		filtered := make([]stream.StreamCandidate, 0, len(candidates))
		for _, c := range candidates {
			if quality.MatchProfile(c, profile) {
				filtered = append(filtered, c)
			}
		}
		if len(filtered) == 0 && !s.cfg.Quality.FallbackToAnyStream {
			if !autoProfileFallbackFromContext(ctx) {
				if s.logger != nil {
					s.logger.WarnContext(ctx, "virtual candidate set empty after profile filter",
						"profile", strings.TrimSpace(profile.Label),
						"total", len(candidates), "matched", 0, "fallback", false)
				}
				return ResolvedVirtualStream{}, fmt.Errorf("no stream matches profile %q", strings.TrimSpace(profile.Label))
			}
			// The profile was auto-picked, not a viewer choice: a listing the
			// server's own recommendation cannot satisfy must still play. Serve
			// the best-ranked candidate unfiltered rather than hard-failing the
			// auto start, which matches fallback_to_any_stream=true for this
			// one resolve without changing the operator setting.
			if s.logger != nil {
				s.logger.InfoContext(ctx, "auto-picked virtual profile matched no candidate; serving best-ranked",
					"profile", strings.TrimSpace(profile.Label),
					"total", len(candidates), "matched", 0, "fallback", true, "auto_picked", true)
			}
		} else if len(filtered) > 0 {
			candidates = filtered
		}
	}

	// The session's own pin (preferredCandidateID) can itself be listed while
	// the probed result id is absent and keeper-remaps to a surviving keeper of
	// another release. The session binding is satisfiable by its own live pin,
	// so serve that pin instead of refusing: rotation was not requested, and a
	// still-listed release must not fail the restart. A pin reachable only
	// through the keeper map (a collapsed variant) keeps the refusal below, so
	// a collapsed session pin under a different release is never swapped in
	// silently.
	if !allowSubstitution && effectiveResultID != "" && effectiveResultID != effectivePreferredID &&
		preferredCandidateID != "" && candidateIDPresent(candidates, preferredCandidateID) {
		effectiveResultID = effectivePreferredID
	}

	// Only an indicted candidate is refused when substitution is disallowed: an
	// explicit exclusion, a collapsed-keeper exclusion, or a confirmed decode
	// rejection. All three arrive through excludedCandidateIDs. A profile
	// mismatch is a selection preference and never blocks.
	_, requestedExcluded := excluded[requestedResultID]
	_, keeperExcluded := excluded[effectiveResultID]
	pinBlocked := effectiveResultID != "" && (requestedExcluded || keeperExcluded)
	// A blocked pin is only substitutable when the caller asked for candidate
	// rotation. Otherwise refuse rather than hand back a different release
	// under the same session binding.
	if pinBlocked && !allowSubstitution {
		return ResolvedVirtualStream{}, fmt.Errorf("pinned virtual candidate %q is excluded and candidate rotation was not requested", effectiveResultID)
	}
	// When substitution is refused and the session's pinned release is
	// resolvable, the candidate actually served must belong to that release. The
	// handler probes candidates by URI, so resultID is the probed candidate and
	// the session pin arrives only as preferredCandidateID; without this guard a
	// present resultID for a different release wins and swaps the release under
	// the session binding even though substitution was refused. A directly
	// listed pin was already normalized to above, so this covers the remaining
	// case: a pin reachable only through the keeper map (its variant collapsed)
	// while a present resultID names a different release. When substitution is
	// allowed the explicit resultID still wins.
	if !allowSubstitution && sessionReleaseResolvable && effectiveResultID != "" && effectiveResultID != effectivePreferredID {
		return ResolvedVirtualStream{}, fmt.Errorf(
			"session-bound virtual candidate %q does not match resolved candidate %q and candidate rotation was not requested",
			effectivePreferredID, effectiveResultID)
	}
	// A pinned release that is absent from the provider list with no surviving
	// keeper is a genuinely dead release. With substitution refused the
	// ranked-alternatives loop below would serve a different release under the
	// viewer's binding; refuse instead. The earlier guards only cover a pin
	// whose release is still resolvable (preferred) or explicitly excluded, so
	// without this a dead pin silently swaps releases. When substitution is
	// allowed (a confirmed rotation) the documented dead-pin fallback still
	// runs, and a resolve that is neither session-bound nor trusted is
	// unaffected.
	sessionReleasePresent := sessionReleaseResolvable ||
		(preferredCandidateID == "" && effectiveResultID != "" && candidateIDPresent(candidates, effectiveResultID))
	if pinnedRelease && !allowSubstitution && effectiveResultID != "" && !pinBlocked && !sessionReleasePresent {
		// Identity presence is logged explicitly: a refusal because the row
		// carries no durable identity at all is a different operator action
		// (backfill, or wait for the row to be re-listed) than a refusal
		// because the row's identity disagreed with every listed candidate
		// (provider renumbered to a different release, or the identity is
		// stale). request_id ties the line back to the edge request.
		_, hasIdentity := persistedCandidateIdentityFromContext(ctx)
		identityTier, identityMatched := rematchReport.Tier, rematchReport.Matched
		emptyTiers := rematchReport.IdentityEmptyTiers
		if len(emptyTiers) == 0 && !hasIdentity {
			// The rematch block never ran (no identity was threaded), so
			// report every tier as empty: this refusal is "the row carries no
			// durable identity", which is a different operator action than a
			// genuine tier mismatch.
			emptyTiers = []string{"video_hash", "guid", "release_name"}
		}
		if persistedCandidateTrustFromContext(ctx) {
			// The caller declared the persisted candidate as the viewer's own
			// selection with resumable evidence inside the store window. Whether
			// or not the row carries a durable identity, the resolver must not
			// substitute a sibling: an identity-less trusted row cannot be
			// re-matched here, but the caller declared a usable stored URL, so the
			// recovery is re-serving that URL — never a release swap. Signal the
			// trust sentinel rather than the rotation sentinel so callers do not
			// rotate away from the viewer's selection.
			if s.logger != nil {
				s.logger.WarnContext(ctx, "trusted persisted virtual candidate is absent from the provider list; refusing to substitute",
					"candidate_id", effectiveResultID,
					"has_identity", hasIdentity,
					"identity_tier", identityTier,
					"identity_matched", identityMatched,
					"identity_empty_tiers", emptyTiers,
					"identity_candidate_empty_tiers", rematchReport.CandidateEmptyTiers,
					"request_id", RequestIDFromContext(ctx))
			}
			return ResolvedVirtualStream{}, fmt.Errorf("trusted persisted virtual candidate %q is no longer listed and candidate rotation was not requested: %w", effectiveResultID, ErrPersistedCandidateTrusted)
		}
		if s.logger != nil {
			s.logger.WarnContext(ctx, "refusing to substitute a dead session-bound virtual candidate",
				"candidate_id", effectiveResultID,
				"has_identity", hasIdentity,
				"identity_tier", identityTier,
				"identity_matched", identityMatched,
				"identity_empty_tiers", emptyTiers,
				"identity_candidate_empty_tiers", rematchReport.CandidateEmptyTiers,
				"request_id", RequestIDFromContext(ctx))
		}
		return ResolvedVirtualStream{}, fmt.Errorf("session-bound virtual candidate %q is no longer listed and candidate rotation was not requested: %w", effectiveResultID, ErrSessionBoundCandidateAbsent)
	}

	ordered := orderCandidates(candidates, effectivePreferredID)

	var lastErr error
	// pendingSkipped records that a pending (actively fetching) candidate was
	// passed over. A pending candidate is never failed through URL validation:
	// its URL names bytes that do not exist yet. When nothing else resolves,
	// the caller learns the release is worth waiting for rather than dead.
	var pendingSkipped bool
	// tryCandidate returns the resolved stream for one candidate, or false when
	// the candidate is excluded, is the blocked pin, does not satisfy
	// requirePin, is pending, or fails URL validation.
	tryCandidate := func(c stream.StreamCandidate, requirePin bool) (ResolvedVirtualStream, bool) {
		id := stream.CandidateVariantID(c)
		if _, skip := excluded[id]; skip {
			return ResolvedVirtualStream{}, false
		}
		if pinBlocked && id == effectiveResultID {
			return ResolvedVirtualStream{}, false
		}
		if requirePin && id != effectiveResultID {
			return ResolvedVirtualStream{}, false
		}
		if c.SourcePending {
			pendingSkipped = true
			return ResolvedVirtualStream{}, false
		}
		validated, validateErr := s.validateStreamURL(ctx, c.URL)
		if validateErr != nil {
			lastErr = validateErr
			return ResolvedVirtualStream{}, false
		}
		return ResolvedVirtualStream{
			URL:            validated,
			URI:            withResultKey(virtualPath, id),
			CandidateID:    id,
			RequestHeaders: c.RequestHeaders,
			ExpiresAt:      c.ExpiresAt,
			// Durable provider identity for the persistence path. The tier
			// order matches candidateDedupKey: hash, then GUID, then the
			// normalized release name + size. ReleaseName is always derived
			// (name+size is the fallback tier), so a row with no hash/GUID is
			// still re-matchable. The hash accepts both the Stremio
			// behaviorHints.videoHash and a torrent infoHash, and the size
			// accepts a parsed size or behaviorHints.videoSize, so an addon
			// that declares identity only through those fields is still
			// durable.
			ProviderVideoHash:   stream.CandidateVideoHash(c),
			ProviderGUID:        c.SourceGUID,
			ProviderReleaseName: resolver.CandidateReleaseName(c),
			ProviderReleaseSize: stream.CandidateDeclaredSize(c),
			// The candidate's provider-declared inventory travels with the
			// resolution so a caller can seed a declared inventory on adoption.
			CodecAudio:        c.CodecAudio,
			AudioLanguages:    c.AudioLanguages,
			SubtitleLanguages: c.SubtitleLanguages,
		}, true
	}

	// The explicit pin is tried first even when rank or a custom-format reject
	// would place it last: a pin overrides rank and reject.
	pinTried := false
	if effectiveResultID != "" && !pinBlocked {
		for _, c := range ordered {
			if id := stream.CandidateVariantID(c); id == effectiveResultID {
				pinTried = true
			}
			if resolved, ok := tryCandidate(c, true); ok {
				resolved.IdentityRematched = identityRematched
				return resolved, nil
			}
		}
	}
	// Ranked alternatives. A session preferredCandidateID is already promoted
	// to the front by orderCandidates; a dead pin, or a blocked pin whose
	// substitution was requested, resolves here. When substitution is refused
	// for a pinned resolve, the pin's own validation failure is the answer:
	// entering the alternatives loop would serve a different release under
	// the viewer's binding (the failure that brought us here is about the
	// pinned candidate, not an invitation to pick a sibling). A pinned
	// request always names a release the viewer is bound to — whether or not
	// the session/trust context is also set — so the gate keys on the pin
	// being tried, not on pinnedRelease.
	if effectiveResultID != "" && !allowSubstitution && pinTried {
		if lastErr != nil {
			return ResolvedVirtualStream{}, fmt.Errorf("pinned virtual candidate %q failed URL validation and candidate rotation was not requested: %w", effectiveResultID, lastErr)
		}
		return ResolvedVirtualStream{}, fmt.Errorf("pinned virtual candidate %q is unavailable and candidate rotation was not requested", effectiveResultID)
	}
	for _, c := range ordered {
		if resolved, ok := tryCandidate(c, false); ok {
			return resolved, nil
		}
	}

	if lastErr != nil {
		return ResolvedVirtualStream{}, fmt.Errorf("virtual playback provider returned an unsafe stream URL: %w", lastErr)
	}
	if pendingSkipped {
		return ResolvedVirtualStream{}, ErrProviderPending
	}
	return ResolvedVirtualStream{}, fmt.Errorf("no streams available from provider")
}

// ListStreams returns the complete candidate list formatted as PlaybackStream records.
func (s *Service) ListStreams(ctx context.Context, virtualPath string) ([]PlaybackStream, error) {
	if s == nil || s.Resolver == nil {
		return nil, ErrVirtualLibraryUnavailable
	}
	candidates, _, _, err := s.Resolver.GetCandidates(ctx, virtualPath)
	if err != nil {
		return nil, err
	}
	return s.playbackStreamsFrom(ctx, virtualPath, candidates), nil
}

// ListStreamsFresh is ListStreams with the bounded provider cache bypassed: the
// caller explicitly asked for a fresh provider listing (the viewer's "refresh
// list" action). It ranks and formats the result identically, so the candidate
// set a refresh persists matches what the version list then shows.
func (s *Service) ListStreamsFresh(ctx context.Context, virtualPath string) ([]PlaybackStream, error) {
	if s == nil || s.Resolver == nil {
		return nil, ErrVirtualLibraryUnavailable
	}
	candidates, _, _, err := s.Resolver.GetCandidatesFresh(ctx, virtualPath)
	if err != nil {
		return nil, err
	}
	return s.playbackStreamsFrom(ctx, virtualPath, candidates), nil
}

// playbackStreamsFrom is the shared ranking and formatting step of the two
// list entry points.
func (s *Service) playbackStreamsFrom(ctx context.Context, virtualPath string, candidates []stream.StreamCandidate) []PlaybackStream {
	// A listing that observed streams is recovery evidence: clear the
	// fail-fast mark here as well as on resolve, so a version list that loads
	// before the next playback does not leave a stale mark to short-circuit
	// that playback.
	if len(candidates) > 0 {
		s.noteNonEmptyTitleListing(virtualPath)
	}
	// Rank here too: the handler's auto-pick walks ListStreams, not
	// ResolveDetailed, so the profile/custom-format order and the rejected
	// verdict must be carried on the stream records.
	s.rankCandidatesForVirtualPath(virtualPath, candidates)
	s.warnIfAllRejected(ctx, virtualPath, candidates)
	formats := s.cfg.Quality.CustomFormats
	if s.cfg.Quality.TrustServerOrder() {
		formats = nil
	}
	streams := make([]PlaybackStream, 0, len(candidates))
	for _, c := range candidates {
		id := stream.CandidateVariantID(c)
		label := stream.CandidateDisplayName(c)
		streams = append(streams, PlaybackStream{
			PreserveProviderOrder: s.cfg.Quality.TrustServerOrder(),

			ID:                  id,
			Label:               label,
			URI:                 withResultKey(virtualPath, id),
			Resolution:          c.Resolution,
			CodecVideo:          c.CodecVideo,
			CodecAudio:          c.CodecAudio,
			HasAtmos:            c.HasAtmos,
			QualityScore:        c.QualityScore,
			MatchedFormats:      quality.MatchedFormatNames(c, formats),
			Rejected:            c.CustomFormatRejected,
			RequestHeaders:      c.RequestHeaders,
			ExpiresAt:           c.ExpiresAt,
			HDR:                 c.HDR,
			SourceType:          c.SourceType,
			FileSize:            c.FileSize,
			Container:           c.Container,
			Bitrate:             0,
			FrameRate:           "",
			AudioLanguages:      c.AudioLanguages,
			SubtitleLanguages:   c.SubtitleLanguages,
			OwnerInstallationID: 0,
			Visible:             true,
			VisibilitySpecified: true,
			AudioChannels:       c.AudioChannels,
			Is10Bit:             c.Is10Bit,
			IsMultiAudio:        c.IsMultiAudio,
			VisualTags:          c.VisualTags,
			AudioTags:           c.AudioTags,
			ProviderURL:         c.URL,
			ProviderVideoHash:   stream.CandidateVideoHash(c),
			ProviderGUID:        c.SourceGUID,
			ProviderReleaseName: resolver.CandidateReleaseName(c),
		})
	}
	return streams
}

// candidateIDPresent reports whether any candidate carries the given stable
// variant id.
func candidateIDPresent(candidates []stream.StreamCandidate, id string) bool {
	if id == "" {
		return false
	}
	for _, c := range candidates {
		if stream.CandidateVariantID(c) == id {
			return true
		}
	}
	return false
}

func orderCandidates(candidates []stream.StreamCandidate, preferredID string) []stream.StreamCandidate {
	out := append([]stream.StreamCandidate(nil), candidates...)
	if preferredID == "" {
		return out
	}
	for i, c := range out {
		if stream.CandidateVariantID(c) == preferredID {
			out[0], out[i] = out[i], out[0]
			break
		}
	}
	return out
}
