package buildinfo

import (
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

func TestCurrentIncludesContainerIdentity(t *testing.T) {
	previousRevision := revisionOverride
	previousDirty := dirtyOverride
	previousBuildNumber := buildNumberOverride
	previousBuiltAt := builtAtOverride
	t.Cleanup(func() {
		revisionOverride = previousRevision
		dirtyOverride = previousDirty
		buildNumberOverride = previousBuildNumber
		builtAtOverride = previousBuiltAt
	})

	revisionOverride = "edf2977f5013df08e57a869bf722af4243a0a4fd"
	dirtyOverride = "false"
	buildNumberOverride = "411"
	builtAtOverride = "2026-08-19T19:45:00Z"

	got := Current()
	if got.BuildNumber != 411 || got.BuiltAt != "2026-08-19T19:45:00Z" {
		t.Fatalf("Current() container identity = build %d at %q", got.BuildNumber, got.BuiltAt)
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		settings         []debug.BuildSetting
		overrideRevision string
		overrideDirty    string
		buildNumber      uint64
		builtAt          string
		want             Info
	}{
		{
			name: "clean revision",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "b4c5aae18aa653725ac697b29a05eac797576008"},
				{Key: "vcs.modified", Value: "false"},
				{Key: "vcs.time", Value: "2026-04-05T22:24:40Z"},
			},
			buildNumber: 411,
			builtAt:     "2026-08-19T19:45:00Z",
			want: Info{
				Display:     "b4c5aae1",
				Revision:    "b4c5aae18aa653725ac697b29a05eac797576008",
				Dirty:       false,
				VCSTime:     "2026-04-05T22:24:40Z",
				BuildNumber: 411,
				BuiltAt:     "2026-08-19T19:45:00Z",
				Available:   true,
			},
		},
		{
			name: "dirty revision",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "b4c5aae18aa653725ac697b29a05eac797576008"},
				{Key: "vcs.modified", Value: "true"},
				{Key: "vcs.time", Value: "2026-04-05T22:24:40Z"},
			},
			want: Info{
				Display:   "b4c5aae1+dirty",
				Revision:  "b4c5aae18aa653725ac697b29a05eac797576008",
				Dirty:     true,
				VCSTime:   "2026-04-05T22:24:40Z",
				Available: true,
			},
		},
		{
			name: "missing revision",
			settings: []debug.BuildSetting{
				{Key: "vcs.modified", Value: "true"},
				{Key: "vcs.time", Value: "2026-04-05T22:24:40Z"},
			},
			want: Info{
				Display:   "unavailable",
				Revision:  "",
				Dirty:     false,
				VCSTime:   "",
				Available: false,
			},
		},
		{
			name: "override revision",
			settings: []debug.BuildSetting{
				{Key: "vcs.modified", Value: "false"},
			},
			overrideRevision: "edf2977f5013df08e57a869bf722af4243a0a4fd",
			want: Info{
				Display:   "edf2977f",
				Revision:  "edf2977f5013df08e57a869bf722af4243a0a4fd",
				Dirty:     false,
				VCSTime:   "",
				Available: true,
			},
		},
		{
			name: "override dirty revision",
			settings: []debug.BuildSetting{
				{Key: "vcs.time", Value: "2026-04-05T22:24:40Z"},
			},
			overrideRevision: "edf2977f5013df08e57a869bf722af4243a0a4fd",
			overrideDirty:    "true",
			want: Info{
				Display:   "edf2977f+dirty",
				Revision:  "edf2977f5013df08e57a869bf722af4243a0a4fd",
				Dirty:     true,
				VCSTime:   "",
				Available: true,
			},
		},
		{
			name:             "ordered container build",
			overrideRevision: "edf2977f5013df08e57a869bf722af4243a0a4fd",
			buildNumber:      411,
			builtAt:          " 2026-08-19T19:45:00Z ",
			want: Info{
				Display:     "edf2977f",
				Revision:    "edf2977f5013df08e57a869bf722af4243a0a4fd",
				Dirty:       false,
				VCSTime:     "",
				BuildNumber: 411,
				BuiltAt:     "2026-08-19T19:45:00Z",
				Available:   true,
			},
		},
		{
			name:             "override short revision",
			overrideRevision: "abc123",
			want: Info{
				Display:   "abc123",
				Revision:  "abc123",
				Dirty:     false,
				VCSTime:   "",
				Available: true,
			},
		},
		{
			name:             "empty override remains unavailable",
			overrideRevision: "   ",
			overrideDirty:    "true",
			want: Info{
				Display:   "unavailable",
				Revision:  "",
				Dirty:     false,
				VCSTime:   "",
				Available: false,
			},
		},
		{
			name: "embedded revision wins over override",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "b4c5aae18aa653725ac697b29a05eac797576008"},
				{Key: "vcs.modified", Value: "false"},
				{Key: "vcs.time", Value: "2026-04-05T22:24:40Z"},
			},
			overrideRevision: "edf2977f5013df08e57a869bf722af4243a0a4fd",
			overrideDirty:    "true",
			want: Info{
				Display:   "b4c5aae1",
				Revision:  "b4c5aae18aa653725ac697b29a05eac797576008",
				Dirty:     false,
				VCSTime:   "2026-04-05T22:24:40Z",
				Available: true,
			},
		},
		{
			name: "short revision",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "abc123"},
				{Key: "vcs.modified", Value: "false"},
			},
			want: Info{
				Display:   "abc123",
				Revision:  "abc123",
				Dirty:     false,
				VCSTime:   "",
				Available: true,
			},
		},
		{
			name: "missing vcs time",
			settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "b4c5aae18aa653725ac697b29a05eac797576008"},
				{Key: "vcs.modified", Value: "false"},
			},
			want: Info{
				Display:   "b4c5aae1",
				Revision:  "b4c5aae18aa653725ac697b29a05eac797576008",
				Dirty:     false,
				VCSTime:   "",
				Available: true,
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			overrideRevision, overrideDirty := parseOverrides(tc.overrideRevision, tc.overrideDirty)
			got := resolve(tc.settings, overrideRevision, overrideDirty, tc.buildNumber, tc.builtAt)
			if got != tc.want {
				t.Fatalf("resolve() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestParseBuildNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		want  uint64
	}{
		{value: "411", want: 411},
		{value: " 411 ", want: 411},
		{value: ""},
		{value: "0"},
		{value: "-1"},
		{value: "not-a-number"},
	}

	for _, tc := range tests {
		if got := parseBuildNumber(tc.value); got != tc.want {
			t.Fatalf("parseBuildNumber(%q) = %d, want %d", tc.value, got, tc.want)
		}
	}
}

// Go refuses to send a request whose header value holds a control character,
// so an odd injected revision must not reach the User-Agent verbatim.
func TestUserAgentForKeepsHeaderTokenCharacters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		info Info
		want string
	}{
		{info: Info{Available: true, Display: "ce6a0f53+dirty"}, want: "Silo/ce6a0f53+dirty"},
		{info: Info{Available: true, Display: "ce6a 0f53\r\n"}, want: "Silo/ce6a0f53"},
		{info: Info{Available: true, Display: "\n"}, want: "Silo/dev"},
		{info: unavailableInfo(), want: "Silo/dev"},
	}
	for _, tt := range tests {
		if got := userAgentFor(tt.info); got != tt.want {
			t.Errorf("userAgentFor(%q) = %q, want %q", tt.info.Display, got, tt.want)
		}
	}
}

