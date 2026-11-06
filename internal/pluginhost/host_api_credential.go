package pluginhost

import (
	"context"
	"errors"
)

// ErrHostAPINotGranted means an installation has no usable host API key: its
// manifest asks for no host_api_scopes, an administrator hasn't approved
// them, or the key was revoked. GetHostAPICredential answers it with
// FAILED_PRECONDITION and the wrapped message.
var ErrHostAPINotGranted = errors.New("no approved host API key")

// HostAPICredential is the API key the host issued for one installation's
// approved host_api_scopes, and the scopes it carries.
type HostAPICredential struct {
	Token  string
	Scopes []string
}

// HostAPICredentialSource looks up the host API key issued to an
// installation. The installation is the calling process's, fixed by the host
// when it started the plugin, never one the plugin names.
type HostAPICredentialSource interface {
	HostAPICredential(ctx context.Context, installationID int) (HostAPICredential, error)
}
