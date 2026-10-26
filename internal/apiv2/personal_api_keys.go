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
)

// PersonalAPIKeyService keeps credentials scoped to the login account, independently of profiles.
type PersonalAPIKeyService interface {
	ListPersonalAPIKeysPage(context.Context, int, *auth.APIKeyPageKey, int) ([]handlers.APIKeyListItem, bool, error)
	CreateAdminAPIKey(context.Context, int, string, []string) (*models.APIKey, error)
	RevokePersonalAPIKey(context.Context, int, int64) error
	// MayCreatePersonalAPIKey applies the household-manager rule to key
	// creation: (account, acting profile, PIN verifier).
	MayCreatePersonalAPIKey(context.Context, int, string, func(string) error) (bool, error)
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
		Label  string   `json:"label" minLength:"1"`
		Scopes []string `json:"scopes,omitempty"`
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

func registerPersonalAPIKeys(reg *Registry) {
	op := func(method, path, id string) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "api-keys", "Manage the login account's API keys."), Class: ClassAuthenticated, DemoRestricted: isMutatingMethod(method), ServiceBacked: true}
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
	Register(reg, op(http.MethodGet, "/api-keys/scopes", "getPersonalAPIKeyScopes"), func(_ context.Context, _ *struct{}) (*PersonalAPIKeyScopesOutput, error) {
		out := new(PersonalAPIKeyScopesOutput)
		out.Body.Available = reg.deps.PersonalAPIKeys != nil
		out.Body.Scopes = auth.APIKeyScopeCatalog()
		return out, nil
	})
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op(http.MethodGet, "/api-keys", "listPersonalAPIKeys"), func(ctx context.Context, in *CursorListInput) (*PersonalAPIKeyListOutput, error) {
		userID, p := personalAPIKeyAccount(ctx)
		if p != nil {
			return nil, p
		}
		if reg.deps.PersonalAPIKeys == nil {
			return nil, unavailable("API keys")
		}
		scope := CursorScope{OperationID: "listPersonalAPIKeys", Security: strconv.Itoa(userID), Filter: "limit=" + strconv.Itoa(in.Limit), Sort: "created_at:desc", Tiebreaker: "id:desc"}
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
		rows, more, err := reg.deps.PersonalAPIKeys.ListPersonalAPIKeysPage(ctx, userID, after, in.Limit)
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
	create.Description = "Only a server admin's login session may create a key, acting through the account's primary profile. X-Profile-Id must name the primary profile, with X-Profile-Token when that profile is PIN-protected (without it the request is 403 profile_verification_required); naming any other profile is 403 permission_denied. A request without X-Profile-Id is accepted only while no profile on the account is PIN-protected or access-restricted (content-rating, advisory-age or library limits); otherwise it is 403 permission_denied."
	Register(reg, create, func(ctx context.Context, in *PersonalAPIKeyCreateInput) (*PersonalAPIKeyCreatedOutput, error) {
		userID, p := personalAPIKeyAccount(ctx)
		if p != nil {
			return nil, p
		}
		// Only server admins create API keys (#1189 AC2). Listing and revocation
		// stay open so any account can still see and revoke keys it already owns.
		if claimsFrom(ctx).Role != models.RoleAdmin {
			return nil, NewProblem(TypePermissionDenied, "Only server admins can create API keys.")
		}
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		if reg.deps.PersonalAPIKeys == nil {
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
		scopes := row.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		return &PersonalAPIKeyCreatedOutput{Body: AdminAPIKeyCreated{ID: ID(strconv.FormatInt(row.ID, 10)), UserID: ID(strconv.Itoa(row.UserID)), Label: row.Label, Key: row.Key, RateTier: row.RateTier, Scopes: scopes, CreatedAt: NewInstant(row.CreatedAt)}}, nil
	})
	Register(reg, op(http.MethodDelete, "/api-keys/{id}", "revokePersonalAPIKey"), func(ctx context.Context, in *PersonalAPIKeyIDInput) (*struct{}, error) {
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
		if err := reg.deps.PersonalAPIKeys.RevokePersonalAPIKey(ctx, userID, id); err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		return nil, nil
	})
}
