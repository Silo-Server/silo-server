package activitylog

import "net/netip"

// NormalizeClientIPFilter validates a ListOptions.ClientIP filter and returns
// its canonical form, or false when the value is not one the inet comparison
// in List can take. It accepts an IPv4/IPv6 address without a zone, or a
// prefix whose host bits are kept, because PostgreSQL inet equality compares
// the address and the mask. Every reader of the filter (history and the live
// log stream) normalizes through here so both see the same value.
func NormalizeClientIPFilter(raw string) (string, bool) {
	if addr, err := netip.ParseAddr(raw); err == nil && addr.Zone() == "" {
		return addr.String(), true
	}
	if prefix, err := netip.ParsePrefix(raw); err == nil {
		return prefix.String(), true
	}
	return "", false
}
