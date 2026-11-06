package encodinglimit

import (
	"errors"
	"io"
	"os/exec"
	"sync"
	"testing"
)

func TestConcurrentAdmissionSharesOneCeiling(t *testing.T) {
	Configure(4)
	t.Cleanup(func() { Configure(0) })
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan func(), 32)
	for range 32 {
		wg.Go(func() {
			<-start
			release, err := Acquire()
			if err != nil && !errors.Is(err, ErrAtCapacity) {
				t.Errorf("admission: %v", err)
			}
			results <- release
		})
	}
	close(start)
	wg.Wait()
	close(results)
	var admitted []func()
	for release := range results {
		if release != nil {
			admitted = append(admitted, release)
		}
	}
	if len(admitted) != 4 {
		t.Errorf("admitted %d jobs, want 4", len(admitted))
	}
	if got := Snapshot(); got.ActiveJobs != 4 || got.AvailableJobs == nil || *got.AvailableJobs != 0 {
		t.Errorf("capacity: %+v", got)
	}
	for _, release := range admitted {
		release()
		release()
	}
	if got := Snapshot(); got.ActiveJobs != 0 || *got.AvailableJobs != 4 {
		t.Errorf("released capacity: %+v", got)
	}
}

func TestProcessSlotLastsUntilWaitAndFailedStartReleases(t *testing.T) {
	Configure(1)
	t.Cleanup(func() { Configure(0) })
	cmd := exec.Command("sh", "-c", "read line")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := Start(cmd); err != nil {
		t.Fatal(err)
	}
	if err := Run(exec.Command("sh", "-c", "exit 0")); !errors.Is(err, ErrAtCapacity) {
		t.Errorf("second process: %v", err)
	}
	_, _ = io.WriteString(stdin, "exit\n")
	_ = stdin.Close()
	if err := Wait(cmd); err != nil {
		t.Fatal(err)
	}
	if got := Snapshot(); got.ActiveJobs != 0 {
		t.Fatalf("completed process retained slot: %+v", got)
	}
	if err := Start(exec.Command("/does-not-exist")); err == nil {
		t.Fatal("missing binary started")
	}
	if err := Run(exec.Command("sh", "-c", "exit 0")); err != nil {
		t.Fatalf("failed Start leaked slot: %v", err)
	}
}

func TestUnlimitedCapacityAndDiscovery(t *testing.T) {
	Configure(0)
	var releases []func()
	for range 12 {
		release, err := Acquire()
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if got := Snapshot(); got.ActiveJobs != 12 || got.AvailableJobs != nil {
		t.Errorf("unlimited: %+v", got)
	}
	for _, release := range releases {
		release()
	}
	Configure(1)
	t.Cleanup(func() { Configure(0) })
	release, _ := Acquire()
	defer release()
	// Discovery can run while all encoding slots are occupied.
	cmd := exec.Command("sh", "-c", "exit 0", "-encoders")
	if _, err := CombinedOutput(cmd); err != nil {
		t.Fatalf("discovery consumed an encoding slot: %v", err)
	}
	// A media input cannot disguise an encode with a discovery-like argument.
	cmd = exec.Command("sh", "-c", "exit 0", "-i", "-encoders")
	if _, err := CombinedOutput(cmd); !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("media input bypassed the cap: %v", err)
	}
}
