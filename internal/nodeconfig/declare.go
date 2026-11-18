package nodeconfig

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/Silo-Server/silo-server/internal/config"
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

// registrationUnsupported reports an answer that means the route is not
// served: a core without it, whose router knows neither the path nor the
// method, or a gateway in front of the core that does not pass it (403). The
// core itself answers a refused key with 401, never 403.
func registrationUnsupported(err error) bool {
	switch nodecontrol.Status(err) {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented, http.StatusForbidden:
		return true
	}
	return false
}

// Release sends an empty declaration once, which releases the fields the
// node declared at an earlier start. It never holds startup up: when the core
// cannot take it now, it is offered again every RemotePollInterval in the
// background. That includes a core that does not serve the route, because
// from here it looks the same as a gateway that briefly refused it, and a
// node that gave up there would leave its earlier fields locked.
func Release(ctx, background context.Context, registrar Registrar) {
	_, err := registrar.Register(ctx, nodecontrol.Registration{})
	if err == nil || ctx.Err() != nil {
		return
	}
	if _, refused := errors.AsType[*nodecontrol.RegistrationError](err); refused {
		slog.WarnContext(ctx, "the core refused to release this node's earlier declaration", "component", "nodeconfig", "error", err)
		return
	}
	// Info, not a warning: against a core that predates the route this is
	// every start, and nothing here needs an operator. It is the explanation
	// for a field that stays read-only in the node editor.
	slog.InfoContext(ctx, "the core has not taken this node's empty declaration; fields it declared at an earlier start stay read-only until it does. Offering it again every poll",
		"component", "nodeconfig", "route", "PUT "+nodecontrol.PathRegistration, "error", err)
	go RetryDeclaration(background, registrar, nodecontrol.Registration{}, func(*nodecontrol.RegistrationResult) {})
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

// SetDeclared makes the node run with the values it declares from its
// environment wherever it acts on them itself: hardware acceleration and
// device, source access, and the prepared-download directory and budget. The
// core writes the same values to the node's row, and the configuration
// document it serves follows the row, but whoever runs the core can change
// the row; these hold whatever the document says. Call it before Load.
func (r *Remote) SetDeclared(declared nodecontrol.Registration) {
	r.declared = declared
}

// applyDeclared writes the declared values over the document's.
func (r *Remote) applyDeclared(cfg *config.Config) {
	d := r.declared
	if d.HWAccelOverride != nil {
		cfg.Playback.HWAccel = *d.HWAccelOverride
	}
	if d.HWDeviceOverride != nil {
		cfg.Playback.HWDevice = *d.HWDeviceOverride
	}
	if d.DownloadArtifactDirOverride != nil {
		cfg.Download.ArtifactDir = *d.DownloadArtifactDirOverride
	}
	if d.DownloadArtifactMaxBytesOverride != nil {
		cfg.Download.ArtifactMaxBytes = *d.DownloadArtifactMaxBytesOverride
	}
}

// DeclarationRefreshInterval is how often a node that declares something
// sends its declaration again while it runs, so a row or a lock changed on
// the core behind its back is put back within a minute and without a
// restart. The node enforces what it acts on itself either way; the row is
// what the core's planner and the node editor read, so the interval bounds
// how long they can disagree with the node. One send a minute is a sixth of
// the registration budget's refill (one every ten seconds), leaving the burst
// for restarts, and an unchanged declaration commits no configuration
// revision, so it costs the core one small transaction on the node's row.
const DeclarationRefreshInterval = time.Minute

// MaintainDeclaration sends the declaration every DeclarationRefreshInterval
// until ctx ends. pending is a node that started without the core taking its
// declaration (an older core, or a gateway that blocks the route): it is
// offered every RemotePollInterval instead until the core takes it, and then
// accepted runs. A failure never stops the node: it is logged once, and again
// only after a send has succeeded, so a core that keeps refusing does not
// fill the log.
func MaintainDeclaration(ctx context.Context, registrar Registrar, registration nodecontrol.Registration, pending bool, accepted func(*nodecontrol.RegistrationResult)) {
	logged := false
	for {
		interval := DeclarationRefreshInterval
		if pending {
			interval = RemotePollInterval
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		result, err := registrar.Register(ctx, registration)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			// A pending node already said at startup that the core does not
			// take declarations; only a refusal is news.
			_, refused := errors.AsType[*nodecontrol.RegistrationError](err)
			if !logged && (refused || !pending) {
				logged = true
				logRefreshFailure(ctx, err)
			}
			continue
		}
		logged = false
		switch {
		case pending:
			pending = false
			accepted(result)
		case result.Changed:
			slog.InfoContext(ctx, "the main server's row for this node differed from its declaration and was put back",
				"component", "nodeconfig", "env_managed", result.EnvManaged)
		}
	}
}

// logRefreshFailure logs a declaration the core did not take while the node
// runs.
func logRefreshFailure(ctx context.Context, err error) {
	if refused, ok := errors.AsType[*nodecontrol.RegistrationError](err); ok {
		slog.ErrorContext(ctx, "the main server refused this node's declaration; the node keeps running with its own values, but its row may disagree until the variable is fixed",
			"component", "nodeconfig", "field", refused.Field, "reason", refused.Reason, "detail", refused.Detail)
		return
	}
	slog.WarnContext(ctx, "cannot send this node's declaration to the main server; offering it again at the next interval",
		"component", "nodeconfig", "route", "PUT "+nodecontrol.PathRegistration, "error", err)
}
