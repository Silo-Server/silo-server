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

// CapabilitiesTimeout bounds an uncached LoadCapabilities, which runs up to
// four listings (filters, muxers, Chromaprint's options, the concat check),
// each on listingTimeout.
const CapabilitiesTimeout = 4 * listingTimeout

// Capabilities is what an ffmpeg binary offers sampling runs. The zero value
// offers nothing.
type Capabilities struct {
	filters map[string]struct{}
	muxers  map[string]struct{}
	// chromaprintRaw records that the chromaprint muxer offers fp_format raw.
	chromaprintRaw bool
	// concatSamples records that the concat demuxer reads a Samples list
	// from stdin; see checkConcatSamples.
	concatSamples bool
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

// ErrUnsupported marks a Require error: the binary's capability listing
// succeeded and lacks something the request needs. A failed listing is not
// ErrUnsupported, because it proves nothing about the binary.
var ErrUnsupported = errors.New("ffmpeg lacks a required capability")

// unsupportedError keeps Require's specific message while matching
// ErrUnsupported.
type unsupportedError string

func (e unsupportedError) Error() string        { return string(e) }
func (e unsupportedError) Is(target error) bool { return target == ErrUnsupported }

// Require reports the first thing req needs that the binary lacks. Its errors
// match ErrUnsupported.
func (c Capabilities) Require(req Request) error {
	if req.Audio != nil && req.Audio.Fingerprint {
		if !c.HasMuxer("chromaprint") {
			return unsupportedError("ffmpeg does not list the chromaprint muxer")
		}
		if !c.chromaprintRaw {
			return unsupportedError("ffmpeg chromaprint muxer does not advertise raw fingerprint output")
		}
	}
	if req.Audio != nil && req.Audio.Silence != nil && !c.HasFilter("silencedetect") {
		return unsupportedError("ffmpeg does not list the silencedetect filter")
	}
	if req.Samples != nil && !c.concatSamples {
		return unsupportedError("ffmpeg cannot read a sampled input list (concat demuxer with file_packet_meta, file and pipe protocols)")
	}
	if req.Stats != nil {
		for _, filter := range statsFilters {
			if !c.HasFilter(filter) {
				return unsupportedError(fmt.Sprintf("ffmpeg does not list the %s filter", filter))
			}
		}
	}
	return nil
}

// listFunc runs one bounded ffmpeg listing, with stdin when it is not nil,
// and returns its combined output.
type listFunc func(ctx context.Context, name string, stdin []byte, args ...string) ([]byte, error)

func runListing(ctx context.Context, name string, stdin []byte, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.CombinedOutput()
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
	runWith := func(stdin []byte, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), listingTimeout)
		defer cancel()
		return list(ctx, ffmpegPath, stdin, args...)
	}
	run := func(args ...string) ([]byte, error) { return runWith(nil, args...) }
	filters, err := run(hideBanner, "-filters")
	if err != nil {
		return Capabilities{}, fmt.Errorf("ffmpeg filter listing failed: %w", err)
	}
	muxers, err := run(hideBanner, "-muxers")
	if err != nil {
		return Capabilities{}, fmt.Errorf("ffmpeg muxer preflight failed: %w", err)
	}
	caps := Capabilities{filters: parseFilterList(filters), muxers: parseMuxerList(muxers)}
	if caps.HasMuxer("chromaprint") {
		help, err := run(hideBanner, "-h", "muxer=chromaprint")
		if err != nil {
			return Capabilities{}, fmt.Errorf("ffmpeg chromaprint help failed: %w", err)
		}
		lower := bytes.ToLower(help)
		caps.chromaprintRaw = bytes.Contains(lower, []byte("fp_format")) && bytes.Contains(lower, []byte("raw"))
	}
	caps.concatSamples = checkConcatSamples(runWith)
	return caps, nil
}

// concatCheckInput is the input the concat check names. It must not exist.
const concatCheckInput = "/nonexistent/silo-concat-check.mkv"

// checkConcatSamples reports whether ffmpeg reads a Samples list: it hands
// ffmpeg a one-sample list, with every directive a Samples list uses, naming
// an input that does not exist. An ffmpeg that parses the list goes on to
// open that input and fails there, which the concat demuxer reports as
// "Impossible to open"; one that lacks the demuxer, a directive, or a
// protocol fails before, typically with "Invalid data found when processing
// input". Without this check that message would read as a broken file and
// mark every sampled input unusable.
func checkConcatSamples(run func(stdin []byte, args ...string) ([]byte, error)) bool {
	list, err := buildConcatList(concatCheckInput, []float64{1}, 0)
	if err != nil {
		return false
	}
	args := append(quietArgs(errorLogLevel), concatInputArgs...)
	args = append(args, "-i", concatListInput, "-f", "null", "-")
	// The run fails by design, so only its output counts.
	output, _ := run(list, args...)
	return bytes.Contains(bytes.ToLower(output), []byte("impossible to open 'file:"+concatCheckInput+"'"))
}

// FilterCapabilities returns the capabilities an `ffmpeg -filters` listing
// shows: its filters, and no muxers or sampled inputs. It serves tests of
// packages that give a Runner its capabilities.
func FilterCapabilities(listing []byte) Capabilities {
	return Capabilities{filters: parseFilterList(listing)}
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
