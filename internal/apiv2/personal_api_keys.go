package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/Silo-Server/silo-server/internal/access"
	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
	"github.com/Silo-Server/silo-server/internal/userstore"
)

// PersonalAPIKeyService keeps credentials scoped to the login account, independently of profiles.
type PersonalAPIKeyService interface {
	// ListPersonalAPIKeysPage and RevokePersonalAPIKey take the profile the
	// caller is limited to (PersonalAPIKeyView), "" for the whole account.
	ListPersonalAPIKeysPage(context.Context, int, string, *auth.APIKeyPageKey, int) ([]handlers.APIKeyListItem, bool, error)
	RevokePersonalAPIKey(context.Context, int, string, int64) error
	// PersonalAPIKeyView decides which keys (account, acting profile) may
	// list and revoke: the profile to narrow to, and whether any at all.
	PersonalAPIKeyView(context.Context, int, string) (string, bool, error)
	CreateAdminAPIKey(context.Context, int, string, []string) (*models.APIKey, error)
	// MayCreatePersonalAPIKey applies the household-manager rule to an
	// admin's account key: (account, acting profile, PIN verifier).
	MayCreatePersonalAPIKey(context.Context, int, string, func(*userstore.Profile) error) (bool, error)
	// UserAPIKeysEnabled reads the server setting behind every other key.
	UserAPIKeysEnabled(context.Context) (bool, error)
	// MayCreateProfileAPIKey authorizes such a key: (account, acting
	// profile, profile to bind or "", PIN verifier). It returns the acting
	// profile as verified.
	MayCreateProfileAPIKey(context.Context, int, string, string, func(*userstore.Profile) error) (bool, auth.APIKeyActingProfile, error)
	// CreatePersonalAPIKey mints it: (account, acting profile as verified,
	// profile to bind or "", label, scopes).
	CreatePersonalAPIKey(context.Context, int, auth.APIKeyActingProfile, string, string, []string) (*models.APIKey, error)
}

type PersonalAPIKeyListItem struct {
	AdminAPIKey
	LastUsedAt *Instant `json:"last_used_at,omitempty"`
}
type PersonalAPIKeyListOutput struct {
	Body Collection[PersonalAPIKeyListItem]
}
type PersonalAPIKeyCreateInput struct {
	RawBody []byte
	Body    struct {
		Label     string   `json:"label" minLength:"1"`
		Scopes    []string `json:"scopes,omitempty"`
		ProfileID string   `json:"profile_id,omitempty" minLength:"1" doc:"Bind the key to this profile of the account: it always acts as that profile and cannot name another. Requires user_keys_enabled and user scopes"`
	}
}
type PersonalAPIKeyIDInput struct {
	ID ID `path:"id" pattern:"^[1-9][0-9]*$"`
}
type PersonalAPIKeyCreatedOutput struct{ Body AdminAPIKeyCreated }
type PersonalAPIKeyScopesOutput struct {
	Body struct {
		Available bool               `json:"available"`
		Scopes    []auth.APIKeyScope `json:"scopes"`
		// UserKeysEnabled is the server setting auth.user_api_keys.
		UserKeysEnabled bool `json:"user_keys_enabled" doc:"Whether accounts that are not admins, and profiles other than the primary one, may create keys. Such a key, and any key bound to a profile, carries scopes whose audience is user"`
		MaxUserKeys     int  `json:"max_user_keys" doc:"How many keys an account may hold before it can create no more for itself"`
	}
}

func personalAPIKeyAccount(ctx context.Context) (int, *Problem) {
	claims := claimsFrom(ctx)
	if claims == nil || claims.UserID <= 0 {
		return 0, NewProblem(TypeAuthenticationRequired, "Authentication required.")
	}
	if claims.TokenType == auth.TokenTypeAPIKey {
		return 0, NewProblem(TypePermissionDenied, "API key management requires a login session.")
	}
	return claims.UserID, nil
}