func TestWithVersion(t *testing.T) {
	t.Parallel()

	clean := Info{Display: "b4c5aae1", Revision: "b4c5aae18aa653725ac697b29a05eac797576008", BuildNumber: 411, Available: true}
	got := withVersion(clean, "0.24.0")
	if got.Display != "0.24.0" || got.Version != "0.24.0" || got.Revision != clean.Revision || got.BuildNumber != 411 {
		t.Fatalf("withVersion(release) = %+v", got)
	}

	dirty := clean
	dirty.Dirty = true
	if got := withVersion(dirty, "1.0.0-rc.1").Display; got != "1.0.0-rc.1+dirty" {
		t.Fatalf("withVersion(dirty).Display = %q", got)
	}

	if got := withVersion(clean, ""); got != clean {
		t.Fatalf("withVersion(no version) = %+v, want unchanged", got)
	}
	if got := withVersion(unavailableInfo(), "0.24.0"); got.Version != "" || got.Display != unavailableDisplay {
		t.Fatalf("withVersion(unavailable) = %+v, want unchanged", got)
	}
}

func TestValidVersion(t *testing.T) {
	t.Parallel()

	for _, v := range []string{"0.24.0", "1.0.0", "1.0.0-rc.1", "1.1.0-rc.12"} {
		if got := validVersion(v); got != v {
			t.Errorf("validVersion(%q) = %q, want it kept", v, got)
		}
	}
	for _, v := range []string{"", "v0.24.0", "0.24", "1.0.0-RC1", "1.0.0-beta.1", "1.0.0-rc.0", "01.0.0", "0.24.0\nrm -rf"} {
		if got := validVersion(v); got != "" {
			t.Errorf("validVersion(%q) = %q, want it rejected", v, got)
		}
	}
}

func TestReadVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "version")
	if err := os.WriteFile(file, []byte("0.24.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readVersion("", file); got != "0.24.0" {
		t.Fatalf("readVersion(file) = %q", got)
	}
	if got := readVersion("1.0.0-rc.1", file); got != "1.0.0-rc.1" {
		t.Fatalf("readVersion(ldflag) = %q, want the ldflag to win", got)
	}
	if got := readVersion("", filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("readVersion(missing file) = %q", got)
	}
	if err := os.WriteFile(file, []byte("build-1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readVersion("", file); got != "" {
		t.Fatalf("readVersion(invalid file) = %q", got)
	}
}
