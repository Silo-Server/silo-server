package apiv2

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
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
// its member, its case in decisionFor, and the session lookup that sets it on
// the claims; the gate does not change.
type OriginDecisions struct {
	// GrantedApp is the decision for a session another app's key opened.
	GrantedApp *SessionDecision `json:"granted_app,omitempty"`
}

// decisionFor is the decision for a kind of session; nil for a kind no
// member judges (the person's own sessions).
func (d OriginDecisions) decisionFor(kind auth.SessionKind) *SessionDecision {
	switch kind {
	case auth.SessionKindGrantedApp:
		return d.GrantedApp
	}
	return nil
}

func (d OriginDecisions) empty() bool { return d.GrantedApp == nil }

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
	reasonCapabilityRead = "a capability read: what the server offers this caller"
	reasonCatalogRead    = "reading the catalogue"
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
	reasonDiagnostics    = "a diagnostics upload, for the administrator to read"
)

// Reasons for the operations such a session is refused.
const (
	reasonRefusedAccount      = "changes the account"
	reasonRefusedSignIn       = "changes how the account signs in, or reads or hands out its other credentials"
	reasonRefusedOtherDevice  = "reaches the person's other devices"
	reasonRefusedLeaves       = "leaves something behind that outlives the app's key"
	reasonRefusedOwnDevice    = "until the server knows the session's own device"
	reasonRefusedHousehold    = "changes the household's profiles, PINs or limits"
	reasonRefusedRoomHost     = "acts as a room's host or changes what other members play"
	reasonRefusedRequests     = "refused for now: making media requests"
	reasonRefusedSubtitleEdit = "refused for now: editing subtitles every account sees"
	reasonRefusedImport       = "refused for now: importing collections, which keep syncing"
	reasonRefusedRefresh      = "refused for now: refreshing the shared catalogue"
	reasonRefusedNotifyPrefs  = "refused for now: changing notification preferences"
	reasonRefusedCatalogEdit  = "refused for now: catalogue edits under a delegated permission"
	reasonNotDecided          = "not decided"
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
	grantedDiagnostics    = grantedApp(decideAllowed(reasonDiagnostics))

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
	grantedRefusedOwnDevice    = grantedApp(decideRefused(reasonRefusedOwnDevice))
	grantedRefusedHousehold    = grantedApp(decideRefused(reasonRefusedHousehold))
	grantedRefusedRoomHost     = grantedApp(decideRefused(reasonRefusedRoomHost))
	grantedRefusedRequests     = grantedApp(decideRefused(reasonRefusedRequests))
	grantedRefusedSubtitleEdit = grantedApp(decideRefused(reasonRefusedSubtitleEdit))
	grantedRefusedImport       = grantedApp(decideRefused(reasonRefusedImport))
	grantedRefusedRefresh      = grantedApp(decideRefused(reasonRefusedRefresh))
	grantedRefusedNotifyPrefs  = grantedApp(decideRefused(reasonRefusedNotifyPrefs))
	grantedRefusedCatalogEdit  = grantedApp(decideRefused(reasonRefusedCatalogEdit))
	grantedNotDecided          = grantedApp(decideRefused(reasonNotDecided))
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
	for kind, d := range map[auth.SessionKind]*SessionDecision{auth.SessionKindGrantedApp: op.Origins.GrantedApp} {
		if d == nil {
			continue
		}
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
	op.Extensions[extOriginDecisions] = op.Origins
}

// originGate refuses a session whose kind the operation's decisions refuse,
// before anything else about the request is judged. It reads the kind the
// authentication gate put on the claims, so it costs no query. An operation
// that states no decision for the kind refuses it: every operation a
// non-administrator reaches states one, and one that does not has not been
// decided. Every caller of no judged kind passes untouched.
func originGate(decisions OriginDecisions) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims := claimsFrom(r.Context())
			if !claims.IsGrantedApp() {
				next.ServeHTTP(w, r)
				return
			}
			if d := decisions.decisionFor(auth.SessionKindGrantedApp); d != nil && d.Verdict != VerdictRefused {
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
