// Package encodinglimit enforces the dedicated worker's operator-owned ceiling
// at FFmpeg's process boundary, including detached probes and session restarts.
package encodinglimit

import (
	"errors"
	"os/exec"
	"sync"
)

var ErrAtCapacity = errors.New("node is at encoding capacity")

// Capacity is a consistent snapshot. Zero MaxJobs and null AvailableJobs mean
// unlimited. ActiveJobs counts admitted media processes, including startup.
type Capacity struct {
	MaxJobs       int  `json:"max_jobs"`
	ActiveJobs    int  `json:"active_jobs"`
	AvailableJobs *int `json:"available_jobs"`
}

var state struct {
	sync.Mutex
	maxJobs    int
	activeJobs int
}

var processes sync.Map // *exec.Cmd -> release func()

// Configure is called once at worker startup, before any media work begins.
// It is deliberately independent of the customer's reloaded DB configuration.
func Configure(maxJobs int) {
	if maxJobs < 0 {
		panic("negative encoding limit")
	}
	state.Lock()
	defer state.Unlock()
	if state.activeJobs != 0 {
		panic("encoding limit configured while jobs are active")
	}
	state.maxJobs = maxJobs
}

func Snapshot() Capacity {
	state.Lock()
	defer state.Unlock()
	result := Capacity{MaxJobs: state.maxJobs, ActiveJobs: state.activeJobs}
	if state.maxJobs > 0 {
		available := max(0, state.maxJobs-state.activeJobs)
		result.AvailableJobs = &available
	}
	return result
}

// Acquire reserves before process launch. Release is safe to call repeatedly,
// but must be held until the process has exited or its launch has failed.
func Acquire() (func(), error) {
	state.Lock()
	defer state.Unlock()
	if state.maxJobs > 0 && state.activeJobs >= state.maxJobs {
		return nil, ErrAtCapacity
	}
	state.activeJobs++
	return sync.OnceFunc(func() {
		state.Lock()
		state.activeJobs--
		state.Unlock()
	}), nil
}

func acquireCommand(cmd *exec.Cmd) (func(), error) {
	// Listings have no media input. Check -i first so a media argument that
	// happens to look like a listing flag cannot bypass admission.
	for _, arg := range cmd.Args[1:] {
		if arg == "-i" {
			return Acquire()
		}
	}
	for _, arg := range cmd.Args[1:] {
		switch arg {
		case "-version", "-encoders", "-decoders", "-filters", "-muxers", "-demuxers", "-formats", "-bsfs", "-hwaccels", "-h", "-help":
			return func() {}, nil
		}
	}
	return Acquire()
}

// StartWith preserves platform-specific launch behavior such as lowered
// background priority. The existing owner must call Wait, never a second reaper.
// Admission failure precedes exec.Start; owners must close any pipes they created.
func StartWith(cmd *exec.Cmd, start func() error) error {
	release, err := acquireCommand(cmd)
	if err != nil {
		return err
	}
	if err := start(); err != nil {
		release()
		return err
	}
	processes.Store(cmd, release)
	return nil
}

func Start(cmd *exec.Cmd) error { return StartWith(cmd, cmd.Start) }

func Wait(cmd *exec.Cmd) error {
	err := cmd.Wait()
	if release, ok := processes.LoadAndDelete(cmd); ok {
		if free, ok := release.(func()); ok {
			free()
		}
	}
	return err
}

func Run(cmd *exec.Cmd) error {
	release, err := acquireCommand(cmd)
	if err != nil {
		return err
	}
	defer release()
	return cmd.Run()
}

func CombinedOutput(cmd *exec.Cmd) ([]byte, error) {
	release, err := acquireCommand(cmd)
	if err != nil {
		return nil, err
	}
	defer release()
	return cmd.CombinedOutput()
}
