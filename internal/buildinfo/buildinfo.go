package buildinfo

import (
	"runtime/debug"
	"strconv"
	"strings"
)

const unavailableDisplay = "unavailable"

var (
	revisionOverride    string
	dirtyOverride       string
	buildNumberOverride string
	builtAtOverride     string
)

// Info describes the running Silo build from Go VCS metadata and CI-injected
// container identity.
type Info struct {
	Display     string `json:"display"`
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

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return buildInfo(overrideRevision, overrideDirty, "", overrideBuildNumber, overrideBuiltAt)
	}
	return resolve(info.Settings, overrideRevision, overrideDirty, overrideBuildNumber, overrideBuiltAt)
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
