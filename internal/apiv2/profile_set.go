package apiv2

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	apimw "github.com/Silo-Server/silo-server/internal/api/middleware"
	"github.com/Silo-Server/silo-server/internal/appkeys"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/ratelimit"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// ProfileSetService applies and reads an account's profile set.
// *handlers.ProfileHandler implements it.
type ProfileSetService interface {
	ProfileSetSupported() bool
	ApplyProfileSet(ctx context.Context, cmd handlers.ProfileSetCommand) (handlers.ProfileSetResult, error)
	HeldProfileSet(ctx context.Context, userID int) (handlers.ProfileSetHeld, error)
}

// profileSetDomain names the capability in its problems.
const profileSetDomain = "profile set"

// bucketProfileSet is the rate-limit budget PUT /profiles/set spends.
const bucketProfileSet = "profile_set"

// maxProfileSetBodyBytes bounds a profile set request.
const maxProfileSetBodyBytes = 256 << 10

// ProfileSetSettings maps settings-contract keys to values. The contract,
// not this schema, shapes the values.
type ProfileSetSettings map[string]json.RawMessage

func (ProfileSetSettings) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeObject, AdditionalProperties: true, Extensions: map[string]any{extExtensionBag: "profile-set-settings"},
		Description: "Settings-contract keys stored per profile, each to its value (getSettingsManifest). Keys the server does not know, and keys marked server_local, are ignored."}
}

// ProfileSetValue is any JSON value.
type ProfileSetValue json.RawMessage

// MarshalJSON writes the value as it is.
func (v ProfileSetValue) MarshalJSON() ([]byte, error) {
	if len(v) == 0 {
		return []byte("null"), nil
	}
	return v, nil
}

func (ProfileSetValue) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Description: "Any JSON value.", Extensions: map[string]any{extExtensionBag: "profile-set-value"}}
}

// profileSetPINFields is the schema of an item's PIN instruction.
type profileSetPINFields struct {
	Set   *string `json:"set,omitempty" pattern:"^[0-9]{4}$" doc:"Set this PIN, exactly four digits. Compared with the stored PIN first; written only when it differs" example:"1234"`
	Keep  *bool   `json:"keep,omitempty" enum:"true" doc:"Leave the PIN this server holds alone. A profile created from keep has no PIN and is reported in awaiting_pins until it gets one"`
	Clear *bool   `json:"clear,omitempty" enum:"true" doc:"Remove the PIN"`
}

// ProfileSetPIN is an item's PIN instruction: exactly one of set, keep and
// clear. Other members are ignored, as in an item.
type ProfileSetPIN profileSetPINFields

func (ProfileSetPIN) Schema(r huma.Registry) *huma.Schema {
	return openSchema(r, reflect.TypeFor[profileSetPINFields](), "profile-set-pin",
		"Exactly one of set, keep and clear. Members this server does not know are ignored.")
}

// openSchema is the schema of t with members it doesn't name allowed, for
// the parts of a set a newer app may extend: this server ignores them.
func openSchema(r huma.Registry, t reflect.Type, bag, description string) *huma.Schema {
	s := huma.SchemaFromType(r, t)
	s.AdditionalProperties = true
	s.Extensions = map[string]any{extExtensionBag: bag}
	s.Description = description
	return s
}

// profileSetItemFields is the schema of a set item's known members.
type profileSetItemFields struct {
	SyncID             string             `json:"sync_id" format:"uuid" pattern:"^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$" doc:"The profile's ID on every server, a lower-case UUID. Unique in the set" example:"0f8fad5b-d9cb-469f-a165-70867728950e"`
	Name               string             `json:"name" minLength:"1" doc:"1 to 64 characters once trimmed; unique in the set, compared trimmed and without case" example:"Sam"`
	Picture            string             `json:"picture" maxLength:"200" doc:"preset:<id> for a built-in picture (avatar-1 to avatar-8, or dicebear:<style>:<seed>), sha256:<64 lower-case hex> of an uploaded picture as it was first uploaded, or empty for none" example:"preset:avatar-3"`
	PIN                ProfileSetPIN      `json:"pin"`
	IsChild            bool               `json:"is_child" example:"false"`
	MaxContentRating   string             `json:"max_content_rating" maxLength:"32" doc:"Content-rating ceiling; empty for none" example:""`
	MaxAdvisoryAge     *int               `json:"max_advisory_age" nullable:"true" minimum:"1" maximum:"21" doc:"Advisory-age limit; null for none" example:"12"`
	RequireAdvisoryAge bool               `json:"require_advisory_age" example:"false"`
	RequestsBlocked    bool               `json:"requests_blocked" example:"false"`
	Settings           ProfileSetSettings `json:"settings"`
	SettingsReset      []string           `json:"settings_reset,omitempty" maxItems:"256" doc:"Keys whose profile value this server removes, so the profile inherits again. A key in both settings and settings_reset is 422" example:"[\"playback.audio_language\"]"`
}

