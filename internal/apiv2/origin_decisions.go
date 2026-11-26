package apiv2

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/playback"
)

// What each operation lets a login session do, by how the session was
// opened. The person's own sessions (a password, a sign-in provider, their
// own device key) are governed by the class and the handler alone. A session
// another app's key opened (auth.SessionKindGrantedApp) is judged apart: the
// app's access ends when its key is revoked, so it may not leave anything
// behind that outlives the key, read or change the person's other devices,
// or change the account. Every operation a non-administrator can reach
// states its decision for such a session (docs/architecture/app-keys.md,
// "Grants"); TestEveryOperationDecidesForGrantedApps holds that.

// Verdict is one operation's decision for a kind of session.
type Verdict string

const (
	// VerdictAllowed: any session of the kind may call the operation; the
	// reason says why an app signed in that way needs it.
	VerdictAllowed Verdict = "allowed"
	// VerdictOwnOnly: the session may act only on its own things. A gate
	// cannot tell what is the session's own, so the handler's check named
	// by Check enforces it.
	VerdictOwnOnly Verdict = "own_only"
	// VerdictRefused: the gate answers 403 permission_denied before the
	// handler or any service runs, reads included.
	VerdictRefused Verdict = "refused"
)

// SessionDecision is one operation's decision for one kind of session.
type SessionDecision struct {
	Verdict Verdict `json:"verdict"`
	// Reason is required on every verdict.
	Reason string `json:"reason"`
	// Check, on VerdictOwnOnly only, names the handler check that enforces
	// "its own" and what "own" means there.
	Check string `json:"check,omitempty"`
}

// OriginDecisions holds an operation's decision for each kind of session
// judged apart, one member per auth.SessionKind. A kind is added by adding
// its member and its row in originMembers, and the session lookup that sets
// it on the claims. The gate, the registration check and the published
// extension read every kind through originMembers and do not change; what a
// kind does beyond its verdict (the granted app's declared-device strip,
// stripsDeclaredDevice) is per kind.
type OriginDecisions struct {
	// GrantedApp is the decision for a session another app's key opened.
	GrantedApp *SessionDecision `json:"granted_app,omitempty"`
}

// originMember is one judged kind of session and how to read its decision.
type originMember struct {
	kind auth.SessionKind
	of   func(OriginDecisions) *SessionDecision
}

// originMembers lists every judged kind, one row per OriginDecisions member.
var originMembers = []originMember{
	{auth.SessionKindGrantedApp, func(d OriginDecisions) *SessionDecision { return d.GrantedApp }},
}

// decisionFor is the decision for a kind of session; nil for a kind no
// member judges (the person's own sessions).
func (d OriginDecisions) decisionFor(kind auth.SessionKind) *SessionDecision {
	for _, m := range originMembers {
		if m.kind == kind {
			return m.of(d)
		}
	}
	return nil
}

// byKind is every decision the operation states, keyed by kind: what the
// document publishes and the registration check validates.
func (d OriginDecisions) byKind() map[auth.SessionKind]*SessionDecision {
	out := map[auth.SessionKind]*SessionDecision{}
	for _, m := range originMembers {
		if dec := m.of(d); dec != nil {
			out[m.kind] = dec
		}
	}
	return out
}

func (d OriginDecisions) empty() bool { return len(d.byKind()) == 0 }

// mayRefuse reports whether the gate or a handler can answer some judged
// kind of session 403: a refusal, or a limit to its own things. The
// document then declares 403 for the operation.
func (d OriginDecisions) mayRefuse() bool {
	for _, dec := range d.byKind() {
		if dec.Verdict == VerdictRefused || dec.Verdict == VerdictOwnOnly {
			return true
		}
	}
	return false
}

// decideAllowed, decideOwnOnly and decideRefused build the three verdicts.
func decideAllowed(reason string) *SessionDecision {
	return &SessionDecision{Verdict: VerdictAllowed, Reason: reason}
}

func decideOwnOnly(check, reason string) *SessionDecision {
	return &SessionDecision{Verdict: VerdictOwnOnly, Reason: reason, Check: check}
}

func decideRefused(reason string) *SessionDecision {
	return &SessionDecision{Verdict: VerdictRefused, Reason: reason}
}

// grantedApp is the decision set for an operation only a granted app's
// session is judged apart on, which is every operation today.
func grantedApp(d *SessionDecision) OriginDecisions { return OriginDecisions{GrantedApp: d} }

