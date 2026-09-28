//go:build linux

package librarymonitor

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// integrationBackend is one backend the scenario table runs against.
type integrationBackend struct {
	name      string
	configure func(t *testing.T, cfg *Config)
}

var integrationBackends = []integrationBackend{
	{
		name:      "inotify",
		configure: func(*testing.T, *Config) {},
	},
}

// scenarioEnv is one scenario's filesystem: root is the library's configured
// path, outside is a sibling on the same filesystem for moves and hardlinks.
type scenarioEnv struct {
	root    string
	outside string
	// walks receives the path of every walk after the initial one.
	walks chan string
}

// waitWalk waits until the monitor walked the library folder again.
func (e scenarioEnv) waitWalk(t *testing.T) {
	t.Helper()
	select {
	case <-e.walks:
	case <-time.After(waitTimeout):
		t.Fatal("timed out waiting for the library folder to be walked again")
	}
}

func (e scenarioEnv) in(parts ...string) string {
	return filepath.Join(append([]string{e.root}, parts...)...)
}

func (e scenarioEnv) out(parts ...string) string {
	return filepath.Join(append([]string{e.outside}, parts...)...)
}

// subtree is the key of a subtree target in library 1.
func (e scenarioEnv) subtree(parts ...string) string {
	return fmt.Sprintf("1 subtree %s %s", e.in(parts...), Trigger)
}

type scenarioStep struct {
	act func(t *testing.T, e scenarioEnv)
	// want lists the target keys this step must produce.
	want func(e scenarioEnv) []string
}