// ProfileSetItem is one profile of a set. Members a server does not know
// are ignored, so a newer app's set applies on an older server.
type ProfileSetItem profileSetItemFields

func (ProfileSetItem) Schema(r huma.Registry) *huma.Schema {
	return openSchema(r, reflect.TypeFor[profileSetItemFields](), "profile-set-item",
		"One profile of the set. Members this server does not know are ignored when applying; the content hash still covers them.")
}

// profileSetDocumentFields is the schema of the set object.
type profileSetDocumentFields struct {
	Version int64            `json:"version" minimum:"0" maximum:"9007199254740991" doc:"Goes up with every change. A whole number; 7.0 is 7" example:"7"`
	Items   []ProfileSetItem `json:"items" minItems:"1" maxItems:"64" doc:"The main profile first, then the rest in the order the person chose"`
}

// ProfileSetDocument is the set in the session form. The handler reads it
// from the raw body, with the parser the signed form uses.
type ProfileSetDocument json.RawMessage

// UnmarshalJSON keeps nothing: the handler reads the raw body.
func (d *ProfileSetDocument) UnmarshalJSON([]byte) error { return nil }

func (ProfileSetDocument) Schema(r huma.Registry) *huma.Schema {
	return openSchema(r, reflect.TypeFor[profileSetDocumentFields](), "profile-set",
		"The set. Members this server does not know are ignored.")
}

// ProfileSetPair says which existing profile an item becomes, for the first
// use on an account that already has profiles.
type ProfileSetPair struct {
	SyncID  string `json:"sync_id" format:"uuid" doc:"An item's sync_id" example:"0f8fad5b-d9cb-469f-a165-70867728950e"`
	LocalID ID     `json:"local_id" doc:"The profile on this server it becomes" example:"15"`
}

// ProfileSetPutBody is either form of a set: a signed profile-set envelope,
// or the set itself from a login session verified as the main profile.
type ProfileSetPutBody struct {
	KeyID     *string             `json:"kid,omitempty" minLength:"1" maxLength:"64" doc:"Signed form: the person key's key ID" example:"PgujdSfBbNNyXngLWCsOgw"`
	Payload   *string             `json:"payload,omitempty" minLength:"1" maxLength:"262144" doc:"Signed form: base64url of the profile-set payload {purpose, server_id, iat, exp, challenge, account_id, set, pairs?, edits_seen?}" example:"eyJwdXJwb3NlIjoicHJvZmlsZS1zZXQifQ"`
	Sig       *string             `json:"sig,omitempty" minLength:"1" maxLength:"128" doc:"Signed form: the person key's signature" example:"nnyAt7pV-XsUSmpX0IAk1d0QmUrO4z9CZ5Oeoy9QlVSyCq2EED4Ns_Aakhd5DgOmSqWWrT5xs8an1Ye0a4smgg"`
	Set       *ProfileSetDocument `json:"set,omitempty" doc:"Session form: the set"`
	Pairs     []ProfileSetPair    `json:"pairs,omitempty" maxItems:"64" doc:"Session form: which existing profile each item becomes, for the first use"`
	EditsSeen *string             `json:"edits_seen,omitempty" maxLength:"64" doc:"Session form: the edits_token getProfileSet answered when the set was built; absent is the empty string" example:""`
}

// ProfileSetPutInput is the putProfileSet request.
type ProfileSetPutInput struct {
	RawBody []byte
	Body    ProfileSetPutBody
}

// ProfileSetMember is one profile the account holds from the set.
type ProfileSetMember struct {
	SyncID string `json:"sync_id" format:"uuid" example:"0f8fad5b-d9cb-469f-a165-70867728950e"`
	ID     ID     `json:"id" doc:"This server's ID for the profile" example:"12"`
}