// Reasons shared by the families of operations a session another app's key
// opened may use (docs/architecture/app-keys.md, "Grants", what a granted
// app can do).
const (
	reasonWhoItIs        = "who it is and which profile it acts as"
	reasonCurrentUser    = "who it is, built by accountForSession: for such a session the email is empty, download_allowed false and permissions only those with an operation open to it"
	reasonVerifyPIN      = "acting as a PIN-protected profile; its attempts count under its own allowance per profile and app key (GrantedAppPINPolicy) and never spend the household's"
	reasonShuffle        = "playing a shuffle of the profile's; a shuffle's id is random and not listed"
	reasonPersonRead     = "reading the catalog; reading a person can queue that person's metadata refresh when one is due"
	reasonRoomJoin       = "joining a room by its code, as a member"
	reasonCapabilityRead = "a capability read: what the server offers this caller"
	reasonCatalogRead    = "reading the catalog"
	reasonHomeRead       = "reading home"
	reasonLibraryRead    = "reading libraries"
	reasonCollectionRead = "reading collections"
	reasonRecommendRead  = "reading recommendations"
	reasonSubtitleRead   = "reading subtitles"
	reasonSettingsRead   = "reading settings and preferences"
	reasonPlayback       = "starting playback and playing its own streams"
	reasonProfileState   = "the profile's own state, which the person allowed the app to keep"
	reasonInboxState     = "the profile's own inbox: reading it and marking it read"
	reasonEventsSocket   = "the events socket carries the profile's own changes"
	reasonRoomMember     = "as a member of a room the person put it in"
	reasonOwnSession     = "its own session and its own key"
	reasonPlayerState    = "a player's own state for the profile it acts as"
	reasonWatchlistTitle = "the profile's own watchlist; for such a session no media request is made, followed or withdrawn"
)

// Reasons for the operations such a session is refused.
const (
	reasonRefusedAccount      = "changes the account"
	reasonRefusedSignIn       = "changes how the account signs in, or reads or hands out its other credentials"
	reasonRefusedOtherDevice  = "reaches the person's other devices"
	reasonRefusedLeaves       = "leaves something behind that outlives the app's key"
	reasonRefusedDeviceState  = "refused by decision: device-keyed state (the device list, device settings, the connection record, downloads) stays with the person's own apps"
	reasonRefusedHousehold    = "changes the household's profiles, PINs or limits"
	reasonRefusedRoomHost     = "acts as a room's host or changes what other members play"
	reasonRefusedRequests     = "refused for now: making media requests"
	reasonRefusedSubtitleEdit = "refused for now: editing subtitles every account sees"
	reasonRefusedImport       = "refused for now: importing collections, which keep syncing"
	reasonRefusedRefresh      = "refused for now: refreshing the shared catalog"
	reasonRefusedNotifyPrefs  = "refused for now: changing notification preferences"
	reasonRefusedCatalogEdit  = "refused for now: catalog edits under a delegated permission"

	reasonRefusedOtherDevicesShow = "refused by decision: the profile's own, but it changes what the person's other devices show"
	reasonRefusedOnboarding       = "refused by decision: onboarding is set up from the person's own apps"
	reasonRefusedSeriesMute       = "refused by decision: muting a series changes what the person is told on every device"
	reasonRefusedRequestFlow      = "refused by decision: media requests, and finding titles to request, stay with the person's own apps"
	reasonRefusedExternalReads    = "refused by decision: reads the person's links to external accounts"
	reasonRefusedProfileExport    = "refused by decision: exports the profile's whole data at once"
	reasonRefusedJobReads         = "refused by decision: library and administration jobs are not a player's"
	reasonRefusedAccountSetup     = "refused by decision: the account's sign-in, credential and plugin setup is not a player's"
	reasonRefusedSharedWrite      = "refused by decision: writes shared catalog data"
	reasonRefusedSnapshotSlots    = "refused by decision: the account has two progress snapshot slots, which the app could hold"
	reasonRefusedDiagnosticsQuota = "refused by decision: a diagnostics upload spends a per-account quota"
	reasonRefusedProviderSearch   = "refused by decision: a provider search spends the server's allowance with its subtitle providers"
	reasonRefusedChannelReads     = "refused by decision: reads the person's email address or Discord link"
	reasonRefusedProfileSet       = "refused: the profile set is read and sent only by a session that may manage the account's keys"
	reasonRefusedSettingByKey     = "refused by decision: some setting keys and scopes reach beyond the profile, and none has been read for that yet"
	reasonRefusedPluginSettings   = "refused by decision: a plugin's per-user values may hold tokens for other services, and a plugin acting on what an app wrote is the plugin proxy's problem again"
	reasonRefusedOtherProfiles    = "refused by decision: it is about the account's other profiles"
	reasonRefusedTasteSeed        = "refused by decision: it records up to 200 picks as the profile's favorites at once and, when any is new, refreshes the profile's taste in one of the few immediate refreshes each API process runs at a time, or queues the refresh when none is free; such a session adds favorites one at a time instead"
	reasonRefusedImpersonationEnd = "refused by decision: such a session is never a View as user session"
)

