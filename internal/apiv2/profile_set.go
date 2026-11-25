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
	Settings           ProfileSetSettings `json:"settings,omitempty"`
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
	Field  string          `json:"field" doc:"An item field name, or settings.<key>" example:"max_content_rating"`
	Value  ProfileSetValue `json:"value"`
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
	HiddenByOwner          []string                   `json:"hidden_by_owner" doc:"Profiles the owner removed on this server; always empty today"`
	Overrides              []ProfileSetOverride       `json:"overrides" doc:"Values the owner set on this server; always empty today"`
	SkippedSettings        []ProfileSetSkippedSetting `json:"skipped_settings" doc:"Known settings whose value this server refused; it kept its own value"`
	JellyfinBareNameSignIn *bool                      `json:"jellyfin_bare_name_sign_in,omitempty" doc:"Whether signing in on the Jellyfin-compatible surface with the bare account name (no #profile) picks a profile, as the account's profiles stand after the set: true when exactly one profile is named like the account, or else exactly one profile has no PIN. Absent while that surface is off" example:"true"`
}

// ProfileSetPutOutput is the putProfileSet response.
type ProfileSetPutOutput struct {
	Body ProfileSetApplied
}

// ProfileSetFieldChange is a field edited on this server outside a set.
type ProfileSetFieldChange struct {
	Field string  `json:"field" example:"name"`
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
	ChangedHere        []ProfileSetFieldChange `json:"changed_here" doc:"Fields edited on this server outside a set since the last set it accepted; always empty today"`
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
	DeletedHere            []ProfileSetDeletion `json:"deleted_here" doc:"Profiles of the set deleted on this server outside a set; always empty today"`
	Overrides              []ProfileSetOverride `json:"overrides" doc:"Values the owner set on this server; always empty today"`
	LocalOnly              []ProfileSetLocal    `json:"local_only" doc:"Profiles on this account that are not part of the set"`
	JellyfinBareNameSignIn *bool                `json:"jellyfin_bare_name_sign_in,omitempty" doc:"Whether signing in on the Jellyfin-compatible surface with the bare account name (no #profile) picks a profile as the account's profiles now stand: true when exactly one profile is named like the account, or else exactly one profile has no PIN. Absent while that surface is off" example:"true"`
	EditsToken             string               `json:"edits_token" doc:"Names the person's own edits on this server that no set has settled; empty when there are none. A set sends it back as edits_seen" example:""`
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
	"Sessions opened by device sign-in, by another app's key, or by an administrator acting as the account, remote-playback handoffs, and sessions older than the record of how sessions open are 403 permission_denied; an API key carries no login session and is 401 authentication_required. In demo mode a non-administrator is 403 permission_denied in both forms. " +
	"The first item is the main profile; items match profiles by sync_id, then by pairs, and are otherwise created in set order up to the household's profile limit (the rest are left_off; nothing is deleted to fit). " +
	"A profile holding a sync_id the set no longer has is deleted with everything that belongs to it; a profile with no sync_id that pairs does not name is left alone. " +
	"Only a newer version changes anything: an older version, the same version with other content, the same version whose PINs differ from the stored ones, and a set without the person's latest edits (edits_seen) are 409 profile_set_stale; the same version and content again answers 200 and writes nothing. " +
	"A set that changes the main profile's PIN ends every X-Profile-Token minted for its old PIN, the caller's included. " +
	"A malformed member is 422 validation_failed at its member (at body.payload in the signed form); a new item named like a profile outside the set is 422 with code name_conflict. " +
	"Spends the per-address profile_set rate-limit budget; the signed form also spends its challenge. A body over 256 KiB is 413 payload_too_large. " +
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
	get.Description = "The same sessions as putProfileSet's session form may read it, without the main profile's PIN. An account that never took a set answers version 0, no items, and every profile in local_only. changed_here, deleted_here and overrides are always empty today. Strong ETag; If-None-Match answers 304. 501 capability_unsupported where profiles are not kept in PostgreSQL."
	get.Errors = []int{http.StatusForbidden, http.StatusNotImplemented}
	Register(reg, Operation{Operation: get, Class: ClassProfileScoped, ProfileOptional: true, ServiceBacked: true, Conditional: true}, reg.getProfileSet)
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
	if demoRestricted(ctx, reg.deps.DemoSettings) {
		return nil, NewProblem(TypePermissionDenied, "This action is not available in demo mode.")
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
	result, err := sets.ApplyProfileSet(withClientDevice(ctx), cmd)
	if err != nil {
		return nil, profileSetProblem(err, signed)
	}
	return &ProfileSetPutOutput{Body: profileSetAppliedOf(result)}, nil
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
		return handlers.ProfileSetCommand{}, profileSetProblem(err, true)
	}
	return handlers.ProfileSetCommand{UserID: authority.UserID, Set: authority.Set, Pairs: authority.Pairs,
		EditsSeen: authority.EditsSeen, Signed: true}, nil
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
	if err := svc.ProfileSetSessionAllowed(ctx, claims); err != nil {
		return appKeyProblem(err)
	}
	return nil
}

// profileSetProblem maps a profile set refusal to its problem.
func profileSetProblem(err error, signed bool) error {
	var field *handlers.ProfileSetFieldError
	switch {
	case errors.As(err, &field):
		location := locationBody + "." + field.Location
		if signed {
			location = locationBody + ".payload"
		}
		return validationProblem(location, field.Code, field.Message)
	case errors.Is(err, handlers.ErrProfileSetNoPrimary):
		return NewProblem(TypeConflict, "This account's profiles are in a state the server can't apply a profile set to: it has profiles but no main profile.")
	case errors.Is(err, handlers.ErrProfileSetStale):
		return NewProblem(TypeProfileSetStale, "This server holds a newer profile set, or the same version with other content; read it and send a newer one.")
	case errors.Is(err, auth.ErrProfileSetAdminAccount):
		return NewProblem(TypePermissionDenied, "An administrator's profiles change only from a session verified as the main profile.")
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
		HiddenByOwner: []string{}, Overrides: []ProfileSetOverride{},
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
		return nil, profileSetProblem(err, false)
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
		DeletedHere: []ProfileSetDeletion{}, Overrides: []ProfileSetOverride{},
		LocalOnly: make([]ProfileSetLocal, 0, len(h.LocalOnly)), EditsToken: h.EditsToken,
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
			ChangedHere: []ProfileSetFieldChange{},
		})
	}
	for _, local := range h.LocalOnly {
		out.LocalOnly = append(out.LocalOnly, ProfileSetLocal{ID: ID(local.ID), Name: local.Name, HasPIN: local.HasPIN})
	}
	return out
}
