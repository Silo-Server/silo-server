import { useMemo } from "react";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useHWAccelDetection } from "@/hooks/queries/admin/system";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";
import {
  buildHWDeviceRows,
  nodeInventoriesDiverge,
  parseHWDeviceList,
  toggleHWDevice,
} from "./playbackSettings.utils";

const KEYS = [
  "playback.ffmpeg_path",
  "playback.transcode_dir",
  "playback.hw_accel",
  "playback.hw_device",
  "playback.transcode_enabled",
  "playback.local_transcode_fallback",
  "allow_4k_transcode",
  "enable_transcode_throttle",
  "transcode_throttle_seconds",
  "playback.chapter_thumbnail_workers",
  "playback.chapter_thumbnail_execution",
  "playback.chapter_thumbnail_node_capacity",
  "playback.chapter_thumbnail_hdr_policy",
  "playback.chapter_thumbnail_software_tone_map_enabled",
  "playback.watched_threshold",
  "playback.min_resume_threshold",
];

export default function PlaybackSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
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

  if (form.isLoading) return <div>Loading...</div>;

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Playback</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure transcoding, segment generation, and watched-state behavior.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Transcoding">
          <SettingField
            label="FFmpeg Path"
            value={form.getValue("playback.ffmpeg_path")}
            onChange={(v) => form.setValue("playback.ffmpeg_path", v)}
          />
          <SettingField
            label="Transcode Directory"
            value={form.getValue("playback.transcode_dir")}
            onChange={(v) => form.setValue("playback.transcode_dir", v)}
          />
          <SettingField
            label="Hardware Acceleration"
            type="select"
            options={[
              { value: "auto", label: "Auto" },
              { value: "qsv", label: "Intel Quick Sync (QSV)" },
              { value: "vaapi", label: "VA-API" },
              { value: "nvenc", label: "NVIDIA NVENC" },
              { value: "none", label: "Software" },
            ]}
            value={form.getValue("playback.hw_accel")}
            onChange={(v) => form.setValue("playback.hw_accel", v)}
          />
          {form.getValue("playback.hw_accel") === "auto" && hwDetection.data && (
            <div className="-mt-1 flex items-center gap-2 text-xs">
              <span
                className={`inline-block h-1.5 w-1.5 rounded-full ${
                  hwDetection.data.resolved !== "none" ? "bg-emerald-500" : "bg-amber-500"
                }`}
              />
              <span className="text-muted-foreground">
                {formatResolved(hwDetection.data.resolved)}
                {hwDetection.data.render_devices?.[0] && ` — ${hwDetection.data.render_devices[0]}`}
                {hwDetection.data.source === "transcode_node" && " (transcode node)"}
              </span>
            </div>
          )}
          {form.getValue("playback.hw_accel") === "auto" && hwDetection.isLoading && (
            <p className="text-muted-foreground -mt-1 text-xs">Detecting hardware...</p>
          )}
          {hwAccel !== "none" && isNvenc && selectedDevices.length > 1 && (
            <p className="-mt-1 text-xs text-amber-500">
              Multi-GPU balancing supports QSV/VA-API only; with NVENC the server uses the first
              configured device ({selectedDevices[0]}).
            </p>
          )}
          {hwAccel !== "none" && !isNvenc && deviceRows.length > 0 && (
            <div className="flex flex-col gap-2 py-3">
              <div className="space-y-0.5">
                <Label className="text-sm font-medium">GPU Devices</Label>
                <p className="text-muted-foreground text-xs">
                  {selectedDevices.length === 0
                    ? "Auto — the first available device handles every transcode. Select devices to pin or balance."
                    : selectedDevices.length === 1
                      ? "All transcodes run on the selected device."
                      : "Transcode sessions balance across the selected devices (least loaded first)."}
                  {hwDetection.data?.source === "transcode_node" &&
                    " Devices reported by a transcode node."}
                </p>
                {inventoriesDiverge && (
                  <p className="text-xs text-amber-500">
                    This setting applies to every transcode node, but the nodes report different
                    devices. Only paths present on all nodes are safe to select.
                  </p>
                )}
              </div>
              <div className="space-y-2">
                {deviceRows.map((row) => (
                  <div key={row.path} className="flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <p
                        className={`truncate text-sm ${row.detected ? "" : "text-muted-foreground"}`}
                      >
                        {row.description}
                      </p>
                      <p className="text-muted-foreground truncate font-mono text-xs">{row.path}</p>
                      {row.missingOnNodes.length > 0 && (
                        <p className="truncate text-xs text-amber-500">
                          Not present on: {row.missingOnNodes.join(", ")}
                        </p>
                      )}
                    </div>
                    <Switch
                      checked={selectedDevices.includes(row.path)}
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
                  </div>
                ))}
              </div>
            </div>
          )}
          <SettingField
            label="Transcoding Enabled"
            type="toggle"
            value={form.getValue("playback.transcode_enabled")}
            onChange={(v) => form.setValue("playback.transcode_enabled", v)}
          />
          <SettingField
            label="Local Transcode Fallback"
            type="toggle"
            hint="When no eligible transcode node is available, transcode on this server instead. Disable to keep all transcoding on dedicated nodes — playback that requires transcoding fails while no node is eligible."
            value={form.getValue("playback.local_transcode_fallback") || "true"}
            onChange={(v) => form.setValue("playback.local_transcode_fallback", v)}
          />
          <SettingField
            label="Allow 4K Transcoding"
            type="toggle"
            value={form.getValue("allow_4k_transcode")}
            onChange={(v) => form.setValue("allow_4k_transcode", v)}
          />
          <SettingField
            label="Enable Transcode Throttling"
            type="toggle"
            value={form.getValue("enable_transcode_throttle")}
            onChange={(v) => form.setValue("enable_transcode_throttle", v)}
          />
          {form.getValue("enable_transcode_throttle") === "true" && (
            <SettingField
              label="Throttle Buffer (seconds)"
              type="number"
              hint="How many seconds ahead FFmpeg transcodes before pausing. Minimum: 60."
              value={form.getValue("transcode_throttle_seconds")}
              onChange={(v) => form.setValue("transcode_throttle_seconds", v)}
            />
          )}
        </FieldGroup>

        <FieldGroup label="Segments">
          <SettingField
            label="Chapter Thumbnail Workers"
            type="number"
            hint="Global chapter thumbnail dispatcher concurrency. Higher values improve throughput but can drive more local or remote extraction work at once."
            value={form.getValue("playback.chapter_thumbnail_workers")}
            onChange={(v) => form.setValue("playback.chapter_thumbnail_workers", v)}
          />
          <SettingField
            label="Chapter Thumbnail Execution"
            type="select"
            options={[
              { value: "local", label: "Local only" },
              { value: "prefer_transcode_nodes", label: "Prefer transcode nodes" },
              { value: "transcode_nodes_only", label: "Transcode nodes only" },
            ]}
            hint="Controls whether chapter thumbnails run on the API node or are offloaded to available transcode nodes."
            value={form.getValue("playback.chapter_thumbnail_execution") || "local"}
            onChange={(v) => form.setValue("playback.chapter_thumbnail_execution", v)}
          />
          <SettingField
            label="Chapter Thumbnail Node Capacity"
            type="number"
            hint="Per transcode-node budget for chapter thumbnail jobs when remote execution is enabled."
            value={form.getValue("playback.chapter_thumbnail_node_capacity")}
            onChange={(v) => form.setValue("playback.chapter_thumbnail_node_capacity", v)}
          />
          <SettingField
            label="HDR Chapter Thumbnail Policy"
            type="select"
            options={[
              { value: "best_effort", label: "Best effort tone mapping" },
              { value: "disabled", label: "Disable HDR/DV thumbnails" },
            ]}
            hint="Controls whether chapter thumbnails are generated for HDR or Dolby Vision sources. SDR files are unaffected."
            value={form.getValue("playback.chapter_thumbnail_hdr_policy") || "best_effort"}
            onChange={(v) => form.setValue("playback.chapter_thumbnail_hdr_policy", v)}
          />
          <SettingField
            label="Enable CPU Tone Mapping"
            type="toggle"
            hint="Allows CPU/software tone mapping when hardware HDR chapter-thumbnail extraction is unavailable or fails. Disabled by default because it can be CPU-intensive."
            value={form.getValue("playback.chapter_thumbnail_software_tone_map_enabled") || "false"}
            onChange={(v) =>
              form.setValue("playback.chapter_thumbnail_software_tone_map_enabled", v)
            }
            disabled={form.getValue("playback.chapter_thumbnail_hdr_policy") === "disabled"}
          />
        </FieldGroup>

        <FieldGroup label="Behavior">
          <SettingField
            label="Watched Threshold (%)"
            type="number"
            hint="Mark as watched after this % is played (default: 90)"
            value={form.getValue("playback.watched_threshold")}
            onChange={(v) => form.setValue("playback.watched_threshold", v)}
          />
          <SettingField
            label="Min Resume Threshold (%)"
            type="number"
            hint="Ignore progress below this % of duration (default: 5)"
            value={form.getValue("playback.min_resume_threshold")}
            onChange={(v) => form.setValue("playback.min_resume_threshold", v)}
          />
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={form.restartRequired}
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
    case "none":
      return "Software";
    default:
      return resolved;
  }
}