type scenario struct {
	name string
	// symlinkRoot configures the library through a symlink to the real
	// folder; targets must still use the configured path.
	symlinkRoot bool
	// setup builds the layout before monitoring starts; the initial walk
	// must queue nothing for it.
	setup func(t *testing.T, e scenarioEnv)
	steps []scenarioStep
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func integrationScenarios() []scenario {
	return []scenario{
		{
			name:  "copy into an existing folder",
			setup: func(t *testing.T, e scenarioEnv) { mkdirs(t, e.root, "Movie A") },
			steps: []scenarioStep{{
				act:  func(t *testing.T, e scenarioEnv) { writeFile(t, e.in("Movie A", "Movie A (2020).mkv"), "data") },
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie A")} },
			}},
		},
		{
			name:        "copy through a symlinked root",
			symlinkRoot: true,
			setup:       func(t *testing.T, e scenarioEnv) { mkdirs(t, e.root, "Movie A") },
			steps: []scenarioStep{{
				act:  func(t *testing.T, e scenarioEnv) { writeFile(t, e.in("Movie A", "a.mkv"), "data") },
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie A")} },
			}},
		},
		{
			name: "hardlink import",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Movie B")
				writeFile(t, e.out("download.mkv"), "data")
			},
			steps: []scenarioStep{{
				act: func(t *testing.T, e scenarioEnv) {
					if err := os.Link(e.out("download.mkv"), e.in("Movie B", "Movie B.mkv")); err != nil {
						t.Fatal(err)
					}
				},
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie B")} },
			}},
		},
		{
			name: "file moved in",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Movie C")
				writeFile(t, e.out("c.mkv"), "data")
			},
			steps: []scenarioStep{{
				act:  func(t *testing.T, e scenarioEnv) { mustRename(t, e.out("c.mkv"), e.in("Movie C", "c.mkv")) },
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie C")} },
			}},
		},
		{
			name: "folder moved in is watched afterwards",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.outside, "Movie D")
				writeFile(t, e.out("Movie D", "d.mkv"), "data")
			},
			steps: []scenarioStep{
				{
					act:  func(t *testing.T, e scenarioEnv) { mustRename(t, e.out("Movie D"), e.in("Movie D")) },
					want: func(e scenarioEnv) []string { return []string{e.subtree("Movie D")} },
				},
				{
					act: func(t *testing.T, e scenarioEnv) {
						mkdirs(t, e.root, "Movie D/Featurettes")
						writeFile(t, e.in("Movie D", "Featurettes", "f.mkv"), "data")
					},
					want: func(e scenarioEnv) []string { return []string{e.subtree("Movie D", "Featurettes")} },
				},
			},
		},
		{
			name: "file moved out",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Movie E")
				writeFile(t, e.in("Movie E", "e.mkv"), "data")
			},
			steps: []scenarioStep{{
				act:  func(t *testing.T, e scenarioEnv) { mustRename(t, e.in("Movie E", "e.mkv"), e.out("e.mkv")) },
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie E")} },
			}},
		},
		{
			name: "folder moved out leaves no stale watches",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Movie F/Extras")
				writeFile(t, e.in("Movie F", "f.mkv"), "data")
			},
			steps: []scenarioStep{
				{
					act:  func(t *testing.T, e scenarioEnv) { mustRename(t, e.in("Movie F"), e.out("Movie F")) },
					want: func(e scenarioEnv) []string { return []string{e.subtree("Movie F")} },
				},
				{
					// Changes in the moved-out folder must not surface under
					// its old path; the sentinel check catches any. Deletes
					// matter most: they would resolve to vanished targets.
					act: func(t *testing.T, e scenarioEnv) {
						writeFile(t, e.out("Movie F", "late.mkv"), "data")
						if err := os.Remove(e.out("Movie F", "f.mkv")); err != nil {
							t.Fatal(err)
						}
						if err := os.RemoveAll(e.out("Movie F", "Extras")); err != nil {
							t.Fatal(err)
						}
					},
				},
			},
		},
		{
			name: "file renamed",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Movie G")
				writeFile(t, e.in("Movie G", "g.mkv"), "data")
			},
			steps: []scenarioStep{{
				act:  func(t *testing.T, e scenarioEnv) { mustRename(t, e.in("Movie G", "g.mkv"), e.in("Movie G", "g2.mkv")) },
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie G")} },
			}},
		},
		{
			name:  "folder renamed rewrites paths below it",
			setup: func(t *testing.T, e scenarioEnv) { mkdirs(t, e.root, "Movie H/Extras") },
			steps: []scenarioStep{
				{
					act:  func(t *testing.T, e scenarioEnv) { mustRename(t, e.in("Movie H"), e.in("Movie H2")) },
					want: func(e scenarioEnv) []string { return []string{e.subtree("Movie H"), e.subtree("Movie H2")} },
				},
				{
					act:  func(t *testing.T, e scenarioEnv) { writeFile(t, e.in("Movie H2", "Extras", "x.mkv"), "data") },
					want: func(e scenarioEnv) []string { return []string{e.subtree("Movie H2", "Extras")} },
				},
			},
		},
		{
			name: "file deleted",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Movie I")
				writeFile(t, e.in("Movie I", "i.mkv"), "data")
			},
			steps: []scenarioStep{{
				act: func(t *testing.T, e scenarioEnv) {
					if err := os.Remove(e.in("Movie I", "i.mkv")); err != nil {
						t.Fatal(err)
					}
				},
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie I")} },
			}},
		},
		{
			name: "folder deleted",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Movie J/Extras")
				writeFile(t, e.in("Movie J", "j.mkv"), "data")
				writeFile(t, e.in("Movie J", "Extras", "x.mkv"), "data")
			},
			steps: []scenarioStep{{
				act: func(t *testing.T, e scenarioEnv) {
					if err := os.RemoveAll(e.in("Movie J")); err != nil {
						t.Fatal(err)
					}
				},
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie J")} },
			}},
		},
		{
			name:  "burst in one folder",
			setup: func(t *testing.T, e scenarioEnv) { mkdirs(t, e.root, "Movie K") },
			steps: []scenarioStep{{
				act: func(t *testing.T, e scenarioEnv) {
					for i := 0; i < 50; i++ {
						writeFile(t, e.in("Movie K", fmt.Sprintf("part %02d.mkv", i)), "data")
					}
				},
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie K")} },
			}},
		},
		{
			name: "new folder filled by a copy",
			steps: []scenarioStep{{
				act: func(t *testing.T, e scenarioEnv) {
					mkdirs(t, e.root, "Movie L/Subs")
					writeFile(t, e.in("Movie L", "l.mkv"), "data")
					writeFile(t, e.in("Movie L", "Subs", "l.srt"), "data")
				},
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie L")} },
			}},
		},
		{
			name:  "atomic write through a hidden temp name",
			setup: func(t *testing.T, e scenarioEnv) { mkdirs(t, e.root, "Movie N") },
			steps: []scenarioStep{{
				// rsync's pattern; the temp name is not on the ignore list,
				// and its disappearance must not queue a scan of its own.
				act: func(t *testing.T, e scenarioEnv) {
					writeFile(t, e.in("Movie N", ".Movie N.mkv.Xy12Ab"), "data")
					mustRename(t, e.in("Movie N", ".Movie N.mkv.Xy12Ab"), e.in("Movie N", "Movie N.mkv"))
				},
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie N")} },
			}},
		},
		{
			name: "symlinked folder renamed, then deleted",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.outside, "Target")
				if err := os.Symlink(e.out("Target"), e.in("Link")); err != nil {
					t.Fatal(err)
				}
			},
			steps: []scenarioStep{
				{
					act:  func(t *testing.T, e scenarioEnv) { mustRename(t, e.in("Link"), e.in("Link2")) },
					want: func(e scenarioEnv) []string { return []string{e.subtree("Link"), e.subtree("Link2")} },
				},
				{
					// Changes in the target surface under the new name.
					act:  func(t *testing.T, e scenarioEnv) { writeFile(t, e.out("Target", "new.mkv"), "data") },
					want: func(e scenarioEnv) []string { return []string{e.subtree("Link2")} },
				},
				{
					act: func(t *testing.T, e scenarioEnv) {
						if err := os.Remove(e.in("Link2")); err != nil {
							t.Fatal(err)
						}
					},
					want: func(e scenarioEnv) []string { return []string{e.subtree("Link2")} },
				},
			},
		},
		{
			name: "nothing in a folder with .nomedia is scanned",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Ignored")
				writeFile(t, e.in("Ignored", ".nomedia"), "")
			},
			steps: []scenarioStep{{
				act: func(t *testing.T, e scenarioEnv) {
					writeFile(t, e.in("Ignored", "a.mkv"), "data")
					mkdirs(t, e.root, "Ignored/Sub")
					writeFile(t, e.in("Ignored", "Sub", "b.mkv"), "data")
				},
			}},
		},
		{
			name: "folder with .nomedia is watched again once the marker goes",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Movie Q/Extras", "Movie Q/Other")
				writeFile(t, e.in("Movie Q", ".nomedia"), "")
				writeFile(t, e.in("Movie Q", "q.mkv"), "data")
			},
			steps: []scenarioStep{
				{
					act: func(t *testing.T, e scenarioEnv) {
						if err := os.Remove(e.in("Movie Q", ".nomedia")); err != nil {
							t.Fatal(err)
						}
					},
					want: func(e scenarioEnv) []string { return []string{e.subtree("Movie Q")} },
				},
				{
					act:  func(t *testing.T, e scenarioEnv) { writeFile(t, e.in("Movie Q", "Extras", "x.mkv"), "data") },
					want: func(e scenarioEnv) []string { return []string{e.subtree("Movie Q", "Extras")} },
				},
				{
					// Ignored again: the sentinel check catches a target for
					// Other.
					act: func(t *testing.T, e scenarioEnv) {
						writeFile(t, e.in("Movie Q", ".nomedia"), "")
						writeFile(t, e.in("Movie Q", "Other", "o.mkv"), "data")
					},
				},
			},
		},
		{
			// The walk records Target/Deep only through Linked, which sorts
			// first; deleting the link must not leave Target unwatched.
			name: "symlink alias deleted, its target folder stays watched",
			setup: func(t *testing.T, e scenarioEnv) {
				mkdirs(t, e.root, "Target/Deep")
				if err := os.Symlink(e.in("Target"), e.in("Linked")); err != nil {
					t.Fatal(err)
				}
			},
			steps: []scenarioStep{
				{
					act: func(t *testing.T, e scenarioEnv) {
						if err := os.Remove(e.in("Linked")); err != nil {
							t.Fatal(err)
						}
						e.waitWalk(t)
					},
					want: func(e scenarioEnv) []string { return []string{e.subtree("Linked")} },
				},
				{
					act:  func(t *testing.T, e scenarioEnv) { writeFile(t, e.in("Target", "Deep", "x.mkv"), "data") },
					want: func(e scenarioEnv) []string { return []string{e.subtree("Target", "Deep")} },
				},
			},
		},
		{
			name:  "download renamed from a temp name",
			setup: func(t *testing.T, e scenarioEnv) { mkdirs(t, e.root, "Movie M") },
			steps: []scenarioStep{{
				act: func(t *testing.T, e scenarioEnv) {
					writeFile(t, e.in("Movie M", "m.mkv.part"), "data")
					mustRename(t, e.in("Movie M", "m.mkv.part"), e.in("Movie M", "m.mkv"))
				},
				want: func(e scenarioEnv) []string { return []string{e.subtree("Movie M")} },
			}},
		},
	}
}

