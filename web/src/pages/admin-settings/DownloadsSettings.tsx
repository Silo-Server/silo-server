import { useMemo } from "react";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { LimitField } from "@/components/settings/LimitField";
import { PathSettingField } from "@/components/settings/PathSettingField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Skeleton } from "@/components/ui/skeleton";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";

import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";
import { effectiveDownloadArtifactDir } from "./settingsPathDefaults";

// Decimal GB, matching how drives and object stores are sold and reported.
const BYTES_PER_GB = 1_000_000_000;

// Shown without a disclosure: whether offline downloads exist at all, and the
// one cap a household admin actually reaches for.
const ESSENTIAL_KEYS = ["download.enabled", "download.user_bandwidth_mbps"];

// Enforced per user: QuantityLimiter counts concurrency and period usage
// against a user ID (internal/downloads/limiter.go), and the bandwidth cap
// shapes each user's transfer. They are listed first, under their own
// heading, so they do not read as server-wide budgets.
const PER_USER_ADVANCED_KEYS = [
  "download.max_concurrent_per_user",
  "download.max_per_period",
  "download.period_duration",
];

// Enforced across the whole server.
const GLOBAL_ADVANCED_KEYS = [
  "download.server_bandwidth_mbps",
  "download.transcode_enabled",
  "download.local_transcode_fallback",
  "download.artifact_dir",
  "download.max_concurrent_prepares",
  "download.artifact_max_bytes",
];

const ADVANCED_KEYS = [...PER_USER_ADVANCED_KEYS, ...GLOBAL_ADVANCED_KEYS];

const KEYS = [...ESSENTIAL_KEYS, ...ADVANCED_KEYS];

export default function DownloadsSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();

  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));

  // Where a blank prepared-file directory actually resolves to. The transcode
  // directory it derives from belongs to the Playback page, which is its own
  // form instance, so this is the saved value: `getValue` falls through to the
  // full effective settings for keys this page does not stage.
  const derivedArtifactDir = effectiveDownloadArtifactDir(
    "",
    form.getValue("playback.transcode_dir"),
  );

  if (form.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-40" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">Loading settings</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="Downloads" className="mb-8" />

      <div className="flex-1 space-y-5">
        <FieldGroup label="Downloads" restartAll={allRestart(KEYS)} dirty={anyDirty(KEYS)}>
          <SettingField
            label="Allow downloads"
            type="toggle"
            value={form.getValue("download.enabled")}
            onChange={(v) => form.setValue("download.enabled", v)}
            restartRequired={restartKeys.has("download.enabled")}
          />
          <LimitField
            label="Per-user bandwidth"
            unit="Mbps"
            value={form.getValue("download.user_bandwidth_mbps")}
            onChange={(v) => form.setValue("download.user_bandwidth_mbps", v)}
            restartRequired={restartKeys.has("download.user_bandwidth_mbps")}
          />

          <AdvancedSection
            id="downloads"
            count={ADVANCED_KEYS.length}
            forceOpen={anyDirty(ADVANCED_KEYS)}
          >
            <SettingsSubheading>Per user</SettingsSubheading>
            <LimitField
              label="Downloads at once per user"
              hint="Counted per user, alongside the per-user bandwidth cap above."
              value={form.getValue("download.max_concurrent_per_user")}
              onChange={(v) => form.setValue("download.max_concurrent_per_user", v)}
              restartRequired={restartKeys.has("download.max_concurrent_per_user")}
            />
            <LimitField
              label="Downloads per period"
              hint="How many each user may start in the period below."
              value={form.getValue("download.max_per_period")}
              onChange={(v) => form.setValue("download.max_per_period", v)}
              restartRequired={restartKeys.has("download.max_per_period")}
            />
            <SettingField
              label="Period length"
              type="duration"
              description="Rolling window each user's count is measured over, e.g. 24h or 168h."
              value={form.getValue("download.period_duration")}
              onChange={(v) => form.setValue("download.period_duration", v)}
              restartRequired={restartKeys.has("download.period_duration")}
            />

            <SettingsSubheading>Whole server</SettingsSubheading>
            <LimitField
              label="Server bandwidth"
              unit="Mbps"
              hint="All downloads on this server combined."
              value={form.getValue("download.server_bandwidth_mbps")}
              onChange={(v) => form.setValue("download.server_bandwidth_mbps", v)}
              restartRequired={restartKeys.has("download.server_bandwidth_mbps")}
            />
            <SettingField
              label="Prepare device-friendly copies"
              type="toggle"
              description="Converts a file the device cannot play before download."
              value={form.getValue("download.transcode_enabled")}
              onChange={(v) => form.setValue("download.transcode_enabled", v)}
              restartRequired={restartKeys.has("download.transcode_enabled")}
            />
            <SettingField
              label="Prepare locally when workers are unavailable"
              type="toggle"
              description="This is separate from live playback routing."
              value={form.getValue("download.local_transcode_fallback") || "true"}
              onChange={(v) => form.setValue("download.local_transcode_fallback", v)}
              restartRequired={restartKeys.has("download.local_transcode_fallback")}
            />
            <PathSettingField
              label="Prepared file directory"
              defaultValue={derivedArtifactDir}
              description="Leave blank for a silo-download-artifacts folder beside the transcode directory."
              value={form.getValue("download.artifact_dir")}
              onChange={(v) => form.setValue("download.artifact_dir", v)}
              restartRequired={restartKeys.has("download.artifact_dir")}
            />
            {/* Not a LimitField: the server reads 0 as "use the built-in
                worker count" (2), not as unlimited. */}
            <SettingField
              label="Files prepared at once"
              type="number"
              description="0 uses the built-in default of 2."
              value={form.getValue("download.max_concurrent_prepares")}
              onChange={(v) => form.setValue("download.max_concurrent_prepares", v)}
              restartRequired={restartKeys.has("download.max_concurrent_prepares")}
            />
            {/* Stored as raw bytes (download.artifact_max_bytes); typed in GB
                because nobody sizes a disk budget in bytes. Unlimited stays
                the default and still writes the 0 sentinel. */}
            <LimitField
              label="Prepared file storage budget"
              unit="GB"
              scale={BYTES_PER_GB}
              hint="Least recently used files are deleted first."
              value={form.getValue("download.artifact_max_bytes")}
              onChange={(v) => form.setValue("download.artifact_max_bytes", v)}
              restartRequired={restartKeys.has("download.artifact_max_bytes")}
            />
          </AdvancedSection>
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
