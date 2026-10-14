import { useMemo, useState } from "react";
import { ArrowRight } from "lucide-react";
import { Link } from "react-router";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useHWAccelDetection } from "@/hooks/queries/admin/system";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import { useLibraryCapabilities } from "@/hooks/queries/admin/libraries";
import { useAdminMarkerCapabilities } from "@/hooks/queries/admin/markers";
import { useAdminTrickplayLibraries } from "@/hooks/queries/admin/trickplay";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Switch } from "@/components/ui/switch";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { PathSettingField } from "@/components/settings/PathSettingField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { SettingField, SettingFieldRow, SettingFieldStatus } from "./SettingField";
import { SaveBar } from "@/components/SaveBar";
import { FieldGroup } from "./FieldGroup";
import { MarkerTasksCard } from "./MarkerTasksCard";
import { DEFAULT_FFMPEG_PATH, DEFAULT_TRANSCODE_DIR } from "./settingsPathDefaults";
import {
  IMAGE_EXECUTION_DEFAULT,
  HW_ACCEL_OPTIONS,
  buildHWDeviceRows,
  imageExecutionOptions,
  describeDetection,
  hasUsableTranscodeNode,
  hasUsableTrickplayNode,
  nodeInventoriesDiverge,
  parseHWDeviceList,
  toggleHWDevice,
} from "./playbackSettings.utils";

// Shown without a disclosure: the controls a household admin actually
// touches, including the HDR tone mapping switches, throttling and where
// segments are written.
const TRANSCODING_ESSENTIAL_KEYS = [
  "playback.transcode_enabled",
  "playback.hw_accel",
  "allow_4k_transcode",
  "playback.allow_hevc_encoding",
  "playback.transcode_hardware_tone_map_enabled",
  "playback.transcode_software_tone_map_enabled",
  "enable_transcode_throttle",
  "transcode_throttle_seconds",
  "playback.transcode_dir",
];

const TRANSCODING_ADVANCED_KEYS = [
  "playback.ffmpeg_path",
  "playback.hw_device",
  "playback.segment_retention_seconds",
  "playback.copy_keyframe_playlist",
];

// Chapter thumbnails and seek previews are images cut from the video, not part
// of a transcode, so they get their own group. Width, interval and the chapter
// thumbnail worker count show without a disclosure.
const PREVIEW_SHOWN_KEYS = [
  "playback.preview_image_width",
  "playback.trickplay_interval_seconds",
  "playback.chapter_thumbnail_workers",
];

// Where the images are made is only a real choice once a transcode node could
// take the work, so these sit beside width and interval then and in Advanced
// otherwise.
const PREVIEW_PLACEMENT_KEYS = [
  "playback.chapter_thumbnail_execution",
  "playback.trickplay_execution",
];

const PREVIEW_ADVANCED_KEYS = [
  "playback.trickplay_workers",
  "playback.chapter_thumbnail_hdr_policy",
  "playback.chapter_thumbnail_software_tone_map_enabled",
];

const PREVIEW_IMAGE_KEYS = [
  ...PREVIEW_SHOWN_KEYS,
  ...PREVIEW_PLACEMENT_KEYS,
  ...PREVIEW_ADVANCED_KEYS,
];

// A new preview image width makes every chapter thumbnail and seek preview
// again; a new interval, every seek preview. The defaults are the server's,
// for a row that was never set.
const PREVIEW_WIDTH_KEY = "playback.preview_image_width";
const TRICKPLAY_INTERVAL_KEY = "playback.trickplay_interval_seconds";
const REMAKE_DEFAULTS: Record<string, string> = {
  [PREVIEW_WIDTH_KEY]: "300",
  [TRICKPLAY_INTERVAL_KEY]: "10",
};

/** What saving a new width or interval makes again, for the confirmation. */
function remakeDescription(widthChanged: boolean, previews: number | null): string {
  const files = previews === null ? "existing" : previews === 1 ? "1 file's" : `${previews} files'`;
  const what = !widthChanged
    ? previews === null
      ? "Existing seek previews are"
      : `${files} seek previews are`
    : previews === null || previews > 0
      ? `Every chapter thumbnail and ${files} seek previews are`
      : "Every chapter thumbnail is";
  const how = widthChanged ? "at the new width" : "with the new interval";
  return `${what} made again ${how}. This runs in the background and can take a long time on a large library; players keep the current images until each is replaced.`;
}

