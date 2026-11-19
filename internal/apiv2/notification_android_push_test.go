package apiv2

import (
	"context"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeOrderedPush struct {
	cmd   notifications.AndroidPushCommand
	calls int
	err   error
}

func (*fakeOrderedPush) OrderedAndroidPushAvailable() bool { return true }
func (f *fakeOrderedPush) ApplyAndroidPush(_ context.Context, cmd notifications.AndroidPushCommand) (notifications.AndroidPushReceipt, error) {
	f.cmd = cmd
	f.calls++
	return notifications.AndroidPushReceipt{Generation: cmd.Generation, RegistrationID: "registration", ServerDeviceID: "server-device", PushMode: notifications.PushModePrivatePush, Removed: cmd.Remove}, f.err
}

type fakeAndroidPushRetirement struct {
	calls int
	cmd   notifications.AndroidPushRetirement
	err   error
}

func (*fakeAndroidPushRetirement) AndroidPushRetirementAvailable() bool { return true }
func (f *fakeAndroidPushRetirement) RetireAndroidPush(_ context.Context, cmd notifications.AndroidPushRetirement) (notifications.AndroidPushReceipt, error) {
	f.calls++
	f.cmd = cmd
	return notifications.AndroidPushReceipt{Generation: cmd.Generation, Removed: true}, f.err
}
