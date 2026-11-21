package playback

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/Silo-Server/silo-server/internal/clientip"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/streamlocation"
	"github.com/Silo-Server/silo-server/internal/tonemap"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// PositionUnknown marks a session whose latest playback position was not reported.
const PositionUnknown = -1

// Transport output facts are independent of the whole-session play method.
const (
	OutputContainerFMP4   = "fmp4"
	OutputContainerMPEGTS = "mpegts"
	OutputProtocolHLS     = "hls"
	OutputProtocolHTTP    = "http"
)

// ClientCancelEvidence records when a transport serve observed the client
// cancel the request, scoped to the route it was observed on. A cancel is
// evidence about one request, never a verdict on the session: the viewer may
// have navigated off, or hls.js may have given up and retried. The revision and
// binding generation let a later reader tell a cancel for the current route
// from a predecessor's stale one instead of treating the timestamp alone as
// proof the session died. Zero At means no observed cancellation.
type ClientCancelEvidence struct {
	At                time.Time
	StreamRevision    uint64
	BindingGeneration uint64
}

// ClientCancelNamesCurrentRoute reports whether the recorded transport cancel
// still names this session's current route. A newer stream revision or binding
// generation means the cancel described a request the session has since
// superseded, so it is evidence about a predecessor route, not the live one.
func (s *Session) ClientCancelNamesCurrentRoute() bool {
	return !s.ClientCanceled.At.IsZero() &&
		s.ClientCanceled.StreamRevision == s.streamRevision &&
		s.ClientCanceled.BindingGeneration == s.virtualSourceGeneration
}

// Session represents an active playback session.
type Session struct {
	ID                   string
	UserID               int
	ProfileID            string
	MediaFileID          int
	RequestedMediaFileID int
	PlayMethod           PlayMethod
	BasePlayMethod       PlayMethod
	TranscodeAudio       bool // when true, remux should transcode audio to AAC
	RemuxDVMode          RemuxDVMode
	// DVProfile is the Dolby Vision profile the committed plan probed and
	// validated. Virtual catalog rows can carry empty video_tracks while the
	// plan's probe has ground truth, so stream time must trust the session's
	// profile over the re-derived file value.
	DVProfile int
	// DVProfilePin is the first-verified-wins Dolby Vision profile for the
	// session's effective file. A replan re-reads the catalog row and can see a
	// drifting dv_profile on bytes that never changed, so the pin freezes the
	// value the start (or first replan) verified and every later replan reuses
	// it. It is keyed by file id: a candidate-binding move to a different
	// effective file re-arms the pin from that file's first read. Zero Profile
	// means no pin.
	DVProfilePin     DVPinV3
	ClientIP         string // resolved client IP for the playback session
	DeviceID         string // client installation identifier, when available
	DevicePlatform   string // client platform declaration, when available
	StreamLocation   string // local/remote policy classification fixed at playback negotiation
	ClientName       string // reported playback client name, when available
	ClientVersion    string // reported playback client version, when available
	ClientBuild      string // opaque reported client build identifier, when available
	ClientChannel    string // opaque reported client distribution channel, when available
	ClientUserAgent  string // trimmed request user agent for the playback session
	IsJellyfinCompat bool   // immutable origin identity for Jellyfin compatibility sessions
	// VirtualSourceURI is the provider-neutral result selected and probed for
	// this session. Provider URLs never enter session state; handlers resolve
	// this URI again when a transport or subtitle input is opened.
	VirtualSourceURI                 string
	VirtualSourceOwnerInstallationID int
	// VirtualSubtitleTracks and VirtualExternalSubtitles are the subtitle
	// inventories captured at plan time. A virtual catalog row is mutable
	// (candidate rotation re-probes it), so the serve paths must extract from
	// the same evidence the plan promised, not whatever the row holds now.
	VirtualSubtitleTracks    []models.SubtitleTrack
	VirtualExternalSubtitles []models.ExternalSubtitle
	// VirtualAudioTracks is the audio inventory captured alongside the
	// subtitle evidence. A same-row rotation (same catalog row id, different
	// underlying release) replaces the row's tracks without moving the row id,
	// so the selection remap needs the plan-time audio inventory to compare
	// against and remap from.
	VirtualAudioTracks []models.AudioTrack
	// VirtualAudioSelectionInvalid records that the session's committed audio
	// selection could not be re-identified in the verified inventory a deferred
	// probe replaced the carried overlay with. A vanished or ambiguous selection
	// is a server-enforced invalid selection: no positional index survives to
	// name whichever track now occupies the old slot, and the transport fails
	// closed so the client replans and re-selects. Cleared when a new selection
	// is committed or the candidate binding moves.
	VirtualAudioSelectionInvalid bool
	// VirtualSourceRevision is the opaque media-generation revision the planner
	// published for this session's virtual candidate (PlanV3.VirtualSourceRevision).
	// A candidate binding move clears it (see SetVirtualSource), so a non-empty
	// value marks evidence that a plan has re-probed against the current
	// binding; it is the rotation signal a track fingerprint cannot always
	// provide.
	VirtualSourceRevision string
	// VirtualProbeOutcome is the disposition of the most recent background probe
	// for this session's bound virtual candidate. It is set to pending when the
	// full track enumeration is deferred past the first-byte commit, then to
	// verified or failed by the probe worker. Empty means no deferred probe is
	// outstanding: the inventory the plan published is already final. It exists
	// so an inventory reader can distinguish an unfinished probe from one that
	// terminally failed and let the client leave its loading state either way.
	VirtualProbeOutcome string
	// virtualProbeArmedAt is the instant the deferred probe lifecycle for the
	// current binding generation entered pending. It bounds the pending state:
	// the handler's convergence watchdog fails a session whose pending outcome
	// has been outstanding past the bound, so a probe no worker will finish can
	// never leave a client waiting forever. Zero means no deferred lifecycle is
	// outstanding (empty, verified, or failed).
	virtualProbeArmedAt time.Time
	// virtualProbeArmedBinding is the candidate binding the outstanding pending
	// mark was armed against, captured when the outcome entered pending and
	// cleared whenever it leaves pending. It lets a caller tell a LIVE pending —
	// owned by the session's current binding, which an admitted probe will land —
	// from a STALE one left behind by a row-only move that never advanced the
	// generation (see SetEffectiveMediaFileID). A stale pending no worker's fence
	// can land, so the current binding owes a fresh probe; a live pending already
	// has one and must not be duplicated.
	//
	// It is in-memory only, not persisted with the session. A session
	// reconstructed after a restart carries the zero snapshot, so any pending mark
	// on it reads as stale and a replacement may be admitted (the pre-restart
	// worker is gone, so that is correct, not merely safe); the convergence
	// watchdog still bounds a pending mark no worker lands.
	virtualProbeArmedBinding VirtualSourceBindingSnapshot
	// virtualProbeLifecycle is a monotonic token that changes every time a
	// deferred-probe lifecycle enters pending. It is the ownership fence the
	// replacement path's terminal write must match. SetEffectiveMediaFileID moves
	// a session's effective row without advancing virtualSourceGeneration, so a
	// row-only move can return a session to a binding byte-for-byte equal to the
	// one an earlier arm captured; binding equality alone then cannot tell a
	// superseded attempt from the live lifecycle another arm created for the same
	// binding. The token does: only the arm that created the current lifecycle can
	// stamp its terminal outcome.
	virtualProbeLifecycle uint64
	// VirtualSubtitleEvidenceURI names the provider-neutral candidate the
	// evidence above was captured for. It is the provenance anchor: evidence
	// may only be applied to a file bound to the same candidate, and a
	// rotation must move or clear it together with the evidence.
	VirtualSubtitleEvidenceURI string
	// VirtualSubtitleEvidenceFileID is the media_files row id the evidence was
	// captured from. Duplicate catalog rows for one release share candidate
	// URIs and neutral keys, so the URI alone cannot tell the rows apart; a
	// session that planned against row A must not apply A's inventory to the
	// sibling row B that happens to name the same candidate. It is deliberately
	// left at the old row when the binding moves (see SetVirtualSource), which
	// is what makes the cross-row mismatch detectable. Zero means unknown
	// (a legacy or reconstructed session) and falls back to the URI anchor.
	VirtualSubtitleEvidenceFileID int
	// VirtualSubtitleEvidenceOwnerInstallationID is the provider installation
	// the evidence URI was resolved through. It is captured with the evidence
	// and, like the URI and row id, deliberately left untouched when the
	// binding moves (see SetVirtualSource). A seek reanchor that replays a
	// frozen candidate must resolve through the candidate's own installation,
	// never the moved binding's (VirtualSourceOwnerInstallationID): the two
	// diverge after a cross-installation move, and using the live owner would
	// resolve the pinned bytes through another installation's credentials.
	// Zero means unknown, and the seek then fails closed rather than borrowing.
	VirtualSubtitleEvidenceOwnerInstallationID int
	VirtualSubtitleEvidenceSet                 bool
	// VirtualFontBundleKey freezes the font-bundle cache identity captured
	// with the plan's subtitle evidence. A background probe persists provider
	// identity and inventory onto the mutable catalog row between plan and
	// request, so re-deriving the key from that row would store the warmed
	// bundle under an identity no HTTP request asks for. The warm and both
	// serve paths reuse this exact key while it still names the same file row;
	// a session without one (legacy or reconstructed) falls back to live
	// derivation.
	VirtualFontBundleKey    FontBundleKey
	VirtualFontBundleKeySet bool
	// VirtualSubtitleSelectionIndex is the viewer's committed combined subtitle
	// ordinal on the current evidence generation (-1 = subtitles off).
	// SubtitleTrackIndex is a different space (the transport stream operand), so
	// a candidate rotation must not re-key from it. Set is false when no
	// selection was ever committed.
	VirtualSubtitleSelectionIndex int
	VirtualSubtitleSelectionSet   bool
	// VirtualSubtitleCarryFromIndex / ToIndex describe the viewer's selection
	// carried across the most recent virtual candidate rotation: a subtitle URL
	// minted before the rotation still names FromIndex, and the serve path
	// rewrites it to ToIndex. A second rotation re-keys from ToIndex, which is
	// why the commit records the new position rather than the original one.
	VirtualSubtitleCarryFromIndex int
	VirtualSubtitleCarryToIndex   int
	VirtualSubtitleCarrySet       bool
	// VirtualSubtitleCarryLostIndex records the carried ordinal a rotation could
	// not preserve. A subtitle fetch still naming it answers the retryable
	// source-changed answer instead of resolving the dead pin or spawning ffmpeg.
	VirtualSubtitleCarryLostIndex int
	VirtualSubtitleCarryLostSet   bool
	// SelectionOrigin mirrors the attempt's audio selection intent onto the
	// live session ("auto" when the server resolved an omitted selection,
	// "explicit" when the viewer named a track). In-memory recovery and
	// reconnects keep the intent alongside the track index, so a resumed
	// session cannot silently downgrade to an ordinal-only comparison.
	// Empty for sessions that started before intent capture.
	SelectionOrigin string
	// PreferredAudioLanguage and SeriesAudioPreferenceSignature replay the
	// start-time inputs of the auto selection (SelectAudioTrack's
	// preferredLang and series-snapshot arguments); SelectedAudioSignature
	// names the committed track they produced. Reconciliation replays these
	// against the verified inventory, never against a shifted ordinal.
	PreferredAudioLanguage         string
	SeriesAudioPreferenceSignature *userstore.AudioTrackSignature
	SelectedAudioSignature         *userstore.AudioTrackSignature
	// RemuxResumeLeadingPictureDrop freezes the planner's best-effort request
	// to drop open-GOP leading pictures from a seeked progressive remux.
	RemuxResumeLeadingPictureDrop bool
	// RequireMediaAuthorization distinguishes v3 transports whose session ID is
	// only a route (media requests must present an authenticated user) from
	// legacy sessions whose ID doubles as a bearer credential. Secure transports
	// carry no reconstruction token; a session rebuilt from the API's
	// server-side recovery card gets the flag back from that card
	// (RecipeCard.RequireMediaAuthorization). It is sticky for the life of the
	// session; MediaAuthorizationSet records whether the creating request
	// negotiated the field at all, so older persisted states keep their bearer
	// behavior across upgrades.
	RequireMediaAuthorization bool
	MediaAuthorizationSet     bool

	TranscodeNodeURL     string // URL of assigned transcode node (empty = local/integrated)
	TranscodeTransportID string // remote node process identity; empty means session ID
	AudioTrackIndex      int

	// RoutingNetworkProvider is the validated access path selected when preparing
	// playback: nil means unknown, an empty value means the default network.
	// RoutingWorkload and the execution/egress fields describe the committed
	// node-routing assignment independently from the transcode process route.
	// Node URLs are internal identities used by the session sync layer to join
	// stable stream-node IDs; they are never returned as client media origins.
	RoutingNetworkProvider  *string
	RoutingWorkload         string
	RoutingExecution        string
	RoutingExecutionNodeID  int
	RoutingExecutionNodeURL string
	RoutingEgress           string
	RoutingEgressNodeID     int
	RoutingEgressNodeURL    string

	StreamBitrateKbps int    // currently delivered bitrate, when known
	TargetResolution  string // requested output resolution for transcodes
	TargetVideoCodec  string // requested output video codec for transcodes
	OutputContainer   string // selected muxer/segment container; empty when unreported
	OutputProtocol    string // hls or http, independent of the container
	TargetAudioCodec  string // requested output audio codec when audio is transcoded
	// SourceContainer is the effective source file's container. It stays beside
	// the copied-audio recipe because a copied AAC track needs ADTS framing
	// converted only when its container frames it that way (see
	// CopiedAudioADTSCodec); a codec label alone is not enough evidence.
	SourceContainer        string
	SourceAudioChannels    int          // selected source track channels; zero means unknown/legacy
	TargetAudioChannels    int          // requested encoded audio channel count
	TargetAudioBitrateKbps int          // requested encoded audio bitrate cap
	TargetBitrateKbps      int          // requested output bitrate cap for transcodes
	TranscodeHWAccel       string       // effective hardware acceleration mode for transcodes
	ToneMapMode            tonemap.Mode // effective HDR-to-SDR executor for transcodes

	// Byte-affecting transcode recipe fields the offloaded restart path needs to
	// rebuild the exact same stream after an audio switch. Local transcodes read
	// these from the live ts.Opts(); offloaded transcodes own no local runtime, so
	// the session is the only place to recover them (see the track_change replan
	// operation in internal/api/handlers/playback_v3.go).
	SubtitleTrackIndex int // -1 = no subtitles
	SubtitleBurnIn     bool
	SegmentDuration    int // HLS segment length in seconds (cadence)

	Position float64
	IsPaused bool
	// Watch records measured playback and pause time from progress samples.
	// TimelineDuration is the length in seconds of the timeline Position is
	// measured on when that is more than the session's file, such as a whole
	// Audiobookshelf book of several files. Zero means the file's own
	// duration.
	TimelineDuration float64
	// ReportedListening is the highest running listening total the client
	// has reported for the play (see UpdateProgressReported).
	ReportedListening float64
	// AdaptationReasons says why the route changes the file; empty for direct
	// play or an unplanned session.
	AdaptationReasons []string
	// Subtitle is the subtitle the current plan delivers, for the admin views;
	// nil when it shows none or the session has no plan.
	Subtitle *SessionSubtitle
	Watch    WatchTime
	// ClientCanceled records the last transport cancel observed for this
	// session, scoped to the route revision and binding generation it happened
	// on. It is liveness evidence for one request, not a session verdict: the
	// recovery path never uses it to fence a live client, only to recognize a
	// cancel that a committed successor has already superseded. A committed
	// replacement, a rollback, or a progress report clears it.
	ClientCanceled ClientCancelEvidence
	// StopReported marks a session a client reported stopped without an ID
	// that could end it (#1454). It only hides the session from the live
	// admin view: pause state and idle grace are untouched, so a stale stop
	// can't shorten the lifetime of a play that is really paused. The next
	// progress report clears it.
	StopReported               bool
	HasWebSocket               bool
	HasRealtimeConnection      bool
	DisableProgressPersistence bool
	StartedAt                  time.Time
	UpdatedAt                  time.Time
	LastActivityAt             time.Time
	PlanCommittedAt            time.Time
	activeTransportCount       int
	replacementPlayMethod      PlayMethod
	streamRevision             uint64
	// autoFallback and autoFallbackSet record whether this session may fail
	// over to another media version when its source dies or is unplayable. It
	// is set from the viewer's auto selection at start (an explicit pick turns
	// it off) and may be flipped back on by a later auto re-selection. It never
	// authorizes a healthy mid-play version switch: only a dead/unplayable
	// source advances it. autoFallbackSet distinguishes an explicit "off" from
	// a session that never negotiated the field.
	autoFallback    bool
	autoFallbackSet bool
	// virtualSourceGeneration counts candidate-binding moves for this session.
	// It is the fence a cache-handoff re-resolve captures before it lists
	// afresh and passes back to SetVirtualSourceIfGeneration, so a handoff that
	// started before a newer binding move is refused instead of clobbering it.
	virtualSourceGeneration uint64
	// virtualProbeNotifiedGeneration is the binding generation whose terminal
	// probe outcome was last pushed to the client, distinct from
	// VirtualProbeOutcome (the verdict itself). It lets the notification
	// dispatcher tell an outstanding push (terminal outcome generation newer
	// than this) from an already-delivered one, so the overflow rescan
	// converges instead of republishing every terminal session forever. It
	// advances forward-only: an older generation's ack never overwrites a newer
	// binding's notified state, and an ack for a generation newer than the
	// current binding is refused as stale.
	virtualProbeNotifiedGeneration uint64
	// confirmedPauseSequence fences progress already accepted when a realtime
	// pause state was confirmed; replaying it must not undo that confirmation.
	confirmedPauseSequence int64
	// positionSequence is the latest accepted progress sequence the live
	// position reflects or supersedes (a sample, or a seek planned after it).
	positionSequence int64
	// remoteTransport marks a session whose media bytes are served by another
	// node, so this server never sees the transport request that would
	// otherwise keep it alive. See SetRemoteTransport.
	remoteTransport bool
}

