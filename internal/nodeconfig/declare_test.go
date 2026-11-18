package nodeconfig

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Silo-Server/silo-server/internal/nodecontrol"
	"github.com/Silo-Server/silo-server/internal/nodesource"
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

// The node runs with the values it declares, whatever the document built from
// its row says: a row edited on the core behind the node's back changes
// neither its acceleration, its device, its source access nor its
// prepared-download storage.
func TestRemoteRunsWithDeclaredValues(t *testing.T) {
	body := remoteBody()
	body.Playback.HWAccel, body.Playback.HWDevice = "nvenc", "/dev/dri/renderD129"
	body.SourceAccess = nodesource.ModeCore
	body.Download = nodecontrol.DownloadConfig{ArtifactDir: "/edited", ArtifactMaxBytes: 1 << 40}
	remote := NewRemote(newFakeCore(body), ":8080")
	accel, device, access, dir, budget := "vaapi", "/dev/dri/renderD128", nodesource.ModeLocal, "/declared", int64(1<<30)
	remote.SetDeclared(nodecontrol.Registration{HWAccelOverride: &accel, HWDeviceOverride: &device, SourceAccess: &access,
		DownloadArtifactDirOverride: &dir, DownloadArtifactMaxBytesOverride: &budget})
	if err := remote.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg := remote.Config()
	if cfg.Playback.HWAccel != accel || cfg.Playback.HWDevice != device || remote.SourceAccess() != access ||
		cfg.Download.ArtifactDir != dir || cfg.Download.ArtifactMaxBytes != budget {
		t.Fatalf("node runs with accel %q device %q access %q dir %q budget %d, want its declared values",
			cfg.Playback.HWAccel, cfg.Playback.HWDevice, remote.SourceAccess(), cfg.Download.ArtifactDir, cfg.Download.ArtifactMaxBytes)
	}
}

// A running node keeps sending its declaration; a refusal does not stop it.
func TestMaintainDeclarationOutlivesARefusal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		refusal := &nodecontrol.RegistrationError{Status: http.StatusUnprocessableEntity,
			RegistrationRefusal: nodecontrol.RegistrationRefusal{Reason: nodecontrol.RegistrationInvalid, Field: "group"}}
		registrar := &scriptedRegistrar{answers: []error{refusal, status(http.StatusServiceUnavailable)}}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			MaintainDeclaration(ctx, registrar, nodecontrol.Registration{}, false, nil)
		}()
		time.Sleep(3*DeclarationRefreshInterval + time.Second) //nolint:forbidigo // synctest fake clock: advances three refresh intervals instantly
		synctest.Wait()
		cancel()
		<-done
		if registrar.calls != 3 {
			t.Fatalf("%d sends in three intervals, want 3", registrar.calls)
		}
	})
}
