package autoscan

import "errors"

// ErrIntegrationNotFound is returned when an autoscan operation references a
// request integration that does not exist.
var ErrIntegrationNotFound = errors.New("autoscan: integration not found")