// Decisions shared by whole families: what a session another app's key
// opened may do (allowed), its own things only (the handler check named),
// and what it is refused.
var (
	grantedWhoItIs        = grantedApp(decideAllowed(reasonWhoItIs))
	grantedCapability     = grantedApp(decideAllowed(reasonCapabilityRead))
	grantedCatalogRead    = grantedApp(decideAllowed(reasonCatalogRead))
	grantedHomeRead       = grantedApp(decideAllowed(reasonHomeRead))
	grantedLibraryRead    = grantedApp(decideAllowed(reasonLibraryRead))
	grantedCollectionRead = grantedApp(decideAllowed(reasonCollectionRead))
	grantedRecommendRead  = grantedApp(decideAllowed(reasonRecommendRead))
	grantedSubtitleRead   = grantedApp(decideAllowed(reasonSubtitleRead))
	grantedSettingsRead   = grantedApp(decideAllowed(reasonSettingsRead))
	grantedPlayback       = grantedApp(decideAllowed(reasonPlayback))
	grantedProfileState   = grantedApp(decideAllowed(reasonProfileState))
	grantedInbox          = grantedApp(decideAllowed(reasonInboxState))
	grantedEvents         = grantedApp(decideAllowed(reasonEventsSocket))
	grantedRoomMember     = grantedApp(decideAllowed(reasonRoomMember))
	grantedOwnSession     = grantedApp(decideAllowed(reasonOwnSession))
	grantedPlayerState    = grantedApp(decideAllowed(reasonPlayerState))
	grantedWatchlistTitle = grantedApp(decideAllowed(reasonWatchlistTitle))
	grantedCurrentUser    = grantedApp(decideAllowed(reasonCurrentUser))
	grantedVerifyPIN      = grantedApp(decideAllowed(reasonVerifyPIN))
	grantedShuffle        = grantedApp(decideAllowed(reasonShuffle))
	grantedPersonRead     = grantedApp(decideAllowed(reasonPersonRead))
	grantedRoomJoin       = grantedApp(decideAllowed(reasonRoomJoin))

	grantedOwnSessionList = grantedApp(decideOwnOnly(
		"SessionLimitedToItself: every other session's item is redacted to its id", reasonOwnSession))
	grantedOwnSessionEnd = grantedApp(decideOwnOnly(
		"SessionLimitedToItself in deleteSession: only the login session making the request", reasonOwnSession))
	grantedOwnKeyRevoke = grantedApp(decideOwnOnly(
		"RevokeOwnDeviceKey: only the device key that opened the session", reasonOwnSession))
	grantedOwnStreamsList = grantedApp(decideOwnOnly(
		"sessionReachesOtherDevices in listHouseholdSessions: only the streams its own login session started", reasonPlayback))
	grantedOwnStream = grantedApp(decideOwnOnly(
		"mayActOnAttempt / refuseOtherStream: only a stream its own login session started", reasonPlayback))
	grantedOwnStreamMedia = grantedApp(decideOwnOnly(
		"grantedAppStreamError on the media routes: only the media of a stream its own login session started", reasonPlayback))
	grantedOwnRouteEvent = grantedApp(decideOwnOnly(
		"mayActOnAttempt in ReportRouteEventV2: only an attempt its own login session started", reasonPlayback))
	grantedOwnStreamTicket = grantedApp(decideOwnOnly(
		"refuseOtherStream on the control ticket: only a stream its own login session started", reasonPlayback))
	grantedOwnSuggestion = grantedApp(decideOwnOnly(
		"DeleteOwnSuggestion: only suggestions of the account and profile it acts as, which includes those the person's own devices made as that profile (a suggester is stored as account and profile, not as the session)", reasonRoomMember))
	grantedOwnSeat = grantedApp(decideOwnOnly(
		"ConnectOtherApp: only a member's seat no other connection holds, never the host's controls", reasonRoomMember))

	grantedRefusedAccount      = grantedApp(decideRefused(reasonRefusedAccount))
	grantedRefusedSignIn       = grantedApp(decideRefused(reasonRefusedSignIn))
	grantedRefusedOtherDevice  = grantedApp(decideRefused(reasonRefusedOtherDevice))
	grantedRefusedLeaves       = grantedApp(decideRefused(reasonRefusedLeaves))
	grantedRefusedDeviceState  = grantedApp(decideRefused(reasonRefusedDeviceState))
	grantedRefusedHousehold    = grantedApp(decideRefused(reasonRefusedHousehold))
	grantedRefusedRoomHost     = grantedApp(decideRefused(reasonRefusedRoomHost))
	grantedRefusedRequests     = grantedApp(decideRefused(reasonRefusedRequests))
	grantedRefusedSubtitleEdit = grantedApp(decideRefused(reasonRefusedSubtitleEdit))
	grantedRefusedImport       = grantedApp(decideRefused(reasonRefusedImport))
	grantedRefusedRefresh      = grantedApp(decideRefused(reasonRefusedRefresh))
	grantedRefusedNotifyPrefs  = grantedApp(decideRefused(reasonRefusedNotifyPrefs))
	grantedRefusedCatalogEdit  = grantedApp(decideRefused(reasonRefusedCatalogEdit))

	grantedRefusedOtherDevicesShow = grantedApp(decideRefused(reasonRefusedOtherDevicesShow))
	grantedRefusedOnboarding       = grantedApp(decideRefused(reasonRefusedOnboarding))
	grantedRefusedSeriesMute       = grantedApp(decideRefused(reasonRefusedSeriesMute))
	grantedRefusedRequestFlow      = grantedApp(decideRefused(reasonRefusedRequestFlow))
	grantedRefusedExternalReads    = grantedApp(decideRefused(reasonRefusedExternalReads))
	grantedRefusedProfileExport    = grantedApp(decideRefused(reasonRefusedProfileExport))
	grantedRefusedJobReads         = grantedApp(decideRefused(reasonRefusedJobReads))
	grantedRefusedAccountSetup     = grantedApp(decideRefused(reasonRefusedAccountSetup))
	grantedRefusedSharedWrite      = grantedApp(decideRefused(reasonRefusedSharedWrite))
	grantedRefusedSnapshotSlots    = grantedApp(decideRefused(reasonRefusedSnapshotSlots))
	grantedRefusedDiagnosticsQuota = grantedApp(decideRefused(reasonRefusedDiagnosticsQuota))
	grantedRefusedProviderSearch   = grantedApp(decideRefused(reasonRefusedProviderSearch))
	grantedRefusedChannelReads     = grantedApp(decideRefused(reasonRefusedChannelReads))
	grantedRefusedProfileSet       = grantedApp(decideRefused(reasonRefusedProfileSet))
	grantedRefusedSettingByKey     = grantedApp(decideRefused(reasonRefusedSettingByKey))
	grantedRefusedPluginSettings   = grantedApp(decideRefused(reasonRefusedPluginSettings))
	grantedRefusedOtherProfiles    = grantedApp(decideRefused(reasonRefusedOtherProfiles))
	grantedRefusedTasteSeed        = grantedApp(decideRefused(reasonRefusedTasteSeed))
	grantedRefusedImpersonationEnd = grantedApp(decideRefused(reasonRefusedImpersonationEnd))
)