// SessionStreamState stores the mutable stream-specific details that can
// change after a session is created (audio track, client IP, transcode target,
// and reported bitrate).
type SessionStreamState struct {
	PlayMethod      PlayMethod
	BasePlayMethod  PlayMethod
	AudioTrackIndex int
	// VirtualAudioSelectionInvalid carries the server-enforced invalid-audio
	// marker across a stream-state snapshot so a replacement rollback can
	// restore it. Forward application (applySessionStreamStateLocked) still
	// clears the marker unconditionally: a newly applied stream state carries
	// a fresh selection that supersedes it. Only the rollback restore path
	// copies this field back.
	VirtualAudioSelectionInvalid bool
	TranscodeAudio               bool
	RemuxDVMode                  RemuxDVMode
	DVProfile                    int
	// DVProfilePin carries the session's first-verified-wins Dolby Vision pin
	// across a stream-state snapshot. A full route-set commit merges it rather
	// than replacing it, so a later replan that reads a drifted catalog value
	// cannot overwrite the pinned profile.
	DVProfilePin      DVPinV3
	ClientIP          string
	ClientName        string
	ClientVersion     string
	ClientUserAgent   string
	StreamBitrateKbps int
	TargetResolution  string
	TargetVideoCodec  string
	OutputContainer   string
	OutputProtocol    string
	TargetAudioCodec  string
	// SourceContainer is the effective source's container, kept with the audio
	// recipe because a copied AAC track needs ADTS framing converted only when
	// the container frames it that way (see CopiedAudioADTSCodec).
	SourceContainer                  string
	SourceAudioChannels              int
	TargetAudioChannels              int
	TargetAudioBitrateKbps           int
	TargetBitrateKbps                int
	TranscodeHWAccel                 string
	ToneMapMode                      tonemap.Mode
	TranscodeNodeURL                 string
	TranscodeTransportID             string
	TranscodeRouteSet                bool
	RoutingNetworkProvider           *string
	RoutingWorkload                  string
	RoutingExecution                 string
	RoutingExecutionNodeID           int
	RoutingExecutionNodeURL          string
	RoutingEgress                    string
	RoutingEgressNodeID              int
	RoutingEgressNodeURL             string
	RequireMediaAuthorization        bool
	MediaAuthorizationSet            bool
	VirtualSourceURI                 string
	VirtualSourceOwnerInstallationID int
	VirtualSourceSet                 bool
	// VirtualSourceOwnershipSet marks a complete v3 stream snapshot that owns
	// the virtual-source binding and its carried evidence outright. When set,
	// the state application replaces the binding even for a non-virtual
	// effective file (clearing evidence), instead of the legacy partial-update
	// behavior that only ever overwrites.
	VirtualSourceOwnershipSet bool
	// VirtualSourceRevision is the opaque media-generation revision the planner
	// published for this session's virtual candidate (PlanV3.VirtualSourceRevision).
	// It changes when a candidate is resolved even if the catalog row id and the
	// provider-neutral effective URI stay fixed, so it records a same-row
	// rotation that an inventory fingerprint alone cannot prove. A candidate
	// move clears it (see SetVirtualSource) until the planner re-probes.
	VirtualSourceRevision    string
	VirtualSubtitleTracks    []models.SubtitleTrack
	VirtualExternalSubtitles []models.ExternalSubtitle
	VirtualAudioTracks       []models.AudioTrack
	// VirtualSubtitleEvidenceURI names the provider-neutral candidate the
	// carried evidence belongs to. It is the provenance anchor for the serve
	// path: evidence may only be applied to a file bound to the same candidate.
	VirtualSubtitleEvidenceURI string
	// VirtualSubtitleEvidenceFileID is the row id the evidence was captured
	// from. Duplicate rows for one release share candidate URIs, so the URI
	// alone cannot tell them apart; a session planned against row A must not
	// apply A's inventory to sibling row B. Zero means unknown (legacy or
	// reconstructed) and falls back to the URI anchor.
	VirtualSubtitleEvidenceFileID int
	// VirtualSubtitleEvidenceOwnerInstallationID is the provider installation
	// the evidence was resolved through, captured alongside the evidence URI
	// and row id. It survives a binding move so a seek reanchor can restore the
	// frozen candidate's own owner instead of the moved binding's. Zero means
	// unknown.
	VirtualSubtitleEvidenceOwnerInstallationID int
	VirtualSubtitleEvidenceSet                 bool
	// VirtualFontBundleKey/Set travel with the evidence generation so a plan
	// commit freezes the font-bundle identity the warm and serve paths share
	// (see Session). A subsequent rotation re-freezes it with the evidence.
	VirtualFontBundleKey    FontBundleKey
	VirtualFontBundleKeySet bool
	// VirtualSubtitleSelectionIndex/Set and the VirtualSubtitleCarry* fields
	// commit the viewer's subtitle position and the most recent rotation carry
	// with the evidence generation (see Session).
	VirtualSubtitleSelectionIndex int
	VirtualSubtitleSelectionSet   bool
	VirtualSubtitleCarryFromIndex int
	VirtualSubtitleCarryToIndex   int
	VirtualSubtitleCarrySet       bool
	VirtualSubtitleCarryLostIndex int
	VirtualSubtitleCarryLostSet   bool
	// SelectionOrigin, PreferredAudioLanguage,
	// SeriesAudioPreferenceSignature and SelectedAudioSignature mirror the
	// attempt's audio selection intent (see Session) onto the stream state so
	// ApplyReplacement survives a session-manager-level rebuild.
	// SelectionOriginSet distinguishes an explicit origin ("", unset) from a
	// state that never carried intent: an unset origin keeps the session's
	// intent, so a legacy builder does not accidentally blank a newer intent.
	SelectionOrigin                string
	SelectionOriginSet             bool
	PreferredAudioLanguage         string
	SeriesAudioPreferenceSignature *userstore.AudioTrackSignature
	SelectedAudioSignature         *userstore.AudioTrackSignature
	// RemuxResumeLeadingPictureDrop freezes the planner's best-effort request
	// to drop open-GOP leading pictures from a seeked progressive remux.
	RemuxResumeLeadingPictureDrop bool

	// Byte-affecting transcode recipe fields preserved so an offloaded restart
	// (e.g. audio switch) can rebuild the exact same stream. SubtitleTrackIndex
	// defaults to 0 on a zero-value state; callers that manage subtitles must set
	// it explicitly (-1 for none) — burn-in is additionally gated by
	// SubtitleBurnIn so a zero index never burns track 0 by accident.
	SubtitleTrackIndex int
	SubtitleBurnIn     bool
	SegmentDuration    int
}

// TranscodeRoute identifies the process serving a playback session. An empty
// NodeURL means the integrated server owns the process; an empty TransportID
// means a remote process uses the public playback session ID.
type TranscodeRoute struct {
	NodeURL     string
	TransportID string
}

// NodeRoutingAssignment is the committed placement of one playback workload.
// The string vocabulary belongs to the routing package; playback stores it as
// opaque session state to avoid coupling session lifetime management to route
// selection.
type NodeRoutingAssignment struct {
	NetworkProvider  *string
	Workload         string
	Execution        string
	ExecutionNodeID  int
	ExecutionNodeURL string
	Egress           string
	EgressNodeID     int
	EgressNodeURL    string
}

// SessionReplacement is the complete mutable session state associated with a
// protocol-v3 replacement plan. Position is optional because ordinary failure
// recovery must preserve the player's latest progress while seek recovery
// intentionally moves the authoritative timeline.
type SessionReplacement struct {
	EffectiveMediaFileID int
	StreamState          SessionStreamState
	PositionSeconds      *float64
	// PositionSequence is the latest accepted progress sequence when the
	// replacement was planned; PositionSeconds supersedes samples up to it.
	// A sample another replica accepts after that read but before the
	// replacement applies still counts as newer, so a realtime confirmation
	// may show its pre-seek position until the player's next report.
	PositionSequence int64
	IsPaused         bool
	PreservePaused   bool
}

// SessionReplacementRollback is an opaque compare-and-swap token returned by
// ApplyReplacement. It can restore the previous session state only while no
// newer stream or progress mutation has superseded the replacement.
type SessionReplacementRollback struct {
	sessionID                    string
	appliedRevision              uint64
	previousEffectiveMediaFileID int
	previousStreamState          SessionStreamState
	previousPosition             float64
	previousPaused               bool
	previousPositionSequence     int64
	restoreProgress              bool
	previousReplacementMethod    PlayMethod
	previousPlanCommittedAt      time.Time
}

// ErrSessionReplacementSuperseded means a replacement rollback would overwrite
// a newer session mutation. Callers should terminate the session rather than
// expose state that disagrees with the durable playback plan.
var ErrSessionReplacementSuperseded = errors.New("session replacement was superseded")

type clientInfoContextKey struct{}

// ClientInfo carries best-effort client metadata from request handling into
// the playback session manager.
type ClientInfo struct {
	Name     string
	Version  string
	DeviceID string
	// Build is the client's opaque per-platform build identifier (Android
	// versionCode, Apple CFBundleVersion, …). The server never parses or
	// compares it; it exists so an admin can name the exact build.
	Build string
	// Channel is the client's opaque distribution channel ("release", "beta",
	// "sideload", "dev", …). Stored verbatim — deliberately not validated
	// against an enum so a new channel needs no server change.
	Channel        string
	UserAgent      string
	DevicePlatform string
	IsCompat       bool
}

// Normalized returns the identity with every field trimmed and clamped to the
// bound published for it (docs/settings-api.md, and maxLength in the v3 request
// schemas). This is the single definition of those bounds, and callers apply it
// at the request boundary: a session stamps normalized values, but the decision
// logs and playback_route_events are written straight from the resolved
// identity, so clamping only at session creation would let a client's oversized
// header through to both.
func (c ClientInfo) Normalized() ClientInfo {
	c.Name = normalizeClientMetadataValue(c.Name, 128)
	c.Version = normalizeClientMetadataValue(c.Version, 64)
	c.Build = normalizeClientMetadataValue(c.Build, 64)
	c.Channel = normalizeClientMetadataValue(c.Channel, 32)
	c.UserAgent = normalizeClientMetadataValue(c.UserAgent, 512)
	return c
}

// LogAttrs renders the app identity as slog key/value pairs, skipping the
// fields the client did not report. This is the single definition of those log
// keys — every playback decision and the session-expiry line share it, so a
// rename cannot leave one surface keyed differently from another. Skipping
// empty values matters as much: browsers and Jellyfin-ecosystem clients report
// none of them, and opslog persists the attrs it is handed, so emitting four
// empty keys per decision would grow /admin/logs for no diagnostic value.
func (c ClientInfo) LogAttrs() []any {
	attrs := make([]any, 0, 8)
	for _, pair := range [...]struct{ key, value string }{
		{"client_name", c.Name},
		{"client_version", c.Version},
		{"client_build", c.Build},
		{"client_channel", c.Channel},
	} {
		if pair.value != "" {
			attrs = append(attrs, pair.key, pair.value)
		}
	}
	return attrs
}

// ClientInfo returns the app identity stamped on the session when it was
// created. Surfaces that only hold a session — expiry logging, route events
// posted out of band — recover the reporting client through this instead of
// re-reading request headers that may no longer be present.
func (s *Session) ClientInfo() ClientInfo {
	if s == nil {
		return ClientInfo{}
	}
	return ClientInfo{
		Name:      s.ClientName,
		Version:   s.ClientVersion,
		Build:     s.ClientBuild,
		Channel:   s.ClientChannel,
		UserAgent: s.ClientUserAgent,
		IsCompat:  s.IsJellyfinCompat,
	}
}

// WithClientInfo stores playback client metadata on a context.
func WithClientInfo(ctx context.Context, info ClientInfo) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, clientInfoContextKey{}, info)
}

// ClientInfoFromContext returns playback client metadata stored on a context.
func ClientInfoFromContext(ctx context.Context) ClientInfo {
	if ctx == nil {
		return ClientInfo{}
	}
	info, _ := ctx.Value(clientInfoContextKey{}).(ClientInfo)
	return info
}

// SessionManager tracks active playback sessions and enforces stream limits.
type LifecycleHooks struct {
	Started       func(context.Context, *Session, string)
	PauseReported func(context.Context, string, bool)
}

type SessionManager struct {
	sessions             map[string]*Session
	mu                   sync.RWMutex
	maxStreams           int
	maxTranscodes        int
	limitProvider        SessionLimitProvider
	admissionDecider     AdmissionDecider
	activeGrace          time.Duration
	pausedGrace          time.Duration
	expireHooks          []func(*Session)
	finishHooks          []func(context.Context, *Session)
	lifecycleHooks       []LifecycleHooks
	compatActivityReader SessionActivityReader
	compatExpiryClaimer  SessionExpiryClaimer
	// deliveryActivityReader reports media that another node served for a
	// remote-transport session. See SetDeliveryActivityReader.
	deliveryActivityReader SessionActivityReader
	// transportStops holds the stop channels of media transports this replica
	// is currently serving, keyed by session ID. See WatchTransportStop.
	transportStops map[string]map[chan struct{}]struct{}
}

// SessionLimits stores per-user admission limits. Zero values mean unlimited.
type SessionLimits struct {
	MaxStreams               int
	MaxTranscodes            int
	TranscodingDisabled      bool
	AudioTranscodingDisabled bool
}

// SessionLimitProvider returns the current admission limits for a user.
type SessionLimitProvider func(ctx context.Context, userID int) (SessionLimits, error)

// AdmissionRequest is the fact set passed to an optional policy admission
// decider. Counts are computed by SessionManager from live in-memory sessions.
type AdmissionRequest struct {
	UserID                  int
	Limits                  SessionLimits
	CurrentActiveStreams    int
	CurrentActiveTranscodes int
	RequestedMethod         PlayMethod
	RequiresVideoTranscode  bool
	RequiresAudioTranscode  bool
}

// AdmissionDecision is the result of an optional policy admission decision.
// Reason is free text for logs; ReasonCode is the typed contract mapped to
// sentinel errors (values mirror the vendor policy reason_code output).
type AdmissionDecision struct {
	Allowed    bool
	Reason     string
	ReasonCode string
}

// Admission reason codes recognized by admissionDenyError. They mirror the
// policy package's ReasonCode* constants; playback cannot import policy
// (policy's adapters import playback), so the shared values are pinned by
// tests on both sides.
const (
	AdmissionReasonMaxStreamsExceeded       = "max_streams_exceeded"
	AdmissionReasonMaxTranscodesExceeded    = "max_transcodes_exceeded"
	AdmissionReasonTranscodingDisabled      = "transcoding_disabled"
	AdmissionReasonAudioTranscodingDisabled = "audio_transcoding_disabled"
)

// AdmissionDecider can replace SessionManager's inline limit comparison while
// keeping session counting in Go.
type AdmissionDecider func(ctx context.Context, req AdmissionRequest) (AdmissionDecision, error)

const (
	// DefaultActiveSessionGrace is how long an unpaused session may go without
	// observed playback activity before it stops counting toward limits.
	DefaultActiveSessionGrace = 45 * time.Second

	// StartupSessionGrace keeps a newly committed playback plan alive while the
	// client waits for its startup timeout before requesting a fallback.
	StartupSessionGrace = 90 * time.Second

	// remoteTransportIdleGrace is the floor on the idle windows for a session
	// whose media is served by another node. See remoteTransportGrace: it must
	// outlast a heartbeat gap on an otherwise healthy proxy stream, while still
	// reaping a session whose client vanished without stopping.
	remoteTransportIdleGrace = 5 * time.Minute

	// DefaultPausedSessionGrace is the longer grace period for paused
	// sessions. It must comfortably cover an intentional pause (dinner
	// break, phone call): reaping a paused session kills its transcode
	// and there is currently no revival path, so a too-short grace makes
	// pressing Play after a long pause freeze the client (issue #243).
	// Keep in sync with pausedSessionGrace in internal/worker/cleanup.go.
	DefaultPausedSessionGrace = 30 * time.Minute
)

// NewSessionManager creates a SessionManager with the given concurrency limits.
// maxStreams limits total active streams per user.
// maxTranscodes limits concurrent transcode streams per user.
func NewSessionManager(maxStreams, maxTranscodes int) *SessionManager {
	return &SessionManager{
		sessions:      make(map[string]*Session),
		maxStreams:    maxStreams,
		maxTranscodes: maxTranscodes,
		activeGrace:   DefaultActiveSessionGrace,
		pausedGrace:   DefaultPausedSessionGrace,
	}
}

// SetLimitProvider overrides the manager defaults with dynamic per-user
// limits. The constructor limits remain the fallback when no provider is set.
func (m *SessionManager) SetLimitProvider(provider SessionLimitProvider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.limitProvider = provider
}

// SetAdmissionDecider installs an optional policy admission hook. A nil decider
// keeps the legacy inline comparison.
func (m *SessionManager) SetAdmissionDecider(decider AdmissionDecider) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.admissionDecider = decider
}

// SetLivenessGracePeriods overrides the grace periods used by admission
// control and stale-session cleanup.
func (m *SessionManager) SetLivenessGracePeriods(active, paused time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if active > 0 {
		m.activeGrace = active
	}
	if paused > 0 {
		m.pausedGrace = paused
	}
}

// SetExpirationHook registers a callback that runs after a session is removed
// by stale cleanup. The hook executes outside the manager lock.
func (m *SessionManager) SetExpirationHook(fn func(*Session)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireHooks = nil
	if fn != nil {
		m.expireHooks = append(m.expireHooks, fn)
	}
}

// AddExpirationHook registers an additional callback without replacing the
// cleanup owned by another playback frontend sharing this session manager.
func (m *SessionManager) AddExpirationHook(fn func(*Session)) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireHooks = append(m.expireHooks, fn)
}

func (m *SessionManager) AddLifecycleHooks(h LifecycleHooks) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lifecycleHooks = append(m.lifecycleHooks, h)
}

// AnnounceStart tells lifecycle hooks that a frontend started this session.
// The session is copied under the read lock so hooks observe a race-free snapshot.
func (m *SessionManager) AnnounceStart(ctx context.Context, sessionID, frontend string) {
	m.mu.RLock()
	s, ok := m.sessions[sessionID]
	var started Session
	if ok {
		started = *s
	}
	hooks := append([]LifecycleHooks(nil), m.lifecycleHooks...)
	m.mu.RUnlock()
	if !ok {
		return
	}
	for _, hook := range hooks {
		if hook.Started != nil {
			hook.Started(ctx, &started, frontend)
		}
	}
}

// ReportPause tells lifecycle hooks about a pause or resume reported without a
// progress sequence. It intentionally does not alter session state; callers
// that own authoritative progress use SetPaused or UpdateProgress.
func (m *SessionManager) ReportPause(ctx context.Context, sessionID string, paused bool) {
	m.mu.RLock()
	hooks := append([]LifecycleHooks(nil), m.lifecycleHooks...)
	m.mu.RUnlock()
	for _, hook := range hooks {
		if hook.PauseReported != nil {
			hook.PauseReported(ctx, sessionID, paused)
		}
	}
}

// AddFinishHook registers a callback that runs after FinishSession removes a
// session. The hook executes outside the manager lock.
func (m *SessionManager) AddFinishHook(fn func(context.Context, *Session)) {
	if fn == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finishHooks = append(m.finishHooks, fn)
}

