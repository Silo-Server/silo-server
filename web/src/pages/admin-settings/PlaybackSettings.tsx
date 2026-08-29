import { useMemo } from "react";
import { Link } from "react-router";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useHWAccelDetection, type HWAccelInfo } from "@/hooks/queries/admin/system";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import { Switch } from "@/components/ui/switch";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { PathSettingField } from "@/components/settings/PathSettingField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { SettingField, SettingFieldRow, SettingFieldStatus } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";
import { DEFAULT_FFMPEG_PATH, DEFAULT_TRANSCODE_DIR } from "./settingsPathDefaults";
import {
  CHAPTER_THUMBNAIL_EXECUTION_DEFAULT,
  buildHWDeviceRows,
  chapterThumbnailExecutionOptions,
  hasUsableTranscodeNode,
  nodeInventoriesDiverge,
  parseHWDeviceList,
  toggleHWDevice,
} from "./playbackSettings.utils";

// Shown without a disclosure: the handful of controls a household admin
// actually touches.
const TRANSCODING_ESSENTIAL_KEYS = [
  "playback.transcode_enabled",
  "playback.hw_accel",
  "allow_4k_transcode",
];

const TRANSCODING_ADVANCED_KEYS = [
  "playback.ffmpeg_path",
  "playback.transcode_dir",
  "playback.hw_device",
  "playback.local_transcode_fallback",
  "playback.transcode_hardware_tone_map_enabled",
  "playback.transcode_software_tone_map_enabled",
  "enable_transcode_throttle",
  "transcode_throttle_seconds",
  "playback.chapter_thumbnail_workers",
  "playback.chapter_thumbnail_execution",
  "playback.chapter_thumbnail_hdr_policy",
  "playback.chapter_thumbnail_software_tone_map_enabled",
];

const WATCH_KEYS = ["playback.watched_threshold", "playback.min_resume_threshold"];

// `playback.chapter_thumbnail_node_capacity` is deliberately absent from the
// UI (hidden tier): it is still saved and read through the settings API, but
// the per-node budget is derived from the node pool rather than typed in.
//
// The `download.*` family is its own page (DownloadsSettings), so it is not
// loaded or saved here.
const KEYS = [...TRANSCODING_ESSENTIAL_KEYS, ...TRANSCODING_ADVANCED_KEYS, ...WATCH_KEYS];

