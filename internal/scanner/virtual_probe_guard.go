package scanner

import (
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// VirtualRuntimeTolerance is the maximum fraction a probed duration may deviate
// from the catalog runtime before the candidate is treated as mislabeled
// content rather than a legitimate release.
//
// 0.30 accommodates anime and international content where fansub groups trim
// OP/ED sequences and streaming cuts differ from broadcast metadata by 3–5
// minutes on a typical 24-minute episode. Genuinely wrong content (a trailer, a
// sample, or a different show) differs by far more than this.
const VirtualRuntimeTolerance = 0.30

// VirtualRuntimePlausible reports whether a probed duration is consistent with
// the item's catalog runtime. Unknown values pass: the gate only rejects when
// both sides are known and clearly disagree.
func VirtualRuntimePlausible(probedSeconds, runtimeMinutes int) bool {
	if probedSeconds <= 0 || runtimeMinutes <= 0 {
		return true
	}
	expected := float64(runtimeMinutes) * 60
	diff := math.Abs(float64(probedSeconds) - expected)
	return diff <= expected*VirtualRuntimeTolerance
}

// VirtualDurationProvesIdentity reports whether a measured duration is a known,
// plausible match for the item's expected runtime. Unlike
// VirtualRuntimePlausible, missing evidence on either side is not a pass. A
// served-size mismatch needs a positive signal, not the absence of a
// contradiction, before the evidence counts for anything — and even then it
// only keeps the bytes playable, never proves release identity.
func VirtualDurationProvesIdentity(probedSeconds, expectedRuntimeMinutes int) bool {
	return probedSeconds > 0 && expectedRuntimeMinutes > 0 &&
		VirtualRuntimePlausible(probedSeconds, expectedRuntimeMinutes)
}

// VirtualServedSizePlausibilityRatio is the largest served-vs-declared size
// ratio a repeated serve may confirm as release identity. A provider's listing
// rounds release sizes (the observed production band is 12–19% for a 0.1 GB
// advertisement), so a duration proof serves as the first observation for a gap
// well beyond the 5% evidence tolerance. Rounding cannot explain a multiple,
// though: the production failure this cap exists for served 17.7 GB for a
// 3.78 GB declaration (4.7x). Twice the advertised size is far outside any
// rounding band and far below that failure, so a gap beyond it can never
// establish release identity.
//
// The cap no longer decides usability. Bytes outside the ratio stay playable,
// inventoried and reported like any other unverified serve; only their release
// identity remains unproven. See ClassifyVirtualServedEvidence.
const VirtualServedSizePlausibilityRatio = 2.0

// VirtualServedSizeImplausible reports whether the served-vs-declared size gap
// is too large for a duration proof to excuse as a rounded provider listing.
// Unknown values pass: the cap only applies when both sizes are known.
func VirtualServedSizeImplausible(declaredBytes, servedBytes int64) bool {
	if declaredBytes <= 0 || servedBytes <= 0 {
		return false
	}
	larger, smaller := servedBytes, declaredBytes
	if declaredBytes > servedBytes {
		larger, smaller = declaredBytes, servedBytes
	}
	return float64(larger) > float64(smaller)*VirtualServedSizePlausibilityRatio
}

// VirtualServedEvidenceVerdict classifies probed evidence against the release
// the selected catalog row declares.
type VirtualServedEvidenceVerdict int

const (
	// VirtualServedEvidenceAdmitted means the served container size agrees with
	// the declared release size. The evidence establishes release identity and
	// may be cached, persisted and used for identity-sensitive decisions.
	VirtualServedEvidenceAdmitted VirtualServedEvidenceVerdict = iota
	// VirtualServedEvidenceUnverified means the served size contradicts the
	// declared release size while nothing the probe measured contradicts the
	// declaration. The bytes are usable: the probe inventory (measured tracks
	// and duration) may be written to the row and reported to clients, and the
	// file may be planned and played. A release can be posted short of the
	// indexer's advertised size, and some deployments serve a materially
	// different size for a normal release.
	//
	// The verdict withholds release identity only. The evidence must not enter
	// the probe cache as authoritative evidence, stamp the row's probe identity,
	// or feed identity-sensitive decisions such as version death or candidate
	// bounding. The first served-size observation a duration-proven probe
	// records is also unverified: one serve cannot distinguish the release from
	// a wrong file of the same runtime.
	VirtualServedEvidenceUnverified
	// VirtualServedEvidenceMismatch means the probe found evidence of a
	// different file: the measured duration contradicts the declared runtime, or
	// the measured track layout contradicts the declaration (a declared video
	// release served with no video stream). Size alone never produces this
	// verdict; see ClassifyVirtualServedEvidence.
	VirtualServedEvidenceMismatch
)

// EstablishesIdentity reports whether the verdict is strong enough to stand as
// the row's release identity.
func (v VirtualServedEvidenceVerdict) EstablishesIdentity() bool {
	return v == VirtualServedEvidenceAdmitted
}

// Usable reports whether the probed bytes may be inventoried, planned and
// played. Only a positive identity contradiction rejects them; a size gap with
// nothing contradicting the declaration is usable but unverified.
func (v VirtualServedEvidenceVerdict) Usable() bool {
	return v != VirtualServedEvidenceMismatch
}

// VirtualDurationContradictsDeclaration reports whether both the measured
// duration and the catalog runtime are known and disagree beyond
// VirtualRuntimeTolerance. This is a positive identity signal: a different
// title (a remux, a trailer, an episode from another season) does not land
// inside the tolerance no matter what size it serves.
func VirtualDurationContradictsDeclaration(probedSeconds, expectedRuntimeMinutes int) bool {
	return probedSeconds > 0 && expectedRuntimeMinutes > 0 &&
		!VirtualRuntimePlausible(probedSeconds, expectedRuntimeMinutes)
}

// VirtualTrackLayoutContradictsDeclaration reports whether the probe's measured
// track layout is positive evidence that the bytes are not the declared release.
// It is deliberately narrow so a legitimate re-encode is never mistaken for a
// different file:
//
//   - the declaration names video (a resolution, a video codec, or video tracks)
//     and the probed bytes carry no video stream at all;
//   - otherwise nothing is claimed. Codec, container, HDR, language and quality
//     differences are normal variants of the same release, and duration remains
//     the primary identity check.
func VirtualTrackLayoutContradictsDeclaration(declared, probed *models.MediaFile) bool {
	if declared == nil || probed == nil || declared.IsAudioOnly() {
		return false
	}
	declaresVideo := strings.TrimSpace(declared.Resolution) != "" ||
		strings.TrimSpace(declared.CodecVideo) != "" ||
		len(declared.VideoTracks) > 0
	if !declaresVideo {
		return false
	}
	if len(probed.VideoTracks) > 0 {
		return false
	}
	// "No video track" is a contradiction only when the probe also measured
	// the container. An empty or thin probe result (a caller-supplied fake, a
	// cache entry that only carried the audio menu, a partial read) is missing
	// evidence, not evidence of an audio-only file.
	return probed.Duration > 0 || probed.ProbedFormatSize > 0
}

// ClassifyVirtualServedEvidence classifies probed evidence against the declared
// release. It separates identity from usability: size alone never rejects the
// bytes. A deployment can serve a materially different size for a normal
// release (provider listing vs. what the storage layer actually holds), so a
// size contradiction is Unverified — usable for inventory, planning and
// playback, but not proof of which release was served.
//
// A genuine different-file signal still rejects: the measured duration must be
// plausible for the item's runtime, and (through
// ClassifyVirtualServedEvidenceForProbe) the measured track layout must not
// contradict a declared video release. Those are positive contradictions, not
// the absence of a size match.
func ClassifyVirtualServedEvidence(declaredBytes, servedBytes int64, probedSeconds, expectedRuntimeMinutes int) VirtualServedEvidenceVerdict {
	if VirtualDurationContradictsDeclaration(probedSeconds, expectedRuntimeMinutes) {
		return VirtualServedEvidenceMismatch
	}
	if VirtualServedSizeMismatch(declaredBytes, servedBytes) {
		return VirtualServedEvidenceUnverified
	}
	return VirtualServedEvidenceAdmitted
}

// VirtualServedEvidenceRejected reports whether probed evidence must be
// rejected as a different file than the catalog row declares: the measured
// duration contradicts the runtime. A size contradiction alone is unverified,
// not rejected: it still plays, but see VirtualServedEvidenceVerdict for what
// it may not be used for.
func VirtualServedEvidenceRejected(declaredBytes, servedBytes int64, probedSeconds, expectedRuntimeMinutes int) bool {
	return ClassifyVirtualServedEvidence(declaredBytes, servedBytes, probedSeconds, expectedRuntimeMinutes) == VirtualServedEvidenceMismatch
}

// VirtualServedSizeSnapshot is the served byte size observed for one pinned
// candidate after a probe whose measured duration proved the content, despite a
// served-vs-declared size gap. Providers routinely advertise a rounded release
// size (an indexer declaration of 2.04 GB for bytes that measure 1.78 GB, ~13%
// off), so the declared size alone can never clear the 5% evidence gate for
// that pin.
//
// One duration-proven serve is an observation, not identity: a wrong file that
// happens to carry the item's runtime would look the same. A snapshot therefore
// only becomes the pin's comparison baseline once a later serve lands on it;
// see RecordVirtualServedSizeSnapshotIfProven and
// ClassifyVirtualServedEvidenceForPinWithSnapshot.
//
// DeclaredBytes is kept so a re-declared release invalidates the snapshot; it is
// never used to replace the row's declared size. FileSize stays the release's
// advertised size for the AltMount-completion comparison and every other
// declared-size decision, so a snapshot can never fake a SourceConfirmed
// verdict.
type VirtualServedSizeSnapshot struct {
	DeclaredBytes  int64
	ObservedBytes  int64
	InventoryToken string
}

// virtualServedSizeSnapshotMaxEntries bounds the snapshot store. Entries are
// per pinned candidate, so the bound tracks the live candidate set the way the
// probe-failure damper does; the oldest snapshot is evicted first.
const virtualServedSizeSnapshotMaxEntries = 512

// virtualServedSizeSnapshotStore is the process-wide snapshot store. It is
// package-level because the guard has no owner object: probes arrive from the
// playback handler and from the probe cache, and both must see each other's
// observations for the same pin.
type virtualServedSizeSnapshotStore struct {
	mu       sync.Mutex
	entries  map[string]virtualServedSizeSnapshotEntry
	sequence int64
}

type virtualServedSizeSnapshotEntry struct {
	snapshot VirtualServedSizeSnapshot
	sequence int64
}

var virtualServedSizeSnapshots = &virtualServedSizeSnapshotStore{entries: make(map[string]virtualServedSizeSnapshotEntry)}

// VirtualServedSizePin names the pinned candidate a served-size snapshot
// belongs to: its provider-neutral virtual path, its owner installation and its
// concrete ?result= pick. A path with no concrete pick names a release rather
// than one candidate's bytes, so it has no pin and returns "": an observed size
// recorded for whatever candidate an auto-pick happened to serve must never
// become the baseline for a sibling.
func VirtualServedSizePin(virtualPath string, ownerInstallationID int) string {
	trimmed := strings.TrimSpace(virtualPath)
	if trimmed == "" {
		return ""
	}
	neutral := trimmed
	resultID := ""
	if parsed, err := url.Parse(trimmed); err == nil && parsed != nil {
		query := parsed.Query()
		resultID = strings.TrimSpace(query.Get("result"))
		if resultID != "" {
			query.Del("result")
			parsed.RawQuery = query.Encode()
			neutral = parsed.String()
		}
	}
	if resultID == "" {
		return ""
	}
	return neutral + "\x00" + strconv.Itoa(ownerInstallationID) + "\x00" + resultID
}

// VirtualServedSizeSnapshotFor returns the snapshot recorded for pin when it
// still describes the pin: the release is still declared at the same size and
// the probed subtitle inventory is unchanged. Either difference invalidates the
// snapshot, because a layout change means the bytes named by the pin's ordinals
// are not the bytes the size was observed for.
func VirtualServedSizeSnapshotFor(pin string, declaredBytes int64, inventoryToken string) (VirtualServedSizeSnapshot, bool) {
	c := virtualServedSizeSnapshots
	if c == nil || pin == "" || declaredBytes <= 0 {
		return VirtualServedSizeSnapshot{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[pin]
	if !ok {
		return VirtualServedSizeSnapshot{}, false
	}
	if entry.snapshot.DeclaredBytes != declaredBytes || entry.snapshot.InventoryToken != inventoryToken || entry.snapshot.ObservedBytes <= 0 {
		delete(c.entries, pin)
		return VirtualServedSizeSnapshot{}, false
	}
	return entry.snapshot, true
}

// RecordVirtualServedSizeSnapshotIfProven records one served-size observation
// for pin when, and only when, a size gap had to be excused by a measured
// duration that proves the content is the item's. It reports whether this call
// recorded the pin's first observation.
//
// Recording an observation is not admission. The observation exists so a later
// serve of the same pin can be compared against it: identical bytes confirm the
// pin, divergent bytes keep it unverified. See
// ClassifyVirtualServedEvidenceForPinWithSnapshot for the verdict side.
//
// It is deliberately the only writer:
//   - a size within tolerance needs no snapshot, so the declared size stays the
//     baseline;
//   - a probe whose duration does not prove identity is unverified or a
//     mismatch and must never teach the gate to accept those bytes;
//   - a gap beyond VirtualServedSizePlausibilityRatio records nothing even with
//     a matching duration. A multiple of the advertised size is not a rounded
//     listing, so there is no byte size a later serve could confirm as identity;
//   - a pin's existing observation is never overwritten by a divergent serve.
//     A wrong file that once passed the duration check would otherwise keep
//     moving the pin's baseline to whatever it serves, which is exactly how one
//     plausible-looking wrong serve becomes identity. A re-declared release or a
//     changed subtitle inventory invalidates the observation instead
//     (VirtualServedSizeSnapshotFor).
func RecordVirtualServedSizeSnapshotIfProven(pin string, declaredBytes, servedBytes int64, probedSeconds, expectedRuntimeMinutes int, inventoryToken string) bool {
	c := virtualServedSizeSnapshots
	if c == nil || pin == "" || declaredBytes <= 0 || servedBytes <= 0 {
		return false
	}
	if !VirtualServedSizeMismatch(declaredBytes, servedBytes) {
		return false
	}
	if VirtualServedSizeImplausible(declaredBytes, servedBytes) {
		return false
	}
	if !VirtualDurationProvesIdentity(probedSeconds, expectedRuntimeMinutes) {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]virtualServedSizeSnapshotEntry)
	}
	if existing, ok := c.entries[pin]; ok &&
		existing.snapshot.DeclaredBytes == declaredBytes &&
		existing.snapshot.InventoryToken == inventoryToken &&
		existing.snapshot.ObservedBytes > 0 {
		// The pin already has an observation for this declaration and layout.
		// Keep it: a second, divergent serve is not evidence that replaces the
		// first one, and letting it would turn a duration-plausible wrong file
		// into the baseline by repetition.
		return false
	}
	if _, exists := c.entries[pin]; !exists && len(c.entries) >= virtualServedSizeSnapshotMaxEntries {
		c.evictOldestLocked()
	}
	c.sequence++
	c.entries[pin] = virtualServedSizeSnapshotEntry{
		snapshot: VirtualServedSizeSnapshot{
			DeclaredBytes:  declaredBytes,
			ObservedBytes:  servedBytes,
			InventoryToken: inventoryToken,
		},
		sequence: c.sequence,
	}
	return true
}

// evictOldestLocked drops the least recently recorded snapshot. Caller holds
// c.mu.
func (c *virtualServedSizeSnapshotStore) evictOldestLocked() {
	victim := ""
	var oldest int64
	for pin, entry := range c.entries {
		if victim == "" || entry.sequence < oldest {
			victim, oldest = pin, entry.sequence
		}
	}
	if victim != "" {
		delete(c.entries, victim)
	}
}

// ResetVirtualServedSizeSnapshots drops every recorded snapshot. Tests use it to
// isolate cases that share a pin; production never clears the store.
func ResetVirtualServedSizeSnapshots() {
	c := virtualServedSizeSnapshots
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]virtualServedSizeSnapshotEntry)
}