func normalizeClientMetadataValue(value string, maxLen int) string {
	// A text column takes neither invalid UTF-8 nor a NUL, and Postgres refuses
	// the whole statement for either. The per-node session upserts share one
	// transaction, so a single malformed client string would stop that entire
	// node from reconciling — not just its own row — until the session goes
	// away. Both scrubs are needed: NUL is perfectly valid UTF-8, so
	// ToValidUTF8 leaves it, and a v3 start body can carry one as the JSON
	// escape (headers cannot — net/http rejects bytes below 0x20).
	// Control characters are stripped wholesale rather than just NUL: none of
	// them belong in an identity label rendered in the admin UI and written to
	// structured logs.
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "")
	}
	if strings.ContainsFunc(value, unicode.IsControl) {
		value = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, value)
	}
	value = strings.TrimSpace(value)
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	// Clamp by runes, not bytes. These bounds are published to clients as JSON
	// Schema maxLength, which counts characters — a byte clamp would silently
	// cut a value the contract calls valid, and cutting mid-rune would produce
	// exactly the invalid UTF-8 scrubbed above.
	runes := 0
	for offset := range value {
		if runes == maxLen {
			return value[:offset]
		}
		runes++
	}
	return value
}

// StartSession creates a new playback session using the same file as both the
// requested and effective source.
func (m *SessionManager) StartSession(userID int, profileID string, fileID int, method PlayMethod, transcodeAudio bool) (*Session, error) {
	return m.StartSessionWithContext(context.Background(), userID, profileID, fileID, method, transcodeAudio)
}

// StartSessionWithContext creates a new playback session using the same file
// as both the requested and effective source.
func (m *SessionManager) StartSessionWithContext(
	ctx context.Context,
	userID int,
	profileID string,
	fileID int,
	method PlayMethod,
	transcodeAudio bool,
) (*Session, error) {
	return m.StartSessionWithFilesContext(ctx, userID, profileID, fileID, fileID, method, transcodeAudio)
}

// StartSessionWithFiles creates a new playback session after checking
// concurrency limits. requestedFileID is the user's requested version while
// effectiveFileID is the file currently backing playback.
// Returns ErrTooManyStreams if the user has reached the max active stream count.
// Returns ErrTooManyTranscodes if the user has reached the max transcode count
// and the requested method is transcode.
// Returns ErrTranscodingDisabled when the user may not start a video or audio transcode.
func (m *SessionManager) StartSessionWithFiles(
	userID int,
	profileID string,
	effectiveFileID int,
	requestedFileID int,
	method PlayMethod,
	transcodeAudio bool,
) (*Session, error) {
	return m.StartSessionWithFilesContext(context.Background(), userID, profileID, effectiveFileID, requestedFileID, method, transcodeAudio)
}

// StartSessionWithFilesContext creates a new playback session after checking
// concurrency limits with request-scoped limit lookup.
func (m *SessionManager) StartSessionWithFilesContext(
	ctx context.Context,
	userID int,
	profileID string,
	effectiveFileID int,
	requestedFileID int,
	method PlayMethod,
	transcodeAudio bool,
) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := m.limitsForUser(ctx, userID)
	if err != nil {
		return nil, err
	}

	for {
		m.mu.Lock()
		decider := m.admissionDecider
		if decider == nil {
			if err := m.inlineAdmissionErrorLocked(userID, method, transcodeAudio, limits); err != nil {
				m.mu.Unlock()
				return nil, err
			}
			s := newSession(ctx, userID, profileID, effectiveFileID, requestedFileID, method, transcodeAudio)
			m.sessions[s.ID] = s
			m.mu.Unlock()
			return s, nil
		}
		activeStreams := m.activeCountLocked(userID)
		activeTranscodes := m.transcodeCountLocked(userID)
		m.mu.Unlock()

		decision, err := decider(ctx, AdmissionRequest{
			UserID:                  userID,
			Limits:                  limits,
			CurrentActiveStreams:    activeStreams,
			CurrentActiveTranscodes: activeTranscodes,
			RequestedMethod:         method,
			RequiresVideoTranscode:  method == PlayTranscode,
			RequiresAudioTranscode:  transcodeAudio,
		})
		fallbackInline := false
		if err != nil {
			// An eval timeout is not a decision: degrade to the inline
			// stream/transcode caps rather than denying a viewer whose content
			// access was already authorized upstream. Every other decider error
			// stays fail-closed, and makes an engine outage distinguishable
			// from a genuine concurrency-limit denial in the logs.
			if errors.Is(err, ErrAdmissionDeciderTimeout) {
				fallbackInline = true
				slog.WarnContext(ctx, "playback admission policy timed out; falling back to inline limits", "component", "playback",
					"user_id", userID, "method", method, "error", err)
			} else {
				slog.WarnContext(ctx, "playback admission decider error; denying session", "component", "playback",
					"user_id", userID, "method", method, "error", err)
				return nil, admissionDenyError("")
			}
		} else if !decision.Allowed {
			return nil, admissionDenyError(decision.ReasonCode)
		}

		m.mu.Lock()
		if activeStreams != m.activeCountLocked(userID) || activeTranscodes != m.transcodeCountLocked(userID) {
			m.mu.Unlock()
			continue
		}
		if fallbackInline {
			if err := m.inlineAdmissionErrorLocked(userID, method, transcodeAudio, limits); err != nil {
				m.mu.Unlock()
				return nil, err
			}
		}
		s := newSession(ctx, userID, profileID, effectiveFileID, requestedFileID, method, transcodeAudio)
		m.sessions[s.ID] = s
		m.mu.Unlock()
		return s, nil
	}
}

func (m *SessionManager) inlineAdmissionErrorLocked(userID int, method PlayMethod, transcodeAudio bool, limits SessionLimits) error {
	if err := transcodingDisabledError(method == PlayTranscode, transcodeAudio, limits); err != nil {
		return err
	}

	if limits.MaxStreams > 0 && m.activeCountLocked(userID) >= limits.MaxStreams {
		return ErrTooManyStreams
	}

	if method == PlayTranscode && limits.MaxTranscodes > 0 && m.transcodeCountLocked(userID) >= limits.MaxTranscodes {
		return ErrTooManyTranscodes
	}
	return nil
}

func newSession(
	ctx context.Context,
	userID int,
	profileID string,
	effectiveFileID int,
	requestedFileID int,
	method PlayMethod,
	transcodeAudio bool,
) *Session {
	now := time.Now()
	// Normalize here as well as at the request boundary: identities also reach
	// the manager from the Jellyfin and Audiobookshelf compat surfaces, which
	// build a ClientInfo from their own header vocabularies.
	clientInfo := ClientInfoFromContext(ctx).Normalized()
	return &Session{
		ID:                     uuid.New().String(),
		UserID:                 userID,
		ProfileID:              profileID,
		MediaFileID:            effectiveFileID,
		RequestedMediaFileID:   requestedFileID,
		PlayMethod:             method,
		BasePlayMethod:         method,
		TranscodeAudio:         transcodeAudio,
		ClientIP:               clientip.FromContext(ctx),
		StreamLocation:         string(streamlocation.FromContext(ctx)),
		RoutingNetworkProvider: new(netaccess.PathFromContext(ctx).Provider),
		Position:               0,
		IsPaused:               false,
		ClientName:             clientInfo.Name,
		ClientVersion:          clientInfo.Version,
		DeviceID:               clientInfo.DeviceID,
		ClientBuild:            clientInfo.Build,
		ClientChannel:          clientInfo.Channel,
		ClientUserAgent:        clientInfo.UserAgent,
		IsJellyfinCompat:       clientInfo.IsCompat,
		StartedAt:              now,
		UpdatedAt:              now,
		LastActivityAt:         now,
	}
}

// admissionDenyError maps a typed reason code to a sentinel error. Anything
// unrecognized — custom-override denials, engine failures — is a generic
// policy denial, not a concurrency-limit error.
func admissionDenyError(reasonCode string) error {
	switch reasonCode {
	case AdmissionReasonMaxStreamsExceeded:
		return ErrTooManyStreams
	case AdmissionReasonMaxTranscodesExceeded:
		return ErrTooManyTranscodes
	case AdmissionReasonTranscodingDisabled:
		return ErrTranscodingDisabled
	case AdmissionReasonAudioTranscodingDisabled:
		return ErrAudioTranscodingDisabled
	default:
		return ErrPlaybackNotAllowed
	}
}