// extOriginDecisions documents OriginDecisions as {kind: {verdict, reason,
// check?}} so another party can check a table against the server's.
const extOriginDecisions = "x-silo-origin-decisions"

// metaOriginDecisions carries OriginDecisions to the gate.
const metaOriginDecisions = "silo.origin_decisions"

// checkOriginDecisions refuses a decision without its reason, an own-only
// decision without its check, a check on any other verdict, an unknown
// verdict, and a decision on a class no gate runs for or no such session
// reaches: public (no gate) and acting admin (a grant is never redeemed on an
// administrator's account, and promotion revokes its keys).
func checkOriginDecisions(op Operation) error {
	if op.Origins.empty() {
		return nil
	}
	if op.Class == ClassPublic || op.Class == ClassActingAdmin {
		return errors.New("an origin decision is inert on class " + string(op.Class) + ": no session another app opened reaches it through a gate that reads it")
	}
	for kind, d := range op.Origins.byKind() {
		switch d.Verdict {
		case VerdictAllowed, VerdictRefused:
			if d.Check != "" {
				return errors.New("origin decision for " + string(kind) + ": only an own-only verdict names a check")
			}
		case VerdictOwnOnly:
			if strings.TrimSpace(d.Check) == "" {
				return errors.New("origin decision for " + string(kind) + ": an own-only verdict names the handler check that enforces it")
			}
		default:
			return errors.New("origin decision for " + string(kind) + ": unknown verdict " + string(d.Verdict))
		}
		if strings.TrimSpace(d.Reason) == "" {
			return errors.New("origin decision for " + string(kind) + ": a verdict needs its reason")
		}
	}
	return nil
}

// recordOriginDecisions copies the decisions onto the operation's metadata
// for the gate and its extensions for the document.
func recordOriginDecisions(op *Operation) {
	op.Metadata[metaOriginDecisions] = op.Origins
	if op.Origins.empty() {
		return
	}
	if op.Extensions == nil {
		op.Extensions = map[string]any{}
	}
	op.Extensions[extOriginDecisions] = op.Origins.byKind()
}

// originGate refuses a session whose kind the operation's decisions refuse,
// before anything else about the request is judged. It reads the kind the
// authentication gate put on the claims, so it costs no query. An operation
// that states no decision for the kind refuses it: every operation a
// non-administrator reaches states one, and one that does not has not been
// decided. Every caller of no judged kind passes, with only the client name
// reserved for another app's session dropped (nameDeclaredClient).
func originGate(decisions OriginDecisions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := claimsFrom(r.Context())
			kind := judgedKind(claims)
			nameDeclaredClient(r.Header, stripsDeclaredDevice(kind))
			if kind == "" {
				next.ServeHTTP(w, r)
				return
			}
			if stripsDeclaredDevice(kind) {
				stripDeclaredDevice(r.Header)
			}
			if d := decisions.decisionFor(kind); d != nil && d.Verdict != VerdictRefused {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Content-Type", mediaTypeJSON)
			w.WriteHeader(http.StatusForbidden)
			body, _ := json.Marshal(map[string]string{"error": legacyForbiddenCode, "message": handlers.SessionNotAllowedDetail})
			_, _ = w.Write(body)
		})
	}
}

// judgedKind is the kind of session the claims' operations judge apart, or
// "" for every caller that passes the gate untouched: only the account's own
// login sessions carry a kind (Claims.IsOwnLoginSession), so an API key, an
// administrator acting as the account, a remote-playback session and a
// stream reference are never judged.
func judgedKind(claims *auth.Claims) auth.SessionKind {
	if claims == nil || !claims.IsOwnLoginSession() {
		return ""
	}
	return claims.SessionKind
}

// stripsDeclaredDevice reports whether a kind of session has its declared
// device removed before anything reads it.
func stripsDeclaredDevice(kind auth.SessionKind) bool {
	return kind == auth.SessionKindGrantedApp
}

