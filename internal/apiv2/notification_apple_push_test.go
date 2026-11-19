package apiv2

import (
	"context"
	"time"

	evt "github.com/Silo-Server/silo-server/internal/events"
	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeOrderedApple struct {
	calls    int
	cmd      notifications.ApplePushCommand
	identity evt.SocketIdentity
	err      error
}

func (*fakeOrderedApple) OrderedApplePushAvailable() bool { return true }
func (f *fakeOrderedApple) RegisterApplePush(_ context.Context, cmd notifications.ApplePushCommand, identity evt.SocketIdentity) (notifications.ApplePushReceipt, string, time.Time, error) {
	f.calls++
	f.cmd = cmd
	f.identity = identity
	return notifications.ApplePushReceipt{Generation: cmd.Generation, RegistrationID: "registration", ServerDeviceID: "server-device", Enabled: true, PushMode: cmd.PushMode}, "synthetic-display", fixedTime().Add(30 * 24 * time.Hour), f.err
}

type fakeApplePushRetirement struct {
	calls int
	cmd   notifications.ApplePushRetirement
	err   error
}

func (*fakeApplePushRetirement) ApplePushRetirementAvailable() bool { return true }
func (f *fakeApplePushRetirement) RetireApplePush(_ context.Context, cmd notifications.ApplePushRetirement) (notifications.ApplePushReceipt, error) {
	f.calls++
	f.cmd = cmd
	return notifications.ApplePushReceipt{Generation: cmd.Generation, Removed: true}, f.err
}

type fakeApplePushReset struct {
	calls int
	cmd   notifications.ApplePushReset
	reset bool
	err   error
}

func (*fakeApplePushReset) ApplePushResetAvailable() bool { return true }
func (f *fakeApplePushReset) ResetApplePushInstallation(_ context.Context, cmd notifications.ApplePushReset) (bool, error) {
	f.calls++
	f.cmd = cmd
	return f.reset, f.err
}
