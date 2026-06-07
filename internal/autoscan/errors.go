package autoscan

import "errors"

// ErrNotFound is returned when an autoscan operation references a connection or
// source that does not exist. The admin API surfaces it as a 404.
var ErrNotFound = errors.New("autoscan: not found")

// ErrPollAlreadyRunning is returned when a source already has an active poll
// event. Callers should treat it as a no-op skip, not a source failure.
var ErrPollAlreadyRunning = errors.New("autoscan: source poll already running")