// declaredDeviceHeaders are the headers a client names its device with.
// X-Device-ID is the v2 playback device the start digests and resolves
// device-scoped settings under; it is a device the caller names too.
var declaredDeviceHeaders = []string{auth.ClientDeviceIDHeader, auth.ClientDeviceNameHeader, auth.ClientDevicePlatformHeader, "X-Device-ID"}

// stripDeclaredDevice removes the device a session another app's key opened
// declares, before any later gate, input binding or handler reads it. The
// server cannot tell that app's device from another of the person's, so the
// device registry, the connection record and the device-scoped settings would
// otherwise be written or read for whichever device the app names. Every
// device-keyed read and write then takes the "no declared device" path. The
// request's header map is shared by every request value the chain derives
// from it (WithContext copies the pointer), and nothing before this gate
// clones it on a login session's path, so no later reader sees the value.
func stripDeclaredDevice(h http.Header) {
	for _, name := range declaredDeviceHeaders {
		h.Del(name)
	}
}

// clientNameHeaders are the headers a client names its app with: the declared
// v2 header, which wins when it is not blank (PlaybackRequestHeaders), and the
// one the first-party apps send, which the media routes, stream telemetry and
// the request log read.
var clientNameHeaders = []string{"X-Client-Name", "X-Silo-Client"}

// clientVersionHeaders are the version, build and channel paired with either
// name header, each with the shape a session another app's key opened must
// keep: the label beside a client name shows the version, and the full label
// adds the build and channel, so for such a session one that does not look
// like a version, or a channel not on the known list, is dropped.
var clientVersionHeaders = map[string]func(string) bool{
	"X-Client-Version": playback.LooksLikeClientVersion, "X-Client-Build": playback.LooksLikeClientVersion, "X-Client-Channel": playback.IsKnownClientChannel,
	"X-Silo-Client-Version": playback.LooksLikeClientVersion, "X-Silo-Client-Build": playback.LooksLikeClientVersion, "X-Silo-Client-Channel": playback.IsKnownClientChannel,
}

// nameDeclaredClient sets the client name a session another app's key opened
// is recorded under: the app could otherwise name its plays after the
// person's own apps in the household streams, playback history and insights.
// Both headers carry playback.GrantedAppClientName, the declared one only when
// the app sent it, so the same header family supplies the rest of the client
// identity (version, build, channel). A version or build stays as declared
// when it looks like one, and a channel when it is on the known list; any
// other value is dropped, so it cannot carry a client name into the label. Every other caller has a name dropped from either
// header when it would be stored as one reading as the reserved name
// (playback.IsGrantedAppClientName), so the request proceeds as if it named
// no client there and nothing else is recorded under it. Like the device strip, this edits the request's shared header map
// before anything after the gate reads it.
func nameDeclaredClient(h http.Header, grantedApp bool) {
	if grantedApp {
		if strings.TrimSpace(h.Get(clientNameHeaders[0])) != "" {
			h.Set(clientNameHeaders[0], playback.GrantedAppClientName)
		} else {
			h.Del(clientNameHeaders[0])
		}
		h.Set(clientNameHeaders[1], playback.GrantedAppClientName)
		for name, keeps := range clientVersionHeaders {
			for _, v := range h.Values(name) {
				if !keeps(strings.TrimSpace(v)) {
					h.Del(name)
					break
				}
			}
		}
		return
	}
	for _, name := range clientNameHeaders {
		for _, v := range h.Values(name) {
			if playback.IsGrantedAppClientName(v) {
				h.Del(name)
				break
			}
		}
	}
}

// grantedAppNamedTargetProblem refuses a session another app's key opened
// that names another profile or another device on a settings read. The
// answer is the same whether the id exists, so the refusal reveals nothing
// about the account's profiles or devices.
func grantedAppNamedTargetProblem(ctx context.Context, profileID ID, deviceID string) *Problem {
	if !grantedAppSession(claimsFrom(ctx)) || (profileID == "" && deviceID == "") {
		return nil
	}
	return NewProblem(TypePermissionDenied, handlers.SessionNotAllowedDetail)
}

// grantedAppSession reports whether the caller is a session another app's
// key opened, from the claims the authentication gate set: the one fact the
// handler checks below need, without a query of their own.
func grantedAppSession(claims *auth.Claims) bool { return claims.IsGrantedApp() }

// withOrigins sets an operation's origin decisions where a helper builds the
// operation, so the mark stays at the call that registers it.
func withOrigins(op Operation, d OriginDecisions) Operation {
	op.Origins = d
	return op
}

// withRawOrigins is withOrigins for a raw operation.
func withRawOrigins(raw RawOperation, d OriginDecisions) RawOperation {
	raw.Origins = d
	return raw
}

// recordOrigins keeps each operation's decisions for the capability reads
// that describe it.
func (reg *Registry) recordOrigins(operationID string, d OriginDecisions) {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.origins == nil {
		reg.origins = map[string]OriginDecisions{}
	}
	reg.origins[operationID] = d
}

