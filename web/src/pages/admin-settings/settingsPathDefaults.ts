/**
 * What the server actually uses for the admin path settings that are allowed to
 * be blank, mirrored from the Go readers that own them:
 *
 * - `DefaultTranscodeDir` (`internal/config/config.go`), also the effective
 *   value the settings API backfills for `playback.transcode_dir`.
 * - `EffectiveDownloadArtifactDir` (`internal/config/config.go`) for
 *   `download.artifact_dir`, which has no static default: blank means "a
 *   sibling of the transcode directory", so the UI has to derive it the same
 *   way the API and transcode-node processes do.
 * - The `playback.ffmpeg_path` fallback in `internal/config/db_loader.go`.
 *
 * Every path the UI quotes is written once here so the fields cannot drift away
 * from the pipeline one placeholder at a time, the same way
 * `web/src/components/admin/brandingAssetSpecs.ts` mirrors the branding asset
 * rules. Change a value in Go and this module has to change with it.
 */

/** Fallback for a blank `playback.transcode_dir`. */
export const DEFAULT_TRANSCODE_DIR = "/tmp/silo-transcode";

/** Fallback for a blank `playback.ffmpeg_path`: the FFmpeg the server ships. */
export const DEFAULT_FFMPEG_PATH = "/usr/lib/jellyfin-ffmpeg/ffmpeg";

/**
 * Directory prepared downloads land in when `download.artifact_dir` is blank.
 * It is a *sibling* of the transcode directory, never a child: the transcode
 * sweep deletes every non-active subdirectory of its own root.
 */
const DOWNLOAD_ARTIFACT_DIR_NAME = "silo-download-artifacts";

/**
 * Where prepared downloads are written for a given pair of stored settings —
 * the configured directory when set, otherwise a `silo-download-artifacts`
 * sibling of the (possibly defaulted) transcode directory.
 *
 * Pass the raw stored or staged strings: this mirrors
 * `config.EffectiveDownloadArtifactDir` exactly. The Go side cleans the
 * transcode path before taking its parent, so a trailing slash still yields
 * the sibling directory rather than nesting inside the transcode root.
 */
export function effectiveDownloadArtifactDir(artifactDir: string, transcodeDir: string): string {
  if (artifactDir !== "") return artifactDir;
  const base = transcodeDir === "" ? DEFAULT_TRANSCODE_DIR : transcodeDir;
  return joinPath(dirName(cleanPath(base)), DOWNLOAD_ARTIFACT_DIR_NAME);
}

/** `filepath.Dir`: everything up to the final slash, cleaned. */
function dirName(path: string): string {
  const lastSlash = path.lastIndexOf("/");
  return cleanPath(lastSlash < 0 ? "" : path.slice(0, lastSlash + 1));
}

/** `filepath.Join` for two non-empty elements. */
function joinPath(dir: string, name: string): string {
  return cleanPath(`${dir}/${name}`);
}

/**
 * `filepath.Clean` for slash-separated paths: collapses repeated separators and
 * resolves `.` and `..` lexically, leaving `.` for an empty result.
 */
function cleanPath(path: string): string {
  const rooted = path.startsWith("/");
  const resolved: string[] = [];
  for (const segment of path.split("/")) {
    if (segment === "" || segment === ".") continue;
    if (segment === "..") {
      if (resolved.length > 0 && resolved[resolved.length - 1] !== "..") {
        resolved.pop();
      } else if (!rooted) {
        // A relative path keeps the `..` it cannot resolve; `/..` is just `/`.
        resolved.push("..");
      }
      continue;
    }
    resolved.push(segment);
  }
  const cleaned = (rooted ? "/" : "") + resolved.join("/");
  return cleaned === "" ? "." : cleaned;
}