const WATCH_KEYS = ["playback.watched_threshold", "playback.min_resume_threshold"];

const MARKER_KEYS = [
  "markers.mode",
  "markers.detect_intros",
  "markers.detect_credits",
  "markers.online_storage",
  "markers.lazy_playback",
  "markers.detection_workers",
];

// `playback.chapter_thumbnail_node_capacity` is deliberately absent from the
// UI (hidden tier): it is still saved and read through the settings API, but
// the per-node budget is derived from the node pool rather than typed in.
//
// The `download.*` family is its own page (DownloadsSettings), so it is not
// loaded or saved here.
const KEYS = [
  ...WATCH_KEYS,
  ...TRANSCODING_ESSENTIAL_KEYS,
  ...TRANSCODING_ADVANCED_KEYS,
  ...PREVIEW_IMAGE_KEYS,
  ...MARKER_KEYS,
];

export default function PlaybackSettings() {
  const supportsTrickplay = useLibraryCapabilities().data?.trickplay === true;
  const keys = useMemo(
    () =>
      supportsTrickplay
        ? KEYS
        : KEYS.filter((key) => key !== PREVIEW_WIDTH_KEY && !key.startsWith("playback.trickplay_")),
    [supportsTrickplay],
  );
  const form = useSettingsForm({ keys });
  const restartKeys = useRestartKeys();
  const hwAccel = form.getValue("playback.hw_accel");
  const hwDetection = useHWAccelDetection(hwAccel !== "none");
  const hwDevice = form.getValue("playback.hw_device");
  const selectedDevices = parseHWDeviceList(hwDevice);
  const deviceRows = buildHWDeviceRows(hwDetection.data, hwDevice);
  const detectedPaths = deviceRows.filter((row) => row.detected).map((row) => row.path);
  // Balancing is QSV/VAAPI-only: NVENC addresses GPUs by CUDA index/UUID, so
  // the multi-select picker is hidden for it (the server uses the first
  // configured entry).
  const isNvenc =
    hwAccel === "nvenc" || (hwAccel === "auto" && hwDetection.data?.resolved === "nvenc");
  const inventoriesDiverge = nodeInventoriesDiverge(hwDetection.data);
  const showDevicePicker = hwAccel !== "none" && !isNvenc && deviceRows.length > 0;

  const nodes = useAdminNodes();
  const trickplayExecution =
    form.getValue("playback.trickplay_execution") || IMAGE_EXECUTION_DEFAULT;
  // Pending replacements can retain a publication. Include them when asking
  // before a width or interval change; a server without previews reports none.
  const trickplayLibraries = useAdminTrickplayLibraries({ enabled: supportsTrickplay });
  const previewsToRemake = (trickplayLibraries.data ?? []).reduce(
    (count, library) => count + library.ready + library.running + library.pending,
    0,
  );
  const changed = (key: string) =>
    form.isDirty(key) &&
    (form.getValue(key) || REMAKE_DEFAULTS[key]) !==
      (form.getPersistedValue(key) || REMAKE_DEFAULTS[key]);
  const widthChanged = changed(PREVIEW_WIDTH_KEY);
  const asksToRemake =
    widthChanged ||
    (changed(TRICKPLAY_INTERVAL_KEY) && (!trickplayLibraries.isSuccess || previewsToRemake > 0));
  const [confirmRemake, setConfirmRemake] = useState(false);
  const save = () => {
    if (asksToRemake) {
      setConfirmRemake(true);
      return;
    }
    return form.save();
  };
  const chapterExecution =
    form.getValue("playback.chapter_thumbnail_execution") || IMAGE_EXECUTION_DEFAULT;
  // Gate the node-backed extraction modes only on a node list we actually
  // have: while the query is in flight or after it failed, leave every option
  // reachable rather than blocking a valid choice on a transient error.
  const transcodeNodeAvailable = !nodes.isSuccess || hasUsableTranscodeNode(nodes.data);
  const trickplayNodeAvailable = !nodes.isSuccess || hasUsableTrickplayNode(nodes.data);
  // Showing the placement selects takes a node list that has one, so a
  // single-server page never shows them while the list loads.
  const showPreviewPlacement = hasUsableTranscodeNode(nodes.data);
  const previewAdvancedKeys = [
    ...PREVIEW_ADVANCED_KEYS,
    ...(showPreviewPlacement ? [] : PREVIEW_PLACEMENT_KEYS),
  ].filter((key) => keys.includes(key));

  const isDirty = form.isDirty;
  const anyDirty = (keys: readonly string[]) => keys.some((key) => isDirty(key));
  const allRestart = (keys: readonly string[]) => keys.every((key) => restartKeys.has(key));

  const throttleOn = form.getValue("enable_transcode_throttle") === "true";
  // Every advanced transcoding key has one row, except the device picker,
  // which renders only when it applies.
  const transcodingAdvancedCount = TRANSCODING_ADVANCED_KEYS.length - (showDevicePicker ? 0 : 1);

  // An older API node saves the detection kind switches but ignores them, so
  // they are offered only where the server says it honors them.
  const { data: markerCapabilities } = useAdminMarkerCapabilities();
  const detectionKindSettings = markerCapabilities?.detection_kind_settings === true;
  const markerMode = form.getValue("markers.mode") || "both";
  const onlineMarkersEnabled = markerMode === "online" || markerMode === "both";
  const onlineMarkerStorage = form.getValue("markers.online_storage") || "stored";
  const localMarkersEnabled = markerMode === "local" || markerMode === "both";
  const showLazyPlayback =
    markerMode !== "off" && (!onlineMarkersEnabled || onlineMarkerStorage === "stored");

  const detection = hwAccel === "none" ? undefined : hwDetection.data;
  const detectedLabel = describeDetection(detection);

  // Everything the hardware-acceleration field has to say about itself, in its
  // own status slot. The NVENC note used to be a bare paragraph between rows,
  // which read as a row with no label and broke the group's rhythm.
  const hwAccelLines =
    hwAccel === "none"
      ? []
      : [
          detectedLabel ? (
            <SettingFieldStatus
              key="detected"
              tone={detection?.resolved && detection.resolved !== "none" ? "ok" : "warn"}
            >
              {detectedLabel}
            </SettingFieldStatus>
          ) : hwDetection.isLoading ? (
            <SettingFieldStatus key="detecting" tone="muted">
              Detecting hardware…
            </SettingFieldStatus>
          ) : null,
          isNvenc && selectedDevices.length > 1 ? (
            <SettingFieldStatus key="nvenc" tone="warn">
              NVENC uses the first configured device ({selectedDevices[0]}).
            </SettingFieldStatus>
          ) : null,
        ].filter(Boolean);
  const hwAccelStatus =
    hwAccelLines.length > 0 ? (
      <span className="flex flex-col items-start gap-1">{hwAccelLines}</span>
    ) : undefined;

  const chapterExecutionField = (
    <SettingField
      label="Generate chapter thumbnails on"
      type="select"
      options={imageExecutionOptions(chapterExecution, transcodeNodeAvailable)}
      status={
        transcodeNodeAvailable ? undefined : (
          <SettingFieldStatus tone="warn">No transcode nodes are connected</SettingFieldStatus>
        )
      }
      value={chapterExecution}
      onChange={(v) => form.setValue("playback.chapter_thumbnail_execution", v)}
      restartRequired={restartKeys.has("playback.chapter_thumbnail_execution")}
    />
  );
  const trickplayExecutionField = supportsTrickplay ? (
    <SettingField
      label="Generate seek previews on"
      type="select"
      options={imageExecutionOptions(trickplayExecution, trickplayNodeAvailable)}
      status={
        trickplayNodeAvailable ? undefined : (
          <SettingFieldStatus tone="warn">
            No connected transcode node supports seek previews
          </SettingFieldStatus>
        )
      }
      value={trickplayExecution}
      onChange={(v) => form.setValue("playback.trickplay_execution", v)}
      restartRequired={restartKeys.has("playback.trickplay_execution")}
    />
  ) : null;

  if (form.isLoading) return <div>Loading...</div>;

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Playback" />

      <div className="flex-1 space-y-5">
        <FieldGroup label="Watch behavior" restartAll={allRestart(WATCH_KEYS)}>
          <SettingField
            label="Mark watched at"
            type="number"
            unit="%"
            value={form.getValue("playback.watched_threshold")}
            onChange={(v) => form.setValue("playback.watched_threshold", v)}
            restartRequired={restartKeys.has("playback.watched_threshold")}
          />
          <SettingField
            label="Show in Continue Watching after"
            type="number"
            unit="%"
            description="Progress below this is ignored."
            value={form.getValue("playback.min_resume_threshold")}
            onChange={(v) => form.setValue("playback.min_resume_threshold", v)}
            restartRequired={restartKeys.has("playback.min_resume_threshold")}
          />
        </FieldGroup>

        <FieldGroup
          label="Transcoding"
          restartAll={allRestart([...TRANSCODING_ESSENTIAL_KEYS, ...TRANSCODING_ADVANCED_KEYS])}
        >
          <SettingField
            label="Video transcoding"
            type="toggle"
            description="Off never re-encodes video. Silo still repackages files and converts audio for devices that need it."
            value={form.getValue("playback.transcode_enabled")}
            onChange={(v) => form.setValue("playback.transcode_enabled", v)}
            restartRequired={restartKeys.has("playback.transcode_enabled")}
          />
          <SettingField
            label="Hardware acceleration"
            type="select"
            options={HW_ACCEL_OPTIONS}
            description="Auto picks the best device. If startup fails, it keeps GPU encoding with CPU decoding before falling back to software."
            status={hwAccelStatus}
            value={hwAccel}
            onChange={(v) => form.setValue("playback.hw_accel", v)}
            restartRequired={restartKeys.has("playback.hw_accel")}
          />
          <SettingField
            label="Allow 4K transcoding"
            type="toggle"
            description="Heavy load on most hardware."
            value={form.getValue("allow_4k_transcode")}
            onChange={(v) => form.setValue("allow_4k_transcode", v)}
            restartRequired={restartKeys.has("allow_4k_transcode")}
          />
          <SettingField
            label="Allow HEVC encoding"
            type="toggle"
            description="Use HEVC for clients that support HEVC over HLS. Other clients keep H.264."
            value={form.getValue("playback.allow_hevc_encoding")}
            onChange={(v) => form.setValue("playback.allow_hevc_encoding", v)}
          />
          <SettingField
            label="Enable Hardware HDR Tone Mapping"
            type="toggle"
            hint="Allows validated local or remote GPU executors to convert HDR video to SDR when transcoding."
            value={form.getValue("playback.transcode_hardware_tone_map_enabled")}
            onChange={(v) => form.setValue("playback.transcode_hardware_tone_map_enabled", v)}
            restartRequired={restartKeys.has("playback.transcode_hardware_tone_map_enabled")}
          />
          <SettingField
            label="Enable Software HDR Tone Mapping"
            type="toggle"
            hint="Allows the CPU to convert HDR video to SDR when transcoding. This can be very CPU-intensive."
            value={form.getValue("playback.transcode_software_tone_map_enabled")}
            onChange={(v) => form.setValue("playback.transcode_software_tone_map_enabled", v)}
            restartRequired={restartKeys.has("playback.transcode_software_tone_map_enabled")}
          />
          <SettingField
            label="Throttle transcoding"
            type="toggle"
            description="Pause encoding once the client is far enough ahead."
            value={form.getValue("enable_transcode_throttle")}
            onChange={(v) => form.setValue("enable_transcode_throttle", v)}
            restartRequired={restartKeys.has("enable_transcode_throttle")}
          />
          {throttleOn && (
            <SettingField
              label="Buffer ahead"
              type="number"
              unit="seconds"
              value={form.getValue("transcode_throttle_seconds")}
              onChange={(v) => form.setValue("transcode_throttle_seconds", v)}
              restartRequired={restartKeys.has("transcode_throttle_seconds")}
            />
          )}
          <PathSettingField
            label="Transcode directory"
            defaultValue={DEFAULT_TRANSCODE_DIR}
            description={`Use fast local storage with room to spare. Leave blank to use ${DEFAULT_TRANSCODE_DIR}.`}
            value={form.getValue("playback.transcode_dir")}
            onChange={(v) => form.setValue("playback.transcode_dir", v)}
            restartRequired={restartKeys.has("playback.transcode_dir")}
          />

          <AdvancedSection
            id="playback.transcoding"
            count={transcodingAdvancedCount}
            forceOpen={anyDirty(TRANSCODING_ADVANCED_KEYS)}
          >
            <PathSettingField
              label="FFmpeg path"
              defaultValue={DEFAULT_FFMPEG_PATH}
              description={`Leave blank to use jellyfin-ffmpeg at ${DEFAULT_FFMPEG_PATH} when it is installed, then ffmpeg on PATH. macOS tries Homebrew's ffmpeg-full first.`}
              value={form.getValue("playback.ffmpeg_path")}
              onChange={(v) => form.setValue("playback.ffmpeg_path", v)}
              restartRequired={restartKeys.has("playback.ffmpeg_path")}
            />
            {showDevicePicker && (
              <div>
                <SettingsSubheading
                  caption={
                    selectedDevices.length === 0
                      ? "Auto: the first available device takes every transcode."
                      : selectedDevices.length === 1
                        ? "All transcodes run on the selected device."
                        : "Transcodes balance across the selected devices."
                  }
                >
                  GPU devices
                </SettingsSubheading>
                {inventoriesDiverge && (
                  <p className="pb-2 text-xs text-amber-500">
                    Nodes report different devices. Only paths on every node are safe to select —
                    for the rest, set per-node overrides on the{" "}
                    <Link
                      to="/admin/nodes"
                      className="font-medium underline-offset-2 hover:underline"
                    >
                      Nodes page
                    </Link>
                    .
                  </p>
                )}
                {/* One shared row shell per device, so these switches land on the
                    same edge as every other control in the group instead of
                    hugging the panel. */}
                {deviceRows.map((row) => (
                  <SettingFieldRow
                    key={row.path}
                    label={
                      <span className={row.detected ? undefined : "text-muted-foreground"}>
                        {row.description}
                      </span>
                    }
                    description={
                      <>
                        <span className="block font-mono">{row.path}</span>
                        {row.missingOnNodes.length > 0 && (
                          <span className="mt-0.5 block text-amber-500">
                            Not present on: {row.missingOnNodes.join(", ")}
                          </span>
                        )}
                      </>
                    }
                  >
                    <Switch
                      checked={selectedDevices.includes(row.path)}
                      aria-label={row.description}
                      onCheckedChange={() =>
                        form.setValue(
                          "playback.hw_device",
                          toggleHWDevice(
                            form.getValue("playback.hw_device"),
                            row.path,
                            detectedPaths,
                          ),
                        )
                      }
                    />
                  </SettingFieldRow>
                ))}
              </div>
            )}
            <SettingField
              label="Transcode back buffer"
              type="number"
              unit="seconds"
              hint="Keeps this much already-downloaded media for instant backward seeking, then reclaims older transcode segments. Use 0 to disable cleanup; enabled values must be at least 120 seconds. Pair with transcode throttling to bound both behind- and ahead-of-client disk usage."
              value={form.getValue("playback.segment_retention_seconds")}
              onChange={(v) => form.setValue("playback.segment_retention_seconds", v)}
              restartRequired={restartKeys.has("playback.segment_retention_seconds")}
            />
            <SettingField
              label="Complete playlists for copied video"
              type="toggle"
              description="Experimental. When Silo copies a file's video, list every segment from the start, using the file's keyframes, instead of a playlist that grows as Silo copies. Stops Safari freezing partway through. A seek ahead of what Silo has copied waits for it to get there."
              value={form.getValue("playback.copy_keyframe_playlist") || "false"}
              onChange={(v) => form.setValue("playback.copy_keyframe_playlist", v)}
              restartRequired={restartKeys.has("playback.copy_keyframe_playlist")}
            />
          </AdvancedSection>
        </FieldGroup>

        <FieldGroup
          label="Preview images"
          description={
            supportsTrickplay
              ? "Chapter thumbnails and seek previews are turned on for each library in Libraries."
              : "Chapter thumbnails are turned on for each library in Libraries."
          }
          restartAll={allRestart(PREVIEW_IMAGE_KEYS)}
          // FieldGroup descriptions are plain text, so the link sits on the
          // heading line, as Skip markers links to its providers.
          actions={
            <Link
              to="/admin/libraries"
              className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
            >
              Libraries
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          }
        >
          {supportsTrickplay && (
            <>
              <SettingField
                label="Preview image width"
                type="number"
                unit="px"
                description="Width of chapter thumbnails and of the thumbnails players show while seeking, 160 to 640. Changing it makes them all again; players keep the current ones until each is replaced."
                value={form.getValue("playback.preview_image_width")}
                onChange={(v) => form.setValue("playback.preview_image_width", v)}
                restartRequired={restartKeys.has("playback.preview_image_width")}
              />
              <SettingField
                label="Seek preview interval"
                type="number"
                unit="seconds"
                description="Time between seek previews, 5 to 60. Changing it makes every library's previews again."
                value={form.getValue("playback.trickplay_interval_seconds")}
                onChange={(v) => form.setValue("playback.trickplay_interval_seconds", v)}
                restartRequired={restartKeys.has("playback.trickplay_interval_seconds")}
              />
            </>
          )}
          <SettingField
            label="Chapter thumbnail workers"
            type="number"
            description="How many files Silo extracts chapter thumbnails from at once. One more worker takes only titles that are playing, so they aren't stuck behind the queue."
            value={form.getValue("playback.chapter_thumbnail_workers")}
            onChange={(v) => form.setValue("playback.chapter_thumbnail_workers", v)}
            restartRequired={restartKeys.has("playback.chapter_thumbnail_workers")}
          />
          {showPreviewPlacement && (
            <>
              {chapterExecutionField}
              {trickplayExecutionField}
            </>
          )}

          <AdvancedSection
            id="playback.preview_images"
            count={previewAdvancedKeys.length}
            forceOpen={
              anyDirty(previewAdvancedKeys) ||
              // Node-only extraction with no node to run on is stranded work;
              // its warning must not sit inside a closed disclosure.
              (!transcodeNodeAvailable && chapterExecution === "transcode_nodes_only") ||
              (!trickplayNodeAvailable && trickplayExecution === "transcode_nodes_only")
            }
          >
            {!showPreviewPlacement && chapterExecutionField}
            {supportsTrickplay && (
              <SettingField
                label="Seek preview workers"
                type="number"
                description="How many files each server makes seek previews for at once. They run at low priority, so playback comes first."
                value={form.getValue("playback.trickplay_workers")}
                onChange={(v) => form.setValue("playback.trickplay_workers", v)}
                restartRequired={restartKeys.has("playback.trickplay_workers")}
              />
            )}
            {!showPreviewPlacement && trickplayExecutionField}
            {/* internal/trickplay tone-maps every HDR seek preview, in
                software when it has to; these two settings are read only by
                chapter thumbnail extraction. */}
            <SettingsSubheading
              caption={supportsTrickplay ? "Seek previews always convert HDR to SDR." : undefined}
            >
              HDR chapter thumbnails
            </SettingsSubheading>
            <SettingField
              label="HDR handling"
              type="select"
              options={[
                { value: "best_effort", label: "Generate when possible" },
                { value: "disabled", label: "Skip HDR and Dolby Vision" },
              ]}
              description="HDR frames need extra color conversion."
              value={form.getValue("playback.chapter_thumbnail_hdr_policy") || "best_effort"}
              onChange={(v) => form.setValue("playback.chapter_thumbnail_hdr_policy", v)}
              restartRequired={restartKeys.has("playback.chapter_thumbnail_hdr_policy")}
            />
            <SettingField
              label="Software HDR tone mapping"
              type="toggle"
              description="Slow, but works without graphics hardware."
              value={form.getValue("playback.chapter_thumbnail_software_tone_map_enabled")}
              onChange={(v) =>
                form.setValue("playback.chapter_thumbnail_software_tone_map_enabled", v)
              }
              disabled={form.getValue("playback.chapter_thumbnail_hdr_policy") === "disabled"}
              restartRequired={restartKeys.has(
                "playback.chapter_thumbnail_software_tone_map_enabled",
              )}
            />
          </AdvancedSection>
        </FieldGroup>

        {/*
          Detection behavior lives here; which online providers answer a lookup,
          and on what terms, is provider configuration and lives with the other
          providers.
        */}
        <FieldGroup
          label="Skip markers"
          description="Markers identify intros, credits, recaps, and previews so players can offer skip controls."
          restartAll={allRestart(MARKER_KEYS)}
          actions={
            <Link
              to="/admin/settings/providers"
              className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
            >
              Marker providers
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          }
        >
          <SettingField
            label="Marker source"
            type="select"
            description="Online markers take priority. Silo skips local detection of an intro or credits when an online one is saved in your library. Local detection uses CPU."
            className="[&_[data-slot=select-trigger]]:h-auto [&_[data-slot=select-trigger]]:min-h-9 [&_[data-slot=select-value]]:line-clamp-none [&_[data-slot=select-value]]:text-left [&_[data-slot=select-value]]:whitespace-normal"
            options={[
              { value: "off", label: "Off" },
              { value: "local", label: "Detect on this server" },
              { value: "both", label: "Online preferred + server detection" },
              { value: "online", label: "Online providers only" },
            ]}
            value={markerMode}
            onChange={(value) => form.setValue("markers.mode", value)}
            restartRequired={restartKeys.has("markers.mode")}
          />

          {localMarkersEnabled && detectionKindSettings && (
            <>
              <SettingField
                label="Detect intros"
                type="toggle"
                description="Finds each episode's opening from embedded chapters, or by comparing the start of episodes in a season."
                value={form.getValue("markers.detect_intros") || "true"}
                onChange={(value) => form.setValue("markers.detect_intros", value)}
                restartRequired={restartKeys.has("markers.detect_intros")}
              />
              <SettingField
                label="Detect credits"
                type="toggle"
                description="Finds end credits from embedded chapters, or by reading the end of each episode and movie. Uses more CPU than intro detection."
                value={form.getValue("markers.detect_credits") || "true"}
                onChange={(value) => form.setValue("markers.detect_credits", value)}
                restartRequired={restartKeys.has("markers.detect_credits")}
              />
            </>
          )}

          {onlineMarkersEnabled && (
            <SettingField
              label="Save online markers"
              type="select"
              description={
                onlineMarkerStorage === "stored"
                  ? "Silo saves markers from enabled providers such as TheIntroDB. The Sync online markers task fetches missing markers and refreshes saved markers daily at 03:00 (server time) by default."
                  : "Fetch markers when needed without saving them to your library. Scheduled online sync is disabled."
              }
              options={[
                { value: "stored", label: "Save to library" },
                { value: "on_demand", label: "Fetch when needed" },
              ]}
              value={onlineMarkerStorage}
              onChange={(value) => {
                form.setValue("markers.online_storage", value);
                if (value === "on_demand") form.setValue("markers.lazy_playback", "true");
              }}
              restartRequired={restartKeys.has("markers.online_storage")}
            />
          )}

          {showLazyPlayback && (
            <SettingField
              label="Find markers on playback"
              type="toggle"
              description={
                onlineMarkersEnabled
                  ? markerMode === "both"
                    ? "Check online first when playback starts. Silo detects only the intro or credits online providers don't have, using this server's CPU."
                    : "Check online for missing or outdated markers when playback starts."
                  : "Detect missing markers when playback starts. Local analysis uses CPU."
              }
              value={form.getValue("markers.lazy_playback") || "true"}
              onChange={(value) => form.setValue("markers.lazy_playback", value)}
              restartRequired={restartKeys.has("markers.lazy_playback")}
            />
          )}

          {localMarkersEnabled && (
            <SettingField
              label="Detection workers"
              type="number"
              description="How many seasons or movies Silo analyzes at once, each with its own ffmpeg process. Defaults to 1. Raise it to finish a large library sooner if your storage and CPU have room."
              value={form.getValue("markers.detection_workers")}
              onChange={(value) => form.setValue("markers.detection_workers", value)}
              restartRequired={restartKeys.has("markers.detection_workers")}
            />
          )}

          <div className="py-3.5">
            <MarkerTasksCard />
          </div>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
      <AlertDialog open={confirmRemake} onOpenChange={setConfirmRemake}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {widthChanged ? "Make preview images again?" : "Make seek previews again?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {remakeDescription(
                widthChanged,
                trickplayLibraries.isSuccess ? previewsToRemake : null,
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void form.save().catch(() => {})}>
              Save
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
