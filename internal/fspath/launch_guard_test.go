package fspath

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// launchesOutsideFspath are the process launches that do not go through
// CommandContext, each with why it never gets a media input. CommandContext
// restricts every relayed input (a transcode node's loopback URL for a source
// it reads through the core) to the relay's protocols and the admitted
// demuxers; a launch that bypasses it and runs FFmpeg or ffprobe on a job's
// input would let FFmpeg open whatever the source's bytes name. Keyed by
// file and enclosing function.
var launchesOutsideFspath = map[string]string{
	"internal/backup/tools.go:toolVersion":                                                   "pg_dump/pg_restore --version",
	"internal/backup/dump.go:dump":                                                           "pg_dump and pg_restore of the database",
	"internal/backup/dump.go:listTOC":                                                        "pg_restore --list of a dump",
	"internal/backup/restore_pg.go:Load":                                                     "pg_restore of a dump",
	"internal/backup/restore_pg.go:toc":                                                      "pg_restore --list of a dump",
	"internal/jellycompat/web_component.go:commandOutput":                                    "the web client installer's git and npm",
	"internal/jellycompat/web_component.go:webInstallCommandRunner":                          "the web client installer's git and npm",
	"internal/nodeinstall/launchd_darwin.go:launchctl":                                       "launchctl on the user's own LaunchAgent",
	"internal/nodemetrics/nvidia.go:<package scope>":                                         "nvidia-smi",
	"internal/playback/gpudetect.go:runFFmpegProbe":                                          "FFmpeg on lavfi test sources",
	"internal/playback/gpudetect.go:runNVIDIASMIQuery":                                       "nvidia-smi",
	"internal/playback/copy_manifest_origin.go:probeSegmentVideoPTS":                         "ffprobe of output segments on stdin (pipe:0)",
	"internal/playback/encoder_warmup.go:runHardwareEncoderWarmup":                           "FFmpeg on lavfi test sources",
	"internal/playback/remux.go:supportsDoviRPUFilter":                                       "ffmpeg -bsfs",
	"internal/playback/remux.go:supportsLeadingPictureDropFilter":                            "ffmpeg -h bsf=noise",
	"internal/playback/transcode.go:supportsScaleReference":                                  "FFmpeg on lavfi test sources",
	"internal/playback/transformations_v3.go:ProbeTransformationRegistryWithToneMapV3Result": "ffmpeg -bsfs and -encoders",
	"internal/playback/transformations_v3.go:probeAudioRecipeFilterV3":                       "FFmpeg on a lavfi anullsrc input",
	"internal/playback/transformations_v3.go:probeHEVCRecipeV3":                              "FFmpeg on a lavfi testsrc2 input",
	"internal/playback/transformations_v3.go:probeSurroundBitstreamRecipeV3":                 "FFmpeg on a lavfi anullsrc input",
	"internal/pluginhost/host.go:Start":                                                      "a plugin binary",
	"internal/plugins/installer.go:loadManifestFromBinary":                                   "a plugin binary's manifest command",
}

// Every process launch outside internal/fspath and internal/procgroup is one
// launchesOutsideFspath accounts for, so FFmpeg and ffprobe reach media only
// through CommandContext, which guards relayed inputs. A new launch that runs
// them on a media input must use CommandContext; one that never does is
// added above with the reason.
func TestEveryMediaLaunchGoesThroughCommandContext(t *testing.T) {
	root := filepath.Join("..", "..")
	found := map[string]bool{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if name := d.Name(); name == "testdata" || name == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/fspath/") || strings.HasPrefix(rel, "internal/procgroup/") {
				return nil
			}
			for _, fn := range launchingFunctions(t, path) {
				found[rel+":"+fn] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, launch := range slices.Sorted(maps.Keys(found)) {
		if _, ok := launchesOutsideFspath[launch]; !ok {
			t.Errorf("%s launches a process without fspath.CommandContext; if it can run FFmpeg or ffprobe on a media input, use fspath.CommandContext, which guards relayed inputs, else add it to launchesOutsideFspath with the reason", launch)
		}
	}
	for _, launch := range slices.Sorted(maps.Keys(launchesOutsideFspath)) {
		if !found[launch] {
			t.Errorf("launchesOutsideFspath lists %s, which no longer launches a process; remove it", launch)
		}
	}
}

// launchingFunctions returns the functions in path that call exec.Command,
// exec.CommandContext or procgroup's equivalents; a launch outside any
// function is reported as "<package scope>".
func launchingFunctions(t *testing.T, path string) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "Command") {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	visit := func(name string, node ast.Node) {
		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && (pkg.Name == "exec" || pkg.Name == "procgroup") && (sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext") {
				out = append(out, name)
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			visit(fn.Name.Name, fn)
		} else {
			visit("<package scope>", decl)
		}
	}
	return out
}