// RegisterReconstructed re-inserts a session under an existing ID after the
// in-memory state was lost (e.g. a server restart). Unlike StartSession* it
// does NOT mint a new UUID and does NOT run admission/limit accounting: the
// session already existed and was admitted before the restart, so counting it
// again would be wrong. If a live session with the same ID already exists
// (a concurrent reconstruct won the race), the existing one is returned and
// the caller's copy is discarded.
//
// The caller is responsible for having re-bound s.UserID to the live
// authenticated request before calling this — RegisterReconstructed performs
// no authorization itself.
func (m *SessionManager) RegisterReconstructed(s *Session) *Session {
	if s == nil || s.ID == "" {
		return s
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.sessions[s.ID]; ok {
		return existing
	}
	now := time.Now()
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	s.UpdatedAt = now
	s.LastActivityAt = now
	s.PlanCommittedAt = now
	m.sessions[s.ID] = s
	return s
}

// RegisterReconstructedWithLimits is RegisterReconstructed plus the same per-user
// admission caps StartSession enforces. Token-carried reconstruct replays a
// signed recipe to rebuild a session lost to a restart; without a cap check a
// client could replay one token repeatedly (or after legitimately reaching its
// limit) and reconstruct past the per-user concurrent stream/transcode caps,
// since RegisterReconstructed skips admission accounting.
//
// Legitimately reconstructing a user's own surviving sessions still succeeds:
// the cap counts the user's *currently-live* sessions, and the one being rebuilt
// is not yet in the map, so the first MaxStreams reconstructs admit. Only the
// over-cap replay or disabled transcode is refused. If an
// identical session id is already live (a concurrent reconstruct won), it is
// returned without re-counting. Caps are looked up via the same limit provider
// as StartSession.
func (m *SessionManager) RegisterReconstructedWithLimits(ctx context.Context, s *Session) (*Session, error) {
	if s == nil || s.ID == "" {
		return s, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := m.limitsForUser(ctx, s.UserID)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.sessions[s.ID]; ok {
		return existing, nil
	}

	// The session being reconstructed is not yet in the map, so the live counts
	// reflect the user's *other* sessions; admitting one more must stay within cap.
	if err := transcodingDisabledError(s.PlayMethod == PlayTranscode, s.TranscodeAudio, limits); err != nil {
		return nil, err
	}
	if limits.MaxStreams > 0 && m.activeCountLocked(s.UserID) >= limits.MaxStreams {
		return nil, ErrTooManyStreams
	}
	if s.PlayMethod == PlayTranscode && limits.MaxTranscodes > 0 &&
		m.transcodeCountLocked(s.UserID) >= limits.MaxTranscodes {
		return nil, ErrTooManyTranscodes
	}

	now := time.Now()
	if s.StartedAt.IsZero() {
		s.StartedAt = now
	}
	s.UpdatedAt = now
	s.LastActivityAt = now
	s.PlanCommittedAt = now
	m.sessions[s.ID] = s
	return s, nil
}

// RollbackReconstructedToneMap removes a failed tone-map reconstruction only
// while expected is still the exact session registered by that attempt. A
// concurrently started or reconstructed successor under the same ID is left
// untouched.
func (m *SessionManager) RollbackReconstructedToneMap(expected *Session) bool {
	if expected == nil || expected.ID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessions[expected.ID] != expected {
		return false
	}
	delete(m.sessions, expected.ID)
	return true
}

// CaptureReconstructedExecution records the session incarnation and stream
// revision before a runtime rebuild. The pointer is only an ownership token.
func (m *SessionManager) CaptureReconstructedExecution(sessionID string) (*Session, uint64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := m.sessions[sessionID]
	if current == nil {
		return nil, 0
	}
	return current, current.streamRevision
}

// ConfirmReconstructedExecution publishes the executors selected by a successful
// runtime reconstruction only while expected still owns the session ID. It
// returns the current session so callers yield to a concurrent legitimate
// successor instead of overwriting it with stale execution facts.
func (m *SessionManager) ConfirmReconstructedExecution(expected *Session, revision uint64, mode tonemap.Mode, encoderHWAccel string) *Session {
	if expected == nil || expected.ID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.sessions[expected.ID]
	if current == expected && current.streamRevision == revision {
		if current.ToneMapMode != mode || current.TranscodeHWAccel != encoderHWAccel {
			current.ToneMapMode = mode
			current.TranscodeHWAccel = encoderHWAccel
			current.streamRevision++
		}
		m.touchSessionLocked(current)
	}
	return current
}

// sessionLimitLookupTimeout bounds the per-user limit provider read once it is
// decoupled from the caller's context. The provider is a small database/policy
// read; a caller whose own deadline is nearly spent (the replan request budget
// after a slow virtual rehydration) must not cancel it.
const sessionLimitLookupTimeout = 3 * time.Second

// sessionAdmissionTimeout bounds a replacement admission (the limit lookup plus
// the optional policy decision) once it is decoupled from the request budget.
const sessionAdmissionTimeout = 5 * time.Second

// inlineLimits returns the manager-wide concurrency caps: the fallback when no
// per-user provider is configured, and the fail-open fallback when a provider
// lookup fails transiently.
func (m *SessionManager) inlineLimits() SessionLimits {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return SessionLimits{MaxStreams: m.maxStreams, MaxTranscodes: m.maxTranscodes}
}

func (m *SessionManager) limitsForUser(ctx context.Context, userID int) (SessionLimits, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	provider := m.limitProvider
	limits := SessionLimits{
		MaxStreams:    m.maxStreams,
		MaxTranscodes: m.maxTranscodes,
	}
	m.mu.RUnlock()

	if provider == nil {
		return limits, nil
	}
	// The lookup is a small, independent database/policy read. A parent
	// context that is already canceled or nearly spent (the replan request
	// budget) would abort it even though the read itself is still valid and
	// fast, so run it under its own short budget decoupled from the parent's
	// cancellation and deadline.
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionLimitLookupTimeout)
	defer cancel()
	limits, err := provider(lookupCtx, userID)
	if err != nil {
		// Tag provider failures with ErrLimitProviderUnavailable so callers can
		// distinguish a transient limit-lookup failure (which replacement
		// admission may fail open on) from a genuine over-cap rejection.
		return SessionLimits{}, fmt.Errorf("load session limits for user %d: %w",
			userID, errors.Join(ErrLimitProviderUnavailable, err))
	}
	return limits, nil
}

// LimitsForUser returns the account-level playback limits admission enforces
// for userID, so planning can avoid offering routes admission would refuse.
func (m *SessionManager) LimitsForUser(ctx context.Context, userID int) (SessionLimits, error) {
	return m.limitsForUser(ctx, userID)
}

// CheckTranscodingAllowed verifies account-level restrictions before an
// existing session switches to video or audio transcoding.
func (m *SessionManager) CheckTranscodingAllowed(ctx context.Context, userID int, requiresVideoTranscode bool) error {
	limits, err := m.limitsForUser(ctx, userID)
	if err != nil {
		return err
	}
	return transcodingDisabledError(requiresVideoTranscode, !requiresVideoTranscode, limits)
}

// CheckReplacementAllowed applies current user limits and admission policy to
// an in-place protocol-v3 recipe replacement. The existing session is excluded
// from the counts because the replacement inherits its stream slot; a direct
// to transcode change still has to acquire an available transcode slot.
func (m *SessionManager) CheckReplacementAllowed(ctx context.Context, sessionID string, method PlayMethod, transcodeAudio bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// A replacement admission is a bounded, in-memory plus policy check for a
	// request that is still live. A nearly-spent replan budget (virtual
	// rehydration can consume most of it before the candidate loop runs) must
	// not cancel the limit read or the policy decision, so run the admission
	// under its own budget decoupled from the parent's cancellation/deadline.
	admissionCtx, cancelAdmission := context.WithTimeout(context.WithoutCancel(ctx), sessionAdmissionTimeout)
	defer cancelAdmission()
	ctx = admissionCtx
	// Bounded CAS: persistent count churn means the user is actively starting
	// and stopping sessions; failing closed after a few rounds beats spinning
	// with a limit-provider DB call per iteration.
	const maxAdmissionRetries = 8
	for attempt := 0; attempt < maxAdmissionRetries; attempt++ {
		m.mu.Lock()
		current, ok := m.sessions[sessionID]
		if !ok {
			m.mu.Unlock()
			return ErrSessionNotFound
		}
		userID := current.UserID
		currentMethod := current.PlayMethod
		// Exclude the replaced session from both counts instead of decrementing
		// the totals: a failed session idle past the liveness grace is already
		// absent from the count, and a blind decrement would free a slot that
		// belongs to another live session.
		otherStreams := m.activeCountExcludingLocked(userID, sessionID)
		otherTranscodes := m.transcodeCountExcludingLocked(userID, sessionID)
		decider := m.admissionDecider
		m.mu.Unlock()

		limits, err := m.limitsForUser(ctx, userID)
		if err != nil {
			if !errors.Is(err, ErrLimitProviderUnavailable) {
				return err
			}
			// The replacement is for the same session and user whose limits
			// were already loaded at start, so denying on a transient provider
			// read blip is both redundant and wrong: it blocks playback for a
			// limit decision the start admission already made. Fall back to the
			// manager-wide inline caps and admit (the inline transcode cap is
			// still enforced below), exactly as the decider-timeout path does.
			slog.WarnContext(ctx, "playback replacement limit lookup unavailable; admitting with inline limits", "component", "playback",
				"user_id", userID, "session", sessionID, "method", method, "error", err)
			limits = m.inlineLimits()
		}
		if err := transcodingDisabledError(method == PlayTranscode, transcodeAudio, limits); err != nil {
			return err
		}
		if decider != nil {
			decision, err := decider(ctx, AdmissionRequest{UserID: userID, Limits: limits, CurrentActiveStreams: otherStreams, CurrentActiveTranscodes: otherTranscodes, RequestedMethod: method, RequiresVideoTranscode: method == PlayTranscode, RequiresAudioTranscode: transcodeAudio})
			if err != nil {
				// An eval timeout is not a decision: fall through to the inline
				// replacement branch below, exactly as when no decider is
				// installed. Every other decider error stays fail-closed, and
				// makes an engine outage distinguishable from a genuine
				// concurrency-limit denial in the logs.
				if errors.Is(err, ErrAdmissionDeciderTimeout) {
					slog.WarnContext(ctx, "playback replacement admission policy timed out; falling back to inline limits", "component", "playback",
						"user_id", userID, "session", sessionID, "method", method, "error", err)
					decider = nil
				} else {
					slog.WarnContext(ctx, "playback replacement admission decider error; denying replacement", "component", "playback",
						"user_id", userID, "session", sessionID, "method", method, "error", err)
					return ErrPlaybackNotAllowed
				}
			} else if !decision.Allowed {
				return admissionDenyError(decision.ReasonCode)
			}
		}
		if decider == nil {
			m.mu.Lock()
			stillCurrent, stillExists := m.sessions[sessionID]
			countsStable := stillExists && stillCurrent.PlayMethod == currentMethod && otherStreams == m.activeCountExcludingLocked(userID, sessionID) && otherTranscodes == m.transcodeCountExcludingLocked(userID, sessionID)
			if !countsStable {
				m.mu.Unlock()
				continue
			}
			if limits.MaxTranscodes > 0 && method == PlayTranscode && otherTranscodes >= limits.MaxTranscodes {
				m.mu.Unlock()
				return ErrTooManyTranscodes
			}
			stillCurrent.replacementPlayMethod = method
			m.mu.Unlock()
			return nil
		}
		m.mu.Lock()
		stillCurrent, stillExists := m.sessions[sessionID]
		countsStable := stillExists && stillCurrent.PlayMethod == currentMethod && otherStreams == m.activeCountExcludingLocked(userID, sessionID) && otherTranscodes == m.transcodeCountExcludingLocked(userID, sessionID)
		if countsStable {
			stillCurrent.replacementPlayMethod = method
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()
	}
	return ErrPlaybackNotAllowed
}

// CancelReplacementReservation releases a protocol-v3 capacity reservation
// after a replacement fails before UpdateStreamState commits its new method.
func (m *SessionManager) CancelReplacementReservation(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if session := m.sessions[sessionID]; session != nil {
		session.replacementPlayMethod = ""
	}
}

func transcodingDisabledError(requiresVideoTranscode, requiresAudioTranscode bool, limits SessionLimits) error {
	if requiresVideoTranscode && limits.TranscodingDisabled {
		return ErrTranscodingDisabled
	}
	if requiresAudioTranscode && limits.TranscodingDisabled && limits.AudioTranscodingDisabled {
		return ErrAudioTranscodingDisabled
	}
	return nil
}

// UpdateProgress updates the playback position and pause state for a session.
func (m *SessionManager) UpdateProgress(sessionID string, position float64, isPaused bool) error {
	return m.updateProgress(sessionID, position, isPaused, -1)
}

// UpdateProgressSample keeps a confirmed pause state when an already accepted
// sequenced sample is replayed. Position and activity still persist normally.
func (m *SessionManager) UpdateProgressSample(sessionID string, sample ProgressSampleV3) error {
	return m.updateProgress(sessionID, sample.Position, sample.IsPaused, sample.Sequence)
}

func (m *SessionManager) updateProgress(sessionID string, position float64, isPaused bool, sequence int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.Position = position
	if sequence >= 0 && sequence <= s.positionSequence {
		m.touchSessionLocked(s)
		return nil
	}
	s.Watch.Observe(position, isPaused, time.Now())
	s.positionSequence = max(s.positionSequence, sequence)
	if sequence < 0 || sequence > s.confirmedPauseSequence {
		s.IsPaused = isPaused
	}
	s.StopReported = false
	// A fresh progress report is positive liveness evidence: the client is
	// still talking to us, so any earlier transport cancel no longer describes
	// it.
	s.ClientCanceled = ClientCancelEvidence{}
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

// SetPaused records the pause state the player confirmed for a remote pause
// or resume. A non-nil latest is the latest accepted progress, which another
// replica may have accepted without this live session seeing it; its position
// replaces the live one unless the live position already reflects it or a
// later seek. It reports whether the pause state changed.
func (m *SessionManager) SetPaused(sessionID string, paused bool, acceptedSequence int64, latest *ProgressSampleV3) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	s.confirmedPauseSequence = max(s.confirmedPauseSequence, acceptedSequence)
	wasPaused := s.IsPaused
	if paused && !wasPaused {
		s.Watch.Pause(time.Now())
	}
	moved := false
	if latest != nil && latest.Sequence > s.positionSequence {
		s.positionSequence = latest.Sequence
		moved = s.Position != latest.Position
		s.Position = latest.Position
		s.Watch.Observe(latest.Position, wasPaused, time.Now())
	}
	if s.IsPaused == paused {
		if moved {
			s.streamRevision++
		}
		return false, nil
	}
	s.IsPaused = paused
	s.streamRevision++
	m.touchSessionLocked(s)
	return true, nil
}

// MarkStopReported records that a client reported this session stopped
// without an ID that could end it. It does not count as activity and leaves
// pause state alone; see Session.StopReported.
func (m *SessionManager) MarkStopReported(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.StopReported = true
	return nil
}

// MarkClientCanceled records that a transport serve observed the client cancel
// the request, scoped to the session's current route revision and binding
// generation (see Session.ClientCanceled). The mark is best-effort liveness
// evidence about that route only: a committed successor, a rollback, or a
// progress report clears it, and the recovery path never treats it as proof the
// session died. An unknown session is ErrSessionNotFound, matching the other
// session mutators.
func (m *SessionManager) MarkClientCanceled(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.ClientCanceled = ClientCancelEvidence{
		At:                time.Now(),
		StreamRevision:    s.streamRevision,
		BindingGeneration: s.virtualSourceGeneration,
	}
	return nil
}

// ClearClientCanceled drops a session's client-canceled evidence. A progress
// report, a committed replacement plan, or a rollback calls it so a live client
// that reconnects after a transient cancel carries no stale evidence.
func (m *SessionManager) ClearClientCanceled(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.ClientCanceled = ClientCancelEvidence{}
	return nil
}

// UpdateAudioTrack updates the audio track index and optionally the play
// method for a session. Used when switching audio tracks mid-playback.
func (m *SessionManager) UpdateAudioTrack(sessionID string, audioTrackIndex int, method PlayMethod) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.AudioTrackIndex = audioTrackIndex
	s.VirtualAudioSelectionInvalid = false
	s.BasePlayMethod = method
	if s.PlayMethod != PlayTranscode || method == PlayTranscode {
		s.PlayMethod = method
	}
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

// UpdateStreamState updates the live stream details for a session. This keeps
// the session manager's authoritative copy in sync with user-driven changes
// like audio track switches and quality changes.
func (m *SessionManager) UpdateStreamState(sessionID string, state SessionStreamState) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	applySessionStreamStateLocked(s, state)
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

// SetStreamLocation preserves the negotiated bitrate-policy classification
// when a compatible client starts its media request on another network path.
func (m *SessionManager) SetStreamLocation(sessionID, location string) error {
	if location != string(streamlocation.Local) && location != string(streamlocation.Remote) {
		return fmt.Errorf("invalid stream location %q", location)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.StreamLocation = location
	m.touchSessionLocked(s)
	return nil
}

// mergeDVPinV3 folds an incoming DV pin into the session's current pin.
// First-verified-wins for the same file identity (row id and source); a
// snapshot naming a different file identity is authoritative and replaces the
// pin (even a zero-profile snapshot, so a moved binding or a same-row release
// rotation cannot leave a stale pin describing old bytes). A zero-profile
// snapshot for the same identity is not evidence, so it never clears or lowers
// an established pin, and a snapshot that carries no file id at all is ignored.
func mergeDVPinV3(current, incoming DVPinV3) DVPinV3 {
	sameIdentity := current.FileID == incoming.FileID && current.Source == incoming.Source
	if incoming.FileID > 0 && !sameIdentity {
		return incoming
	}
	if incoming.Profile <= 0 {
		return current
	}
	if current.Profile <= 0 || !sameIdentity {
		return incoming
	}
	return current
}

// applySessionStreamStateLocked applies a complete stream snapshot while the manager lock is held.
func applySessionStreamStateLocked(s *Session, state SessionStreamState) {
	if state.PlayMethod != "" {
		s.PlayMethod = state.PlayMethod
	}
	if state.BasePlayMethod != "" {
		s.BasePlayMethod = state.BasePlayMethod
	}
	s.AudioTrackIndex = state.AudioTrackIndex
	// A newly applied stream state carries a fresh selection; any invalid-selection
	// marker from a previous verified overlay is superseded (the index above
	// replaces it unconditionally, matching the legacy partial-update behavior).
	s.VirtualAudioSelectionInvalid = false
	if state.TranscodeRouteSet {
		s.OutputContainer = state.OutputContainer
		s.OutputProtocol = state.OutputProtocol
		// These fields are one byte-affecting audio recipe. A full route snapshot
		// owns the whole tuple and can deliberately clear it; legacy partial
		// updates must not leave a frozen surround source paired with zero-value
		// codec, channel, or transcode facts.
		s.TranscodeAudio = state.TranscodeAudio
		s.TargetAudioCodec = state.TargetAudioCodec
		s.SourceContainer = state.SourceContainer
		s.SourceAudioChannels = state.SourceAudioChannels
		s.TargetAudioChannels = state.TargetAudioChannels
		s.TargetAudioBitrateKbps = state.TargetAudioBitrateKbps

		// A full v3 route description owns the DV mode outright: a replan from
		// a DV strip remux to an SDR source must clear the stale mode or every
		// later remux request fails the profile check. Legacy partial updates
		// never carry a mode and must not clobber one.
		s.RemuxDVMode = state.RemuxDVMode
		// First-verified-wins DV pinning: a full route snapshot may carry a
		// drifted profile from a fresh probe of the same bytes. Keep the value
		// the session already pinned for this file identity; a snapshot naming
		// a different identity (a substitution, or a same-row virtual release
		// rotation) owns the pin outright, even when it verified no profile, so
		// the old file's profile never describes the new bytes.
		s.DVProfilePin = mergeDVPinV3(s.DVProfilePin, state.DVProfilePin)
		if s.DVProfilePin.Profile > 0 {
			s.DVProfile = s.DVProfilePin.Profile
		} else {
			s.DVProfile = state.DVProfile
		}
		s.RemuxResumeLeadingPictureDrop = state.RemuxResumeLeadingPictureDrop
	} else if state.RemuxDVMode != "" {
		s.RemuxDVMode = state.RemuxDVMode
		if state.DVProfile > 0 {
			s.DVProfile = state.DVProfile
		}
	}
	s.ClientIP = state.ClientIP
	if value := normalizeClientMetadataValue(state.ClientName, 128); value != "" {
		s.ClientName = value
	}
	if value := normalizeClientMetadataValue(state.ClientVersion, 64); value != "" {
		s.ClientVersion = value
	}
	if value := normalizeClientMetadataValue(state.ClientUserAgent, 512); value != "" {
		s.ClientUserAgent = value
	}
	s.StreamBitrateKbps = state.StreamBitrateKbps
	s.TargetResolution = state.TargetResolution
	s.TargetVideoCodec = state.TargetVideoCodec
	s.TargetBitrateKbps = state.TargetBitrateKbps
	s.TranscodeHWAccel = state.TranscodeHWAccel
	s.ToneMapMode = state.ToneMapMode
	if state.TranscodeRouteSet {
		s.TranscodeNodeURL = state.TranscodeNodeURL
		s.TranscodeTransportID = state.TranscodeTransportID
		s.RoutingNetworkProvider = state.RoutingNetworkProvider
		s.RoutingWorkload = state.RoutingWorkload
		s.RoutingExecution = state.RoutingExecution
		s.RoutingExecutionNodeID = state.RoutingExecutionNodeID
		s.RoutingExecutionNodeURL = state.RoutingExecutionNodeURL
		s.RoutingEgress = state.RoutingEgress
		s.RoutingEgressNodeID = state.RoutingEgressNodeID
		s.RoutingEgressNodeURL = state.RoutingEgressNodeURL
	}
	if state.VirtualSourceOwnershipSet || state.VirtualSourceSet {
		if strings.TrimSpace(s.VirtualSourceURI) != strings.TrimSpace(state.VirtualSourceURI) ||
			s.VirtualSourceOwnerInstallationID != state.VirtualSourceOwnerInstallationID {
			s.virtualSourceGeneration++
		}
		s.VirtualSourceURI = state.VirtualSourceURI
		s.VirtualSourceOwnerInstallationID = state.VirtualSourceOwnerInstallationID
		// A committed plan owns the probe lifecycle: whatever outcome a
		// superseded plan's deferred probe reached does not describe this
		// binding. A fresh-start resolve re-arms it to pending at spawn time.
		if state.VirtualSourceOwnershipSet {
			s.VirtualProbeOutcome = ""
			s.virtualProbeArmedAt = time.Time{}
			s.virtualProbeArmedBinding = VirtualSourceBindingSnapshot{}
		}
		if state.VirtualSubtitleEvidenceSet {
			s.VirtualSubtitleTracks = state.VirtualSubtitleTracks
			s.VirtualExternalSubtitles = state.VirtualExternalSubtitles
			s.VirtualAudioTracks = state.VirtualAudioTracks
			s.VirtualSubtitleEvidenceURI = state.VirtualSubtitleEvidenceURI
			s.VirtualSubtitleEvidenceFileID = state.VirtualSubtitleEvidenceFileID
			s.VirtualSubtitleEvidenceOwnerInstallationID = state.VirtualSubtitleEvidenceOwnerInstallationID
			s.VirtualSourceRevision = state.VirtualSourceRevision
			s.VirtualSubtitleEvidenceSet = true
			s.VirtualFontBundleKey = state.VirtualFontBundleKey
			s.VirtualFontBundleKeySet = state.VirtualFontBundleKeySet
		} else if state.VirtualSourceOwnershipSet {
			// A full v3 snapshot owns the binding and its evidence outright.
			// When the new effective file is not virtual (or the caller carries
			// no evidence), stale evidence from the previous candidate must be
			// cleared or the serve path would keep applying another release's
			// subtitle inventory. Legacy partial updates only ever overwrite
			// when they carry evidence, so they keep their historical shape.
			s.VirtualSubtitleTracks = nil
			s.VirtualExternalSubtitles = nil
			s.VirtualSourceRevision = ""
			s.VirtualSubtitleEvidenceSet = false
			s.VirtualFontBundleKey = FontBundleKey{}
			s.VirtualFontBundleKeySet = false
			if strings.TrimSpace(state.VirtualSourceURI) != "" {
				// Audio is carried independently of the probe-evidence flag.
				// Declared audio tracks are a legitimate placeholder (the
				// planner synthesizes and serves them), so a virtual binding
				// keeps the plan-time inventory for the serve overlay even
				// before a probe stamps the row. The provenance anchor is kept
				// with it: the same candidate URI plus row id let the overlay
				// reject the inventory once a rotation moves the binding, which
				// is what keeps a declared menu from crossing releases.
				s.VirtualAudioTracks = state.VirtualAudioTracks
				s.VirtualSubtitleEvidenceURI = state.VirtualSubtitleEvidenceURI
				s.VirtualSubtitleEvidenceFileID = state.VirtualSubtitleEvidenceFileID
				s.VirtualSubtitleEvidenceOwnerInstallationID = state.VirtualSubtitleEvidenceOwnerInstallationID
			} else {
				// A non-virtual effective file clears any previous virtual
				// inventory and its anchor.
				s.VirtualAudioTracks = nil
				s.VirtualSubtitleEvidenceURI = ""
				s.VirtualSubtitleEvidenceFileID = 0
				s.VirtualSubtitleEvidenceOwnerInstallationID = 0
			}
		}
	}
	// The committed subtitle position and rotation carry travel with the
	// evidence generation. A full v3 snapshot owns them outright; a legacy
	// partial update leaves the session's existing values untouched, exactly
	// like the virtual inventory above.
	if state.VirtualSourceSet || state.VirtualSourceOwnershipSet {
		s.VirtualSubtitleSelectionIndex = state.VirtualSubtitleSelectionIndex
		s.VirtualSubtitleSelectionSet = state.VirtualSubtitleSelectionSet
		s.VirtualSubtitleCarryFromIndex = state.VirtualSubtitleCarryFromIndex
		s.VirtualSubtitleCarryToIndex = state.VirtualSubtitleCarryToIndex
		s.VirtualSubtitleCarrySet = state.VirtualSubtitleCarrySet
		s.VirtualSubtitleCarryLostIndex = state.VirtualSubtitleCarryLostIndex
		s.VirtualSubtitleCarryLostSet = state.VirtualSubtitleCarryLostSet
	}
	s.SubtitleTrackIndex = state.SubtitleTrackIndex
	s.SubtitleBurnIn = state.SubtitleBurnIn
	s.SegmentDuration = state.SegmentDuration
	// Audio selection intent survives every stream-state round trip: it is
	// written once at start and only a user's explicit track change rotates
	// it, so reconciliation always compares the verified inventory against
	// what the viewer actually committed to, not a transient replan default.
	if state.SelectionOriginSet {
		s.SelectionOrigin = state.SelectionOrigin
		s.PreferredAudioLanguage = state.PreferredAudioLanguage
		s.SeriesAudioPreferenceSignature = state.SeriesAudioPreferenceSignature
		s.SelectedAudioSignature = state.SelectedAudioSignature
	}
	if state.TranscodeRouteSet {
		// Only the replacement commit consumes the v3 capacity reservation;
		// unrelated legacy stream updates arriving mid-replan must not release
		// the slot and let a concurrent admission race past the transcode cap.
		s.replacementPlayMethod = ""
	}
}

// snapshotSessionStreamStateLocked captures replaceable stream fields while the manager lock is held.
func snapshotSessionStreamStateLocked(s *Session) SessionStreamState {
	return SessionStreamState{
		PlayMethod:                       s.PlayMethod,
		BasePlayMethod:                   s.BasePlayMethod,
		AudioTrackIndex:                  s.AudioTrackIndex,
		VirtualAudioSelectionInvalid:     s.VirtualAudioSelectionInvalid,
		TranscodeAudio:                   s.TranscodeAudio,
		RemuxDVMode:                      s.RemuxDVMode,
		DVProfile:                        s.DVProfile,
		DVProfilePin:                     s.DVProfilePin,
		RemuxResumeLeadingPictureDrop:    s.RemuxResumeLeadingPictureDrop,
		ClientIP:                         s.ClientIP,
		ClientName:                       s.ClientName,
		ClientVersion:                    s.ClientVersion,
		ClientUserAgent:                  s.ClientUserAgent,
		StreamBitrateKbps:                s.StreamBitrateKbps,
		TargetResolution:                 s.TargetResolution,
		TargetVideoCodec:                 s.TargetVideoCodec,
		OutputContainer:                  s.OutputContainer,
		OutputProtocol:                   s.OutputProtocol,
		TargetAudioCodec:                 s.TargetAudioCodec,
		SourceContainer:                  s.SourceContainer,
		SourceAudioChannels:              s.SourceAudioChannels,
		TargetAudioChannels:              s.TargetAudioChannels,
		TargetAudioBitrateKbps:           s.TargetAudioBitrateKbps,
		TargetBitrateKbps:                s.TargetBitrateKbps,
		TranscodeHWAccel:                 s.TranscodeHWAccel,
		ToneMapMode:                      s.ToneMapMode,
		TranscodeNodeURL:                 s.TranscodeNodeURL,
		TranscodeTransportID:             s.TranscodeTransportID,
		TranscodeRouteSet:                true,
		RoutingNetworkProvider:           s.RoutingNetworkProvider,
		RoutingWorkload:                  s.RoutingWorkload,
		RoutingExecution:                 s.RoutingExecution,
		RoutingExecutionNodeID:           s.RoutingExecutionNodeID,
		RoutingExecutionNodeURL:          s.RoutingExecutionNodeURL,
		RoutingEgress:                    s.RoutingEgress,
		RoutingEgressNodeID:              s.RoutingEgressNodeID,
		RoutingEgressNodeURL:             s.RoutingEgressNodeURL,
		RequireMediaAuthorization:        s.RequireMediaAuthorization,
		MediaAuthorizationSet:            true,
		VirtualSourceURI:                 s.VirtualSourceURI,
		VirtualSourceOwnerInstallationID: s.VirtualSourceOwnerInstallationID,
		VirtualSourceSet:                 true,
		VirtualSourceOwnershipSet:        true,
		VirtualSourceRevision:            s.VirtualSourceRevision,
		VirtualSubtitleTracks:            s.VirtualSubtitleTracks,
		VirtualExternalSubtitles:         s.VirtualExternalSubtitles,
		VirtualAudioTracks:               s.VirtualAudioTracks,
		VirtualSubtitleEvidenceURI:       s.VirtualSubtitleEvidenceURI,
		VirtualSubtitleEvidenceFileID:    s.VirtualSubtitleEvidenceFileID,
		VirtualSubtitleEvidenceOwnerInstallationID: s.VirtualSubtitleEvidenceOwnerInstallationID,
		VirtualSubtitleEvidenceSet:                 s.VirtualSubtitleEvidenceSet,
		VirtualFontBundleKey:                       s.VirtualFontBundleKey,
		VirtualFontBundleKeySet:                    s.VirtualFontBundleKeySet,
		VirtualSubtitleSelectionIndex:              s.VirtualSubtitleSelectionIndex,
		VirtualSubtitleSelectionSet:                s.VirtualSubtitleSelectionSet,
		VirtualSubtitleCarryFromIndex:              s.VirtualSubtitleCarryFromIndex,
		VirtualSubtitleCarryToIndex:                s.VirtualSubtitleCarryToIndex,
		VirtualSubtitleCarrySet:                    s.VirtualSubtitleCarrySet,
		VirtualSubtitleCarryLostIndex:              s.VirtualSubtitleCarryLostIndex,
		VirtualSubtitleCarryLostSet:                s.VirtualSubtitleCarryLostSet,
		SelectionOrigin:                            s.SelectionOrigin,
		SelectionOriginSet:                         true,
		PreferredAudioLanguage:                     s.PreferredAudioLanguage,
		SeriesAudioPreferenceSignature:             s.SeriesAudioPreferenceSignature,
		SelectedAudioSignature:                     s.SelectedAudioSignature,
		SubtitleTrackIndex:                         s.SubtitleTrackIndex,
		SubtitleBurnIn:                             s.SubtitleBurnIn,
		SegmentDuration:                            s.SegmentDuration,
	}
}

// restoreSessionStreamStateLocked restores replaceable stream fields while the manager lock is held.
func restoreSessionStreamStateLocked(s *Session, state SessionStreamState) {
	s.PlayMethod = state.PlayMethod
	s.BasePlayMethod = state.BasePlayMethod
	s.AudioTrackIndex = state.AudioTrackIndex
	// A rollback restores the previous invalid-selection marker outright: the
	// forward-apply path clears it because a replacement carries a fresh
	// selection, but rolling the replacement back must resurrect the previous
	// verdict, or the serve path would derive an ordinal from the -1 sentinel
	// (mapped to 0:a:0) and mux the wrong track.
	s.VirtualAudioSelectionInvalid = state.VirtualAudioSelectionInvalid
	s.TranscodeAudio = state.TranscodeAudio
	s.RemuxDVMode = state.RemuxDVMode
	// The profile and its first-verified-wins pin are one fact; restoring the
	// profile without the pin would leave the rollback applying the withdrawn
	// successor's pin to the previous file's route.
	s.DVProfile = state.DVProfile
	s.DVProfilePin = state.DVProfilePin
	s.RemuxResumeLeadingPictureDrop = state.RemuxResumeLeadingPictureDrop
	s.ClientIP = state.ClientIP
	s.ClientName = state.ClientName
	s.ClientVersion = state.ClientVersion
	s.ClientUserAgent = state.ClientUserAgent
	s.StreamBitrateKbps = state.StreamBitrateKbps
	s.TargetResolution = state.TargetResolution
	s.TargetVideoCodec = state.TargetVideoCodec
	s.TargetAudioCodec = state.TargetAudioCodec
	s.SourceContainer = state.SourceContainer
	s.OutputContainer = state.OutputContainer
	s.OutputProtocol = state.OutputProtocol
	s.SourceAudioChannels = state.SourceAudioChannels
	s.TargetAudioChannels = state.TargetAudioChannels
	s.TargetAudioBitrateKbps = state.TargetAudioBitrateKbps
	s.TargetBitrateKbps = state.TargetBitrateKbps
	s.TranscodeHWAccel = state.TranscodeHWAccel
	s.ToneMapMode = state.ToneMapMode
	s.TranscodeNodeURL = state.TranscodeNodeURL
	s.TranscodeTransportID = state.TranscodeTransportID
	s.RoutingNetworkProvider = state.RoutingNetworkProvider
	s.RoutingWorkload = state.RoutingWorkload
	s.RoutingExecution = state.RoutingExecution
	s.RoutingExecutionNodeID = state.RoutingExecutionNodeID
	s.RoutingExecutionNodeURL = state.RoutingExecutionNodeURL
	s.RoutingEgress = state.RoutingEgress
	s.RoutingEgressNodeID = state.RoutingEgressNodeID
	s.RoutingEgressNodeURL = state.RoutingEgressNodeURL
	s.RequireMediaAuthorization = state.RequireMediaAuthorization
	if strings.TrimSpace(s.VirtualSourceURI) != strings.TrimSpace(state.VirtualSourceURI) ||
		s.VirtualSourceOwnerInstallationID != state.VirtualSourceOwnerInstallationID {
		s.virtualSourceGeneration++
	}
	s.VirtualSourceURI = state.VirtualSourceURI
	s.VirtualSourceOwnerInstallationID = state.VirtualSourceOwnerInstallationID
	s.VirtualSourceRevision = state.VirtualSourceRevision
	if state.VirtualSubtitleEvidenceSet {
		s.VirtualSubtitleTracks = state.VirtualSubtitleTracks
		s.VirtualExternalSubtitles = state.VirtualExternalSubtitles
		s.VirtualAudioTracks = state.VirtualAudioTracks
		s.VirtualSubtitleEvidenceURI = state.VirtualSubtitleEvidenceURI
		s.VirtualSubtitleEvidenceFileID = state.VirtualSubtitleEvidenceFileID
		s.VirtualSubtitleEvidenceOwnerInstallationID = state.VirtualSubtitleEvidenceOwnerInstallationID
		s.VirtualSubtitleEvidenceSet = true
		s.VirtualFontBundleKey = state.VirtualFontBundleKey
		s.VirtualFontBundleKeySet = state.VirtualFontBundleKeySet
	} else {
		s.VirtualSubtitleTracks = nil
		s.VirtualExternalSubtitles = nil
		s.VirtualSubtitleEvidenceSet = false
		s.VirtualFontBundleKey = FontBundleKey{}
		s.VirtualFontBundleKeySet = false
		// Mirror applySessionStreamStateLocked: a virtual binding keeps its
		// plan-time audio inventory and provenance anchor across a rollback
		// even without a probe stamp, so the serve overlay can still describe
		// the release without crossing onto another candidate.
		if strings.TrimSpace(state.VirtualSourceURI) != "" {
			s.VirtualAudioTracks = state.VirtualAudioTracks
			s.VirtualSubtitleEvidenceURI = state.VirtualSubtitleEvidenceURI
			s.VirtualSubtitleEvidenceFileID = state.VirtualSubtitleEvidenceFileID
			s.VirtualSubtitleEvidenceOwnerInstallationID = state.VirtualSubtitleEvidenceOwnerInstallationID
		} else {
			s.VirtualAudioTracks = nil
			s.VirtualSubtitleEvidenceURI = ""
			s.VirtualSubtitleEvidenceFileID = 0
			s.VirtualSubtitleEvidenceOwnerInstallationID = 0
		}
	}
	s.VirtualSubtitleSelectionIndex = state.VirtualSubtitleSelectionIndex
	s.VirtualSubtitleSelectionSet = state.VirtualSubtitleSelectionSet
	s.VirtualSubtitleCarryFromIndex = state.VirtualSubtitleCarryFromIndex
	s.VirtualSubtitleCarryToIndex = state.VirtualSubtitleCarryToIndex
	s.VirtualSubtitleCarrySet = state.VirtualSubtitleCarrySet
	s.VirtualSubtitleCarryLostIndex = state.VirtualSubtitleCarryLostIndex
	s.VirtualSubtitleCarryLostSet = state.VirtualSubtitleCarryLostSet
	s.SubtitleTrackIndex = state.SubtitleTrackIndex
	s.SubtitleBurnIn = state.SubtitleBurnIn
	s.SegmentDuration = state.SegmentDuration
	s.SelectionOrigin = state.SelectionOrigin
	s.PreferredAudioLanguage = state.PreferredAudioLanguage
	s.SeriesAudioPreferenceSignature = state.SeriesAudioPreferenceSignature
	s.SelectedAudioSignature = state.SelectedAudioSignature
}

// SetVirtualSource binds a live session to the provider-neutral candidate that
// was successfully resolved during planning. The caller must pass only the
// canonical virtual URI; provider URLs are never retained in session state.
//
// A binding move is a candidate change: the carried subtitle evidence still
// names the previous candidate's track layout, so it is deliberately left in
// place but no longer matches the new binding. The serve path detects that
// provenance mismatch, discards the evidence, and re-remaps against the live
// layout instead of applying the stale inventory to the new release. The
// planner overwrites the evidence through UpdateStreamState once it has probed
// the new candidate.
func (m *SessionManager) SetVirtualSource(sessionID, virtualURI string, ownerInstallationID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	m.setVirtualSourceLocked(s, virtualURI, ownerInstallationID)
	return nil
}

// setVirtualSourceLocked applies a candidate binding move and bumps both the
// stream revision and the binding generation. The generation is what the
// cache-handoff CAS reads, so every binding move — including a rotation — must
// go through here to stay fenced.
func (m *SessionManager) setVirtualSourceLocked(s *Session, virtualURI string, ownerInstallationID int) {
	if s == nil {
		return
	}
	trimmed := strings.TrimSpace(virtualURI)
	if trimmed != strings.TrimSpace(s.VirtualSourceURI) {
		// The evidence provenance anchor stays at the old candidate, which is
		// what makes the mismatch detectable downstream.
		s.VirtualSourceRevision = ""
		// A carry pair names ordinals on the evidence generation of the move it
		// was committed with. A different binding invalidates it; the next plan
		// commit records a fresh pair, and until then the serve path's evidence
		// remap handles pre-rotation URLs by identity.
		s.VirtualSubtitleCarrySet = false
		s.VirtualSubtitleCarryLostSet = false
	}
	// A binding move starts a fresh probe lifecycle: whatever outcome the
	// previous candidate's deferred probe reached says nothing about this one.
	s.VirtualProbeOutcome = ""
	s.virtualProbeArmedAt = time.Time{}
	s.virtualProbeArmedBinding = VirtualSourceBindingSnapshot{}
	// The invalid-selection marker belongs to the previous candidate's verified
	// inventory; the replacement binding re-plans and re-probes.
	s.VirtualAudioSelectionInvalid = false
	s.VirtualSourceURI = trimmed
	s.VirtualSourceOwnerInstallationID = ownerInstallationID
	s.streamRevision++
	s.virtualSourceGeneration++
	m.touchSessionLocked(s)
}

// Deferred-probe lifecycle outcomes recorded on a session. They are the
// wire-visible inventory_status values: pending while the enumeration is
// outstanding, then verified or failed. Empty means no deferred probe is
// outstanding. These are the single source of truth; the handlers package
// aliases them for its own use.
const (
	VirtualProbeOutcomePending  = "pending"
	VirtualProbeOutcomeVerified = "verified"
	VirtualProbeOutcomeFailed   = "failed"
)

// SetVirtualProbeOutcome records the disposition of the deferred background
// probe for the session's bound virtual candidate: "pending" while the full
// track enumeration is outstanding, then "verified" or "failed" when it lands.
// It is deliberately a dedicated setter rather than part of the stream-state
// snapshot: the outcome is written by the detached probe worker, which must not
// race a plan replacement's wholesale stream-state apply, and an empty value
// means "no deferred probe outstanding". An unknown session is a no-op so a
// probe that completes after its session ended does not error.
func (m *SessionManager) SetVirtualProbeOutcome(sessionID, outcome string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	setVirtualProbeOutcomeLocked(s, outcome)
	return nil
}

// setVirtualProbeOutcomeLocked writes the deferred-probe outcome and keeps the
// convergence timestamp and the armed-binding record in step: entering pending
// stamps the armed instant and captures the binding the mark belongs to, and any
// other value (verified, failed, empty) clears both. Caller holds m.mu.
func setVirtualProbeOutcomeLocked(s *Session, outcome string) {
	if s == nil {
		return
	}
	s.VirtualProbeOutcome = outcome
	if outcome == VirtualProbeOutcomePending {
		setVirtualProbePendingForBindingLocked(s, s.virtualBindingSnapshotLocked())
		return
	}
	s.virtualProbeArmedAt = time.Time{}
	s.virtualProbeArmedBinding = VirtualSourceBindingSnapshot{}
}

// setVirtualProbePendingForBindingLocked enters the pending outcome and records
// the binding the mark belongs to, supplied by the caller rather than reread from
// the session. Admission uses it to record the binding the job was SCHEDULED for:
// if SetEffectiveMediaFileID moves the effective row between the caller's
// binding read and this write, recording the session's current row would make the
// stale mark look live and block the replacement that owns the row the session
// now serves. Both writes are one step under the session lock. Caller holds m.mu.
func setVirtualProbePendingForBindingLocked(s *Session, armed VirtualSourceBindingSnapshot) {
	if s == nil {
		return
	}
	s.VirtualProbeOutcome = VirtualProbeOutcomePending
	s.virtualProbeArmedAt = time.Now()
	s.virtualProbeArmedBinding = armed
	// Every entry into pending is a new lifecycle, so the ownership token moves
	// here: a terminal write fenced on the token can only land on the lifecycle
	// whose arm produced it, even when a row-only move has since returned the
	// binding to a snapshot equal to an earlier one.
	s.virtualProbeLifecycle++
}

// SetVirtualProbeOutcomeIfGeneration records a deferred probe's outcome on the
// session only while its candidate binding is still the exact one the probe was
// scheduled against (compare-and-swap on virtualSourceGeneration). A probe that
// started before a rotation and finishes after it must not stamp its verdict —
// "verified" or "failed" — onto the replacement binding's lifecycle, or a
// subsequent poll would end the new release's loading state with the old
// release's outcome. It returns applied=false when the binding moved, so the
// caller can treat the result as superseded rather than an error. An unknown
// session is reported with ErrSessionNotFound and writes nothing.
//
// It is deliberately a dedicated setter rather than part of the stream-state
// snapshot: the outcome is written by the detached probe worker, which must not
// race a plan replacement's wholesale stream-state apply, and an empty value
// means "no deferred probe outstanding".
func (m *SessionManager) SetVirtualProbeOutcomeIfGeneration(sessionID string, generation uint64, outcome string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	if s.virtualSourceGeneration != generation {
		return false, nil
	}
	setVirtualProbeOutcomeLocked(s, outcome)
	return true, nil
}

// SetVirtualProbeOutcomeIfBinding records a deferred probe's outcome only while
// the session's candidate binding still matches the full snapshot the probe was
// admitted against: generation, virtual URI, owner installation, and effective
// row. It is the stronger form of SetVirtualProbeOutcomeIfGeneration, needed
// because SetEffectiveMediaFileID can move a session's effective row without
// advancing the generation, so a generation-only fence would stamp the old
// probe's verdict - verified or failed - onto the changed binding. A zero field
// in expected is not asserted, so a caller that captured only a generation keeps
// the generation-only behavior. It returns applied=false when the binding moved,
// and an unknown session reports ErrSessionNotFound and writes nothing.
func (m *SessionManager) SetVirtualProbeOutcomeIfBinding(sessionID string, expected VirtualSourceBindingSnapshot, outcome string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	if !s.matchesVirtualBindingLocked(expected) {
		return false, nil
	}
	setVirtualProbeOutcomeLocked(s, outcome)
	return true, nil
}

// SetVirtualProbeOutcomeIfLifecycle records a deferred probe's terminal outcome
// only while the session still carries the exact probe lifecycle the caller
// armed (identified by the token) and that lifecycle is still pending, in
// addition to the full-binding match SetVirtualProbeOutcomeIfBinding applies.
//
// It is the ownership fence the replacement path's late unresolved-row shed
// needs. A row-only move (SetEffectiveMediaFileID) does not advance the
// generation, so a B -> C -> B move returns the session to a snapshot
// byte-for-byte equal to the one an earlier arm captured; the older attempt's
// binding-equality fence would then match and its terminal failure would land on
// a newer lifecycle another recovery armed for the same binding. The token
// changes on every arm, so only the current lifecycle's owner can write.
//
// It returns applied=true when the outcome was written. superseded=true means a
// newer lifecycle (or a reached terminal outcome) now owns the binding, so the
// caller must not attempt recovery — that lifecycle is live and will converge on
// its own. Both false means the token still matched but the binding moved, which
// is the pre-existing stale-binding case the caller recovers. An unknown session
// reports ErrSessionNotFound and writes nothing.
func (m *SessionManager) SetVirtualProbeOutcomeIfLifecycle(sessionID string, token uint64, expected VirtualSourceBindingSnapshot, outcome string) (applied bool, superseded bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, false, ErrSessionNotFound
	}
	if token != 0 && s.virtualProbeLifecycle != token {
		// A newer arm created the current lifecycle for this binding; the
		// superseded attempt owns nothing.
		return false, true, nil
	}
	if s.VirtualProbeOutcome != VirtualProbeOutcomePending {
		// The lifecycle already reached a terminal outcome (verified or failed)
		// under the same token: the terminal write would overwrite a reached
		// verdict, and nothing is owed.
		return false, true, nil
	}
	if !s.matchesVirtualBindingLocked(expected) {
		// The token still matches, so no newer lifecycle owns the binding; the
		// row moved, which is the stale-binding recovery path.
		return false, false, nil
	}
	setVirtualProbeOutcomeLocked(s, outcome)
	return true, false, nil
}

// SetVirtualProbeVerifiedInventoryIfGeneration atomically records a deferred
// probe's verified outcome AND replaces the live session's carried virtual
// inventory with the tracks the probe just committed, under one generation-fenced
// critical section. It is the propagation half of same-session verified
// inventory: the serve bind deliberately keeps serving the session's carried
// inventory (the list the transport was started and muxed against), so the
// verified truth reaches the menu by updating that carried inventory rather than
// by swapping the row's tracks in on every read. A row overlay would publish a
// selection ordinal space the transport never used and silence fast starts.
//
// The outcome and the inventory move together on purpose. Recording verified in
// a separate write would let a poll observe the verified status while the menu
// still carried the provisional plan-time list — the exact transient the
// propagation exists to close. A write that reports applied has therefore already
// published both; a write that reports not-applied (a rotation moved the
// generation) has changed neither.
//
// The candidate provenance anchor (URI, row id, evidence-set flag) is left
// untouched: the committed tracks describe the same bound candidate the plan
// captured, so the anchor still names them. revision replaces
// VirtualSourceRevision so the inventory revision and its ETag move on the
// verified commit and the client's conditional poll stops returning 304;
// streamRevision advances so in-memory snapshot readers observe the change too.
func (m *SessionManager) SetVirtualProbeVerifiedInventoryIfGeneration(sessionID string, generation uint64, revision string, audio []models.AudioTrack, subtitles []models.SubtitleTrack, external []models.ExternalSubtitle) (bool, error) {
	// Generation-only is the binding CAS with an empty expectation: the zero
	// snapshot fences on the generation and nothing else, preserving this
	// method's historical contract for callers that hold no binding snapshot.
	return m.SetVirtualProbeVerifiedInventoryIfBinding(sessionID, VirtualSourceBindingSnapshot{Generation: generation}, revision, audio, subtitles, external)
}

// SetVirtualProbeVerifiedInventoryIfBinding is the full-binding form of
// SetVirtualProbeVerifiedInventoryIfGeneration. It records a deferred probe's
// verified outcome AND replaces the live session's carried virtual inventory
// under one critical section, but only while the session's candidate binding
// still matches the expectation in every dimension the snapshot captured —
// generation, virtual URI, owner installation, and effective media file row.
//
// The effective-row dimension is the reason this form exists. A binding move
// bumps virtualSourceGeneration, but SetEffectiveMediaFileID can point a session
// at a rotated release row without touching the generation; a generation-only
// fence would then let a probe that validated the old row commit the old row's
// inventory onto the changed binding. Comparing the captured row id atomically
// closes that window: a row-id-only mutation between the validate and the commit
// makes the CAS reject rather than mislabel the new binding's inventory.
//
// A zero field in expected is not asserted: EffectiveMediaFileID <= 0, an empty
// VirtualURI and a zero OwnerInstallationID each leave that dimension unfenced,
// so a caller that captured only the generation keeps the prior behavior.
func (m *SessionManager) SetVirtualProbeVerifiedInventoryIfBinding(sessionID string, expected VirtualSourceBindingSnapshot, revision string, audio []models.AudioTrack, subtitles []models.SubtitleTrack, external []models.ExternalSubtitle) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	if !s.matchesVirtualBindingLocked(expected) {
		return false, nil
	}
	// Preserve the selected track's identity across the overlay swap before the
	// list is replaced. The transport derives its audio-only ordinal from the
	// selected track's position in this list (audioStreamOrdinalV3), so leaving
	// the array position untouched when the verified list reorders it would point
	// the next remux at a different stream. Remapping to the same track keeps the
	// derived ordinal naming the stream the running mux was started against.
	//
	// A selection that cannot be re-identified - the track vanished, or the weak
	// fallback is ambiguous (duplicate-language/layout tracks) - is recorded as a
	// server-enforced invalid selection instead of an unchanged position that now
	// names a different track. The transport fails closed on that flag and the
	// client replans; the default-audio reconcile that ran before this write
	// against the plan-time selection has already issued the correction.
	if remapped := RemapAudioSelectionIndex(s.VirtualAudioTracks, s.AudioTrackIndex, audio); remapped >= 0 {
		s.AudioTrackIndex = remapped
		s.VirtualAudioSelectionInvalid = false
	} else if s.AudioTrackIndex >= 0 && s.AudioTrackIndex < len(s.VirtualAudioTracks) {
		s.AudioTrackIndex = -1
		s.VirtualAudioSelectionInvalid = true
	}
	s.VirtualAudioTracks = audio
	s.VirtualSubtitleTracks = subtitles
	s.VirtualExternalSubtitles = external
	s.VirtualSubtitleEvidenceSet = true
	s.VirtualSourceRevision = revision
	setVirtualProbeOutcomeLocked(s, VirtualProbeOutcomeVerified)
	s.streamRevision++
	m.touchSessionLocked(s)
	return true, nil
}

// matchesVirtualBindingLocked reports whether the session still carries the
// exact binding an expectation snapshot captured. Zero-valued expectation fields
// are not asserted, so the empty snapshot is a generation-only fence. Caller
// holds m.mu.
func (s *Session) matchesVirtualBindingLocked(expected VirtualSourceBindingSnapshot) bool {
	if s == nil {
		return false
	}
	if s.virtualSourceGeneration != expected.Generation {
		return false
	}
	if expected.VirtualURI != "" && !sameVirtualSourceURI(s.VirtualSourceURI, expected.VirtualURI) {
		return false
	}
	if expected.OwnerInstallationID != 0 && s.VirtualSourceOwnerInstallationID != expected.OwnerInstallationID {
		return false
	}
	if expected.EffectiveMediaFileID > 0 && s.MediaFileID != expected.EffectiveMediaFileID {
		return false
	}
	return true
}

// sameVirtualSourceURI compares two candidate binding URIs modulo surrounding
// whitespace, the same equality every binding snapshot assertion uses.
func sameVirtualSourceURI(a, b string) bool {
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

// ArmVirtualProbePendingIfGeneration transitions a binding generation's probe
// outcome from empty to pending, and only from empty. It is the exclusive
// ownership fence for deferred-probe admission: a duplicate start whose
// SaveAttempt lost the CAS adopts the winner's session and re-enters the start
// path, but it must not re-arm — and re-probe — a lifecycle the winner already
// armed or completed. It returns false when the outcome was already set (armed,
// verified, or failed), when the binding moved, or for an unknown session, so
// the caller drops the duplicate job without an outcome write.
func (m *SessionManager) ArmVirtualProbePendingIfGeneration(sessionID string, generation uint64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	if s.virtualSourceGeneration != generation {
		return false, nil
	}
	if s.VirtualProbeOutcome != "" {
		// The lifecycle is already armed or terminal for this binding: a
		// duplicate admission owns nothing and must not restart it.
		return false, nil
	}
	setVirtualProbeOutcomeLocked(s, VirtualProbeOutcomePending)
	return true, nil
}

// ArmVirtualProbePendingIfBinding transitions a binding generation's probe
// outcome from empty to pending, and only from empty, exactly like
// ArmVirtualProbePendingIfGeneration — with one difference that closes a
// row-only-move window. The armed-binding record is the caller's captured
// snapshot rather than the session's binding at write time. SetEffectiveMediaFileID
// moves a session's effective row without advancing the generation, so if it
// lands between the caller's binding read and this write, recording the current
// row would leave the pending mark looking LIVE for a binding no worker owns and
// block the replacement that could recover it. Recording the scheduled binding
// keeps the mark stale (or live) according to what the queued job will actually
// fence on, so recovery can hand the moved row a fresh lifecycle. It returns
// false for an unknown session, a moved generation, or an already set outcome
// (armed, verified, failed), so the caller drops the duplicate job unchanged.
func (m *SessionManager) ArmVirtualProbePendingIfBinding(sessionID string, expected VirtualSourceBindingSnapshot) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	if s.virtualSourceGeneration != expected.Generation {
		return false, nil
	}
	if s.VirtualProbeOutcome != "" {
		// The lifecycle is already armed or terminal for this binding: a
		// duplicate admission owns nothing and must not restart it.
		return false, nil
	}
	setVirtualProbePendingForBindingLocked(s, expected)
	return true, nil
}

// VirtualProbePendingSessions returns up to limit live sessions whose deferred
// probe is still pending and was armed at or before cutoff. It is the read side
// of the convergence watchdog: a session listed here has been waiting longer
// than the caller's bound without reaching a terminal outcome, and the caller
// fails it so a client can leave the loading state. A session that already
// reached verified or failed, or was never armed, is not returned. The read
// holds the session lock but stops at limit, so the snapshot is always bounded
// regardless of fleet size; an empty outcome (no deferred lifecycle) and a
// future armed time are both skipped.
func (m *SessionManager) VirtualProbePendingSessions(cutoff time.Time, limit int) []VirtualProbeTerminalSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		return nil
	}
	var result []VirtualProbeTerminalSession
	for _, s := range m.sessions {
		if len(result) >= limit {
			break
		}
		if s.VirtualProbeOutcome != VirtualProbeOutcomePending {
			continue
		}
		if s.virtualProbeArmedAt.IsZero() || s.virtualProbeArmedAt.After(cutoff) {
			continue
		}
		fileID := s.MediaFileID
		if fileID <= 0 {
			fileID = s.RequestedMediaFileID
		}
		result = append(result, VirtualProbeTerminalSession{SessionID: s.ID, FileID: fileID, Generation: s.virtualSourceGeneration})
	}
	return result
}