// personalAPIKeyView resolves which of the account's keys the acting profile
// may list and revoke (PersonalAPIKeyService.PersonalAPIKeyView).
func (reg *Registry) personalAPIKeyView(ctx context.Context, userID int) (string, *Problem) {
	only, allowed, err := reg.deps.PersonalAPIKeys.PersonalAPIKeyView(ctx, userID, profileFrom(ctx))
	if err != nil {
		return "", serviceProblem(err)
	}
	if !allowed {
		return "", NewProblem(TypePermissionDenied, "This profile cannot manage API keys.")
	}
	return only, nil
}

// personalAPIKeyProblem maps the refusals of a key an account creates for
// itself; everything else is adminAPIKeyProblem's.
func personalAPIKeyProblem(ctx context.Context, err error) *Problem {
	switch {
	case errors.Is(err, auth.ErrUserAPIKeysDisabled):
		return NewProblem(TypePermissionDenied, "Only server admins can create API keys.")
	case errors.Is(err, auth.ErrAPIKeyUserScopesRequired):
		return NewProblem(TypeValidationFailed, "Invalid API key configuration.").
			WithErrors(ProblemError{Location: locationBody + ".scopes", Code: codeInvalid, Detail: "this key needs at least one scope, and only scopes whose audience is user"})
	case errors.Is(err, auth.ErrAPIKeyProfileNotFound):
		return NewProblem(TypeValidationFailed, "Invalid API key configuration.").
			WithErrors(ProblemError{Location: locationBody + ".profile_id", Code: codeInvalid, Detail: "no such profile on this account"})
	case errors.Is(err, auth.ErrAPIKeyActingProfileChanged):
		return NewProblem(TypeProfileVerificationRequired, "Creating API keys requires verifying the profile PIN.")
	case errors.Is(err, auth.ErrAPIKeyLimitReached):
		return NewProblem(TypeConflict, "This account holds as many API keys as it may; revoke one first.")
	}
	return adminAPIKeyProblem(ctx, err)
}

func personalAPIKeyCreated(row *models.APIKey) *PersonalAPIKeyCreatedOutput {
	scopes := row.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return &PersonalAPIKeyCreatedOutput{Body: AdminAPIKeyCreated{ID: ID(strconv.FormatInt(row.ID, 10)), UserID: ID(strconv.Itoa(row.UserID)), Label: row.Label, Key: row.Key, RateTier: row.RateTier, Scopes: scopes, ProfileID: row.ProfileID, CreatedAt: NewInstant(row.CreatedAt)}}
}

// createUserAPIKey mints a key that is not an admin's account key: one bound
// to a profile, or any key on an account that is not an admin.
func (reg *Registry) createUserAPIKey(ctx context.Context, userID int, in *PersonalAPIKeyCreateInput) (*PersonalAPIKeyCreatedOutput, error) {
	svc := reg.deps.PersonalAPIKeys
	enabled := false
	if svc != nil {
		var err error
		if enabled, err = svc.UserAPIKeysEnabled(ctx); err != nil {
			return nil, serviceProblem(err)
		}
	}
	if !enabled {
		return nil, personalAPIKeyProblem(ctx, auth.ErrUserAPIKeysDisabled)
	}
	if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
		return nil, p
	}
	allowed, acting, err := svc.MayCreateProfileAPIKey(ctx, userID, profileFrom(ctx), in.Body.ProfileID, verifyHouseholdProfile(ctx))
	if errors.Is(err, access.ErrProfileUnverified) {
		return nil, NewProblem(TypeProfileVerificationRequired, "Creating API keys requires verifying the profile PIN.")
	}
	if err != nil {
		return nil, serviceProblem(err)
	}
	if !allowed {
		return nil, NewProblem(TypePermissionDenied, "A profile can create API keys only for itself; the account's primary profile can create them for any profile.")
	}
	row, err := svc.CreatePersonalAPIKey(ctx, userID, acting, in.Body.ProfileID, in.Body.Label, in.Body.Scopes)
	if err != nil {
		return nil, personalAPIKeyProblem(ctx, err)
	}
	return personalAPIKeyCreated(row), nil
}

