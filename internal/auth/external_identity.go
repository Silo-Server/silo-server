package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/plugins"
)

// External sign-in refusals. Every transport maps them to its own shape; the
// v2 problem types carry the same identifiers
// (docs/architecture/external-sign-in.md).
var (
	// ErrNotPermitted: the provider's access rules refused the person, or
	// the sign-in has no account it may use (ErrAccountRequired is the
	// common case of the latter).
	ErrNotPermitted = errors.New("not permitted to sign in")
	// ErrAccountRequired: the provider admitted the person (its access rules
	// passed), but no account is linked or matched and account creation is
	// off, so an administrator has to add one. It is an ErrNotPermitted, so
	// the frozen v1 surface and the Jellyfin one keep answering not_permitted;
	// the v2 login, the web login page and the app redirect tell it apart.
	ErrAccountRequired = fmt.Errorf("%w: no account on this server, and account creation is off", ErrNotPermitted)
	// ErrEmailInUse: a new account would take an email another account
	// already holds, and email auto-match does not apply.
	ErrEmailInUse = errors.New("an account with this email already exists")
	// ErrIdentityLinkedElsewhere: the external identity already belongs to a
	// different account.
	ErrIdentityLinkedElsewhere = errors.New("external identity is linked to another account")
	// ErrAccountAlreadyLinked: the account already has an identity at this
	// provider.
	ErrAccountAlreadyLinked = errors.New("account is already linked to this provider")
	// ErrProviderUnavailable: the provider or its plugin could not answer.
	ErrProviderUnavailable = errors.New("sign-in provider unavailable")
	// ErrProviderPasswordExpired: the provider requires a new password first.
	ErrProviderPasswordExpired = errors.New("password expired at the sign-in provider")
	// ErrProviderAccountDisabled: the provider answered that the person's
	// account there is disabled, locked or expired. It is an ErrUserDisabled,
	// so sign-in treats it like a disabled Silo account; directory linking
	// tells the two apart.
	ErrProviderAccountDisabled = fmt.Errorf("account disabled at the sign-in provider: %w", ErrUserDisabled)
	// ErrIdentityNotFound: no identity with that id belongs to the account.
	ErrIdentityNotFound = errors.New("external identity not found")
	// ErrLastSignInMethod: removing the identity would leave the account no
	// way to sign in.
	ErrLastSignInMethod = errors.New("the account has no other way to sign in")
)

// ExternalIdentity is what a provider asserted about one person, normalized
// from the plugin's AuthenticateResponse.
type ExternalIdentity struct {
	// Subject is the plugin's exact external_subject (OIDC "iss|sub", LDAP
	// unique id). With the installation it keys the identity.
	Subject     string
	Issuer      string
	Username    string
	Email       string
	DisplayName string
	// EmailVerified is nil when the provider did not say.
	EmailVerified *bool
	ManagedRole   pluginv1.AuthManagedRole
	// RefreshState is the plugin's opaque state for later CheckAccount
	// calls: nil keeps the stored state, an empty Struct clears it.
	RefreshState *structpb.Struct
}

// emailVerified reports an explicit true. Missing counts as false.
func (i ExternalIdentity) emailVerified() bool {
	return i.EmailVerified != nil && *i.EmailVerified
}

// externalIdentityFromResponse validates a sign-in answer and normalizes it.
// A denial (or a response without a subject) becomes the matching refusal.
// Only the typed fields count: the free-form claims Struct is the plugin's
// own (SDK docs/auth-provider.md), and in particular email_verified, which
// gates email auto-match, is never taken from it.
func externalIdentityFromResponse(ctx context.Context, installationID int, response *pluginv1.AuthenticateResponse) (ExternalIdentity, error) {
	if response == nil {
		return ExternalIdentity{}, ErrInvalidCredentials
	}
	if denial := response.GetDenial(); denial != pluginv1.AuthDenial_AUTH_DENIAL_UNSPECIFIED {
		slog.InfoContext(ctx, "external sign-in refused by provider", "component", "auth",
			"installation_id", installationID, "denial", denial.String(), "detail", response.GetDenialDetail())
		return ExternalIdentity{}, denialError(denial)
	}
	subject := response.GetExternalSubject()
	if strings.TrimSpace(subject) == "" {
		return ExternalIdentity{}, ErrInvalidCredentials
	}
	return ExternalIdentity{
		Subject:       subject,
		Issuer:        strings.TrimSpace(response.GetIssuer()),
		Username:      strings.TrimSpace(response.GetUsername()),
		Email:         strings.TrimSpace(response.GetEmail()),
		DisplayName:   strings.TrimSpace(response.GetDisplayName()),
		EmailVerified: response.EmailVerified,
		ManagedRole:   response.GetManagedRole(),
		RefreshState:  response.GetRefreshState(),
	}, nil
}

func denialError(denial pluginv1.AuthDenial) error {
	switch denial {
	case pluginv1.AuthDenial_AUTH_DENIAL_NOT_PERMITTED:
		return ErrNotPermitted
	case pluginv1.AuthDenial_AUTH_DENIAL_ACCOUNT_DISABLED:
		return ErrProviderAccountDisabled
	case pluginv1.AuthDenial_AUTH_DENIAL_PASSWORD_EXPIRED:
		return ErrProviderPasswordExpired
	case pluginv1.AuthDenial_AUTH_DENIAL_PROVIDER_UNAVAILABLE:
		return ErrProviderUnavailable
	default:
		// INVALID_CREDENTIALS, and any value this build does not know: fail
		// closed.
		return ErrInvalidCredentials
	}
}

// pluginCallError maps a failed plugin load or RPC. A disabled installation
// accepts nobody; Unauthenticated is a refused credential; anything else
// means the provider could not answer.
func pluginCallError(ctx context.Context, installationID int, op string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrInvalidCredentials), errors.Is(err, ErrUserDisabled):
		return err
	case errors.Is(err, plugins.ErrInstallationDisabled):
		return ErrInvalidCredentials
	}
	if st, ok := status.FromError(err); ok && st.Code() == codes.Unauthenticated {
		return ErrInvalidCredentials
	}
	slog.WarnContext(ctx, "auth plugin call failed", "component", "auth",
		"installation_id", installationID, "op", op, "error", err)
	return ErrProviderUnavailable
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
