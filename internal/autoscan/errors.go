package autoscan

import "errors"

// ErrNotFound is returned when an autoscan operation references a connection or
// source that does not exist. The admin API surfaces it as a 404.
var ErrNotFound = errors.New("autoscan: not found")
