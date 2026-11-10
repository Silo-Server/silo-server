package pluginhost

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
)

type idleClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *idleClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *idleClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

const (
	testIdlePeriod     = 15 * time.Minute
	testIdleCapability = "/silo.plugin.v1.MetadataProvider/Search"
)

// idleTestHost returns a host mapping one instance with no process behind
// it, and the channel its exit handler reports to.
func idleTestHost(t *testing.T, clock *idleClock, installationID int) (*Host, *instance, chan int) {
	t.Helper()
	host := NewHost(Config{IdleStopAfter: testIdlePeriod})
	host.now = clock.Now
	exited := make(chan int, 1)
	host.SetExitHandler(func(id int) { exited <- id })
	inst := &instance{
		client:         makeTestClient(t, nil),
		activity:       newActivity(clock.Now),
		installationID: installationID,
	}
	host.instances[installationID] = inst
	return host, inst, exited
}

func TestParseIdleStopAfter(t *testing.T) {
	tests := []struct {
		value   string
		want    time.Duration
		wantErr bool
	}{
		{value: "", want: DefaultIdleStopAfter},
		{value: " 30m ", want: 30 * time.Minute},
		{value: "600", want: 10 * time.Minute},
		{value: "0", want: 0},
		{value: "0s", want: 0},
		{value: "5s", want: MinIdleStopAfter},
		{value: "-1m", want: DefaultIdleStopAfter, wantErr: true},
		{value: "-5", want: DefaultIdleStopAfter, wantErr: true},
		{value: "soon", want: DefaultIdleStopAfter, wantErr: true},
		{value: "99999999999999999999", want: DefaultIdleStopAfter, wantErr: true},
	}
	for _, tt := range tests {
		got, err := ParseIdleStopAfter(tt.value)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("ParseIdleStopAfter(%q) = %v, %v; want %v, error %v", tt.value, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestStopIfIdleStopsAnIdleInstanceQuietly(t *testing.T) {
	clock := &idleClock{now: time.Unix(1000, 0)}
	host, inst, exited := idleTestHost(t, clock, 7)

	clock.Advance(testIdlePeriod - time.Second)
	if host.stopIfIdle(context.Background(), 7, inst) {
		t.Fatal("stopped before the idle period passed")
	}
	clock.Advance(time.Second)
	if !host.stopIfIdle(context.Background(), 7, inst) {
		t.Fatal("did not stop an instance idle for the whole period")
	}

	if _, err := host.Client(7); !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("Client after idle stop = %v, want ErrClientNotFound so the next call starts it", err)
	}
	inst.client.mu.RLock()
	unhealthy := inst.client.unhealthy
	inst.client.mu.RUnlock()
	if unhealthy {
		t.Fatal("idle stop marked the client unhealthy")
	}
	select {
	case id := <-exited:
		t.Fatalf("idle stop reported installation %d to the exit handler", id)
	default:
	}
	// The start slot is free again for the next launch.
	if err := host.acquireStart(context.Background(), 7); err != nil {
		t.Fatalf("acquireStart after idle stop: %v", err)
	}
	host.releaseStart(7)
}

func TestStopIfIdleKeepsABusyInstance(t *testing.T) {
	clock := &idleClock{now: time.Unix(1000, 0)}
	host, inst, _ := idleTestHost(t, clock, 7)

	inCall := make(chan struct{})
	release := make(chan struct{})
	invoker := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		close(inCall)
		<-release
		return nil
	}
	done := make(chan error, 1)
	go func() {
		done <- inst.activity.unaryClient(context.Background(), testIdleCapability, nil, nil, nil, invoker)
	}()
	<-inCall

	clock.Advance(4 * testIdlePeriod)
	if host.stopIfIdle(context.Background(), 7, inst) {
		t.Fatal("stopped an instance with a call in flight")
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatalf("call: %v", err)
	}
	// The idle period counts from the end of the call, not its start.
	if host.stopIfIdle(context.Background(), 7, inst) {
		t.Fatal("stopped as soon as a long call returned")
	}
	clock.Advance(testIdlePeriod)
	if !host.stopIfIdle(context.Background(), 7, inst) {
		t.Fatal("did not stop once idle after the call")
	}
}

func TestIdleActivitySources(t *testing.T) {
	noop := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }
	tests := []struct {
		name     string
		use      func(t *testing.T, host *Host, inst *instance)
		wantIdle bool
	}{
		{name: "nothing", use: func(*testing.T, *Host, *instance) {}, wantIdle: true},
		{name: "capability call", use: func(t *testing.T, _ *Host, inst *instance) {
			if err := inst.activity.unaryClient(context.Background(), testIdleCapability, nil, nil, nil, noop); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "host callback", use: func(t *testing.T, _ *Host, inst *instance) {
			handler := func(context.Context, any) (any, error) { return struct{}{}, nil }
			if _, err := inst.activity.unaryServer(context.Background(), nil, &grpc.UnaryServerInfo{}, handler); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "client lookup", use: func(t *testing.T, host *Host, _ *instance) {
			if _, err := host.Client(7); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "health probe", wantIdle: true, use: func(t *testing.T, _ *Host, inst *instance) {
			if err := inst.activity.unaryClient(context.Background(), "/grpc.health.v1.Health/Check", nil, nil, nil, noop); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "status read", wantIdle: true, use: func(t *testing.T, host *Host, _ *instance) {
			if !host.Running(7) {
				t.Fatal("Running = false for a mapped instance")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clock := &idleClock{now: time.Unix(1000, 0)}
			host, inst, _ := idleTestHost(t, clock, 7)
			clock.Advance(testIdlePeriod - time.Second)
			tt.use(t, host, inst)
			clock.Advance(time.Second)
			if got := host.stopIfIdle(context.Background(), 7, inst); got != tt.wantIdle {
				t.Fatalf("stopped = %v, want %v", got, tt.wantIdle)
			}
		})
	}
}

func TestIdleStreamPinsTheInstance(t *testing.T) {
	clock := &idleClock{now: time.Unix(1000, 0)}
	host, inst, _ := idleTestHost(t, clock, 7)
	streamer := func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
		return nil, errors.New("no stream in this test")
	}
	// go-plugin's own broker and stdio streams stay open for the life of
	// every process and must not pin it.
	_, _ = inst.activity.streamClient(context.Background(), nil, nil, "/plugin.GRPCBroker/StartStream", streamer)
	clock.Advance(testIdlePeriod)
	if !inst.activity.idleFor(testIdlePeriod) {
		t.Fatal("a go-plugin stream pinned the instance")
	}
	_, _ = inst.activity.streamClient(context.Background(), nil, nil, "/silo.plugin.v1.Future/Watch", streamer)
	clock.Advance(4 * testIdlePeriod)
	if host.stopIfIdle(context.Background(), 7, inst) {
		t.Fatal("stopped an instance that opened a plugin protocol stream")
	}
}

// A caller that looks the client up while the idle watcher runs either keeps
// the instance alive or is told to start a new one. It is never handed a
// client whose process the watcher then stops.
func TestStopIfIdleRacingClientLookup(t *testing.T) {
	for range 300 {
		clock := &idleClock{now: time.Unix(1000, 0)}
		host, inst, _ := idleTestHost(t, clock, 7)
		clock.Advance(testIdlePeriod)

		const callers = 4
		var found atomic.Int32
		var stopped atomic.Bool
		release := make(chan struct{})
		var wg sync.WaitGroup
		for range callers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-release
				if _, err := host.Client(7); err == nil {
					found.Add(1)
				} else if !errors.Is(err, ErrClientNotFound) {
					t.Errorf("Client = %v", err)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			stopped.Store(host.stopIfIdle(context.Background(), 7, inst))
		}()
		close(release)
		wg.Wait()

		if stopped.Load() && found.Load() != 0 {
			t.Fatalf("instance stopped after %d callers were handed its client", found.Load())
		}
		if !stopped.Load() && found.Load() != callers {
			t.Fatalf("instance kept but only %d of %d callers found it", found.Load(), callers)
		}
	}
}

// A replacing Start owns the installation's start slot. The idle watcher
// waits for it and then leaves the instance that Start mapped alone.
func TestStopIfIdleYieldsToAStartInFlight(t *testing.T) {
	clock := &idleClock{now: time.Unix(1000, 0)}
	host, inst, _ := idleTestHost(t, clock, 7)
	clock.Advance(testIdlePeriod)

	if err := host.acquireStart(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	result := make(chan bool, 1)
	go func() { result <- host.stopIfIdle(context.Background(), 7, inst) }()

	replacement := &instance{client: makeTestClient(t, nil), activity: newActivity(clock.Now), installationID: 7}
	host.mu.Lock()
	host.instances[7] = replacement
	host.mu.Unlock()
	host.releaseStart(7)

	if <-result {
		t.Fatal("idle watcher stopped an installation a Start had just replaced")
	}
	if client, err := host.Client(7); err != nil || client != replacement.client {
		t.Fatalf("Client = %v, %v; want the replacement instance", client, err)
	}

	// A canceled watcher gives up on a held slot instead of waiting for it.
	if err := host.acquireStart(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	defer host.releaseStart(7)
	clock.Advance(testIdlePeriod)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if host.stopIfIdle(canceled, 7, replacement) {
		t.Fatal("canceled idle watcher stopped the instance")
	}
}

// Callers of Stop and Shutdown treat a return as the process being gone, so
// both wait for an idle stop that has already taken the instance.
func TestStopAndShutdownWaitForAnIdleStopInProgress(t *testing.T) {
	waiters := map[string]func(*Host) error{
		"Stop": func(host *Host) error {
			if err := host.Stop(7); !errors.Is(err, ErrClientNotFound) {
				return err
			}
			return nil
		},
		"Shutdown": func(host *Host) error { return host.Shutdown(context.Background()) },
	}
	for name, wait := range waiters {
		t.Run(name, func(t *testing.T) {
			clock := &idleClock{now: time.Unix(1000, 0)}
			host, inst, _ := idleTestHost(t, clock, 7)
			clock.Advance(testIdlePeriod)

			// Stands in for a process that takes a while to exit.
			exiting := make(chan struct{})
			exit := make(chan struct{})
			inst.cancelMonitors = func() {
				close(exiting)
				<-exit
			}
			go host.stopIfIdle(context.Background(), 7, inst)
			<-exiting

			var exitedFirst atomic.Bool
			returned := make(chan error, 1)
			go func() {
				err := wait(host)
				if err == nil && !exitedFirst.Load() {
					err = errors.New("returned while the idle process was still exiting")
				}
				returned <- err
			}()
			exitedFirst.Store(true)
			close(exit)
			if err := <-returned; err != nil {
				t.Fatal(err)
			}
		})
	}
}
