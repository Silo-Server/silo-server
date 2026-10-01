package apiv2

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Silo-Server/silo-server/internal/api/handlers"
	"github.com/Silo-Server/silo-server/internal/auth"
	"github.com/Silo-Server/silo-server/internal/models"
)

// AdminAPIKeyService separates credential creation from secret-free administration.
type AdminAPIKeyService interface {
	GetAdminAPIKey(context.Context, int64) (*handlers.APIKeyConfiguration, error)
	ListAdminAPIKeysPage(context.Context, *auth.APIKeyPageKey, int) ([]handlers.AdminAPIKeyListItem, bool, error)
	CreateAdminAPIKey(context.Context, int, string, []string) (*models.APIKey, error)
	UpdateAdminAPIKeyTier(context.Context, int64, string, auth.APIKeyPrecondition) (*handlers.APIKeyConfiguration, error)
	DeleteAdminAPIKey(context.Context, int64, auth.APIKeyPrecondition) error
}

// AdminAPIKey contains only revision-tracked configuration. Usage belongs to lists.
type AdminAPIKey struct {
	ID        ID       `json:"id"`
	UserID    ID       `json:"user_id"`
	Label     string   `json:"label"`
	KeyPrefix string   `json:"key_prefix"`
	RateTier  string   `json:"rate_tier" enum:"standard,elevated"`
	Scopes    []string `json:"scopes"`
	CreatedAt Instant  `json:"created_at"`
}
type AdminAPIKeyListItem struct {
	AdminAPIKey
	Username   string   `json:"username"`
	LastUsedAt *Instant `json:"last_used_at,omitempty"`
}
type AdminAPIKeyOutput struct {
	Status int
	ETag   string `header:"ETag"`
	Body   AdminAPIKey
}
type AdminAPIKeyCollectionOutput struct {
	Body Collection[AdminAPIKeyListItem]
}
type AdminAPIKeyInput struct {
	ID          ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
}
type AdminAPIKeyTierInput struct {
	ID          ID     `path:"id" pattern:"^[1-9][0-9]*$"`
	IfMatch     string `header:"If-Match"`
	IfNoneMatch string `header:"If-None-Match"`
	Body        struct {
		RateTier string `json:"rate_tier" enum:"standard,elevated"`
	}
}
type AdminAPIKeyCreateInput struct {
	RawBody []byte
	Body    struct {
		Label  string   `json:"label" minLength:"1"`
		UserID ID       `json:"user_id,omitempty" pattern:"^[1-9][0-9]*$"`
		Scopes []string `json:"scopes,omitempty"`
	}
}
type AdminAPIKeyCreated struct {
	ID        ID       `json:"id"`
	UserID    ID       `json:"user_id"`
	Label     string   `json:"label"`
	Key       string   `json:"key" doc:"Credential disclosed only in this creation response. Store it securely."`
	RateTier  string   `json:"rate_tier"`
	Scopes    []string `json:"scopes"`
	CreatedAt Instant  `json:"created_at"`
}
type AdminAPIKeyCreatedOutput struct {
	Location string `header:"Location"`
	Body     AdminAPIKeyCreated
}
type AdminAPIKeyCapabilitiesOutput struct {
	Status       int
	ETag         string `header:"ETag"`
	CacheControl string `header:"Cache-Control"`
	Body         AdminAPIKeyCapabilitiesOutputBody
}

type AdminAPIKeyCapabilitiesOutputBody struct {
	Capability
	Available            bool               `json:"available"`
	GuardedConfiguration bool               `json:"guarded_configuration"`
	Scopes               []auth.APIKeyScope `json:"scopes"`
	RateTiers            []string           `json:"rate_tiers"`
}

const adminAPIKeyStandardTier = "standard"
const adminAPIKeyElevatedTier = "elevated"
const adminAPIKeyPath = "/admin/api-keys"
const listAdminAPIKeysOperation = "listAdminAPIKeys"