// recordPermission keeps which operations a policy permission gates, so a
// read can tell a session of a permission only while one of them is open to
// it (permittedPermissions).
func (reg *Registry) recordPermission(permission, operationID string) {
	if permission == "" {
		return
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.permissionOps == nil {
		reg.permissionOps = map[string][]string{}
	}
	reg.permissionOps[permission] = append(reg.permissionOps[permission], operationID)
}

// permittedPermissions is the part of an account's permissions a kind of
// session may be told of: a read never tells a session it may do what every
// operation a permission gates refuses it. A permission whose operations are
// all refused to that kind is left out, and so is one that gates no
// operation at all (a power checked only by hand); one with an open operation
// stays, so opening a permission's actions to such a session needs no change
// here.
func (reg *Registry) permittedPermissions(kind auth.SessionKind, permissions []Permission) []Permission {
	if kind == "" {
		return permissions
	}
	out := make([]Permission, 0, len(permissions))
	for _, permission := range permissions {
		reg.mu.Lock()
		governed := slices.Clone(reg.permissionOps[string(permission)])
		reg.mu.Unlock()
		if len(governed) > 0 && reg.permittedFor(kind, governed) {
			out = append(out, permission)
		}
	}
	return out
}

// capabilityPermitted is the rule every capability read applies to its
// permission answer: a capability never reports as allowed an action whose
// operations are all refused to the session asking. It reads, for the
// caller's kind of session (judgedKind), the decision of each operation the
// capability names in CapabilityFor; a caller of no judged kind, and a
// capability that names none, is left to its own answer. Today one kind is
// judged; another kind is judged here with no new code. The decisions are
// read at request time, so a capability may name an operation registered
// after it.
func (reg *Registry) capabilityPermitted(governed []string) func(context.Context) bool {
	if len(governed) == 0 {
		return nil
	}
	return func(ctx context.Context) bool {
		return reg.permittedFor(judgedKind(claimsFrom(ctx)), governed)
	}
}

// permittedFor reports whether a kind of session may use at least one of the
// governed operations; every operation is open to an unjudged session ("").
func (reg *Registry) permittedFor(kind auth.SessionKind, governed []string) bool {
	if kind == "" {
		return true
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	for _, id := range governed {
		if d := reg.origins[id].decisionFor(kind); d != nil && d.Verdict != VerdictRefused {
			return true
		}
	}
	return false
}

// capabilityMember is one permission member of a response a judged kind of
// session may read: a boolean named allowed, ending in _allowed or starting
// can_ or self_can_, and any other member a row names because it says what
// the caller may do under another name (a flag such as imports, or a list of
// the sources it may import from). Either it governs (names the operations
// whose action it answers for, and is false, or an empty list, for a session
// to which every one of them is refused), or it is exempt with a reason.
// TestPermissionMembersStateTheirCase fails on a member with neither.
type capabilityMember struct {
	governs []string
	exempt  string
}

// watchTogetherHostOperations are what managing a room means; transport
// control is the socket's play, pause and seek for the room, whose operations
// are the room's start and stop.
var (
	watchTogetherHostOperations      = []string{"closeWatchTogetherRoom", "updateWatchTogetherRoomPolicy", "selectWatchTogetherRoomItem", "updateWatchTogetherRoomSelectionMode", "stageWatchTogetherRoomItem", "promoteWatchTogetherSuggestion"}
	watchTogetherTransportOperations = []string{"startWatchTogetherRoomPlayback", "stopWatchTogetherRoomPlayback"}
)

// collectionImportOperations are what importing a collection means.
var collectionImportOperations = []string{"importMDBListCollection", "importTMDBCollection", "importTMDBListCollection"}

// downloadOperations are the actions a download permission answers for.
var downloadOperations = []string{"createDownloads", "listDownloads", "createDirectDownloadLink", "getDirectDownload", "createDownloadSubscription"}

// capabilityMembers is the table of permission members, keyed by operation
// and member path ("getX allowed", "listY items[].z_allowed"). A capability
// read's top-level allowed is applied by the shared capability adapter
// (Operation.CapabilityFor); a member deeper in a response by its handler
// (memberPermitted).
var capabilityMembers = map[string]capabilityMember{
	"getAccountPasswordCapability allowed":                 {governs: []string{"changePassword"}},
	"getApplePushRegistrationCapabilities allowed":         {governs: []string{"registerApplePushDevice", "resetApplePushInstallation"}},
	"getPushRegistrationCapabilities allowed":              {governs: []string{"registerPushDevice", "unregisterPushDevice"}},
	"getDiagnosticsCapabilities allowed":                   {governs: []string{"uploadDiagnosticsReport", "createDiagnosticsUpload", "putDiagnosticsUploadChunk", "completeDiagnosticsUpload", "abortDiagnosticsUpload"}},
	"getDownloadCapability allowed":                        {governs: downloadOperations},
	"getDownloadCapability download_allowed":               {governs: downloadOperations},
	"getDownloadCapability transcode_user_allowed":         {governs: downloadOperations},
	"getCurrentUser download_allowed":                      {governs: downloadOperations},
	"getHistoryImportCapability allowed":                   {governs: []string{"createHistoryImportRun", "createPlexPin", "loginEmbyConnect", "listHistoryImportRuns", "listHistoryImportSources"}},
	"getNotificationEmailVerificationCapabilities allowed": {governs: []string{"requestNotificationEmailVerification"}},
	"getOnboardingCapabilities allowed":                    {governs: []string{"getOnboardingFlow", "getOnboardingState", "updateOnboardingProgress"}},
	"getProfileExportCapability allowed":                   {governs: []string{"exportProfile", "importProfile"}},
	opProgressBootstrapCapabilities + " allowed":           {governs: []string{opCreateProgressSnapshot, opGetProgressSnapshot}},
	"getTitleAllowanceCapability allowed":                  {governs: []string{"allowTitle", "disallowTitle", "allowCollectionTitles", "listTitleAllowanceProfiles", "listAllowedTitles"}},
	"getTrailersCapability allowed":                        {governs: []string{"refreshCatalogItemTrailers"}},
	"getMetadataAICapability allowed":                      {governs: []string{"translateCatalogItemDescription"}},
	"getSubtitleSyncStatus allowed":                        {governs: []string{"startSubtitleSync", "syncStoredSubtitle", "setSubtitleTiming", "setStoredSubtitleTiming"}},
	"getSubtitleAIStatus allowed":                          {governs: []string{"createSubtitleAIJob", "cancelSubtitleAIJob"}},
	"getSubtitleProviderStatus allowed":                    {governs: []string{"searchSubtitles", "downloadSubtitle"}},
	"listProfiles items[].requests_allowed":                {governs: []string{"createRequest", "followRequestMedia", "searchRequestMedia"}},
	// A room member is the account and profile, so these describe the
	// profile's seat; the room read and join name them only for a caller
	// that may use them (WithoutSessionPowers).
	"getWatchTogetherRoom room.self_can_manage_room":          {governs: watchTogetherHostOperations},
	"joinWatchTogetherRoom room.self_can_manage_room":         {governs: watchTogetherHostOperations},
	"getWatchTogetherRoom room.self_can_control_transport":    {governs: watchTogetherTransportOperations},
	"joinWatchTogetherRoom room.self_can_control_transport":   {governs: watchTogetherTransportOperations},
	"startPlayback playback_plan.timeline.can_seek_anywhere":  {exempt: "describes the stream's timeline, not a permission: believing it, the app seeks within its own stream, which it may"},
	"replanPlayback playback_plan.timeline.can_seek_anywhere": {exempt: "describes the stream's timeline, not a permission: believing it, the app seeks within its own stream, which it may"},
	// Capabilities some of whose actions are open to such a session; the
	// answer stays the capability's own. A finer member that says what the
	// caller may do has its own row below.
	"getCatalogSearchCapabilities allowed":         {governs: []string{"queryCatalogItems", "listCatalogItems"}},
	"getCollectionCapabilities allowed":            {governs: []string{"listCollections", "getCollection", "createCollection"}},
	"getEbookCapability allowed":                   {governs: []string{"readEbookFile", "saveEbookProgress", "createEbookAnnotation"}},
	"getEventsCapabilities allowed":                {governs: []string{"createEventsSocketTicket"}},
	"getHomeCapabilities allowed":                  {governs: []string{"listHomeSections", "getHomeLayout"}},
	"getLoginSessionCapabilities allowed":          {governs: []string{"listSessions", "deleteSession"}},
	"getNotInterestedCapability allowed":           {governs: []string{"markNotInterested", "listNotInterested"}},
	"getNotificationCapabilities allowed":          {governs: []string{"listNotifications", "markNotificationRead"}},
	"getPersonalListCapability allowed":            {governs: []string{"addFavorite", "addToWatchlist"}},
	"getPlaybackCapabilities allowed":              {governs: []string{"startPlayback"}},
	"getPlaybackControlSocketCapabilities allowed": {governs: []string{"createPlaybackControlSocketTicket"}},
	"getProgressCapabilities allowed":              {governs: []string{"syncProgress", "listProgress"}},
	"getRatingsCapability allowed":                 {governs: []string{"setRating"}},
	"getSettingsContractCapabilities allowed":      {governs: []string{"getSettingsContract"}},
	"getShuffleCapability allowed":                 {governs: []string{"createShuffle"}},
	"getThemeSongsCapability allowed":              {governs: []string{"createThemeSongPlayback"}},
	"getUserLibraryCapabilities allowed":           {governs: []string{"listUserLibraries"}},
	"getWatchTogetherCapabilities allowed":         {governs: []string{"joinWatchTogetherRoom", "createWatchTogetherRoom"}},
	// The collection read's finer members. Every collection write is
	// refused to such a session, so a member that offers one describes the
	// session (false, or no import source); a member that describes how the
	// server reads collections stays the server's.
	"getCollectionCapabilities imports":                     {governs: collectionImportOperations},
	"getCollectionCapabilities import_sources":              {governs: collectionImportOperations},
	"getCollectionCapabilities sync_schedule_editable":      {governs: []string{"updateCollection"}},
	"getCollectionCapabilities item_reorder":                {governs: []string{"reorderCollectionItems"}},
	"getCollectionCapabilities create_description":          {governs: []string{"createCollection"}},
	"getCollectionCapabilities artwork":                     {governs: []string{"uploadCollectionPoster", "deleteCollectionImage"}},
	"getCollectionCapabilities collection_sort_preferences": {governs: []string{"setCollectionSortPreference", "clearCollectionSortPreference"}},
	"getCollectionCapabilities sort_preference_kinds":       {governs: []string{"setCollectionSortPreference", "clearCollectionSortPreference"}},
	"getCollectionCapabilities groups":                      {exempt: "always false: no session may create a collection group, so the app offers none"},
	"getCollectionCapabilities login_sharing":               {exempt: "describes what listCollections returns (other profiles' shared collections) and that only a creator changes a collection; believing it, the app shows shared collections it may read"},
	"getCollectionCapabilities mdblist_search":              {governs: []string{"searchMDBListLists", "listTopMDBListLists"}},
	"getCollectionCapabilities contains_item":               {governs: []string{"listCollections"}},
	"getCollectionCapabilities preview_posters":             {governs: []string{"previewCollection"}},
	"getCollectionCapabilities poster_collages":             {exempt: "describes the posters the collection reads carry; believing it, the app shows the collage a read returns"},
	"getCollectionCapabilities collection_default_sort":     {exempt: "describes the order the collection reads use; believing it, the app shows collections in the order a read returns"},
	"getCollectionCapabilities effective_collection_sort":   {exempt: "describes the sort the collection reads report; believing it, the app shows the sort a read returns"},
	"getCollectionCapabilities display_filter_fields":       {exempt: "the vocabulary of a collection's display filter, which collection reads carry; it offers no action, and every write that would set a filter is refused with the flags above false"},
	"getCollectionCapabilities display_filter_presets":      {exempt: "the vocabulary of a collection's display filter, which collection reads carry; it offers no action, and every write that would set a filter is refused with the flags above false"},
	"getCollectionCapabilities schedule_time_zone":          {exempt: "the server's time zone for sync schedules; it offers no action, and the app could do nothing with it that the gate does not refuse"},
	// The Watch Together read's finer members. A host flag describes what a
	// room's host may do; such a session never hosts a room (creating one
	// and every host operation are refused), so it reads them false. The
	// member features stay: such a session joins rooms as a member.
	"getWatchTogetherCapabilities staged_selection":      {governs: []string{"stageWatchTogetherRoomItem", "startWatchTogetherRoomPlayback"}},
	"getWatchTogetherCapabilities selection_mode_switch": {governs: []string{"updateWatchTogetherRoomSelectionMode"}},
	"getWatchTogetherCapabilities vote_host_override":    {governs: []string{"promoteWatchTogetherSuggestion"}},
	"getWatchTogetherCapabilities stop_playback":         {governs: []string{"stopWatchTogetherRoomPlayback"}},
	"getWatchTogetherCapabilities lobby_ready":           {exempt: "a member marks itself ready over the room socket, which such a session may; believing it, the app sends lobby_ready"},
	"getWatchTogetherCapabilities connection_replaced":   {exempt: "describes how the server closes a displaced room socket; believing it, the app reads the closing message it is sent"},
	"getWatchTogetherCapabilities member_state":          {governs: []string{"queryWatchTogetherMemberState"}},
	"getWatchTogetherCapabilities picker":                {governs: []string{"getWatchTogetherRoomPicker"}},
	"getWatchTogetherCapabilities max_member_state_ids":  {governs: []string{"queryWatchTogetherMemberState"}},
	"getWatchTogetherCapabilities socket_protocol":       {exempt: "names the room socket's protocol, which a member's connection speaks; believing it, the app connects as a member, which it may"},
	// Documents that describe server behavior. Believing allowed, the app
	// would read the document, which every caller of the class may do: the
	// gate refuses it no action the member stands for.
	"getImageCapabilities allowed":         {exempt: "believing it, the app reads how the server sizes and serves images, which it may; the document offers no action the gate refuses"},
	"getNetworkAccessCapabilities allowed": {exempt: "believing it, the app reads the installed network access providers, which it may; the document offers no action the gate refuses"},
	"getPolicyCapability allowed":          {exempt: "believing it, the app reads the policy engine's decision types and state, which it may; the document offers no action the gate refuses"},
	"getServerConnections allowed":         {exempt: "believing it, the app reads the addresses the deployment offers, which it may; the document offers no action the gate refuses"},
}

// powerListMembers says, for each list of named powers a session another
// app's key opened may read, keyed as capabilityMembers is, how the answer
// keeps the list to that session's own powers. TestPowerListsStateTheirCase
// fails on such a list with no row. A sealed reply's tokens.user is the same
// account object as getCurrentUser's (accountForSession); the scan cannot see
// inside the sealed plaintext, so this row stands for both.
var powerListMembers = map[string]string{
	"getCurrentUser permissions": "accountForSession keeps a permission only while an operation it gates is open to the session (permittedPermissions)",
}

// memberPermitted applies a governed permission member below a capability
// read's top level: false when the caller's kind of session is refused every
// operation it names, else true (the handler's own answer then stands).
func (reg *Registry) memberPermitted(ctx context.Context, key string) bool {
	return reg.memberPermittedFor(judgedKind(claimsFrom(ctx)), key)
}

// memberPermittedFor is memberPermitted for a kind of session named outright,
// for an answer whose session is not the request's own (a sealed reply opens
// one).
func (reg *Registry) memberPermittedFor(kind auth.SessionKind, key string) bool {
	governed := capabilityMembers[key].governs
	return len(governed) == 0 || reg.permittedFor(kind, governed)
}
