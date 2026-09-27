// Package debugserver owns the optional loopback-only operational profiler.
package debugserver

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

// Config is read once during bootstrap, independently of database settings.
type Config struct {
	Listen        string
	BlockRate     int
	MutexFraction int
}

// LoadConfig validates literal loopback addresses without resolving DNS. Zero
// ports are reserved for tests that call startListener directly.
func LoadConfig(getenv func(string) string) (Config, error) {
	c := Config{Listen: getenv("SILO_DEBUG_LISTEN")}
	var err error
	c.BlockRate, err = samplingSetting(getenv("SILO_DEBUG_BLOCK_RATE"), "SILO_DEBUG_BLOCK_RATE", 1_000_000, 1_000_000_000)
	if err != nil {
		return Config{}, err
	}
	c.MutexFraction, err = samplingSetting(getenv("SILO_DEBUG_MUTEX_FRACTION"), "SILO_DEBUG_MUTEX_FRACTION", 100, 1_000_000)
	if err != nil {
		return Config{}, err
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func samplingSetting(raw, name string, min, max int) (int, error) {
	if raw == "" || raw == "0" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be 0 (disabled) or an integer between %d and %d", name, min, max)
	}
	return n, nil
}

func (c Config) validate() error {
	if c.Listen == "" {
		if c.BlockRate != 0 || c.MutexFraction != 0 {
			return fmt.Errorf("contention sampling requires SILO_DEBUG_LISTEN")
		}
		return nil
	}
	if !loopbackAuthority(c.Listen) {
		return fmt.Errorf("SILO_DEBUG_LISTEN must be a literal loopback IP and port from 1 to 65535")
	}
	if c.BlockRate != 0 && (c.BlockRate < 1_000_000 || c.BlockRate > 1_000_000_000) {
		return fmt.Errorf("invalid block sampling rate")
	}
	if c.MutexFraction != 0 && (c.MutexFraction < 100 || c.MutexFraction > 1_000_000) {
		return fmt.Errorf("invalid mutex sampling fraction")
	}
	return nil
}

func loopbackAuthority(authority string) bool {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || ip.Zone() != "" || !ip.IsLoopback() {
		return false
	}
	if port == "" {
		return false
	}
	for _, ch := range port {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(port)
	return err == nil && n > 0 && n <= 65535
}