// ProfileSetOverride is a value the owner set on this server, which a set
// does not overwrite.
type ProfileSetOverride struct {
	SyncID string          `json:"sync_id" format:"uuid" example:"0f8fad5b-d9cb-469f-a165-70867728950e"`
	Field  string          `json:"field" doc:"An item field name, or settings.<key>. An open string: new item fields and settings keys will appear, and an app ignores or carries through one it doesn't know" example:"max_content_rating"`
	Value  ProfileSetValue `json:"value,omitempty" doc:"The value this server holds, as a set item encodes it. pin is \"set\" or \"clear\", never a PIN. Absent for a setting the owner reset, which has no profile value here, and in the putProfileSet answer for a picture naming an upload this server kept no hash for"`
}

// ProfileSetSkippedSetting is a known setting whose value this server
// refused and kept its own.
type ProfileSetSkippedSetting struct {
	SyncID string `json:"sync_id" format:"uuid" example:"0f8fad5b-d9cb-469f-a165-70867728950e"`
	Key    string `json:"key" example:"playback.subtitle_mode"`
}

// ProfileSetApplied is the putProfileSet answer.
type ProfileSetApplied struct {
	Version                int64                      `json:"version" example:"7"`
	Profiles               []ProfileSetMember         `json:"profiles" doc:"This server's ID for each profile it holds from the set, in set order"`
	LeftOff                []string                   `json:"left_off" doc:"Items not applied because of the household's profile limit, in set order"`
	AwaitingLibraries      []string                   `json:"awaiting_libraries" doc:"Child profiles with library limits and no libraries yet, waiting for the person to pick them"`
	AwaitingPictures       []string                   `json:"awaiting_pictures" doc:"Items whose picture this server doesn't have: a sha256 picture not uploaded here, or a preset it doesn't know. The profile keeps its old picture"`
	AwaitingPins           []string                   `json:"awaiting_pins" doc:"Profiles created from keep that still have no PIN"`
	HiddenByOwner          []string                   `json:"hidden_by_owner" doc:"Items the owner removed on this server; the set did not bring them back"`
	Overrides              []ProfileSetOverride       `json:"overrides" doc:"Values the owner set on this server for the set's profiles; the set left them as they are"`
	SkippedSettings        []ProfileSetSkippedSetting `json:"skipped_settings" doc:"Known settings whose value this server refused; it kept its own value"`
	JellyfinBareNameSignIn *bool                      `json:"jellyfin_bare_name_sign_in,omitempty" doc:"Whether signing in on the Jellyfin-compatible surface with the bare account name (no #profile) picks a profile, as the account's profiles stand after the set: true when exactly one profile is named like the account, or else exactly one profile has no PIN. Absent while that surface is off" example:"true"`
}

// ProfileSetPutOutput is the putProfileSet response.
type ProfileSetPutOutput struct {
	Body ProfileSetApplied
}

// ProfileSetFieldChange is a field edited on this server outside a set.
type ProfileSetFieldChange struct {
	Field string  `json:"field" doc:"An item field name, or settings.<key>. An open string: new item fields and settings keys will appear, and an app ignores or carries through one it doesn't know" example:"name"`
	At    Instant `json:"at" example:"2026-11-25T03:50:00.000Z"`
	By    string  `json:"by" enum:"account,owner" example:"account"`
}

// ProfileSetDeletion is a profile of the set deleted on this server
// outside a set.
type ProfileSetDeletion struct {
	SyncID string  `json:"sync_id" format:"uuid" example:"0f8fad5b-d9cb-469f-a165-70867728950e"`
	At     Instant `json:"at" example:"2026-11-25T03:50:00.000Z"`
	By     string  `json:"by" enum:"account,owner" example:"account"`
}

// ProfileSetHeldItem is one profile of the set as this server holds it.
type ProfileSetHeldItem struct {
	SyncID             string                  `json:"sync_id" format:"uuid" example:"0f8fad5b-d9cb-469f-a165-70867728950e"`
	ID                 ID                      `json:"id" example:"12"`
	Position           int                     `json:"position" doc:"The profile's place in the set, from 0" example:"0"`
	Name               string                  `json:"name" example:"Sam"`
	Picture            string                  `json:"picture" doc:"preset:<id>, sha256:<hex> naming an upload, or empty" example:"preset:avatar-3"`
	HasPIN             bool                    `json:"has_pin" example:"true"`
	IsChild            bool                    `json:"is_child" example:"false"`
	MaxContentRating   string                  `json:"max_content_rating" example:""`
	MaxAdvisoryAge     *int                    `json:"max_advisory_age" nullable:"true" example:"12"`
	RequireAdvisoryAge bool                    `json:"require_advisory_age" example:"false"`
	RequestsBlocked    bool                    `json:"requests_blocked" example:"false"`
	Settings           ProfileSetSettings      `json:"settings"`
	ChangedHere        []ProfileSetFieldChange `json:"changed_here" doc:"Fields edited on this server outside a set and not yet settled: by account until the next accepted newer set settles them, by owner until the owner's value is replaced"`
}

