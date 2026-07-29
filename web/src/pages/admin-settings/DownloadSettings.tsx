import { useMemo } from "react";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";

const KEYS = [
  "download.enabled",
  "download.server_bandwidth_mbps",
  "download.user_bandwidth_mbps",
  "download.max_concurrent_per_user",
  "download.max_per_period",
  "download.period_duration",
  "download.transcode_enabled",
  "download.artifact_dir",
  "download.max_concurrent_prepares",
  "download.artifact_max_bytes",
];

export default function DownloadSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });

  if (form.isLoading) return <div>Loading...</div>;

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Downloads</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure download permissions, bandwidth limits, and quotas.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="General">
          <SettingField
            label="Downloads Enabled"
            hint="Allow users to download media files"
            type="toggle"
            value={form.getValue("download.enabled")}
            onChange={(v) => form.setValue("download.enabled", v)}
          />
        </FieldGroup>

        <FieldGroup label="Bandwidth Limits">
          <SettingField
            label="Server Bandwidth (Mbps)"
            hint="Total download bandwidth for the entire server in megabits/sec. 0 = unlimited."
            value={form.getValue("download.server_bandwidth_mbps")}
            onChange={(v) => form.setValue("download.server_bandwidth_mbps", v)}
          />
          <SettingField
            label="Per-User Bandwidth (Mbps)"
            hint="Max download bandwidth per user, shared across active downloads. 0 = unlimited."
            value={form.getValue("download.user_bandwidth_mbps")}
            onChange={(v) => form.setValue("download.user_bandwidth_mbps", v)}
          />
        </FieldGroup>

        <FieldGroup label="Quantity Limits">
          <SettingField
            label="Max Concurrent Downloads Per User"
            hint="How many downloads a user can have active at once. 0 = unlimited."
            value={form.getValue("download.max_concurrent_per_user")}
            onChange={(v) => form.setValue("download.max_concurrent_per_user", v)}
          />
          <SettingField
            label="Max Downloads Per Period"
            hint="Total downloads a user can create per period. 0 = unlimited."
            value={form.getValue("download.max_per_period")}
            onChange={(v) => form.setValue("download.max_per_period", v)}
          />
          <SettingField
            label="Period Duration"
            hint="Rolling window for the per-period limit (e.g., 24h, 168h, 720h)"
            value={form.getValue("download.period_duration")}
            onChange={(v) => form.setValue("download.period_duration", v)}
          />
        </FieldGroup>

        <FieldGroup label="Offline Sync (Prepared Downloads)">
          <SettingField
            label="Transcode-to-File Enabled"
            hint="Allow server-side transcode of downloads to a device-friendly file. Requires the per-user download-transcode permission. Downloaded files persist on-device until the user deletes them — there is no expiry or revocation of files already downloaded."
            type="toggle"
            value={form.getValue("download.transcode_enabled")}
            onChange={(v) => form.setValue("download.transcode_enabled", v)}
          />
          <SettingField
            label="Artifact Directory"
            hint="Where prepared (remux/transcode) download files are written. Empty = a 'silo-download-artifacts' directory beside the transcode directory."
            value={form.getValue("download.artifact_dir")}
            onChange={(v) => form.setValue("download.artifact_dir", v)}
          />
          <SettingField
            label="Max Concurrent Prepares"
            hint="Encode/remux worker-pool size for preparing download files."
            value={form.getValue("download.max_concurrent_prepares")}
            onChange={(v) => form.setValue("download.max_concurrent_prepares", v)}
          />
          <SettingField
            label="Artifact Storage Budget (bytes)"
            hint="LRU eviction budget for prepared download files. 0 = unlimited."
            value={form.getValue("download.artifact_max_bytes")}
            onChange={(v) => form.setValue("download.artifact_max_bytes", v)}
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