func adminAPIKeyOf(k *handlers.APIKeyConfiguration) AdminAPIKey {
	scopes := k.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return AdminAPIKey{ID: ID(strconv.FormatInt(k.ID, 10)), UserID: ID(strconv.Itoa(k.UserID)), Label: k.Label, KeyPrefix: k.KeyPrefix, RateTier: k.RateTier, Scopes: scopes, CreatedAt: NewInstant(k.CreatedAt)}
}
func adminAPIKeyTag(ctx context.Context, id, revision int64) EntityTag {
	return RenderETag("admin-api-keys/"+strconv.Itoa(claimsFrom(ctx).UserID)+"/"+profileFrom(ctx), strconv.FormatInt(id, 10), revision)
}
func adminAPIKeyProblem(ctx context.Context, err error) *Problem {
	if conflict, ok := errors.AsType[*auth.APIKeyRevisionConflict](err); ok && conflict.Current != nil {
		return NewProblem(TypePreconditionFailed, "The API key changed; reload before saving.").WithHeader("ETag", adminAPIKeyTag(ctx, conflict.Current.ID, conflict.Current.Revision).String())
	}
	if scopeErr, ok := errors.AsType[*auth.UnknownAPIKeyScopeError](err); ok {
		return NewProblem(TypeValidationFailed, "Invalid API key configuration.").
			WithErrors(ProblemError{Location: locationBody + ".scopes", Code: codeInvalid, Detail: "unknown api key scope " + strconv.Quote(scopeErr.Scope)})
	}
	if _, ok := errors.AsType[*handlers.APIError](err); ok {
		return serviceProblem(err)
	}
	switch {
	case errors.Is(err, auth.ErrAPIKeyNotFound):
		return NewProblem(TypeNotFound, "API key not found.")
	case errors.Is(err, auth.ErrInvalidAPIKeyTier), errors.Is(err, handlers.ErrInvalidAPIKeyCreation):
		return NewProblem(TypeValidationFailed, "Invalid API key configuration.")
	default:
		return NewProblem(TypeInternalError, "Unable to manage API key.")
	}
}
func (reg *Registry) adminAPIKeys() (AdminAPIKeyService, *Problem) {
	if reg.deps.AdminAPIKeys == nil {
		return nil, NewProblem(TypeDependencyUnavailable, "API key administration is unavailable.")
	}
	return reg.deps.AdminAPIKeys, nil
}
func adminAPIKeyID(id ID) (int64, *Problem) {
	n, p := id.positive64("path.id")
	if p != nil {
		return 0, NewProblem(TypeValidationFailed, "Invalid API key ID.")
	}
	return n, nil
}
func adminAPIKeyGuard(in AdminAPIKeyInput, ctx context.Context, row *handlers.APIKeyConfiguration) (auth.APIKeyPrecondition, *Problem) {
	if p := EvaluateGuardedPreconditions(in.IfMatch, in.IfNoneMatch, adminAPIKeyTag(ctx, row.ID, row.Revision)); p != nil {
		return auth.APIKeyPrecondition{}, p
	}
	if strings.TrimSpace(in.IfMatch) == "*" {
		return auth.APIKeyPrecondition{Any: true}, nil
	}
	return auth.APIKeyPrecondition{Revision: row.Revision}, nil
}
func registerAdminAPIKeys(reg *Registry) {
	op := func(method, path, id string, guard bool) Operation {
		o := Operation{Operation: humaOp(method, Prefix+path, id, "admin", "Manage API key credentials."), Class: ClassActingAdmin, DemoRestricted: isMutatingMethod(method), ServiceBacked: true, Guarded: guard}
		if method != http.MethodGet {
			o.RetrySafety = RetrySafetyNonRetryable
		}
		return o
	}
	Register(reg, op(http.MethodGet, adminAPIKeyPath+"/capabilities", "getAdminAPIKeyCapabilities", false), func(_ context.Context, _ *CapabilityInput) (*AdminAPIKeyCapabilitiesOutput, error) {
		out := new(AdminAPIKeyCapabilitiesOutput)
		out.Body.Available = reg.deps.AdminAPIKeys != nil
		out.Body.GuardedConfiguration = out.Body.Available
		out.Body.Scopes = auth.APIKeyScopeCatalog()
		out.Body.RateTiers = []string{adminAPIKeyStandardTier, adminAPIKeyElevatedTier}
		return out, nil
	})
	cursors := NewCursors(reg.deps.CursorSecret)
	Register(reg, op(http.MethodGet, adminAPIKeyPath, listAdminAPIKeysOperation, false), func(ctx context.Context, in *CursorListInput) (*AdminAPIKeyCollectionOutput, error) {
		svc, p := reg.adminAPIKeys()
		if p != nil {
			return nil, p
		}
		scope := CursorScope{OperationID: listAdminAPIKeysOperation, Security: strconv.Itoa(claimsFrom(ctx).UserID) + "/" + profileFrom(ctx), Filter: "v1/limit=" + strconv.Itoa(in.Limit), Sort: "created_at:desc", Tiebreaker: "id:desc"}
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
		rows, more, err := svc.ListAdminAPIKeysPage(ctx, after, in.Limit)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		items := make([]AdminAPIKeyListItem, 0, len(rows))
		for _, row := range rows {
			item := AdminAPIKeyListItem{AdminAPIKey: adminAPIKeyOf(&row.APIKeyConfiguration), Username: row.Username}
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
			next, err = cursors.Encode(scope, auth.APIKeyPageKey{CreatedAt: last.CreatedAt, ID: last.ID})
			if err != nil {
				return nil, NewProblem(TypeInternalError, "Unable to encode cursor.")
			}
		}
		return &AdminAPIKeyCollectionOutput{Body: Paginated(items, next)}, nil
	})
	get := op(http.MethodGet, adminAPIKeyPath+"/{id}", "getAdminAPIKey", false)
	get.Conditional = true
	Register(reg, get, func(ctx context.Context, in *AdminAPIKeyInput) (*AdminAPIKeyOutput, error) {
		svc, p := reg.adminAPIKeys()
		if p != nil {
			return nil, p
		}
		id, p := adminAPIKeyID(in.ID)
		if p != nil {
			return nil, p
		}
		row, err := svc.GetAdminAPIKey(ctx, id)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		tag := adminAPIKeyTag(ctx, row.ID, row.Revision)
		out := &AdminAPIKeyOutput{ETag: tag.String(), Body: adminAPIKeyOf(row)}
		if matched, p := EvaluateReadPreconditions(in.IfMatch, in.IfNoneMatch, tag); p != nil {
			return nil, p
		} else if matched {
			return NotModified(out, tag), nil
		}
		return out, nil
	})
	create := op(http.MethodPost, adminAPIKeyPath, "createAdminAPIKey", false)
	create.DefaultStatus = http.StatusCreated
	Register(reg, create, func(ctx context.Context, in *AdminAPIKeyCreateInput) (*AdminAPIKeyCreatedOutput, error) {
		if p := rejectNonNullableNulls(in.RawBody, nil); p != nil {
			return nil, p
		}
		svc, p := reg.adminAPIKeys()
		if p != nil {
			return nil, p
		}
		userID := claimsFrom(ctx).UserID
		if in.Body.UserID != "" {
			value, p := in.Body.UserID.positive("body.user_id")
			if p != nil {
				return nil, NewProblem(TypeValidationFailed, "Invalid account ID.")
			}
			userID = value
		}
		row, err := svc.CreateAdminAPIKey(ctx, userID, in.Body.Label, in.Body.Scopes)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		scopes := row.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		return &AdminAPIKeyCreatedOutput{Location: Prefix + adminAPIKeyPath + "/" + strconv.FormatInt(row.ID, 10), Body: AdminAPIKeyCreated{ID: ID(strconv.FormatInt(row.ID, 10)), UserID: ID(strconv.Itoa(row.UserID)), Label: row.Label, Key: row.Key, RateTier: row.RateTier, Scopes: scopes, CreatedAt: NewInstant(row.CreatedAt)}}, nil
	})
	Register(reg, op(http.MethodPut, adminAPIKeyPath+"/{id}/tier", "updateAdminAPIKeyTier", true), func(ctx context.Context, in *AdminAPIKeyTierInput) (*AdminAPIKeyOutput, error) {
		svc, p := reg.adminAPIKeys()
		if p != nil {
			return nil, p
		}
		id, p := adminAPIKeyID(in.ID)
		if p != nil {
			return nil, p
		}
		row, err := svc.GetAdminAPIKey(ctx, id)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		guard, p := adminAPIKeyGuard(AdminAPIKeyInput{ID: in.ID, IfMatch: in.IfMatch, IfNoneMatch: in.IfNoneMatch}, ctx, row)
		if p != nil {
			return nil, p
		}
		row, err = svc.UpdateAdminAPIKeyTier(ctx, id, in.Body.RateTier, guard)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		return &AdminAPIKeyOutput{ETag: adminAPIKeyTag(ctx, row.ID, row.Revision).String(), Body: adminAPIKeyOf(row)}, nil
	})
	Register(reg, op(http.MethodDelete, adminAPIKeyPath+"/{id}", "deleteAdminAPIKey", true), func(ctx context.Context, in *AdminAPIKeyInput) (*struct{}, error) {
		svc, p := reg.adminAPIKeys()
		if p != nil {
			return nil, p
		}
		id, p := adminAPIKeyID(in.ID)
		if p != nil {
			return nil, p
		}
		row, err := svc.GetAdminAPIKey(ctx, id)
		if err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		guard, p := adminAPIKeyGuard(*in, ctx, row)
		if p != nil {
			return nil, p
		}
		if err := svc.DeleteAdminAPIKey(ctx, id, guard); err != nil {
			return nil, adminAPIKeyProblem(ctx, err)
		}
		return nil, nil
	})
}

func (c AdminAPIKeyCapabilitiesOutputBody) capabilityState() string {
	return configuredCapabilityState(c.Available)
}