// ProfileSetLocal is a profile on the account that is not part of the set.
type ProfileSetLocal struct {
	ID     ID     `json:"id" example:"15"`
	Name   string `json:"name" example:"Guest"`
	HasPIN bool   `json:"has_pin" example:"false"`
}

// ProfileSetHeld is the getProfileSet answer.
type ProfileSetHeld struct {
	Version                int64                `json:"version" doc:"The version of the last set this server accepted; 0 when it never took one" example:"7"`
	Items                  []ProfileSetHeldItem `json:"items"`
	DeletedHere            []ProfileSetDeletion `json:"deleted_here" doc:"Profiles of the set deleted on this server outside a set. Every removal on record, including sync_ids the set no longer carries"`
	Overrides              []ProfileSetOverride `json:"overrides" doc:"Values the owner set on this server; a set leaves them as they are"`
	HiddenByOwner          []string             `json:"hidden_by_owner" doc:"Profiles the owner removed on this server; a set does not bring them back. Every removal on record, including sync_ids the set no longer carries"`
	LocalOnly              []ProfileSetLocal    `json:"local_only" doc:"Profiles on this account that are not part of the set"`
	JellyfinBareNameSignIn *bool                `json:"jellyfin_bare_name_sign_in,omitempty" doc:"Whether signing in on the Jellyfin-compatible surface with the bare account name (no #profile) picks a profile as the account's profiles now stand: true when exactly one profile is named like the account, or else exactly one profile has no PIN. Absent while that surface is off" example:"true"`
	EditsToken             string               `json:"edits_token" doc:"Names the person's own edits on this server that no set has settled (the by account entries of changed_here and deleted_here); empty when there are none. A newer set must send it back as edits_seen while it is not empty" example:"e1.mFzDq0dX7eTsc3m6SfN0Lw1o2s0kHqgV"`
}

// ProfileSetGetInput is the getProfileSet request.
type ProfileSetGetInput struct {
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match" doc:"An ETag from an earlier answer; 304 when the set is unchanged"`
}

// ProfileSetGetOutput is the getProfileSet response.
type ProfileSetGetOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   ProfileSetHeld
}

const putProfileSetDescription = "Makes the account's household profiles match a versioned set, in one transaction. " +
	"The body is either a signed profile-set envelope ({kid, payload, sig}, signed by the account's person key on a challenge from createAppKeyChallenge; with or without a login session), " +
	"or {set, pairs?, edits_seen?} over the account's own login session acting as its main profile (X-Profile-Id), with X-Profile-Token when that profile has a PIN. " +
	"The signed form is refused for an administrator's account (403 permission_denied) and while auth.app_key_sign_in is off (409 capability_disabled); the session form takes neither rule. " +
	"A session form that is not acting as the main profile is 403 main_profile_required. " +
	"Sessions opened by device sign-in, by another app's key, or by an administrator acting as the account, remote-playback handoffs, and sessions older than the record of how sessions open are 403 permission_denied; an API key carries no login session and is 401 authentication_required. In demo mode a non-administrator is 403 permission_denied in both forms. " +
	"The first item is the main profile; items match profiles by sync_id, then by pairs, and are otherwise created in set order up to the household's profile limit (the rest are left_off; nothing is deleted to fit). " +
	"A profile holding a sync_id the set no longer has is deleted with everything that belongs to it; a profile with no sync_id that pairs does not name is left alone. " +
	"Only a newer version changes anything: an older version, the same version with other content, the same version whose PINs differ from the stored ones (a PIN the owner set here is not compared), and a newer set whose edits_seen is not getProfileSet's current edits_token while that token is not empty are 409 profile_set_stale, with nothing written; the same version and content again answers 200 and writes nothing. " +
	"A field the owner set on this server is left as it is and listed in overrides; an item the owner removed here is not created or paired and is listed in hidden_by_owner. An accepted newer set settles the account's edits its edits_seen named. " +
	"A set that changes the main profile's PIN ends every X-Profile-Token minted for its old PIN, the caller's included. " +
	"Refusals come in this order: a malformed set (422), then a stale one (409 profile_set_stale), then a set the household can't map (pairs), then the held version again (a no-op, never refused for what only a write would do), then what a set that will write would do (422 name_conflict, item 0 taking a sync_id the owner removed). " +
	"A malformed member is 422 validation_failed at its member, the same location, code and detail in both forms (a signed set's members are reported at body.set, as the session form's are); a new item named like a profile outside the set is 422 with code name_conflict. " +
	"Limited per account by the profile_set rate-limit budget (the session's account, or the one a verified signature names), with a per-address budget ten times that spent first; 429 rate_limited with Retry-After from either. The signed form also spends its challenge, and has spent it when a 422 or the account's limit refuses the set: the next try needs a new challenge and a newly signed envelope. A body over 256 KiB is 413 payload_too_large. " +
	"501 capability_unsupported where profiles are not kept in PostgreSQL."

