package nodeconfig

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
)

// Registrar sends a transcode node's declaration to the core.
// *nodecontrol.Client implements it.
type Registrar interface {
	Register(ctx context.Context, registration nodecontrol.Registration) (*nodecontrol.RegistrationResult, error)
}

// ErrRegistrationUnsupported reports a core that does not serve the
// registration route: one older than node declarations.
var ErrRegistrationUnsupported = errors.New("the core does not accept node declarations")

// Declare sends the node's declaration, retrying with the backoff Load uses
// while the core does not answer or answers with a failure a retry can fix,
// a refused key included, as Load retries one. It returns the core's answer;
// a *nodecontrol.RegistrationError when the core refuses a declared value or
// URL; ErrRegistrationUnsupported when the core does not know the route; or
// ctx's error.
func Declare(ctx context.Context, registrar Registrar, registration nodecontrol.Registration) (*nodecontrol.RegistrationResult, error) {
	backoff := remoteInitialBackoff
	for attempt := 1; ; attempt++ {
		result, err := registrar.Register(ctx, registration)
		if err == nil {
			return result, nil
		}
		if _, refused := errors.AsType[*nodecontrol.RegistrationError](err); refused {
			return nil, err
		}
		if registrationUnsupported(err) {
			return nil, ErrRegistrationUnsupported
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		slog.WarnContext(ctx, "cannot send this node's declaration to the core; retrying",
			"component", "nodeconfig", "attempt", attempt, "retry_in", backoff, "error", err)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
		backoff = min(2*backoff, remoteMaxBackoff)
	}
}

// registrationUnsupported reports an answer a core without the route gives:
// its router knows neither the path nor the method.
func registrationUnsupported(err error) bool {
	switch nodecontrol.Status(err) {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	return false
}

// RetryDeclaration sends the declaration every RemotePollInterval until the
// core accepts it, for a node that started against a core without the
// route, then calls accepted. A refusal is logged and ends the retries: the
// node keeps running on the row as the administrator left it.
func RetryDeclaration(ctx context.Context, registrar Registrar, registration nodecontrol.Registration, accepted func(*nodecontrol.RegistrationResult)) {
	ticker := time.NewTicker(RemotePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		result, err := registrar.Register(ctx, registration)
		if err == nil {
			accepted(result)
			return
		}
		if _, refused := errors.AsType[*nodecontrol.RegistrationError](err); refused {
			slog.ErrorContext(ctx, "the core refused this node's declaration; fix the variable and restart the node",
				"component", "nodeconfig", "error", err)
			return
		}
	}
}
