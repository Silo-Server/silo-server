package plugins

import "sync"

// lazyStartFailures remembers, per installation, why the last launch of a
// lazily started (non-resident) plugin failed on this host. Resident plugins
// report failures through the supervisor; a lazy plugin has no supervisor,
// so without this record a plugin whose process cannot start would read as
// merely stopped. Like the supervisor's entries it is in-memory and per host,
// and it is tied to the release and runtime generation that failed: a later
// successful start clears it, and an update, reinstall, config save or
// restart makes it stale. The zero value is ready to use.
type lazyStartFailures struct {
	mu       sync.Mutex
	failures map[int]lazyStartFailure
}

type lazyStartFailure struct {
	message           string
	version           string
	installPath       string
	runtimeGeneration int64
}

func (f *lazyStartFailures) record(installation *Installation, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures == nil {
		f.failures = make(map[int]lazyStartFailure)
	}
	f.failures[installation.ID] = lazyStartFailure{
		message:           err.Error(),
		version:           installation.Version,
		installPath:       installation.InstallPath,
		runtimeGeneration: installation.RuntimeGeneration,
	}
}

func (f *lazyStartFailures) clear(installationID int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.failures, installationID)
}

// lastError returns the recorded failure while installation is still the
// release and runtime generation that failed.
func (f *lazyStartFailures) lastError(installation *Installation) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	failure, ok := f.failures[installation.ID]
	if !ok || failure.version != installation.Version || failure.installPath != installation.InstallPath ||
		failure.runtimeGeneration != installation.RuntimeGeneration {
		return "", false
	}
	return failure.message, true
}