func registerProfileSet(reg *Registry) {
	put := humaOp(http.MethodPut, Prefix+"/profiles/set", "putProfileSet", "profiles",
		"Make the account's household profiles match a versioned profile set.")
	put.Description = putProfileSetDescription
	put.Errors = []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusNotImplemented}
	put.Metadata = map[string]any{metaAttachSession: true, metaOptionalViewerScope: true}
	putOp := Operation{Operation: put, Class: ClassPublic, RetrySafety: RetrySafetyNonRetryable, ServiceBacked: true, Audited: true,
		RateLimitBucket: bucketProfileSet, MaxBodyBytes: maxProfileSetBodyBytes}
	Register(reg, putOp, reg.putProfileSet)
	declareRetryAfter(reg, "Seconds to wait before sending the set again.", putOp)

	get := humaOp(http.MethodGet, Prefix+"/profiles/set", "getProfileSet", "profiles",
		"Read the account's profile set as this server holds it.")
	get.Description = "The same sessions as putProfileSet's session form may read it, without the main profile's PIN. An account that never took a set answers version 0, no items, and every profile in local_only. " +
		"changed_here and deleted_here list edits made on this server outside a set: by account for the account's own sessions, by owner for an administrator. The next accepted newer set settles the account ones (a repeat of the held version settles nothing); edits_token names them: it changes when one is added, settled, or replaced by an owner's edit of the same field, and with no other owner's edit. " +
		"overrides lists the owner's values, which a set leaves as they are until the account edits that field here, or, for a setting the settings contract mirrors in another key, either key of the pair; hidden_by_owner lists profiles the owner removed, which a set does not bring back. " +
		"Strong ETag; If-None-Match answers 304. 501 capability_unsupported where profiles are not kept in PostgreSQL."
	get.Errors = []int{http.StatusForbidden, http.StatusNotImplemented}
	Register(reg, Operation{Operation: get, Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true, Conditional: true, Origins: grantedRefusedProfileSet}, reg.getProfileSet)
}

// profileSetService answers the service, or the capability problem: not
// configured without it, unsupported off the PostgreSQL user store.
func (reg *Registry) profileSetService() (ProfileSetService, error) {
	svc := reg.deps.ProfileSet
	if svc == nil {
		return nil, CapabilityProblem(StateNotConfigured, profileSetDomain)
	}
	if !svc.ProfileSetSupported() {
		return nil, CapabilityProblem(StateUnsupported, profileSetDomain)
	}
	return svc, nil
}

func (reg *Registry) putProfileSet(ctx context.Context, in *ProfileSetPutInput) (*ProfileSetPutOutput, error) {
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	sets, err := reg.profileSetService()
	if err != nil {
		return nil, err
	}
	// Demo mode refuses a set before the forms split: with no session the
	// caller is never an administrator, so every signed set is refused while
	// demo mode is on, as the ordinary profile writes are.
	// A demo setting that was never read refuses the set too, as the demo
	// gate does for every restricted change.
	restricted, demoErr := demoRestricted(ctx, reg.deps.DemoSettings)
	if demoErr != nil {
		return nil, serviceProblem(demoErr)
	}
	if restricted {
		return nil, demoRestrictedProblem()
	}
	body := in.Body
	signed := body.KeyID != nil || body.Payload != nil || body.Sig != nil
	session := body.Set != nil || body.Pairs != nil || body.EditsSeen != nil
	if signed == session {
		return nil, validationProblem(locationBody, codeInvalid, "Send either a signed envelope {kid, payload, sig} or {set, pairs, edits_seen}, not both or neither.")
	}
	var cmd handlers.ProfileSetCommand
	if signed {
		cmd, err = reg.signedProfileSet(ctx, in.RawBody)
	} else {
		cmd, err = reg.sessionProfileSet(ctx, in.RawBody)
	}
	if err != nil {
		return nil, err
	}
	// One parser for both forms, before anything is spent or locked: what a
	// set may hold does not depend on how it arrived.
	if err := handlers.CheckProfileSet(cmd.Set, cmd.Pairs, cmd.EditsSeen); err != nil {
		return nil, profileSetProblem(err)
	}
	// The account is known for certain now: the session's, or the one a
	// verified signature names. Its budget is the one a person meets; the
	// address spent a looser one before any of this ran.
	if p := reg.spendAccountBudget(ctx, bucketProfileSet, cmd.UserID); p != nil {
		return nil, p
	}
	result, err := sets.ApplyProfileSet(withClientDevice(ctx), cmd)
	if err != nil {
		return nil, profileSetProblem(err)
	}
	return &ProfileSetPutOutput{Body: profileSetAppliedOf(result)}, nil
}