// FailVirtualProbeIfPendingIfGeneration terminally fails a deferred probe
// lifecycle that is still pending and was armed at or before cutoff, only while
// the binding is still the generation the probe was admitted against. It is the
// convergence watchdog's compare-and-swap: it fires from pending to failed and
// from no other outcome, so it can never overwrite a verdict a probe worker
// already recorded (verified or failed), and a rotation that landed while the
// probe was outstanding leaves the replacement binding to its own lifecycle. It
// reports whether the failure write landed. An unknown session reports
// ErrSessionNotFound and writes nothing.
func (m *SessionManager) FailVirtualProbeIfPendingIfGeneration(sessionID string, generation uint64, cutoff time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	if s.virtualSourceGeneration != generation {
		return false, nil
	}
	if s.VirtualProbeOutcome != VirtualProbeOutcomePending {
		return false, nil
	}
	if s.virtualProbeArmedAt.IsZero() || s.virtualProbeArmedAt.After(cutoff) {
		return false, nil
	}
	setVirtualProbeOutcomeLocked(s, VirtualProbeOutcomeFailed)
	return true, nil
}

// VirtualSourceBindingSnapshot captures the full candidate-binding state under the session lock.
type VirtualSourceBindingSnapshot struct {
	VirtualURI           string
	OwnerInstallationID  int
	EffectiveMediaFileID int
	Generation           uint64
	// ProbeLifecycle is the deferred-probe lifecycle token in force when the
	// snapshot was taken (see Session.virtualProbeLifecycle). It is deliberately
	// NOT asserted by matchesVirtualBindingLocked: it is not part of the binding
	// identity, it is the ownership token a replacement path carries forward so a
	// late terminal write can prove it still owns the lifecycle it armed. Zero on
	// snapshots a caller constructed without reading a live session.
	ProbeLifecycle uint64
}

