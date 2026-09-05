import { useMemo } from "react";
import { Link } from "react-router";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useHWAccelDetection, type HWAccelInfo } from "@/hooks/queries/admin/system";
import { useAdminNodes } from "@/hooks/queries/admin/nodes";
import { Switch } from "@/components/ui/switch";
import { Button } from "@/components/ui/button";
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
  "playback.segment_retention_seconds",
  "playback.hw_device",
  "playback.transcode_hardware_tone_map_enabled",
  "playback.transcode_software_tone_map_enabled",
  "enable_transcode_throttle",
  "transcode_throttle_seconds",
  "playback.chapter_thumbnail_workers",
  "playback.chapter_thumbnail_execution",
  "playback.chapter_thumbnail_hdr_policy",
  "playback.chapter_thumbnail_software_tone_map_enabled",
];

const executionOptions = [
  { value: "prefer_worker", label: "Prefer any worker" },
  { value: "prefer_transcode", label: "Prefer transcode node" },
  { value: "worker_only", label: "Any worker only" },
  { value: "prefer_api", label: "Prefer API server" },
  { value: "api_only", label: "API server only" },
];

const egressOptions = [
  { value: "prefer_proxy", label: "Prefer proxy" },
  { value: "proxy_only", label: "Proxy only" },
  { value: "prefer_api", label: "Prefer API server" },
  { value: "api_only", label: "API server only" },
];

type ExecutionWorkload = "remux" | "video_transcode";

function executionPreview(value: string, workload: ExecutionWorkload) {
  const worker = workload === "remux" ? "Any worker" : "Transcode node";
  switch (value) {
    case "prefer_transcode":
      return workload === "remux" ? "Transcode node → any worker → API" : "Transcode node → API";
    case "worker_only":
      return `${worker} only`;
    case "prefer_api":
      return `API → ${worker.toLowerCase()}`;
    case "api_only":
      return "API only";
    default:
      return `${worker} → API`;
  }
}

function egressPreview(value: string) {
  switch (value) {
    case "proxy_only":
      return "Proxy only";
    case "prefer_api":
      return "API → proxy";
    case "api_only":
      return "API only";
    default:
      return "Proxy → API";
  }
}

/** The whole path for a workload that needs an executor, e.g. "Worker → API · Proxy → API". */
function routePreview(execution: string, egress: string, workload: ExecutionWorkload) {
  return `${executionPreview(execution, workload)} · ${egressPreview(egress)}`;
}

// A routing policy either picks who runs the work (execution) or who serves the
// bytes (egress). The kind decides the choices offered and the value the server
// applies while the row has never been set.
const ROUTING_KINDS = {
  execution: { options: executionOptions, serverDefault: "prefer_worker" },
  egress: { options: egressOptions, serverDefault: "prefer_proxy" },
};

type RoutingKind = keyof typeof ROUTING_KINDS;

const ROUTING_KEYS = [
  "playback.routing.direct_play_egress",
  "playback.routing.remux_execution",
  "playback.routing.remux_egress",
  "playback.routing.video_transcode_execution",
  "playback.routing.video_transcode_egress",
] as const;

type RoutingKey = (typeof ROUTING_KEYS)[number];

interface RoutingField {
  key: RoutingKey;
  label: string;
  kind: RoutingKind;
  description?: string;
}

// One row per key in ROUTING_KEYS, in the order they render.
const ROUTING_FIELDS: readonly RoutingField[] = [
  {
    key: "playback.routing.direct_play_egress",
    label: "Direct play egress",
    kind: "egress",
    description: "Original bytes need no executor.",
  },
  {
    key: "playback.routing.remux_execution",
    label: "Remux execution",
    kind: "execution",
    description:
      "A worker can be a proxy or transcode node. Prefer transcode node runs HLS or progressive remux there when supported; progressive output is relayed through the selected proxy.",
  },
  { key: "playback.routing.remux_egress", label: "Remux egress", kind: "egress" },
  {
    key: "playback.routing.video_transcode_execution",
    label: "Video transcode execution",
    kind: "execution",
    description: "Video transcode workers are transcode nodes; proxy nodes only provide egress.",
  },
  {
    key: "playback.routing.video_transcode_egress",
    label: "Video transcode egress",
    kind: "egress",
  },
];

const ROUTING_PRESETS = {
  standard: {
    label: "Silo Defaults",
    values: {
      "playback.routing.direct_play_egress": "prefer_proxy",
      "playback.routing.remux_execution": "prefer_transcode",
      "playback.routing.remux_egress": "prefer_proxy",
      "playback.routing.video_transcode_execution": "prefer_transcode",
      "playback.routing.video_transcode_egress": "prefer_proxy",
    },
  },
  gpu: {
    label: "GPU offload",
    values: {
      "playback.routing.direct_play_egress": "prefer_api",
      "playback.routing.remux_execution": "prefer_api",
      "playback.routing.remux_egress": "prefer_api",
      "playback.routing.video_transcode_execution": "prefer_worker",
      "playback.routing.video_transcode_egress": "prefer_proxy",
    },
  },
  central: {
    label: "Central egress",
    values: {
      "playback.routing.direct_play_egress": "api_only",
      "playback.routing.remux_execution": "prefer_worker",
      "playback.routing.remux_egress": "api_only",
      "playback.routing.video_transcode_execution": "prefer_worker",
      "playback.routing.video_transcode_egress": "api_only",
    },
  },
} as const;

const WATCH_KEYS = ["playback.watched_threshold", "playback.min_resume_threshold"];

