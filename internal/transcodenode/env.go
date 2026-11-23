package transcodenode

import (
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Mode is the MODE that runs a process as a transcode node.
const Mode = "transcode"

// EnvCoreURL is the URL a transcode node reaches the core at. With its own
// key (EnvNodeKey) it is all the node needs: everything else comes from the
// core.
const EnvCoreURL = "SILO_CORE_URL"

// EnvNodeKey is the transcode node's own key, issued by an administrator. The
// node verifies every request the core signs to it with this key.
const EnvNodeKey = "SILO_NODE_KEY"

// ParseCoreURL parses raw as SILO_CORE_URL must be: an absolute http or https
// URL.
func ParseCoreURL(raw string) (*url.URL, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, false
	}
	return parsed, true
}

// EnvDrainTimeout bounds how long a transcode node that received SIGTERM keeps
// serving its sessions before it shuts down. 0 turns the drain off.
const EnvDrainTimeout = "SILO_NODE_DRAIN_TIMEOUT"

// ParseDrainTimeout parses SILO_NODE_DRAIN_TIMEOUT: a Go duration ("15m") or
// whole seconds ("900"). Empty means DefaultDrainTimeout. Anything else, such
// as a negative value, is an error.
func ParseDrainTimeout(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultDrainTimeout, nil
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 && seconds <= math.MaxInt64/int(time.Second) {
		return time.Duration(seconds) * time.Second, nil
	}
	if timeout, err := time.ParseDuration(value); err == nil && timeout >= 0 {
		return timeout, nil
	}
	return 0, errors.New(EnvDrainTimeout + " must be a duration such as 15m or whole seconds such as 900")
}
