package nodeconfig

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
)

// scriptedRegistrar answers each Register call with the next error in its
// script, then accepts.
type scriptedRegistrar struct {
	answers []error
	calls   int
}

func (r *scriptedRegistrar) Register(context.Context, nodecontrol.Registration) (*nodecontrol.RegistrationResult, error) {
	r.calls++
	if len(r.answers) == 0 {
		return &nodecontrol.RegistrationResult{EnvManaged: []string{"group"}}, nil
	}
	err := r.answers[0]
	r.answers = r.answers[1:]
	return nil, err
}

func status(code int) error {
	return &nodecontrol.StatusError{Path: nodecontrol.PathRegistration, Status: code}
}

// The startup failure rules: an unreachable core, a 5xx or a refused key is
// retried with backoff until it answers; a refused value ends the attempt
// with the refusal; an older core without the route is reported as such.
func TestDeclareFailureRules(t *testing.T) {
	refusal := &nodecontrol.RegistrationError{Status: http.StatusConflict,
		RegistrationRefusal: nodecontrol.RegistrationRefusal{Reason: nodecontrol.RegistrationURLTaken, Field: "url"}}
	for _, tc := range []struct {
		name      string
		answers   []error
		wantErr   error
		wantCalls int
	}{
		{"retries until the core answers", []error{errors.New("connection refused"), status(http.StatusServiceUnavailable), status(http.StatusUnauthorized)}, nil, 4},
		{"returns a refusal", []error{refusal}, refusal, 1},
		{"reports an older core", []error{status(http.StatusNotFound)}, ErrRegistrationUnsupported, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				registrar := &scriptedRegistrar{answers: tc.answers}
				_, err := Declare(t.Context(), registrar, nodecontrol.Registration{})
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err %v, want %v", err, tc.wantErr)
				}
				if registrar.calls != tc.wantCalls {
					t.Fatalf("%d calls, want %d", registrar.calls, tc.wantCalls)
				}
			})
		})
	}
}

// A node started against an older core keeps offering its declaration and
// reports it once the core takes it.
func TestRetryDeclarationUntilAccepted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		registrar := &scriptedRegistrar{answers: []error{status(http.StatusNotFound), status(http.StatusNotFound)}}
		var accepted *nodecontrol.RegistrationResult
		RetryDeclaration(t.Context(), registrar, nodecontrol.Registration{}, func(r *nodecontrol.RegistrationResult) { accepted = r })
		if accepted == nil || registrar.calls != 3 {
			t.Fatalf("accepted %v after %d calls, want a result after 3", accepted, registrar.calls)
		}
	})
}