// spendAccountBudget spends accountID's share of a budget keyed by account
// and answers 429 rate_limited with Retry-After once it is used up.
func (reg *Registry) spendAccountBudget(ctx context.Context, bucket string, accountID int) *Problem {
	if reg.deps.AccountRateLimit == nil {
		return nil
	}
	result := reg.deps.AccountRateLimit(ctx, bucket, accountID)
	if result.Allowed {
		return nil
	}
	return NewProblem(TypeRateLimited, "This account has sent too many profile sets; retry after the Retry-After delay.").
		WithRetryAfter(ratelimit.RetryAfterSeconds(result.RetryAfter))
}

// signedProfileSet verifies a signed set: the switch, the envelope, the
// signature on its challenge, and the account it names.
func (reg *Registry) signedProfileSet(ctx context.Context, raw []byte) (handlers.ProfileSetCommand, error) {
	svc, err := reg.appKeysOn(ctx)
	if err != nil {
		return handlers.ProfileSetCommand{}, err
	}
	env, err := appkeys.ParseEnvelope(raw, appkeys.PurposeProfileSet)
	if err != nil {
		return handlers.ProfileSetCommand{}, appKeyProblem(err)
	}
	req, err := reg.keyRequest(ctx, env)
	if err != nil {
		return handlers.ProfileSetCommand{}, err
	}
	authority, err := svc.VerifyProfileSet(ctx, req)
	if err != nil {
		return handlers.ProfileSetCommand{}, profileSetProblem(err)
	}
	if p := reg.validateSignedProfileSet(authority); p != nil {
		return handlers.ProfileSetCommand{}, p
	}
	return handlers.ProfileSetCommand{UserID: authority.UserID, Set: authority.Set, Pairs: authority.Pairs,
		EditsSeen: authority.EditsSeen, Signed: true}, nil
}

// validateSignedProfileSet holds a verified signed set to what the session
// form's body is held to before its handler runs: the request schema, then
// the rule against null members. The set, pairs and edits_seen of the
// payload are checked as the session body {set, pairs, edits_seen} they
// would have been, by the same schema, so the two forms cannot disagree on
// what a set may hold, and a refusal names the same member with the same
// code in both: body.set.items[2].name, though in this form the set
// traveled inside the payload.
func (reg *Registry) validateSignedProfileSet(authority *auth.ProfileSetAuthority) *Problem {
	members := map[string]json.RawMessage{"set": authority.Set}
	if authority.Pairs != nil {
		members["pairs"] = authority.Pairs
	}
	if authority.EditsSeenRaw != nil {
		members["edits_seen"] = authority.EditsSeenRaw
	}
	// A member that is not JSON fails here, in the encoding or the decoding.
	body, err := json.Marshal(members)
	var value any
	if err == nil {
		err = json.Unmarshal(body, &value)
	}
	if err != nil {
		return validationProblem(locationBody+".set", codeInvalid, "The set is not valid JSON.")
	}
	registry := reg.api.OpenAPI().Components.Schemas
	path := huma.NewPathBuffer([]byte{}, 0)
	path.Push(locationBody)
	var result huma.ValidateResult
	huma.Validate(registry, registry.Schema(reflect.TypeFor[ProfileSetPutBody](), true, ""), path, huma.ModeWriteToServer, value, &result)
	if len(result.Errors) > 0 {
		p := NewProblem(TypeValidationFailed, validationSummary)
		for _, err := range result.Errors {
			var detail *huma.ErrorDetail
			if !errors.As(err, &detail) {
				continue
			}
			p.Errors = append(p.Errors, ProblemError{Location: validationLocation(detail), Code: validationCode(detail.Message), Detail: validationDetail(detail.Message)})
		}
		return p
	}
	return rejectNonNullableNulls(body, nil)
}