// ClassifyVirtualServedEvidenceForPin is ClassifyVirtualServedEvidence with the
// pin's recorded observed size as the comparison baseline when a snapshot is
// still valid for it. It is the read-only view: it neither records nor confirms
// an observation, so the returned verdict carries no admission weight by itself.
// A pin with no snapshot (or one invalidated by a declared size or
// subtitle-inventory change) keeps comparing against the declared size, so the
// gate behaves exactly as before for every unproven pin.
//
// A gap beyond VirtualServedSizePlausibilityRatio is classified against the
// declaration before any snapshot is consulted: no observation may confirm
// bytes that far from the release's advertised size.
func ClassifyVirtualServedEvidenceForPin(pin string, declaredBytes, servedBytes int64, probedSeconds, expectedRuntimeMinutes int, inventoryToken string) VirtualServedEvidenceVerdict {
	if VirtualServedSizeImplausible(declaredBytes, servedBytes) {
		return ClassifyVirtualServedEvidence(declaredBytes, servedBytes, probedSeconds, expectedRuntimeMinutes)
	}
	baseline := declaredBytes
	if snapshot, ok := VirtualServedSizeSnapshotFor(pin, declaredBytes, inventoryToken); ok {
		baseline = snapshot.ObservedBytes
	}
	return ClassifyVirtualServedEvidence(baseline, servedBytes, probedSeconds, expectedRuntimeMinutes)
}

