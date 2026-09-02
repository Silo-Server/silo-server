package listener

import (
	"net/http"
	"unsafe"
)

// peek is an analyzer fixture: an audited package importing unsafe.
func peek(h http.Handler) uintptr { return uintptr(unsafe.Pointer(&h)) }