// sessionProfileSet admits the session form: the account's own login
// session, held to the rule for sessions that manage keys, acting as a
// profile the service checks is the verified main profile.
func (reg *Registry) sessionProfileSet(ctx context.Context, raw []byte) (handlers.ProfileSetCommand, error) {
	claims := claimsFrom(ctx)
	if claims == nil {
		return handlers.ProfileSetCommand{}, NewProblem(TypeAuthenticationRequired, "A profile set without a signature needs a login session.")
	}
	if err := reg.profileSetSessionAllowed(ctx, claims); err != nil {
		return handlers.ProfileSetCommand{}, err
	}
	active := profileFrom(ctx)
	if active == "" && apimw.UnverifiedProfileID(ctx) != "" {
		return handlers.ProfileSetCommand{}, NewProblem(TypeProfileVerificationRequired, "Verify the main profile's PIN first.")
	}
	var members struct {
		Set       json.RawMessage `json:"set"`
		Pairs     json.RawMessage `json:"pairs"`
		EditsSeen string          `json:"edits_seen"`
	}
	if err := json.Unmarshal(raw, &members); err != nil {
		return handlers.ProfileSetCommand{}, validationProblem(locationBody, codeInvalid, "The body is not a profile set.")
	}
	return handlers.ProfileSetCommand{UserID: claims.UserID, Set: members.Set, Pairs: members.Pairs, EditsSeen: members.EditsSeen,
		ActiveProfileID: active, VerifyProfile: verifyHouseholdProfile(ctx)}, nil
}

// profileSetSessionAllowed applies the session rule. It needs app keys
// wired, not switched on: the session form works with app keys off.
func (reg *Registry) profileSetSessionAllowed(ctx context.Context, claims *auth.Claims) error {
	svc, err := reg.appKeysWired()
	if err != nil {
		return err
	}
	if err := svc.SessionMayManageKeys(ctx, claims); err != nil {
		return appKeyProblem(err)
	}
	return nil
}

// profileSetProblem maps a profile set refusal to its problem.
func profileSetProblem(err error) error {
	var field *handlers.ProfileSetFieldError
	switch {
	case errors.As(err, &field):
		// The same location in both forms: a signed set's members are named
		// as the session form names them (validateSignedProfileSet).
		return validationProblem(locationBody+"."+field.Location, field.Code, field.Message)
	case errors.Is(err, handlers.ErrProfileSetNoPrimary):
		return NewProblem(TypeConflict, "This account's profiles are in a state the server can't apply a profile set to: it has profiles but no main profile.")
	case errors.Is(err, handlers.ErrProfileSetStale):
		return NewProblem(TypeProfileSetStale, "This server holds a newer profile set, other content at the same version, or edits on this server the set has not seen; read it and send a newer one.")
	case errors.Is(err, auth.ErrProfileSetAdminAccount):
		return NewProblem(TypePermissionDenied, "An administrator's profiles change only from a session verified as the main profile.")
	case errors.Is(err, handlers.ErrProfileSetNotMainProfile):
		return mainProfileRequiredProblem()
	case errors.Is(err, access.ErrProfileUnverified):
		return NewProblem(TypeProfileVerificationRequired, "Verify the main profile's PIN first.")
	case errors.Is(err, userstore.ErrProfileSetAccountGone):
		// As any request for a deleted account: its sessions ended with it.
		return NewProblem(TypeSessionExpired, "The login session is no longer valid.")
	case errors.Is(err, userstore.ErrProfileSetUnsupported):
		return CapabilityProblem(StateUnsupported, profileSetDomain)
	}
	return appKeyProblem(err)
}

func profileSetAppliedOf(r handlers.ProfileSetResult) ProfileSetApplied {
	out := ProfileSetApplied{
		Version: r.Version, LeftOff: NonNil(r.LeftOff), AwaitingLibraries: NonNil(r.AwaitingLibraries),
		AwaitingPictures: NonNil(r.AwaitingPictures), AwaitingPins: NonNil(r.AwaitingPins),
		HiddenByOwner: NonNil(r.HiddenByOwner), Overrides: profileSetOverridesOf(r.Overrides),
		Profiles: make([]ProfileSetMember, 0, len(r.Profiles)), SkippedSettings: make([]ProfileSetSkippedSetting, 0, len(r.SkippedSettings)),
		JellyfinBareNameSignIn: r.JellyfinBareNameSignIn,
	}
	for _, p := range r.Profiles {
		out.Profiles = append(out.Profiles, ProfileSetMember{SyncID: p.SyncID, ID: ID(p.ID)})
	}
	for _, s := range r.SkippedSettings {
		out.SkippedSettings = append(out.SkippedSettings, ProfileSetSkippedSetting{SyncID: s.SyncID, Key: s.Key})
	}
	return out
}