func TestIntegrationScenarios(t *testing.T) {
	for _, backend := range integrationBackends {
		t.Run(backend.name, func(t *testing.T) {
			for _, sc := range integrationScenarios() {
				t.Run(sc.name, func(t *testing.T) {
					runScenario(t, backend, sc)
				})
			}
		})
	}
}

func runScenario(t *testing.T, backend integrationBackend, sc scenario) {
	parent := t.TempDir()
	walks := make(chan string, 16)
	env := scenarioEnv{root: filepath.Join(parent, "library"), outside: filepath.Join(parent, "outside"), walks: walks}
	mkdirs(t, parent, "outside", "Sentinel")
	if sc.symlinkRoot {
		mkdirs(t, parent, "real")
		if err := os.Symlink(filepath.Join(parent, "real"), env.root); err != nil {
			t.Fatal(err)
		}
	} else {
		mkdirs(t, parent, "library")
	}
	mkdirs(t, env.root, "Sentinel")
	if sc.setup != nil {
		sc.setup(t, env)
	}

	folders := &fakeFolders{}
	folders.set(library(1, env.root))
	queue, status := newFakeQueue(), newFakeStatus()
	cfg := testConfig(folders, queue, status)
	cfg.hooks.afterWalk = func(path, _ string) {
		select {
		case walks <- path:
		default:
		}
	}
	backend.configure(t, &cfg)
	startMonitor(t, cfg)
	rows := waitStatus(t, status, "monitoring", hasState(1, StateMonitoring))
	if rows[0].Backend != backend.name {
		t.Fatalf("backend = %q, want %q", rows[0].Backend, backend.name)
	}
	// The initial walk; steps wait for the ones after it.
	env.waitWalk(t)

	allowed := map[string]bool{env.subtree("Sentinel"): true}
	var received []string
	for _, step := range sc.steps {
		step.act(t, env)
		if step.want == nil {
			continue
		}
		want := step.want(env)
		for _, key := range want {
			allowed[key] = true
		}
		received = append(received, waitTargets(t, queue, want...)...)
	}

	// Everything the scenario caused reaches the queue no later than a
	// change made after it, so the sentinel bounds the check for stray
	// targets (stale paths, the initial walk, ignored temp files).
	writeFile(t, env.in("Sentinel", fmt.Sprintf("s-%d.mkv", time.Now().UnixNano())), "data")
	received = append(received, waitTargets(t, queue, env.subtree("Sentinel"))...)
	for _, key := range received {
		if !allowed[key] {
			t.Errorf("unexpected target %q; allowed %v", key, keys(allowed))
		}
		if !strings.HasSuffix(key, " "+Trigger) {
			t.Errorf("target %q does not carry the %s trigger", key, Trigger)
		}
	}
	if !slices.Contains(received, env.subtree("Sentinel")) {
		t.Fatalf("sentinel target missing from %v", received)
	}
}
