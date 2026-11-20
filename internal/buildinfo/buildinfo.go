package buildinfo

import (
	"os"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
)

const unavailableDisplay = "unavailable"

var (
	revisionOverride    string
	dirtyOverride       string
	buildNumberOverride string
	builtAtOverride     string
	versionOverride     string
)

// versionFile holds the release version of a published release image. The
// release workflow adds it as a layer on top of the image CI already built and
// tested, so the binary itself never changes between a build and its release.
// Builds outside Docker can set versionOverride with -ldflags instead.
const versionFile = "/usr/share/silo/version"

// releaseVersion matches a release tag without its leading v: 0.24.0 or
// 1.0.0-rc.1. Anything else in the file or the ldflag is ignored.
var releaseVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-rc\.[1-9][0-9]*)?$`)

// Info describes the running Silo build from Go VCS metadata and CI-injected
// container identity. Display is the release version for a release build and
// the short revision otherwise.
type Info struct {
	Display     string `json:"display"`
	Version     string `json:"version,omitempty"`
	Revision    string `json:"revision"`
	Dirty       bool   `json:"dirty"`
	VCSTime     string `json:"vcs_time"`
	BuildNumber uint64 `json:"build_number"`
	BuiltAt     string `json:"built_at"`
	Available   bool   `json:"available"`
}

// Current reads build metadata from the running binary.
func Current() Info {
	overrideRevision, overrideDirty := parseOverrides(revisionOverride, dirtyOverride)
	overrideBuildNumber := parseBuildNumber(buildNumberOverride)
	overrideBuiltAt := strings.TrimSpace(builtAtOverride)

	var current Info
	if info, ok := debug.ReadBuildInfo(); ok {
		current = resolve(info.Settings, overrideRevision, overrideDirty, overrideBuildNumber, overrideBuiltAt)
	} else {
		current = buildInfo(overrideRevision, overrideDirty, "", overrideBuildNumber, overrideBuiltAt)
	}
	return withVersion(current, version())
}

// version is read once: the file belongs to the image and cannot change while
// the process runs.
var version = sync.OnceValue(func() string { return readVersion(versionOverride, versionFile) })

func readVersion(override, path string) string {
	if v := strings.TrimSpace(override); v != "" {
		return validVersion(v)
	}
	data, err := os.ReadFile(path) //nolint:gosec // G304: versionFile in production, a temp file in tests
	if err != nil {
		return ""
	}
	return validVersion(strings.TrimSpace(string(data)))
}

func validVersion(v string) string {
	if !releaseVersion.MatchString(v) {
		return ""
	}
	return v
}

// withVersion names a release build by its version, keeping the revision in
// Revision and a dirty tree visible in Display.
func withVersion(info Info, version string) Info {
	if version == "" || !info.Available {
		return info
	}
	info.Version = version
	info.Display = version
	if info.Dirty {
		info.Display += "+dirty"
	}
	return info
}

// UserAgent names this Silo build for third-party APIs that ask callers to
// identify their application and version, for example "Silo/ce6a0f53". A build
// without revision metadata reports "Silo/dev".
func UserAgent() string { return userAgent() }

var userAgent = sync.OnceValue(func() string { return userAgentFor(Current()) })

// userAgentFor keeps only HTTP token characters of the build's display name,
// so a revision injected at build time can never make the header invalid
// and fail every request that carries it.
func userAgentFor(info Info) string {
	version := ""
	if info.Available {
		version = strings.Map(func(r rune) rune {
			if r < 0x80 && (r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
				return r
			}
			return -1
		}, info.Display)
	}
	if version == "" {
		version = "dev"
	}
	return "Silo/" + version
}

func resolve(settings []debug.BuildSetting, fallbackRevision string, fallbackDirty bool, buildNumber uint64, builtAt string) Info {
	var (
		revision string
		vcsTime  string
		dirty    bool
	)

	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.time":
			vcsTime = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			dirty = strings.EqualFold(strings.TrimSpace(setting.Value), "true")
		}
	}

	if revision != "" {
		return buildInfo(revision, dirty, vcsTime, buildNumber, builtAt)
	}

	return buildInfo(fallbackRevision, fallbackDirty, "", buildNumber, builtAt)
}

func parseOverrides(revision, dirty string) (string, bool) {
	return strings.TrimSpace(revision), strings.EqualFold(strings.TrimSpace(dirty), "true")
}

func parseBuildNumber(value string) uint64 {
	buildNumber, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return buildNumber
}

func buildInfo(revision string, dirty bool, vcsTime string, buildNumber uint64, builtAt string) Info {
	revision = strings.TrimSpace(revision)
	vcsTime = strings.TrimSpace(vcsTime)
	builtAt = strings.TrimSpace(builtAt)
	if revision == "" {
		return unavailableInfo()
	}

	display := revision
	if len(display) > 8 {
		display = display[:8]
	}
	if dirty {
		display += "+dirty"
	}

	return Info{
		Display:     display,
		Revision:    revision,
		Dirty:       dirty,
		VCSTime:     vcsTime,
		BuildNumber: buildNumber,
		BuiltAt:     builtAt,
		Available:   true,
	}
}

func unavailableInfo() Info {
	return Info{
		Display:     unavailableDisplay,
		Revision:    "",
		Dirty:       false,
		VCSTime:     "",
		BuildNumber: 0,
		BuiltAt:     "",
		Available:   false,
	}
}
