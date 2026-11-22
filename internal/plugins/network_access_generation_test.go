package plugins

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/Silo-Server/silo-server/internal/netaccess"
	"github.com/Silo-Server/silo-server/internal/pluginhost"
)

func TestNetworkAccessProviderHonorsDeadlineWhileProcessIsLocked(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := &Service{host: newSeqFakeHost()}
		service.resident = newResidentSupervisor(service, ResidentOptions{})
		service.resident.entries[5] = &residentEntry{id: 5, state: ResidentRunning}
		lock := service.runtimeLock(5)
		lock.Lock()
		defer lock.Unlock()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		reported := make(chan netaccess.Status, 1)
		go func() {
			reported <- service.applyNetworkAccess(ctx, NetworkAccessProvider{InstallationID: 5, Provider: "stub"}, networkAccessRead.command())
		}()
		select {
		case status := <-reported:
			if status.State != netaccess.StateUnavailable || status.Error != context.DeadlineExceeded.Error() {
				t.Fatalf("status while the process lock is held = %+v", status)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("network access status did not return before the lock was released")
		}
	})
}

func TestNetworkAccessLateRPCDoesNotRestoreRevokedOrReplacedStatus(t *testing.T) {
	f := newResidentFixture(t, ResidentOptions{})
	f.service.SetNetworkAccessStatusSink(f.broker)
	ctx := context.Background()
	f.service.StartResidents(ctx)
	waitState(t, f.service, 5, "running", running)
	provider, err := f.service.networkAccessProvider(ctx, "stub")
	if err != nil {
		t.Fatal(err)
	}
	token, ok := f.broker.IngressToken(5)
	if !ok {
		t.Fatal("no token issued")
	}
	f.service.applyNetworkAccess(ctx, provider, func(context.Context, *pluginhost.NetworkAccessProviderClient) (*pluginv1.NetworkAccessStatus, error) {
		// A successful RPC response arrives, then the process stops before
		// the caller can publish that response to the shared cache.
		f.broker.Revoke(5, token)
		return &pluginv1.NetworkAccessStatus{State: netaccess.StateConnected, Origin: "https://old.example.test"}, nil
	})
	if _, ok := f.broker.Status.Get(5); ok {
		t.Fatal("late RPC response restored a revoked origin")
	}
	fresh, err := f.broker.Issue(5, "stub")
	if err != nil {
		t.Fatal(err)
	}
	f.broker.ReportFor(5, fresh, netaccess.Status{InstallationID: 5, Provider: "stub", State: netaccess.StateDisconnected})
	f.service.applyNetworkAccess(ctx, provider, func(context.Context, *pluginhost.NetworkAccessProviderClient) (*pluginv1.NetworkAccessStatus, error) {
		return &pluginv1.NetworkAccessStatus{State: netaccess.StateConnected, Origin: "https://old.example.test"}, nil
	})
	if got, ok := f.broker.Status.Get(5); !ok || got.State != netaccess.StateDisconnected {
		t.Fatalf("old client overwrote replacement status: %+v %v", got, ok)
	}
}