// virtualBindingSnapshotLocked captures the session's full candidate-binding
// identity under the session lock. It is the one place the four binding
// dimensions are read together, so every snapshot (the public reader, the
// pending-armed record, and the live-pending predicate) agrees on what "the
// binding" means. Caller holds m.mu.
func (s *Session) virtualBindingSnapshotLocked() VirtualSourceBindingSnapshot {
	if s == nil {
		return VirtualSourceBindingSnapshot{}
	}
	return VirtualSourceBindingSnapshot{
		VirtualURI:           s.VirtualSourceURI,
		OwnerInstallationID:  s.VirtualSourceOwnerInstallationID,
		EffectiveMediaFileID: s.MediaFileID,
		Generation:           s.virtualSourceGeneration,
		ProbeLifecycle:       s.virtualProbeLifecycle,
	}
}

// virtualProbePendingIsLiveLocked reports whether the session's outstanding
// pending mark is still owned by the session's CURRENT candidate binding: a
// probe armed for this exact generation, candidate URI, owner, and effective row
// is in flight and will land against it. A pending mark whose armed binding no
// longer matches (a row-only move that did not advance the generation) is stale
// — no worker's fence can land it — so the current binding owes a fresh probe.
// Caller holds m.mu.
func (s *Session) virtualProbePendingIsLiveLocked() bool {
	if s == nil || s.VirtualProbeOutcome != VirtualProbeOutcomePending {
		return false
	}
	armed := s.virtualProbeArmedBinding
	return armed.Generation == s.virtualSourceGeneration &&
		sameVirtualSourceURI(armed.VirtualURI, s.VirtualSourceURI) &&
		armed.OwnerInstallationID == s.VirtualSourceOwnerInstallationID &&
		armed.EffectiveMediaFileID == s.MediaFileID
}

// VirtualSourceBinding captures the session's candidate-binding identity and generation
// atomically under the session manager lock.
func (m *SessionManager) VirtualSourceBinding(sessionID string) (VirtualSourceBindingSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return VirtualSourceBindingSnapshot{}, ErrSessionNotFound
	}
	return s.virtualBindingSnapshotLocked(), nil
}

// ArmVirtualProbeReplacementForCurrentBinding atomically re-arms the deferred
// probe pending mark for the session's CURRENT candidate binding. It is the
// replacement half of dropped-probe recovery: a probe admitted for a binding the
// session has since left is dropped by its fence without writing, and when the
// current binding carries no live lifecycle of its own this hands it a fresh one
// so it is not left un-enumerated.
//
// It refuses (returns applied=false and writes nothing) when:
//   - the session's current candidate is not candidateURI, so a replacement built
//     from the dropped probe's input cannot describe the current binding and must
//     not be admitted; or
//   - ownerInstallationID is non-zero and the current binding carries a different
//     owner. The replacement reuses the dropped job's probe input (stream URL and
//     request headers), which was resolved for one owner installation. A same-URI
//     owner move is a different source behind the same candidate: the input is
//     stale and the fresh resolve that produced the new binding is the only safe
//     source of its URL and headers, so no replacement is admitted. The effective
//     row is deliberately NOT fenced here: the replacement re-resolves the row it
//     writes (see replacementCatalogRow), and a row-only move that keeps the
//     candidate and owner serves the same bytes, so only the write target moves; or
//   - the current binding already carries a LIVE pending probe (a worker will land
//     it) or a terminal outcome (verified or failed), so nothing is owed.
//
// Otherwise the mark is stale (armed for a superseded binding) or absent, and the
// call enters pending for the current binding and returns the snapshot it armed
// against. Because the pending record captures the current binding, the
// replacement job fences on that binding and the dropped job can never write
// against it. The returned snapshot's ProbeLifecycle is the ownership token of
// the lifecycle just armed, so a job that later sheds can fence its terminal
// write on the exact lifecycle it created rather than binding equality alone. An
// unknown session reports ErrSessionNotFound and writes nothing.
func (m *SessionManager) ArmVirtualProbeReplacementForCurrentBinding(sessionID, candidateURI string, ownerInstallationID int) (VirtualSourceBindingSnapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.armVirtualProbeReplacementForCurrentBindingLocked(sessionID, candidateURI, ownerInstallationID)
}

// armVirtualProbeReplacementForCurrentBindingLocked is the critical section
// shared by the replacement arm and its saturated-fallback twin: it applies the
// candidate/owner/liveness checks and, when they pass, enters pending for the
// current binding. It returns the binding it acted on whether or not it armed,
// so a caller can report the row the decision targeted. Caller holds m.mu.
func (m *SessionManager) armVirtualProbeReplacementForCurrentBindingLocked(sessionID, candidateURI string, ownerInstallationID int) (VirtualSourceBindingSnapshot, bool, error) {
	s, ok := m.sessions[sessionID]
	if !ok {
		return VirtualSourceBindingSnapshot{}, false, ErrSessionNotFound
	}
	binding := s.virtualBindingSnapshotLocked()
	if !sameVirtualSourceURI(binding.VirtualURI, candidateURI) {
		return binding, false, nil
	}
	if ownerInstallationID != 0 && binding.OwnerInstallationID != ownerInstallationID {
		// The dropped job's input was resolved for a different owner
		// installation; reusing it would probe another source's bytes and stamp
		// them onto this binding's row.
		return binding, false, nil
	}
	if s.virtualProbePendingIsLiveLocked() {
		return binding, false, nil
	}
	if s.VirtualProbeOutcome != "" && s.VirtualProbeOutcome != VirtualProbeOutcomePending {
		// A terminal verdict for this binding: a replacement would overwrite a
		// reached outcome, not fill a gap.
		return binding, false, nil
	}
	setVirtualProbeOutcomeLocked(s, VirtualProbeOutcomePending)
	return s.virtualBindingSnapshotLocked(), true, nil
}

// FailVirtualProbeReplacementForCurrentBinding atomically arms the deferred
// probe pending mark for the session's current candidate binding AND records the
// terminal failure on it, in one critical section. It is the saturated-fallback
// twin of the replacement arm: the handler uses it when the bounded retry set
// cannot take a rejected shed's recovery job, so the current binding is failed
// directly instead of being left pending.
//
// The two writes must be one step. A row-only move (SetEffectiveMediaFileID) does
// not advance the binding generation, so if the arm and the failure were separate
// manager calls a B -> C -> B move could land between them: the failure's
// binding-equality fence would then match the returned-to B and stamp this
// fallback's failure onto a DIFFERENT, newer B lifecycle that another recovery
// armed after the row came back. Arming and failing under one lock makes the
// failure belong to exactly the lifecycle this call created, and a later arm for
// the same binding is refused because the outcome is already terminal.
func (m *SessionManager) FailVirtualProbeReplacementForCurrentBinding(sessionID, candidateURI string, ownerInstallationID int) (VirtualSourceBindingSnapshot, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	binding, armed, err := m.armVirtualProbeReplacementForCurrentBindingLocked(sessionID, candidateURI, ownerInstallationID)
	if err != nil || !armed {
		return binding, false, err
	}
	setVirtualProbeOutcomeLocked(m.sessions[sessionID], VirtualProbeOutcomeFailed)
	return binding, true, nil
}