export default function PlaybackSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
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
  const chapterExecution =
    form.getValue("playback.chapter_thumbnail_execution") || CHAPTER_THUMBNAIL_EXECUTION_DEFAULT;
  // Gate the node-backed extraction modes only on a node list we actually
  // have: while the query is in flight or after it failed, leave every option
  // reachable rather than blocking a valid choice on a transient error.
  const transcodeNodeAvailable = !nodes.isSuccess || hasUsableTranscodeNode(nodes.data);

  const isDirty = form.isDirty;
  const anyDirty = (keys: string[]) => keys.some((key) => isDirty(key));
  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));

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

  if (form.isLoading) return <div>Loading...</div>;

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Playback" className="mb-8" />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label="Transcoding"
          restartAll={allRestart([...TRANSCODING_ESSENTIAL_KEYS, ...TRANSCODING_ADVANCED_KEYS])}
        >
          <SettingField
            label="Transcoding"
            type="toggle"
            description="Off serves only files clients can already play."
            value={form.getValue("playback.transcode_enabled")}
            onChange={(v) => form.setValue("playback.transcode_enabled", v)}
            restartRequired={restartKeys.has("playback.transcode_enabled")}
          />
          <SettingField
            label="Hardware acceleration"
            type="select"
            options={[
              { value: "auto", label: "Auto" },
              { value: "qsv", label: "Intel Quick Sync (QSV)" },
              { value: "vaapi", label: "VA-API" },
              { value: "nvenc", label: "NVIDIA NVENC" },
              { value: "videotoolbox", label: "VideoToolbox (macOS)" },
              { value: "none", label: "Software" },
            ]}
            description="Auto picks the best device this server can see."
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

          <AdvancedSection
            id="playback.transcoding"
            count={TRANSCODING_ADVANCED_KEYS.length - (showDevicePicker ? 0 : 1)}
            forceOpen={anyDirty(TRANSCODING_ADVANCED_KEYS)}
          >
            <PathSettingField
              label="FFmpeg path"
              defaultValue={DEFAULT_FFMPEG_PATH}
              description={`Leave blank to use the FFmpeg that ships with the server, at ${DEFAULT_FFMPEG_PATH}.`}
              value={form.getValue("playback.ffmpeg_path")}
              onChange={(v) => form.setValue("playback.ffmpeg_path", v)}
              restartRequired={restartKeys.has("playback.ffmpeg_path")}
            />
            <PathSettingField
              label="Transcode directory"
              defaultValue={DEFAULT_TRANSCODE_DIR}
              description={`Use fast local storage with room to spare. Leave blank to use ${DEFAULT_TRANSCODE_DIR}.`}
              value={form.getValue("playback.transcode_dir")}
              onChange={(v) => form.setValue("playback.transcode_dir", v)}
              restartRequired={restartKeys.has("playback.transcode_dir")}
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
              label="Local transcode fallback"
              type="toggle"
              description="Encode here when no transcode node is free."
              value={form.getValue("playback.local_transcode_fallback") || "true"}
              onChange={(v) => form.setValue("playback.local_transcode_fallback", v)}
              restartRequired={restartKeys.has("playback.local_transcode_fallback")}
            />
            <SettingField
              label="Enable Hardware HDR Tone Mapping"
              type="toggle"
              hint="Allows validated local or remote GPU executors to convert HDR video to SDR when transcoding."
              value={form.getValue("playback.transcode_hardware_tone_map_enabled") || "false"}
              onChange={(v) => form.setValue("playback.transcode_hardware_tone_map_enabled", v)}
              restartRequired={restartKeys.has("playback.transcode_hardware_tone_map_enabled")}
            />
            <SettingField
              label="Enable Software HDR Tone Mapping"
              type="toggle"
              hint="Allows the CPU to convert HDR video to SDR when transcoding. This can be very CPU-intensive."
              value={form.getValue("playback.transcode_software_tone_map_enabled") || "false"}
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
            {form.getValue("enable_transcode_throttle") === "true" && (
              <SettingField
                label="Buffer ahead"
                type="number"
                unit="seconds"
                value={form.getValue("transcode_throttle_seconds")}
                onChange={(v) => form.setValue("transcode_throttle_seconds", v)}
                restartRequired={restartKeys.has("transcode_throttle_seconds")}
              />
            )}
            <SettingField
              label="Chapter thumbnail workers"
              type="number"
              description="Parallel extraction jobs per library scan."
              value={form.getValue("playback.chapter_thumbnail_workers")}
              onChange={(v) => form.setValue("playback.chapter_thumbnail_workers", v)}
              restartRequired={restartKeys.has("playback.chapter_thumbnail_workers")}
            />
            <SettingField
              label="Generate chapter thumbnails on"
              type="select"
              options={chapterThumbnailExecutionOptions(chapterExecution, transcodeNodeAvailable)}
              status={
                transcodeNodeAvailable ? undefined : (
                  <SettingFieldStatus tone="warn">
                    No transcode nodes are connected
                  </SettingFieldStatus>
                )
              }
              value={chapterExecution}
              onChange={(v) => form.setValue("playback.chapter_thumbnail_execution", v)}
              restartRequired={restartKeys.has("playback.chapter_thumbnail_execution")}
            />
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
              value={
                form.getValue("playback.chapter_thumbnail_software_tone_map_enabled") || "false"
              }
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
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}

function formatResolved(resolved: string): string {
  switch (resolved) {
    case "qsv":
      return "Intel Quick Sync (QSV)";
    case "vaapi":
      return "VA-API";
    case "nvenc":
      return "NVIDIA NVENC";
    case "videotoolbox":
      return "VideoToolbox (macOS)";
    case "none":
      return "Software";
    default:
      return resolved;
  }
}

/**
 * One-line detection result, e.g. "Detected VA-API on renderD128". Returns
 * undefined while nothing has been probed yet so the caller can show its own
 * "detecting" state instead of an empty phrase.
 */
function describeDetection(detection: HWAccelInfo | undefined): string | undefined {
  if (!detection) return undefined;
  if (detection.resolved === "none") return "No supported graphics hardware found";
  const device = detection.render_devices?.[0];
  const onNode = detection.source === "transcode_node" ? " (transcode node)" : "";
  return `Detected ${formatResolved(detection.resolved)}${device ? ` on ${device}` : ""}${onNode}`;
}