func registerPersonalAPIKeys(reg *Registry) {
	op := func(method, path, id string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "api-keys", "Manage the login account's API keys."), Class: ClassAuthenticated, DemoRestricted: isMutatingMethod(method), ServiceBacked: true}
		// Creating and revoking a key are credential actions the audit log records.
		o.Audited = isMutatingMethod(method)
		if method == http.MethodPost {
			o.RetrySafety = RetrySafetyNonRetryable
			o.DefaultStatus = http.StatusCreated
		}
		if method == http.MethodDelete {
			o.RetrySafety = RetrySafetyNaturalIdempotent
			o.DefaultStatus = http.StatusNoContent
		}
		return o
	}
	Register(reg, op(http.MethodGet, "/api-keys/scopes", "getPersonalAPIKeyScopes"), func(ctx context.Context, _ *struct{}) (*PersonalAPIKeyScopesOutput, error) {
		out := new(PersonalAPIKeyScopesOutput)
		out.Body.Available = reg.deps.PersonalAPIKeys != nil
		out.Body.Scopes = auth.APIKeyScopeCatalog()
		out.Body.MaxUserKeys = auth.MaxUserAPIKeys
		if reg.deps.PersonalAPIKeys != nil {
			enabled, err := reg.deps.PersonalAPIKeys.UserAPIKeysEnabled(ctx)
			if err != nil {
				return nil, serviceProblem(err)
			}
			out.Body.UserKeysEnabled = enabled
		}
		return out, nil
	})
	cursors := NewCursors(reg.deps.CursorSecret)
	list := op(http.MethodGet, "/api-keys", "listPersonalAPIKeys")
	// An API key cannot list keys: 403 (personalAPIKeyAccount).
	list.Errors = append(list.Errors, http.StatusForbidden)
	// Profile scoped, profile optional: the primary profile sees every key on
	// the account, any other profile only the keys bound to it.
	list.Class = ClassProfileScoped
	list.ProfileOptional = true
	list.HouseholdProfileGate = true
	list.Description = "The account's primary profile lists every key on the account; any other profile lists only the keys bound to it. A request without X-Profile-Id lists them all while no profile on the account is PIN-protected or access-restricted, and is otherwise 422, as on the catalog reads. This is for a login session: an API key cannot list keys and receives 403 permission_denied."
	Register(reg, list, func(ctx context.Context, in *CursorListInput) (*PersonalAPIKeyListOutput, error) {
		userID, p := personalAPIKeyAccount(ctx)
		if p != nil {
			return nil, p
		}
		if reg.deps.PersonalAPIKeys == nil {
			return nil, unavailable("API keys")
		}
		only, p := reg.personalAPIKeyView(ctx, userID)
		if p != nil {
			return nil, p
		}
		scope := CursorScope{OperationID: "listPersonalAPIKeys", Security: strconv.Itoa(userID), Filter: "limit=" + strconv.Itoa(in.Limit), Sort: "created_at:desc", Tiebreaker: "id:desc"}
		// A cursor minted for one view of the keys does not continue another.
		scope.Security += "/" + only
		var after *auth.APIKeyPageKey
		if in.Cursor != "" {
			after = new(auth.APIKeyPageKey)
			if p := cursors.Decode(scope, in.Cursor, after); p != nil {
				return nil, p
			}
			if after.ID <= 0 || after.CreatedAt.IsZero() {
				return nil, NewProblem(TypeInvalidCursor, "Invalid API key cursor.")
			}
		}
		rows, more, err := reg.deps.PersonalAPIKeys.ListPersonalAPIKeysPage(ctx, userID, only, after, in.Limit)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		items := make([]PersonalAPIKeyListItem, 0, len(rows))
		for _, row := range rows {
			item := PersonalAPIKeyListItem{AdminAPIKey: adminAPIKeyOf(&row.APIKeyConfiguration)}
			if row.LastUsedAt != nil {
				item.LastUsedAt = new(NewInstant(*row.LastUsedAt))
			}
			items = append(items, item)
		}
		next := ""
		if more {
			if len(rows) == 0 {
				return nil, NewProblem(TypeInternalError, "Unable to page API keys.")
			}
			last := rows[len(rows)-1]
			next, err = cursors.Encode(scope, auth.APIKeyPageKey{ID: last.ID, CreatedAt: last.CreatedAt})
			if err != nil {
				return nil, serviceProblem(err)
			}
		}
		return &PersonalAPIKeyListOutput{Body: Paginated(items, next)}, nil
	})
	// Creation is profile scoped, profile optional, like createProfile: the
	// key skips every profile's PIN, so only the household manager — on an
	// admin account, the acting admin through the verified primary profile —
	// may mint one, and the viewer gate must resolve that profile first.
	create := op(http.MethodPost, "/api-keys", "createPersonalAPIKey")
	create.Class = ClassProfileScoped
	create.ProfileOptional = true
	create.Description = "A server admin's login session creates an account key (no profile_id) acting through the account's primary profile: X-Profile-Id must name the primary profile, with X-Profile-Token when that profile is PIN-protected (without it the request is 403 profile_verification_required); naming any other profile is 403 permission_denied. A request without X-Profile-Id is accepted only while no profile on the account is PIN-protected or access-restricted (content-rating, advisory-age or library limits); otherwise it is 403 permission_denied. While user_keys_enabled is true (getPersonalAPIKeyScopes), any account may also create keys that carry user scopes only: the primary profile an account key or a key bound to any profile of the account (profile_id), and any other profile a key bound to itself, each with its own X-Profile-Token when PIN-protected. Past max_user_keys the request is 409 conflict."
	create.Errors = append(create.Errors, http.StatusConflict)
	Register(reg, create, func(ctx context.Context, in *PersonalAPIKeyCreateInput) (*PersonalAPIKeyCreatedOutput, error) {
		userID, p := personalAPIKeyAccount(ctx)
		if p != nil {
			return nil, p
		}
		// An admin's account key keeps its own rule below. Every other key
		// (one bound to a profile, or any key on an account that is not an
		// admin) exists only while the server allows user keys; with the
		// setting off only server admins create API keys (#1189 AC2).
		// Listing and revocation stay open so an account can still see and
		// revoke keys it already holds.
		svc := reg.deps.PersonalAPIKeys
		if claimsFrom(ctx).Role != models.RoleAdmin || in.Body.ProfileID != "" {
			return reg.createUserAPIKey(ctx, userID, in)
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		if svc == nil {
			return nil, unavailable("API keys")
		}
		allowed, err := reg.deps.PersonalAPIKeys.MayCreatePersonalAPIKey(ctx, userID, profileFrom(ctx), verifyHouseholdProfile(ctx))
		if errors.Is(err, access.ErrProfileUnverified) {
			return nil, NewProblem(TypeProfileVerificationRequired, "Creating API keys requires verifying the primary profile PIN.")
		}
		if err != nil {
			return nil, serviceProblem(err)
		}
		if !allowed {
			return nil, NewProblem(TypePermissionDenied, "Creating API keys requires the account's primary profile.")
		}
		row, err := reg.deps.PersonalAPIKeys.CreateAdminAPIKey(ctx, userID, in.Body.Label, in.Body.Scopes)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		return personalAPIKeyCreated(row), nil
	})
	// Revocation sees the keys listing does.
	revoke := op(http.MethodDelete, "/api-keys/{id}", "revokePersonalAPIKey")
	revoke.Class = ClassProfileScoped
	revoke.ProfileOptional = true
	revoke.HouseholdProfileGate = true
	revoke.Description = "Revokes a key the caller can list (listPersonalAPIKeys); any other key is 404. This is for a login session: an API key cannot revoke keys and receives 403 permission_denied."
	Register(reg, revoke, func(ctx context.Context, in *PersonalAPIKeyIDInput) (*struct{}, error) {
		userID, p := personalAPIKeyAccount(ctx)
		if p != nil {
			return nil, p
		}
		if reg.deps.PersonalAPIKeys == nil {
			return nil, unavailable("API keys")
		}
		id, p := adminAPIKeyID(in.ID)
		if p != nil {
			return nil, p
		}
		only, p := reg.personalAPIKeyView(ctx, userID)
		if p != nil {
			return nil, p
		}
		if err := reg.deps.PersonalAPIKeys.RevokePersonalAPIKey(ctx, userID, only, id); err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		return nil, nil
	})
}