// SetVirtualSourceIfBinding applies the candidate handoff and updates the effective
// media file ID atomically under the lock only while the session's binding generation,
// virtual URI, owner, and effective file ID match the expected snapshot.
func (m *SessionManager) SetVirtualSourceIfBinding(
	sessionID string,
	expected VirtualSourceBindingSnapshot,
	virtualURI string,
	ownerInstallationID int,
	effectiveMediaFileID int,
) (uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return 0, false, ErrSessionNotFound
	}
	if s.virtualSourceGeneration != expected.Generation ||
		strings.TrimSpace(s.VirtualSourceURI) != strings.TrimSpace(expected.VirtualURI) ||
		s.VirtualSourceOwnerInstallationID != expected.OwnerInstallationID ||
		(expected.EffectiveMediaFileID > 0 && s.MediaFileID != expected.EffectiveMediaFileID) {
		return s.virtualSourceGeneration, false, nil
	}
	m.setVirtualSourceLocked(s, virtualURI, ownerInstallationID)
	if effectiveMediaFileID > 0 {
		s.MediaFileID = effectiveMediaFileID
	}
	return s.virtualSourceGeneration, true, nil
}

// VirtualSourceGeneration returns the session's current candidate-binding
// generation. A cache handoff captures it before re-listing and passes it to
// SetVirtualSourceIfGeneration so a late handoff cannot clobber a newer binding.
func (m *SessionManager) VirtualSourceGeneration(sessionID string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return 0, ErrSessionNotFound
	}
	return s.virtualSourceGeneration, nil
}

// VirtualProbeNotifiedGeneration returns the binding generation whose terminal
// probe outcome was last acked delivered (see MarkVirtualProbeNotified). It is
// the read side of the terminal-notification watermark: the dispatcher's
// overflow rescan skips a session whose binding generation is not newer than
// this value, and convergence tests assert it catches up to the binding
// generation once the push lands.
func (m *SessionManager) VirtualProbeNotifiedGeneration(sessionID string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return 0, ErrSessionNotFound
	}
	return s.virtualProbeNotifiedGeneration, nil
}

// SetAutoFallback records whether a session may fail over to another media
// version when its source dies or is unplayable. It is negotiated at start from
// the viewer's auto selection and re-negotiable when the viewer re-selects Auto.
func (m *SessionManager) SetAutoFallback(sessionID string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.autoFallback = enabled
	s.autoFallbackSet = true
	return nil
}

// SetAudioSelectionIntent records the attempt's durable audio selection intent
// on the live session: whether the committed selection came from the server's
// language preference ("auto") or an explicit viewer choice ("explicit"), the
// resolved preferred language, the series-preference snapshot and the
// signature of the committed track. Probe-landing reconciliation replays
// these fields against the verified inventory; legacy sessions carry the zero
// value and are never reconciled.
func (m *SessionManager) SetAudioSelectionIntent(sessionID, origin, preferredLang string, seriesSig, selectedSig *userstore.AudioTrackSignature) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.SelectionOrigin = origin
	s.PreferredAudioLanguage = preferredLang
	s.SeriesAudioPreferenceSignature = seriesSig
	s.SelectedAudioSignature = selectedSig
	m.touchSessionLocked(s)
	return nil
}

// RestoreAutoFallback returns a session's auto-fallback state to a value
// captured by AutoFallback before a speculative renegotiation. It restores both
// the boolean and the set-bit, so a session that had never negotiated the field
// goes back to reporting ok=false rather than a spurious explicit "off". A
// failed replan that applied a new policy before execution must roll back to
// exactly the prior state, not just the boolean, or a later replan would treat
// an unset session as having explicitly disabled fallback.
func (m *SessionManager) RestoreAutoFallback(sessionID string, enabled bool, set bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.autoFallback = enabled
	s.autoFallbackSet = set
	return nil
}

// AutoFallback reports the session's negotiated auto-fallback state. ok is
// false for a session that never set it (for example a reconstruction), so a
// caller keeps its own conservative default instead of assuming a value.
func (m *SessionManager) AutoFallback(sessionID string) (enabled bool, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, false
	}
	return s.autoFallback, s.autoFallbackSet
}

// SetVirtualSourceIfGeneration is the generation-fenced form of
// SetVirtualSource. It applies the binding move only while the session still
// carries expectedGeneration, so a handoff whose replacement source was
// resolved before a newer binding move is refused rather than silently
// overriding it. It returns the generation after the attempt and whether it
// applied; a false result with a nil error is a benign lost race.
func (m *SessionManager) SetVirtualSourceIfGeneration(sessionID string, expectedGeneration uint64, virtualURI string, ownerInstallationID int) (uint64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return 0, false, ErrSessionNotFound
	}
	if s.virtualSourceGeneration != expectedGeneration {
		return s.virtualSourceGeneration, false, nil
	}
	m.setVirtualSourceLocked(s, virtualURI, ownerInstallationID)
	return s.virtualSourceGeneration, true, nil
}

// ApplyReplacement atomically updates every live-session field owned by a
// protocol-v3 plan and returns a CAS rollback token for a later persistence
// failure.
func (m *SessionManager) ApplyReplacement(sessionID string, replacement SessionReplacement) (SessionReplacementRollback, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return SessionReplacementRollback{}, ErrSessionNotFound
	}
	return m.applyReplacementLocked(s, sessionID, replacement)
}

// ApplyReplacementIfRoute applies a complete replacement only while the
// session still routes to expected. It publishes route and stream state in one
// critical section so callers never expose a successor with predecessor state.
func (m *SessionManager) ApplyReplacementIfRoute(
	sessionID string,
	expected TranscodeRoute,
	replacement SessionReplacement,
) (SessionReplacementRollback, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return SessionReplacementRollback{}, false, ErrSessionNotFound
	}
	if s.TranscodeNodeURL != expected.NodeURL || s.TranscodeTransportID != expected.TransportID {
		return SessionReplacementRollback{}, false, nil
	}
	rollback, err := m.applyReplacementLocked(s, sessionID, replacement)
	return rollback, err == nil, err
}

func (m *SessionManager) applyReplacementLocked(
	s *Session,
	sessionID string,
	replacement SessionReplacement,
) (SessionReplacementRollback, error) {
	if replacement.EffectiveMediaFileID <= 0 {
		return SessionReplacementRollback{}, errors.New("replacement effective media file id is invalid")
	}

	rollback := SessionReplacementRollback{
		sessionID:                    sessionID,
		previousEffectiveMediaFileID: s.MediaFileID,
		previousStreamState:          snapshotSessionStreamStateLocked(s),
		previousReplacementMethod:    s.replacementPlayMethod,
		previousPlanCommittedAt:      s.PlanCommittedAt,
	}
	if replacement.PositionSeconds != nil {
		rollback.previousPosition = s.Position
		rollback.previousPaused = s.IsPaused
		rollback.previousPositionSequence = s.positionSequence
		rollback.restoreProgress = true
	}

	s.MediaFileID = replacement.EffectiveMediaFileID
	// A replacement stream means the play is active again, so a stop mark from
	// an ID-less stop no longer applies, and an earlier transport cancel no
	// longer describes this committed successor.
	s.StopReported = false
	s.ClientCanceled = ClientCancelEvidence{}
	applySessionStreamStateLocked(s, replacement.StreamState)
	s.virtualSourceGeneration++
	if replacement.PositionSeconds != nil {
		s.Position = *replacement.PositionSeconds
		s.positionSequence = max(s.positionSequence, replacement.PositionSequence)
		if !replacement.PreservePaused {
			s.IsPaused = replacement.IsPaused
		}
	}
	s.streamRevision++
	rollback.appliedRevision = s.streamRevision
	m.touchSessionLocked(s)
	// A committed fallback plan restarts the startup window: the client is
	// still waiting out its startup timeout for this plan.
	s.PlanCommittedAt = s.LastActivityAt
	return rollback, nil
}

// RollbackReplacement restores the state captured by ApplyReplacement when no
// newer session mutation has superseded it.
func (m *SessionManager) RollbackReplacement(sessionID string, rollback SessionReplacementRollback) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	if rollback.sessionID != sessionID || rollback.appliedRevision == 0 || s.streamRevision != rollback.appliedRevision {
		return ErrSessionReplacementSuperseded
	}
	s.MediaFileID = rollback.previousEffectiveMediaFileID
	// Rolling back a replacement is still an active play, not a stopped one.
	s.StopReported = false
	// A rollback re-arms the previous route; the canceled mark belonged to the
	// withdrawn successor, so it must not linger past the rollback.
	s.ClientCanceled = ClientCancelEvidence{}
	restoreSessionStreamStateLocked(s, rollback.previousStreamState)
	s.virtualSourceGeneration++
	if rollback.restoreProgress {
		s.Position = rollback.previousPosition
		s.IsPaused = rollback.previousPaused
		s.positionSequence = rollback.previousPositionSequence
	}
	s.replacementPlayMethod = rollback.previousReplacementMethod
	// A rolled-back replan handed the client no plan, so it must not restart
	// the startup window.
	s.PlanCommittedAt = rollback.previousPlanCommittedAt
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

var _ interface {
	SetTranscodeStreamDetails(string, string, string, bool, string, tonemap.Mode, string, int) error
} = (*SessionManager)(nil)

// SetTranscodeStreamDetails records the actual encode decisions of a running
// transcode on the session — video copy vs re-encode, and whether audio is
// re-encoded — together with the confirmed hardware and tone-map executors —
// so session sync and the admin activity views describe what ffmpeg is doing
// rather than relying on requested transport defaults.
func (m *SessionManager) SetTranscodeStreamDetails(sessionID, targetVideoCodec, targetAudioCodec string, transcodeAudio bool, hwAccel string, toneMapMode tonemap.Mode, targetResolution string, targetBitrateKbps int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.TargetVideoCodec = targetVideoCodec
	s.TargetAudioCodec = targetAudioCodec
	s.TranscodeAudio = transcodeAudio
	s.TranscodeHWAccel = hwAccel
	s.ToneMapMode = toneMapMode
	s.TargetResolution = targetResolution
	s.TargetBitrateKbps = targetBitrateKbps
	m.touchSessionLocked(s)
	return nil
}

// SetOutputFormat records the format selected by the serving transport, not
// a guess based on a codec or a whole-session playback classification.
func (m *SessionManager) SetOutputFormat(sessionID, container, protocol string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.OutputContainer = container
	s.OutputProtocol = protocol
	m.touchSessionLocked(s)
	return nil
}

// SetTranscodeNodeURL assigns a transcode node URL to an existing session.
func (m *SessionManager) SetTranscodeNodeURL(sessionID, url string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.TranscodeNodeURL = url
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

// SetNodeRoutingAssignment records the successfully prepared route exposed by
// live-session observability. Callers invoke it only after the selected route
// has enough authority to serve the client.
func (m *SessionManager) SetNodeRoutingAssignment(sessionID string, assignment NodeRoutingAssignment) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.RoutingNetworkProvider = assignment.NetworkProvider
	s.RoutingWorkload = assignment.Workload
	s.RoutingExecution = assignment.Execution
	s.RoutingExecutionNodeID = assignment.ExecutionNodeID
	s.RoutingExecutionNodeURL = assignment.ExecutionNodeURL
	s.RoutingEgress = assignment.Egress
	s.RoutingEgressNodeID = assignment.EgressNodeID
	s.RoutingEgressNodeURL = assignment.EgressNodeURL
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

// SetTranscodeRoute atomically assigns the node and process identity used to
// serve a transcode.
func (m *SessionManager) SetTranscodeRoute(sessionID string, route TranscodeRoute) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.TranscodeNodeURL = route.NodeURL
	s.TranscodeTransportID = route.TransportID
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

// SetEffectiveMediaFileID updates the currently delivered source file while
// preserving the originally requested file selection. Kept for the fork's
// virtual rotation path (StreamHandler rebinds a session to the rotated
// release row); upstream's test-prune pass removed it as unused upstream.
func (m *SessionManager) SetEffectiveMediaFileID(sessionID string, fileID int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	if fileID > 0 {
		s.MediaFileID = fileID
	}
	s.streamRevision++
	m.touchSessionLocked(s)
	return nil
}

// ReanchorVirtualEffectiveRow atomically points a live virtual session at the
// concrete catalog row that owns its bound candidate and re-anchors its carried
// track evidence to that row, but only while the full candidate binding still
// matches the snapshot the caller captured before it resolved the owner row.
//
// It is the late-owner adoption for a first-ever auto start: the concrete
// ?result= row did not exist when the plan resolved, so the session kept the
// release's provider-neutral sibling, and the deferred probe stamped that
// sibling. When the owner row materializes later in the same session, the serve
// and inventory binds resolve it by exact path, but the session's effective row
// and evidence anchor still name the neutral sibling, so the verified overlay is
// rejected as a different candidate and the menus freeze. Moving both the
// effective row and the evidence row id to the owner keeps the overlay applying.
//
// The expectation fences every binding dimension at once - generation, virtual
// URI, owner installation, and effective row - through the same predicate the
// verified-inventory CAS uses. A rotation that lands while the caller is
// resolving the owner row bumps the generation, and a row-only move changes the
// effective row; either makes the CAS reject rather than bless a stale adoption.
//
// The deferred-probe lifecycle must not be pending. A row-only move that lands
// while a probe is outstanding would make that probe's full-binding propagation
// CAS reject and strand the pending outcome until the convergence watchdog, with
// no replacement probe owning it. Deferring the adoption until the lifecycle
// reaches a terminal outcome keeps the probe's binding valid so its verified
// inventory lands, and the adoption then applies on the next serve or inventory
// bind. The probe is bounded, so the deferral is temporary by construction.
//
// The evidence URI is set to the bound candidate URI the caller resolved, so
// the anchor names the exact row the session now serves. A zero field in
// expected is not asserted (mirroring matchesVirtualBindingLocked), so a caller
// that captured only a generation keeps the legacy behavior; a rowID <= 0 or
// equal to the current row is a no-op. An unknown session reports
// ErrSessionNotFound.
func (m *SessionManager) ReanchorVirtualEffectiveRow(sessionID string, expected VirtualSourceBindingSnapshot, rowID int, evidenceURI string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	if !s.matchesVirtualBindingLocked(expected) {
		return false, nil
	}
	if s.VirtualProbeOutcome == VirtualProbeOutcomePending {
		return false, nil
	}
	if rowID <= 0 || s.MediaFileID == rowID {
		return false, nil
	}
	s.MediaFileID = rowID
	s.VirtualSubtitleEvidenceFileID = rowID
	if trimmed := strings.TrimSpace(evidenceURI); trimmed != "" {
		s.VirtualSubtitleEvidenceURI = trimmed
	}
	s.streamRevision++
	m.touchSessionLocked(s)
	return true, nil
}

// TranscodeExecutorMove is the new executor for a running transcode whose
// previous executor stopped answering. The transport identity, the egress, and
// every byte-affecting field stay as they were: only where FFmpeg runs changes.
type TranscodeExecutorMove struct {
	// NodeURL is the transcode node now running the transport, or empty when
	// this API process runs it.
	NodeURL string
	// Execution and ExecutionNodeID are the routing vocabulary for the new
	// executor. They are applied only to a session that already carries a
	// committed routing assignment.
	Execution       string
	ExecutionNodeID int
	// TranscodeHWAccel is the encoder backend the new executor reported. Empty
	// keeps the previous value.
	TranscodeHWAccel string
}

// MoveTranscodeExecutor re-points a live transcode at a new executor only while
// the session still serves expected. It reports false, changing nothing, when a
// replan or another move already replaced that route, so a stale move can never
// overwrite a newer one. The check covers this process only: the moved route
// is not shared with other API processes, and a session rebuilt from its
// stream token still names the old node.
func (m *SessionManager) MoveTranscodeExecutor(sessionID string, expected TranscodeRoute, move TranscodeExecutorMove) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false, ErrSessionNotFound
	}
	if s.TranscodeNodeURL != expected.NodeURL || s.TranscodeTransportID != expected.TransportID {
		return false, nil
	}
	s.TranscodeNodeURL = move.NodeURL
	// A session without a routing assignment predates node routing. Writing
	// only the execution half would read as a partially committed route, which
	// the serve paths refuse.
	if s.RoutingWorkload != "" {
		s.RoutingExecution = move.Execution
		s.RoutingExecutionNodeID = move.ExecutionNodeID
		s.RoutingExecutionNodeURL = move.NodeURL
	}
	if move.TranscodeHWAccel != "" {
		s.TranscodeHWAccel = move.TranscodeHWAccel
	}
	s.streamRevision++
	m.touchSessionLocked(s)
	return true, nil
}

// SetRealtimeConnection marks whether a realtime control connection is active for a session.
func (m *SessionManager) SetRealtimeConnection(sessionID string, connected bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.HasRealtimeConnection = connected
	// The admin/session sync layer still exposes a generic websocket flag.
	s.HasWebSocket = connected
	m.touchSessionLocked(s)
	return nil
}

// SetProgressPersistenceDisabled controls whether session progress updates and
// stop events should write resume/history state. This is useful for players
// whose resume timeline is not the same as the session's file-local timeline.
func (m *SessionManager) SetProgressPersistenceDisabled(sessionID string, disabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.DisableProgressPersistence = disabled
	m.touchSessionLocked(s)
	return nil
}

// SetTimelineDuration records the length of the timeline the session's
// Position is measured on, for a player whose position spans more than the
// session's file. See Session.TimelineDuration.
func (m *SessionManager) SetTimelineDuration(sessionID string, seconds float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.TimelineDuration = seconds
	return nil
}

// TouchActivity refreshes the session's activity timestamp without changing
// any other playback state.
// MarkPlanCommitted records that startup planning completed, allowing normal lifecycle accounting.
func (m *SessionManager) MarkPlanCommitted(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	now := time.Now()
	s.PlanCommittedAt = now
	s.LastActivityAt = now
	s.UpdatedAt = now
	return nil
}

func (m *SessionManager) TouchActivity(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	// Activity refreshes the idle deadline but deliberately does not clear the
	// transport-cancel evidence: which request a cancel belonged to is not a
	// property of "anything touched us since", and clearing on every HLS
	// manifest or segment request made the evidence timing-dependent. The
	// revision and binding generation it carries are what let a reader tell a
	// current-route cancel from a predecessor's.
	m.touchSessionLocked(s)
	return nil
}

// BeginTransport increments the count of in-flight media transport requests
// for the session and refreshes its activity timestamp.
func (m *SessionManager) BeginTransport(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	s.activeTransportCount++
	m.touchSessionLocked(s)
	return nil
}