// VirtualServedSizeSubtitleToken is the subtitle inventory token a snapshot is
// keyed on. It is the same token the subtitle cache uses, so a layout change
// that invalidates warmed subtitle artifacts invalidates the size snapshot too.
func VirtualServedSizeSubtitleToken(tracks []models.SubtitleTrack) string {
	return playback.SubtitleInventoryToken(tracks)
}

// ClassifyVirtualServedEvidenceForPinWithSnapshot returns the evidence verdict
// for a probe of pin and records the pin's first served-size observation when
// the only reason the evidence is not admitted is a size gap that a measured
// duration proves is legitimate.
//
// A single duration-proven serve is an observation, not identity: a wrong file
// of the right runtime and a plausible size would produce exactly the same
// probe. So the serve that records the observation stays Unverified — playable
// for this request, but not cached, persisted as the row's identity, or used to
// bound candidates — and only a later serve that lands on the recorded size is
// Admitted. After that, the observation is the pin's baseline and the cost is
// one extra probe per pin lifetime.
//
// A mismatch (a duration contradiction) is returned unchanged and records
// nothing: a different file must never teach the gate to accept its bytes. A
// serve that diverges from an existing observation is Unverified as well and
// leaves the observation untouched, so a later wrong file cannot displace the
// bytes the pin was proven to serve. A gap beyond
// VirtualServedSizePlausibilityRatio is unverified at most: the bytes stay
// usable, but no observation or repeat serve can confirm identity that far from
// the advertised size.
func ClassifyVirtualServedEvidenceForPinWithSnapshot(pin string, declaredBytes, servedBytes int64, probedSeconds, expectedRuntimeMinutes int, inventoryToken string) VirtualServedEvidenceVerdict {
	if VirtualServedSizeImplausible(declaredBytes, servedBytes) {
		return ClassifyVirtualServedEvidence(declaredBytes, servedBytes, probedSeconds, expectedRuntimeMinutes)
	}
	snapshot, hasSnapshot := VirtualServedSizeSnapshotFor(pin, declaredBytes, inventoryToken)
	baseline := declaredBytes
	if hasSnapshot {
		baseline = snapshot.ObservedBytes
	}
	verdict := ClassifyVirtualServedEvidence(baseline, servedBytes, probedSeconds, expectedRuntimeMinutes)
	if verdict == VirtualServedEvidenceMismatch {
		return verdict
	}
	if hasSnapshot {
		// A serve inside the observation's tolerance is the second observation
		// that admits the pin. Anything else stays unverified without moving
		// the baseline (RecordVirtualServedSizeSnapshotIfProven refuses to
		// overwrite it).
		return verdict
	}
	if verdict == VirtualServedEvidenceAdmitted {
		// The served size clears the declaration on its own; no observation is
		// needed to excuse a gap, so the pin is admitted without one.
		return verdict
	}
	RecordVirtualServedSizeSnapshotIfProven(pin, declaredBytes, servedBytes, probedSeconds, expectedRuntimeMinutes, inventoryToken)
	return VirtualServedEvidenceUnverified
}

// ClassifyVirtualServedEvidenceForProbe is the one gate every probe acceptance
// point calls. It applies the positive identity contradictions first — a
// measured duration or track layout that contradicts the declaration — and then
// the pin-aware served-size comparison. Composing them here keeps a fresh probe
// and a probe-cache hit on the same rule: a size gap never rejects the bytes,
// while a duration or layout contradiction rejects them on every path.
func ClassifyVirtualServedEvidenceForProbe(
	pin string,
	declaredBytes int64,
	declared, probed *models.MediaFile,
	expectedRuntimeMinutes int,
) VirtualServedEvidenceVerdict {
	if probed == nil {
		// No probe result is no evidence; callers must not treat it as a
		// playable inventory.
		return VirtualServedEvidenceMismatch
	}
	if VirtualTrackLayoutContradictsDeclaration(declared, probed) {
		return VirtualServedEvidenceMismatch
	}
	return ClassifyVirtualServedEvidenceForPinWithSnapshot(
		pin, declaredBytes, probed.ProbedFormatSize, probed.Duration, expectedRuntimeMinutes,
		VirtualServedSizeSubtitleToken(probed.SubtitleTracks),
	)
}
