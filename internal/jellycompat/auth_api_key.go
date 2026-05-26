package jellycompat

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/models"
)

type adminAPIKeyContextKey string

const adminAPIKeyKey adminAPIKeyContextKey = "jellycompat_admin_api_key"

type apiKeyValidator interface {
	GetByKey(ctx context.Context, key string) (*models.APIKey, error)
	UpdateLastUsed(ctx context.Context, id int64) error
}

type apiKeyUserLoader interface {
	GetByID(ctx context.Context, id int) (*models.User, error)
}

type AdminAPIKeyAuthenticator struct {
	keys  apiKeyValidator
	users apiKeyUserLoader
}

type adminAPIKeyAuthResult struct {
	ctx     context.Context
	status  int
	code    string
	message string
	ok      bool
}

func NewAdminAPIKeyAuthenticator(keys apiKeyValidator, users apiKeyUserLoader) *AdminAPIKeyAuthenticator {
	if keys == nil || users == nil {
		return nil
	}
	return &AdminAPIKeyAuthenticator{keys: keys, users: users}
}

func AdminAPIKeyFromContext(ctx context.Context) bool {
	ok, _ := ctx.Value(adminAPIKeyKey).(bool)
	return ok
}

func (a *AdminAPIKeyAuthenticator) RequireAdminAPIKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := a.authenticate(r)
		if !result.ok {
			writeError(w, result.status, result.code, result.message)
			return
		}
		next.ServeHTTP(w, r.WithContext(result.ctx))
	})
}

func RequireSessionOrAdminAPIKey(sessionAuth *Authenticator, keyAuth *AdminAPIKeyAuthenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token, ok := ExtractToken(r)
			if ok && strings.HasPrefix(token, "sa_") {
				result := keyAuth.authenticate(r)
				if !result.ok {
					writeError(w, result.status, result.code, result.message)
					return
				}
				next.ServeHTTP(w, r.WithContext(result.ctx))
				return
			}
			sessionAuth.RequireSession(next).ServeHTTP(w, r)
		})
	}
}

func (a *AdminAPIKeyAuthenticator) authenticate(r *http.Request) adminAPIKeyAuthResult {
	unauthorized := adminAPIKeyAuthResult{
		ctx:     r.Context(),
		status:  http.StatusUnauthorized,
		code:    "Unauthorized",
		message: "Invalid API key",
	}
	if a == nil || a.keys == nil || a.users == nil {
		return unauthorized
	}
	token, ok := ExtractToken(r)
	if !ok || !strings.HasPrefix(token, "sa_") {
		return unauthorized
	}
	apiKey, err := a.keys.GetByKey(r.Context(), token)
	if err != nil || apiKey == nil {
		return unauthorized
	}
	user, err := a.users.GetByID(r.Context(), apiKey.UserID)
	if err != nil || user == nil || !user.Enabled {
		return unauthorized
	}
	if user.Role != "admin" {
		return adminAPIKeyAuthResult{
			ctx:     r.Context(),
			status:  http.StatusForbidden,
			code:    "Forbidden",
			message: "Admin access required",
		}
	}
	go func(id int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.keys.UpdateLastUsed(ctx, id); err != nil {
			slog.Debug("jellycompat api key last-used update failed", "id", id, "error", err)
		}
	}(apiKey.ID)
	return adminAPIKeyAuthResult{
		ctx:    context.WithValue(r.Context(), adminAPIKeyKey, true),
		status: http.StatusOK,
		ok:     true,
	}
}