// `playback.chapter_thumbnail_node_capacity` is deliberately absent from the
// UI (hidden tier): it is still saved and read through the settings API, but
// the per-node budget is derived from the node pool rather than typed in.
//
// The `download.*` family is its own page (DownloadsSettings), so it is not
// loaded or saved here.
const KEYS = [
  ...TRANSCODING_ESSENTIAL_KEYS,
  ...TRANSCODING_ADVANCED_KEYS,
  ...ROUTING_KEYS,
  ...WATCH_KEYS,
];

/** One line of the preferred-path summary: workload on the left, route on the right. */
function PreferredPathRow({ label, route }: { label: string; route: string }) {
  return (
    <div className="flex justify-between gap-5">
      <span className="text-muted-foreground">{label}</span>
      <span className="font-medium">{route}</span>
    </div>
  );
}

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
  const proxyNodeAvailable =
    !nodes.isSuccess ||
    (nodes.data ?? []).some((node) => node.type === "proxy" && node.enabled && node.healthy);

  const routingValues = Object.fromEntries(
    ROUTING_KEYS.map((key) => [key, form.getValue(key)]),
  ) as Record<RoutingKey, string>;
  const activeRoutingPreset = Object.values(ROUTING_PRESETS).find((preset) =>
    ROUTING_KEYS.every((key) => routingValues[key] === preset.values[key]),
  );
  const applyRoutingPreset = (values: Record<RoutingKey, string>) => {
    for (const key of ROUTING_KEYS) form.setValue(key, values[key]);
  };

  const usesProxyOnlyEgress = ROUTING_KEYS.some((key) => routingValues[key] === "proxy_only");
  // A remux can run on a proxy node (progressive) or a transcode node (HLS), so
  // worker-only remuxing is only stranded when neither kind is available. Both
  // availability flags read as available until the node list has loaded, which
  // keeps the warning quiet on a transient error.
  const strandedRoute =
    (usesProxyOnlyEgress && !proxyNodeAvailable) ||
    (routingValues["playback.routing.remux_execution"] === "worker_only" &&
      !proxyNodeAvailable &&
      !transcodeNodeAvailable) ||
    (routingValues["playback.routing.video_transcode_execution"] === "worker_only" &&
      !transcodeNodeAvailable);

  const isDirty = form.isDirty;
  const anyDirty = (keys: readonly string[]) => keys.some((key) => isDirty(key));
  const allRestart = (keys: readonly string[]) => keys.every((key) => restartKeys.has(key));

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
              label="Transcode back buffer"
              type="number"
              unit="seconds"
              hint="Keeps this much already-downloaded media for instant backward seeking, then reclaims older transcode segments. Use 0 to disable cleanup; enabled values must be at least 120 seconds. Pair with transcode throttling to bound both behind- and ahead-of-client disk usage."
              value={form.getValue("playback.segment_retention_seconds")}
              onChange={(v) => form.setValue("playback.segment_retention_seconds", v)}
              restartRequired={restartKeys.has("playback.segment_retention_seconds")}
            />
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

        <FieldGroup label="Node routing" restartAll={allRestart(ROUTING_KEYS)}>
          <SettingFieldRow
            label="Routing preset"
            description="Presets update the five primitive policies below; Custom is not stored."
          >
            <div className="flex flex-wrap justify-end gap-2">
              {Object.entries(ROUTING_PRESETS).map(([id, preset]) => (
                <Button
                  key={id}
                  type="button"
                  size="sm"
                  variant={activeRoutingPreset === preset ? "default" : "outline"}
                  onClick={() => applyRoutingPreset(preset.values)}
                >
                  {preset.label}
                </Button>
              ))}
              {!activeRoutingPreset && (
                <span className="text-muted-foreground self-center text-xs">Custom</span>
              )}
            </div>
          </SettingFieldRow>

          {ROUTING_FIELDS.map((field) => (
            <SettingField
              key={field.key}
              label={field.label}
              type="select"
              options={ROUTING_KINDS[field.kind].options}
              description={field.description}
              value={routingValues[field.key] || ROUTING_KINDS[field.kind].serverDefault}
              onChange={(v) => form.setValue(field.key, v)}
              restartRequired={restartKeys.has(field.key)}
            />
          ))}

          <SettingFieldRow
            label="Preferred paths"
            description="Arrows show soft fallback order; only modes never cross that boundary."
          >
            <div className="grid min-w-64 gap-1 text-xs">
              <PreferredPathRow
                label="Direct play"
                route={egressPreview(routingValues["playback.routing.direct_play_egress"])}
              />
              <PreferredPathRow
                label="Remux"
                route={routePreview(
                  routingValues["playback.routing.remux_execution"],
                  routingValues["playback.routing.remux_egress"],
                  "remux",
                )}
              />
              <PreferredPathRow
                label="Video transcode"
                route={routePreview(
                  routingValues["playback.routing.video_transcode_execution"],
                  routingValues["playback.routing.video_transcode_egress"],
                  "video_transcode",
                )}
              />
            </div>
          </SettingFieldRow>

          {strandedRoute && (
            <SettingFieldStatus tone="warn">
              An “only” route currently has no healthy supporting node. Saving is allowed so nodes
              can join later.
            </SettingFieldStatus>
          )}
          {usesProxyOnlyEgress && (
            <SettingFieldStatus tone="warn">
              Proxy-only egress requires every native client to support authorized media origins;
              older clients may be unable to start that workload.
            </SettingFieldStatus>
          )}
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