// SetRemoteTransport records whether this session's media bytes are served by
// another node (a proxy), rather than by a transport request this server
// handles itself.
//
// A locally-served stream is protected from the idle reaper by
// BeginTransport/EndTransport around the serve call. A proxy-served stream
// never enters that path: the client talks to the proxy directly, so from this
// server's point of view a healthy multi-hour stream looks identical to an
// abandoned session, and a heartbeat gap longer than the active grace would
// reap it mid-playback — after which progress, stop, and replan all fail with
// session-not-found while bytes are still flowing.
//
// This marks the session as remotely transported for its whole lifetime rather
// than per-request, because there is no request to bracket. Progress
// heartbeats still drive UI liveness; this only prevents reaping.
func (m *SessionManager) SetRemoteTransport(sessionID string, remote bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.remoteTransport = remote
	m.touchSessionLocked(s)
	return nil
}

// EndTransport decrements the count of in-flight media transport requests for
// the session and refreshes its activity timestamp.
func (m *SessionManager) EndTransport(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}

	if s.activeTransportCount > 0 {
		s.activeTransportCount--
	}
	m.touchSessionLocked(s)
	return nil
}

// WatchTransportStop returns a channel that is closed when the session is
// stopped, and the release the caller must run when its transport ends.
//
// A progressive remux is a single HTTP response whose ffmpeg is owned by the
// serving handler and canceled only by that request's context, so removing the
// session from this manager does not reach it: an unnegotiated client would go
// on consuming a stream the server has already disowned — for a copy-unsafe
// source, corrupt bytes its decoder cannot recover from. This is the smallest
// handle that lets a stop interrupt one. Segmented transports (HLS, transcode)
// do not need it: each of their requests is short, and the next one is refused
// once the session is gone.
//
// A session that is already gone yields an immediately-closed channel. The stop
// that removed it has run and will never run again, so a watcher registered
// after it would be closed by nobody: the caller's BeginTransport can succeed
// and the session be stopped before the registration lands, and the progressive
// remux that hole leaves behind runs to EOF serving bytes the server disowned.
// Reporting the stop it missed collapses that race into the ordinary path.
//
// The channel is closed at most once: StopSession takes the whole watcher set
// out of the map under the lock before closing it, and release drops a watcher
// that was never signaled.
func (m *SessionManager) WatchTransportStop(sessionID string) (<-chan struct{}, func()) {
	if m == nil || sessionID == "" {
		return nil, func() {}
	}

	stop := make(chan struct{})
	m.mu.Lock()
	if _, live := m.sessions[sessionID]; !live {
		m.mu.Unlock()
		close(stop)
		return stop, func() {}
	}
	if m.transportStops == nil {
		m.transportStops = make(map[string]map[chan struct{}]struct{})
	}
	watchers, ok := m.transportStops[sessionID]
	if !ok {
		watchers = make(map[chan struct{}]struct{})
		m.transportStops[sessionID] = watchers
	}
	watchers[stop] = struct{}{}
	m.mu.Unlock()

	var once sync.Once
	return stop, func() {
		once.Do(func() { m.releaseTransportStop(sessionID, stop) })
	}
}

func (m *SessionManager) releaseTransportStop(sessionID string, stop chan struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()

	watchers, ok := m.transportStops[sessionID]
	if !ok {
		return
	}
	delete(watchers, stop)
	if len(watchers) == 0 {
		delete(m.transportStops, sessionID)
	}
}

// stopTransportsLocked signals every transport registered for the session. The
// close is cheap and never blocks, and the watchers it wakes cancel an ffmpeg
// rather than calling back into the manager, so it is safe to do under the lock.
func (m *SessionManager) stopTransportsLocked(sessionID string) {
	watchers, ok := m.transportStops[sessionID]
	if !ok {
		return
	}
	delete(m.transportStops, sessionID)
	for stop := range watchers {
		close(stop)
	}
}

// StopSession removes a session from the manager and interrupts any media
// transport it is still serving.
func (m *SessionManager) StopSession(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.sessions[sessionID]; !ok {
		return ErrSessionNotFound
	}

	delete(m.sessions, sessionID)
	m.stopTransportsLocked(sessionID)
	return nil
}

// FinishSession is StopSession for a play that has ended, as opposed to a
// session replaced mid-play. The finish hooks run only when this call removed
// the session, so a retried stop, or one that loses to stale cleanup, runs
// them at most once per local copy.
func (m *SessionManager) FinishSession(ctx context.Context, sessionID string) error {
	m.mu.Lock()
	s, ok := m.sessions[sessionID]
	if !ok {
		m.mu.Unlock()
		return ErrSessionNotFound
	}
	finished := *s
	delete(m.sessions, sessionID)
	m.stopTransportsLocked(sessionID)
	hooks := append([]func(context.Context, *Session){}, m.finishHooks...)
	m.mu.Unlock()

	for _, hook := range hooks {
		hook(ctx, &finished)
	}
	return nil
}

// FinishDetached runs finish hooks for a snapshot this replica does not hold.
// It leaves live sessions untouched and marks the context for hook enrichment.
func (m *SessionManager) FinishDetached(ctx context.Context, s Session) {
	m.mu.RLock()
	hooks := append([]func(context.Context, *Session){}, m.finishHooks...)
	m.mu.RUnlock()
	ctx = context.WithValue(ctx, detachedFinishKey{}, true)
	for _, hook := range hooks {
		hook(ctx, &s)
	}
}

// PlaybackEndReason describes why a playback session ended.
// The empty value means no explicit reason was supplied.
type PlaybackEndReason string

// EndReasonReplaced is the end reason of a session another one replaced
// mid-play: a Jellyfin client switched play method.
const EndReasonReplaced PlaybackEndReason = "replaced"

type endReasonKey struct{}
type detachedFinishKey struct{}

// WithEndReason tells the finish hooks why the play ended. Without it a
// finished play ended because its client stopped it.
func WithEndReason(ctx context.Context, reason string) context.Context {
	return context.WithValue(ctx, endReasonKey{}, reason)
}

// EndReasonFromContext is the reason WithEndReason recorded, or "".
func EndReasonFromContext(ctx context.Context) string {
	reason, _ := ctx.Value(endReasonKey{}).(string)
	return reason
}

// IsDetachedFinish reports whether a finish hook runs for a FinishDetached
// snapshot rather than a copy this replica held.
func IsDetachedFinish(ctx context.Context) bool {
	detached, _ := ctx.Value(detachedFinishKey{}).(bool)
	return detached
}

func (m *SessionManager) GetSession(sessionID string) (*Session, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	// Return a copy to avoid races.
	cp := *s
	return &cp, nil
}

// GetSessionWithSourceGeneration returns the session copy and, from the same
// lock, the candidate-binding generation that copy carries. The generation is
// read under the lock with the copy so a reader cannot pair a copy with a
// generation that belongs to a different binding move: a caller that builds a
// payload from the copy and later compares the generation gets a pairing that
// was never torn.
func (m *SessionManager) GetSessionWithSourceGeneration(sessionID string) (*Session, uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return nil, 0, ErrSessionNotFound
	}

	cp := *s
	return &cp, cp.virtualSourceGeneration, nil
}

// GetUserSessions returns all active sessions for a user.
func (m *SessionManager) GetUserSessions(userID int) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var result []*Session
	for _, s := range m.sessions {
		if s.UserID == userID {
			cp := *s
			result = append(result, &cp)
		}
	}
	return result
}

// GetSessionsByMediaFileID returns active sessions associated with the given file.
func (m *SessionManager) GetSessionsByMediaFileID(fileID int) []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if fileID <= 0 {
		return nil
	}

	var result []*Session
	for _, s := range m.sessions {
		if s.MediaFileID != fileID && s.RequestedMediaFileID != fileID {
			continue
		}
		cp := *s
		result = append(result, &cp)
	}
	return result
}

// VirtualProbeTerminalSession describes one live session with an outstanding
// terminal-probe notification, for the dispatcher's overflow rescan: the
// session ID to re-park, the file whose inventory the push re-reads, and the
// binding generation the notification carries (so the ack after publish marks
// exactly that generation delivered).
type VirtualProbeTerminalSession struct {
	SessionID  string
	FileID     int
	Generation uint64
}

// VirtualProbeOutstandingNotifications returns up to limit live sessions whose
// terminal probe outcome has not yet been pushed — the binding generation that
// reached a terminal ("verified" or "failed") outcome is newer than the
// generation last acked delivered. It exists for the terminal-notification
// dispatcher's overflow rescan so the rescan converges: a session whose push
// already landed is acked (MarkVirtualProbeNotified) and never returned again.
// The read holds the session lock but stops at limit, so the snapshot is always
// bounded regardless of fleet size; a full page (len == limit) tells the caller
// more may remain. A session that ends after the snapshot is skipped downstream
// by the publish path, which re-reads the live session set.
func (m *SessionManager) VirtualProbeOutstandingNotifications(limit int) []VirtualProbeTerminalSession {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		return nil
	}
	var result []VirtualProbeTerminalSession
	for _, s := range m.sessions {
		if len(result) >= limit {
			break
		}
		if s.VirtualProbeOutcome != VirtualProbeOutcomeVerified && s.VirtualProbeOutcome != VirtualProbeOutcomeFailed {
			continue
		}
		// Outstanding means the terminal outcome's binding generation is newer
		// than the last delivered one. An equal-or-older generation was already
		// pushed (or never armed a notification) and must not be republished.
		if s.virtualSourceGeneration <= s.virtualProbeNotifiedGeneration {
			continue
		}
		fileID := s.MediaFileID
		if fileID <= 0 {
			fileID = s.RequestedMediaFileID
		}
		result = append(result, VirtualProbeTerminalSession{SessionID: s.ID, FileID: fileID, Generation: s.virtualSourceGeneration})
	}
	return result
}

// MarkVirtualProbeNotified records that the terminal probe outcome for the
// given binding generation was pushed to the client, advancing the session's
// delivered-generation watermark. It is forward-only: a generation older than
// the watermark (a stale worker's late ack) is a no-op, and a generation newer
// than the session's current binding (an ack for a binding that was superseded
// before its push landed) is refused, so an old probe's delivery never clears a
// newer binding's outstanding notification. It reports whether the watermark
// advanced. An unknown session is a no-op so a push that lands after teardown
// does not error.
func (m *SessionManager) MarkVirtualProbeNotified(sessionID string, generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false
	}
	if generation > s.virtualSourceGeneration || generation <= s.virtualProbeNotifiedGeneration {
		return false
	}
	s.virtualProbeNotifiedGeneration = generation
	return true
}

// VirtualProbeNotificationOutstanding reports whether a session's terminal
// probe outcome is still owed a push: the binding generation that reached a
// terminal outcome is newer than the generation last acked delivered. It is
// the per-session form of VirtualProbeOutstandingNotifications, used by the
// overflow rescan to re-validate a snapshot candidate at re-park time — the
// snapshot is taken outside the publish lock, so a session whose push landed
// and was acked between the snapshot and the re-park must not be re-parked
// (re-parking it would schedule a duplicate push).
func (m *SessionManager) VirtualProbeNotificationOutstanding(sessionID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return false
	}
	if s.VirtualProbeOutcome != VirtualProbeOutcomeVerified && s.VirtualProbeOutcome != VirtualProbeOutcomeFailed {
		return false
	}
	return s.virtualSourceGeneration > s.virtualProbeNotifiedGeneration
}

// ActiveCount returns the number of active sessions for a user.
func (m *SessionManager) ActiveCount(userID int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeCountLocked(userID)
}

// TranscodeCount returns the number of active transcode sessions for a user.
func (m *SessionManager) TranscodeCount(userID int) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.transcodeCountLocked(userID)
}

// activeCountLocked counts active sessions for a user. Caller must hold the lock.
func (m *SessionManager) activeCountLocked(userID int) int {
	return m.activeCountExcludingLocked(userID, "")
}

// activeCountExcludingLocked counts a user's limit-relevant sessions while
// ignoring one session entirely. Replacement admission uses this instead of
// subtracting one from the total: a failed session awaiting replan is often
// idle past the liveness grace and already absent from the count, so a blind
// decrement would free another session's slot.
func (m *SessionManager) activeCountExcludingLocked(userID int, excludeSessionID string) int {
	now := time.Now()
	count := 0
	for _, s := range m.sessions {
		if excludeSessionID != "" && s.ID == excludeSessionID {
			continue
		}
		if s.UserID == userID && m.countsTowardLimitsLocked(s, now) {
			count++
		}
	}
	return count
}

// transcodeCountLocked counts transcode sessions for a user. Caller must hold the lock.
func (m *SessionManager) transcodeCountLocked(userID int) int {
	return m.transcodeCountExcludingLocked(userID, "")
}

func (m *SessionManager) transcodeCountExcludingLocked(userID int, excludeSessionID string) int {
	now := time.Now()
	count := 0
	for _, s := range m.sessions {
		if excludeSessionID != "" && s.ID == excludeSessionID {
			continue
		}
		if s.UserID == userID && (s.PlayMethod == PlayTranscode || s.replacementPlayMethod == PlayTranscode) && m.countsTowardLimitsLocked(s, now) {
			count++
		}
	}
	return count
}

// AllSessions returns a snapshot of all active sessions. Each session is
// copied to avoid data races with concurrent updates.
func (m *SessionManager) AllSessions() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		cp := *s
		result = append(result, &cp)
	}
	return result
}

// CleanExpired removes sessions whose last playback activity exceeds maxIdle.
// Paused sessions receive a 3x grace period for backwards compatibility.
func (m *SessionManager) CleanExpired(maxIdle time.Duration) []*Session {
	return m.CleanInactive(maxIdle, maxIdle*3)
}

// CleanStale removes sessions that have exceeded the manager's configured
// liveness grace windows.
func (m *SessionManager) CleanStale() []*Session {
	m.mu.RLock()
	active := m.activeGrace
	paused := m.pausedGrace
	m.mu.RUnlock()
	return m.CleanInactive(active, paused)
}

// CleanInactive removes sessions whose last playback activity exceeds the
// provided grace period. Sessions with an active media transport request are
// preserved even if they have not emitted a recent heartbeat yet.
func (m *SessionManager) CleanInactive(activeIdle, pausedIdle time.Duration) []*Session {
	m.refreshDeliveryActivity(activeIdle, pausedIdle, 0)
	protected := m.refreshCompatActivity(activeIdle, pausedIdle, 0)
	m.mu.Lock()

	now := time.Now()
	claimed := m.claimCompatExpiryLocked(now, activeIdle, pausedIdle, 0, protected)
	var expired []*Session
	for id, s := range m.sessions {
		if protected[s] || s.IsJellyfinCompat && claimed != nil && !claimed[id] {
			continue
		}
		if s.activeTransportCount > 0 {
			continue
		}
		activeIdle, pausedIdle := remoteTransportGrace(s, activeIdle, pausedIdle)
		if m.sessionIsInactiveLocked(s, now, activeIdle, pausedIdle) {
			cp := *s
			expired = append(expired, &cp)
			delete(m.sessions, id)
		}
	}
	hooks := append([]func(*Session){}, m.expireHooks...)
	m.mu.Unlock()

	for _, hook := range hooks {
		for _, s := range expired {
			hook(s)
		}
	}
	return expired
}

func (m *SessionManager) touchSessionLocked(s *Session) {
	now := time.Now()
	s.LastActivityAt = now
	s.UpdatedAt = now
}

func (m *SessionManager) countsTowardLimitsLocked(s *Session, now time.Time) bool {
	if s == nil {
		return false
	}
	// Shared compatibility activity may be newer than this replica's snapshot.
	// Its retained slot is released by removal; admission must not infer that
	// release from a stale local timestamp before cleanup or an explicit stop.
	if s.IsJellyfinCompat && m.compatExpiryClaimer != nil {
		return true
	}
	if s.activeTransportCount > 0 {
		return true
	}
	activeGrace, pausedGrace := remoteTransportGrace(s, m.activeGrace, m.pausedGrace)
	return !m.sessionIsInactiveLocked(s, now, activeGrace, pausedGrace)
}

// remoteTransportGrace widens the idle windows for a session whose bytes are
// served by another node.
//
// A locally-served stream is held open by an in-flight transport request. A
// proxy-served one has no such request here, so it is protected by the
// client's progress heartbeats and by the serving node's delivery records (see
// SetDeliveryActivityReader) — and a gap in both longer than the active grace
// would reap it while media is still flowing. The windows are widened rather than
// made infinite: there is no absolute session lifetime cap in this manager, so
// unconditional immunity would leak a session forever whenever a client
// disappears without stopping. A client that has gone quiet for this long has
// genuinely stopped watching.
func remoteTransportGrace(s *Session, activeIdle, pausedIdle time.Duration) (time.Duration, time.Duration) {
	if s == nil || !s.remoteTransport {
		return activeIdle, pausedIdle
	}
	if activeIdle > 0 && activeIdle < remoteTransportIdleGrace {
		activeIdle = remoteTransportIdleGrace
	}
	if pausedIdle > 0 && pausedIdle < remoteTransportIdleGrace {
		pausedIdle = remoteTransportIdleGrace
	}
	return activeIdle, pausedIdle
}

func (m *SessionManager) sessionIsInactiveLocked(s *Session, now time.Time, activeIdle, pausedIdle time.Duration) bool {
	if s == nil {
		return true
	}

	lastActivity := s.LastActivityAt
	// A committed plan gets its own startup grace: clients may take longer than
	// the ordinary activity window to attach and emit their first sample.
	if !s.PlanCommittedAt.IsZero() && s.PlanCommittedAt.Add(StartupSessionGrace).After(now) {
		return false
	}
	if lastActivity.IsZero() {
		lastActivity = s.UpdatedAt
	}
	if lastActivity.IsZero() {
		lastActivity = s.StartedAt
	}
	if lastActivity.IsZero() {
		return false
	}

	grace := activeIdle
	if s.IsPaused {
		grace = pausedIdle
	}
	if grace <= 0 {
		return !lastActivity.After(now)
	}

	return !lastActivity.Add(grace).After(now)
}

// String returns a human-readable summary of a session.
func (s *Session) String() string {
	return fmt.Sprintf("Session{id=%s user=%d file=%d method=%s pos=%.1f paused=%v}",
		s.ID, s.UserID, s.MediaFileID, s.PlayMethod, s.Position, s.IsPaused)
}

// UpdateProgressReported advances the running listening total an ABS client
// reported, crediting the delta to the session's watch time.
func (m *SessionManager) UpdateProgressReported(sessionID string, position, total float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[sessionID]
	if !ok {
		return ErrSessionNotFound
	}
	s.Position = position
	s.IsPaused = false
	s.StopReported = false
	s.streamRevision++
	m.touchSessionLocked(s)
	switch {
	case total <= 0 && s.ReportedListening <= 0:
		s.Watch.Observe(position, false, s.UpdatedAt)
	case total > s.ReportedListening:
		listened := total - s.ReportedListening
		s.ReportedListening = total
		s.Watch.Credit(position, listened, s.StartedAt, s.UpdatedAt)
	}
	return nil
}
