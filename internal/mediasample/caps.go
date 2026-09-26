package mediasample

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/Silo-Server/silo-server/internal/tonemap"
)

// listingTimeout bounds each ffmpeg listing command.
const listingTimeout = 3 * time.Second

// Capabilities is what an ffmpeg binary offers sampling runs. The zero value
// offers nothing.
type Capabilities struct {
	filters map[string]struct{}
	muxers  map[string]struct{}
	// chromaprintRaw records that the chromaprint muxer offers fp_format raw.
	chromaprintRaw bool
}

// HasFilter reports whether ffmpeg lists the named filter.
func (c Capabilities) HasFilter(name string) bool {
	_, ok := c.filters[name]
	return ok
}

// HasMuxer reports whether ffmpeg lists the named muxer.
func (c Capabilities) HasMuxer(name string) bool {
	_, ok := c.muxers[name]
	return ok
}

// statsFilters are the filters a Stats output needs beyond ffmpeg's
// built-in crop, scale, and format.
var statsFilters = []string{filterBlackframe, filterSignalstats, filterMetadata}

// Require reports the first thing req needs that the binary lacks.
func (c Capabilities) Require(req Request) error {
	if req.Audio != nil && req.Audio.Fingerprint {
		if !c.HasMuxer("chromaprint") {
			return errors.New("ffmpeg does not list the chromaprint muxer")
		}
		if !c.chromaprintRaw {
			return errors.New("ffmpeg chromaprint muxer does not advertise raw fingerprint output")
		}
	}
	if req.Audio != nil && req.Audio.Silence != nil && !c.HasFilter("silencedetect") {
		return errors.New("ffmpeg does not list the silencedetect filter")
	}
	if req.Stats != nil {
		for _, filter := range statsFilters {
			if !c.HasFilter(filter) {
				return fmt.Errorf("ffmpeg does not list the %s filter", filter)
			}
		}
	}
	return nil
}

// listFunc runs one bounded ffmpeg listing and returns its combined output.
type listFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

func runListing(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

var capsCache = struct {
	sync.Mutex
	entries map[string]Capabilities
	group   singleflight.Group
	// generation counts invalidations and is part of every cache and
	// singleflight key, so InvalidateCapabilities supersedes a load already in
	// flight: that load stores its inventory under a key nobody asks for.
	generation uint64
	// list runs the listing commands; tests replace it.
	list listFunc
}{entries: make(map[string]Capabilities), list: runListing}

// LoadCapabilities returns the inventory for the ffmpeg binary at ffmpegPath.
// Inventories are cached per binary identity (resolved path, size, and
// modification time), so replacing the binary in place loads a new one.
// Concurrent loads of the same binary share one set of commands, which run on
// their own deadlines so one caller's cancellation cannot fail the others.
// Failures are not cached.
func LoadCapabilities(ctx context.Context, ffmpegPath string) (Capabilities, error) {
	capsCache.Lock()
	key := capabilitiesKey(capsCache.generation, ffmpegPath)
	if cached, ok := capsCache.entries[key]; ok {
		capsCache.Unlock()
		return cached, nil
	}
	list := capsCache.list
	capsCache.Unlock()

	resultCh := capsCache.group.DoChan(key, func() (any, error) {
		capsCache.Lock()
		cached, ok := capsCache.entries[key]
		capsCache.Unlock()
		if ok {
			return cached, nil
		}
		caps, err := loadCapabilities(ffmpegPath, list)
		if err != nil {
			return nil, err
		}
		capsCache.Lock()
		capsCache.entries[key] = caps
		capsCache.Unlock()
		return caps, nil
	})
	select {
	case <-ctx.Done():
		return Capabilities{}, ctx.Err()
	case result := <-resultCh:
		if result.Err != nil {
			return Capabilities{}, result.Err
		}
		caps, ok := result.Val.(Capabilities)
		if !ok {
			return Capabilities{}, errors.New("invalid shared ffmpeg capability result")
		}
		return caps, nil
	}
}

// InvalidateCapabilities drops every cached inventory. The hardware re-probe
// calls it beside tonemap.InvalidateProbeCache, so an operator who upgraded
// ffmpeg without changing its identity still gets a fresh inventory.
func InvalidateCapabilities() {
	capsCache.Lock()
	defer capsCache.Unlock()
	capsCache.generation++
	capsCache.entries = make(map[string]Capabilities)
}

func capabilitiesKey(generation uint64, ffmpegPath string) string {
	identity := strings.TrimSpace(ffmpegPath)
	if _, key, ok := tonemap.FFmpegBinaryIdentity(identity); ok {
		identity = key
	}
	return strconv.FormatUint(generation, 10) + "\x00" + identity
}

func loadCapabilities(ffmpegPath string, list listFunc) (Capabilities, error) {
	run := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), listingTimeout)
		defer cancel()
		return list(ctx, ffmpegPath, args...)
	}
	filters, err := run("-hide_banner", "-filters")
	if err != nil {
		return Capabilities{}, fmt.Errorf("ffmpeg filter listing failed: %w", err)
	}
	muxers, err := run("-hide_banner", "-muxers")
	if err != nil {
		return Capabilities{}, fmt.Errorf("ffmpeg muxer preflight failed: %w", err)
	}
	caps := Capabilities{filters: parseFilterList(filters), muxers: parseMuxerList(muxers)}
	if caps.HasMuxer("chromaprint") {
		help, err := run("-hide_banner", "-h", "muxer=chromaprint")
		if err != nil {
			return Capabilities{}, fmt.Errorf("ffmpeg chromaprint help failed: %w", err)
		}
		lower := bytes.ToLower(help)
		caps.chromaprintRaw = bytes.Contains(lower, []byte("fp_format")) && bytes.Contains(lower, []byte("raw"))
	}
	return caps, nil
}

// parseFilterList reads `ffmpeg -filters`. Filter rows are a flags column,
// the name, and an input->output signature such as "A->A" or "|->V"; the
// legend rows above them have no signature.
func parseFilterList(output []byte) map[string]struct{} {
	filters := make(map[string]struct{})
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.Contains(fields[2], "->") {
			continue
		}
		filters[fields[1]] = struct{}{}
	}
	return filters
}

// parseMuxerList reads `ffmpeg -muxers`. Rows after the "--" separator are a
// flags column ("E", "DE", "Ed") and a comma-separated list of names.
func parseMuxerList(output []byte) map[string]struct{} {
	muxers := make(map[string]struct{})
	inRows := false
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if !inRows {
			inRows = strings.Trim(fields[0], "-") == "" && len(fields) == 1
			continue
		}
		if len(fields) < 2 || !strings.Contains(fields[0], "E") || strings.Trim(fields[0], "DEd.") != "" {
			continue
		}
		for _, name := range strings.Split(fields[1], ",") {
			if name != "" {
				muxers[name] = struct{}{}
			}
		}
	}
	return muxers
}
