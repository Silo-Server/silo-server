package push

import (
	"context"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/notifications"
)

type fakeDeviceSource struct {
	devices  []Device
	enqueued []struct {
		nid int64
		dev Device
		nb  time.Time
	}
	eligibleErr error
}

func (f *fakeDeviceSource) EligibleDevices(_ context.Context, _ int, _, _ string) ([]Device, error) {
	if f.eligibleErr != nil {
		return nil, f.eligibleErr
	}
	return f.devices, nil
}
func (f *fakeDeviceSource) EnqueueDelivery(_ context.Context, nid int64, d Device, nb time.Time) error {
	f.enqueued = append(f.enqueued, struct {
		nid int64
		dev Device
		nb  time.Time
	}{nid, d, nb})
	return nil
}

func TestEnqueuer_WritesOnePerDevice(t *testing.T) {
	src := &fakeDeviceSource{devices: []Device{
		{UserID: 7, DeviceID: "d1", Transport: "apns", Token: "t1"},
		{UserID: 7, DeviceID: "d2", Transport: "webpush", Token: "t2"},
	}}
	now := time.Unix(1_000_000, 0)
	e := NewEnqueuer(src, 30*time.Second, func() time.Time { return now })

	link := "/requests"
	e.EnqueueForNotification(context.Background(), &notifications.Notification{
		ID: 5, UserID: 7, Category: notifications.CategoryRequest, Title: "Approved", Link: &link,
	})

	if len(src.enqueued) != 2 {
		t.Fatalf("enqueued = %d, want 2", len(src.enqueued))
	}
	if !src.enqueued[0].nb.Equal(now.Add(30 * time.Second)) {
		t.Fatalf("not_before = %v, want now+30s", src.enqueued[0].nb)
	}
	if src.enqueued[0].nid != 5 {
		t.Fatalf("notification id = %d, want 5", src.enqueued[0].nid)
	}
}

func TestEnqueuer_NoDevicesNoop(t *testing.T) {
	src := &fakeDeviceSource{devices: nil}
	e := NewEnqueuer(src, 30*time.Second, time.Now)
	e.EnqueueForNotification(context.Background(), &notifications.Notification{ID: 1, UserID: 7, Category: notifications.CategoryContent, Title: "x"})
	if len(src.enqueued) != 0 {
		t.Fatal("no devices should enqueue nothing")
	}
}

func TestEnqueuer_EligibleErrorIsSwallowed(t *testing.T) {
	src := &fakeDeviceSource{eligibleErr: context.DeadlineExceeded}
	e := NewEnqueuer(src, 30*time.Second, time.Now)
	e.EnqueueForNotification(context.Background(), &notifications.Notification{ID: 1, UserID: 7, Category: notifications.CategorySystem, Title: "x"})
	if len(src.enqueued) != 0 {
		t.Fatal("error path enqueues nothing")
	}
}

func TestEnqueuer_NilOrZeroUserNoop(t *testing.T) {
	src := &fakeDeviceSource{devices: []Device{{UserID: 7, DeviceID: "d1"}}}
	e := NewEnqueuer(src, 30*time.Second, time.Now)
	e.EnqueueForNotification(context.Background(), nil)
	e.EnqueueForNotification(context.Background(), &notifications.Notification{ID: 1, UserID: 0, Title: "x", Category: notifications.CategoryContent})
	if len(src.enqueued) != 0 {
		t.Fatal("nil/zero-user enqueues nothing")
	}
}