func (reg *Registry) getProfileSet(ctx context.Context, in *ProfileSetGetInput) (*ProfileSetGetOutput, error) {
	sets, err := reg.profileSetService()
	if err != nil {
		return nil, err
	}
	claims := claimsFrom(ctx)
	if err := reg.profileSetSessionAllowed(ctx, claims); err != nil {
		return nil, err
	}
	held, err := sets.HeldProfileSet(ctx, claims.UserID)
	if err != nil {
		return nil, profileSetProblem(err)
	}
	body := profileSetHeldOf(held)
	tag, err := profileSetTag(body)
	if err != nil {
		return nil, err
	}
	out := &ProfileSetGetOutput{ETag: tag.String(), Body: body}
	if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
		return nil, p
	} else if matched {
		return NotModified(out, tag), nil
	}
	return out, nil
}

// profileSetTag is the answer's strong ETag: a hash of its body, which holds
// no URL or time that changes on its own.
func profileSetTag(body ProfileSetHeld) (EntityTag, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return EntityTag{}, err
	}
	sum := sha256.Sum256(encoded)
	return EntityTag{Opaque: "ps1-" + base64.RawURLEncoding.EncodeToString(sum[:])[:22]}, nil
}

func profileSetHeldOf(h handlers.ProfileSetHeld) ProfileSetHeld {
	out := ProfileSetHeld{
		Version: h.Version, Items: make([]ProfileSetHeldItem, 0, len(h.Items)),
		DeletedHere: make([]ProfileSetDeletion, 0, len(h.DeletedHere)), Overrides: profileSetOverridesOf(h.Overrides),
		HiddenByOwner: NonNil(h.HiddenByOwner), LocalOnly: make([]ProfileSetLocal, 0, len(h.LocalOnly)), EditsToken: h.EditsToken,
		JellyfinBareNameSignIn: h.JellyfinBareNameSignIn,
	}
	for _, item := range h.Items {
		var age *int
		if item.MaxAdvisoryAge != 0 {
			age = new(int)
			*age = item.MaxAdvisoryAge
		}
		settings := ProfileSetSettings{}
		keys := make([]string, 0, len(item.Settings))
		for key := range item.Settings {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			settings[key] = json.RawMessage(bytes.Clone(item.Settings[key]))
		}
		out.Items = append(out.Items, ProfileSetHeldItem{
			SyncID: item.SyncID, ID: ID(item.ID), Position: item.Position, Name: item.Name, Picture: item.Picture,
			HasPIN: item.HasPIN, IsChild: item.IsChild, MaxContentRating: item.MaxContentRating, MaxAdvisoryAge: age,
			RequireAdvisoryAge: item.RequireAdvisoryAge, RequestsBlocked: item.RequestsBlocked, Settings: settings,
			ChangedHere: profileSetChangesOf(item.ChangedHere),
		})
	}
	for _, d := range h.DeletedHere {
		out.DeletedHere = append(out.DeletedHere, ProfileSetDeletion{SyncID: d.SyncID, At: NewInstant(d.At), By: d.By})
	}
	for _, local := range h.LocalOnly {
		out.LocalOnly = append(out.LocalOnly, ProfileSetLocal{ID: ID(local.ID), Name: local.Name, HasPIN: local.HasPIN})
	}
	return out
}

func profileSetChangesOf(changes []handlers.ProfileSetFieldChange) []ProfileSetFieldChange {
	out := make([]ProfileSetFieldChange, 0, len(changes))
	for _, c := range changes {
		out = append(out, ProfileSetFieldChange{Field: c.Field, At: NewInstant(c.At), By: c.By})
	}
	return out
}

func profileSetOverridesOf(overrides []handlers.ProfileSetOverride) []ProfileSetOverride {
	out := make([]ProfileSetOverride, 0, len(overrides))
	for _, o := range overrides {
		out = append(out, ProfileSetOverride{SyncID: o.SyncID, Field: o.Field, Value: ProfileSetValue(bytes.Clone(o.Value))})
	}
	return out
}
