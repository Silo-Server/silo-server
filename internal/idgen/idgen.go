// Package idgen provides unique ID generation for Silo entities.
// All content IDs (media items, seasons, episodes) are generated locally
// using Sonyflake — a distributed unique ID generator that produces
// time-sorted 64-bit integers encoded as decimal strings.
package idgen

import (
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/sony/sonyflake/v2"
)

// epoch is the Sonyflake epoch: 2024-01-01 UTC.
// Sonyflake measures elapsed time from this point.
var epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

var sf *sonyflake.Sonyflake

func init() {
	var err error
	sf, err = newSonyflake(sonyflake.Settings{StartTime: epoch})
	if err != nil {
		panic(fmt.Sprintf("idgen: failed to initialize sonyflake: %v", err))
	}
}

// newSonyflake creates a generator from st. Sonyflake's default machine ID is
// the lower 16 bits of the host's private IPv4 address. A host or container
// without one (no IPv4 network, a non-RFC 1918 subnet, or host networking on a
// public address) gets a random machine ID instead of failing to start.
func newSonyflake(st sonyflake.Settings) (*sonyflake.Sonyflake, error) {
	gen, err := sonyflake.New(st)
	if errors.Is(err, sonyflake.ErrNoPrivateAddress) {
		machineID := rand.IntN(1 << 16)
		slog.Warn("idgen: no private IPv4 address; using a random Sonyflake machine ID",
			"machine_id", machineID)
		st.MachineID = func() (int, error) { return machineID, nil }
		gen, err = sonyflake.New(st)
	}
	if errors.Is(err, sonyflake.ErrStartTimeAhead) {
		return nil, fmt.Errorf("system clock %s is before the ID epoch %s: %w",
			time.Now().UTC().Format(time.RFC3339), st.StartTime.UTC().Format(time.RFC3339), err)
	}
	return gen, err
}

// NextID returns a new unique Sonyflake ID as a decimal string.
func NextID() (string, error) {
	id, err := sf.NextID()
	if err != nil {
		return "", fmt.Errorf("idgen: %w", err)
	}
	return strconv.FormatInt(id, 10), nil
}
