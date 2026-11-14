import { useMemo } from "react";
import { Link } from "react-router";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { LimitField } from "@/components/settings/LimitField";
import { PathSettingField } from "@/components/settings/PathSettingField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT } from "@/i18n";
import { SaveBar } from "@/components/SaveBar";

import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";
import { effectiveDownloadArtifactDir } from "./settingsPathDefaults";

// Decimal GB, matching how drives and object stores are sold and reported.
const BYTES_PER_GB = 1_000_000_000;

// Shown without a disclosure: whether offline downloads exist at all, whether
// Silo converts them, and where and how much it prepares. Without conversion no
// lower quality is offered, and a device gets the original video even when it
// can't play it (DownloadQualityResolver in internal/downloads/policy.go).
const ESSENTIAL_KEYS = [
  "download.enabled",
  "download.transcode_enabled",
  "download.max_concurrent_prepares",
  "download.artifact_dir",
  "download.artifact_max_bytes",
  "download.artifact_disk_ceiling_percent",
  "download.server_bandwidth_mbps",
];

// Enforced per user: QuantityLimiter counts concurrency and period usage
// against a user ID (internal/downloads/limiter.go), and the bandwidth cap
// shapes each user's transfer. They are listed first, under their own
// heading, so they do not read as server-wide budgets.
const PER_USER_ADVANCED_KEYS = [
  "download.user_bandwidth_mbps",
  "download.max_concurrent_per_user",
  "download.max_per_period",
  "download.period_duration",
];

// Enforced across the whole server.
const GLOBAL_ADVANCED_KEYS = ["download.local_transcode_fallback", "download.artifact_cache_hours"];

const ADVANCED_KEYS = [...PER_USER_ADVANCED_KEYS, ...GLOBAL_ADVANCED_KEYS];

const KEYS = [...ESSENTIAL_KEYS, ...ADVANCED_KEYS];

export default function DownloadsSettings() {
  const t = useT();
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

  if (form.isPending)
    return (
      <div
        className="space-y-6"
        role="status"
        aria-label={t("web.admin.settings.fields.loadingSettings")}
      >
        <Skeleton className="h-8 w-40" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">{t("web.admin.settings.fields.loadingSettings")}</span>
      </div>
    );

  if (form.loadError && !form.loaded) {
    return (
      <div className="space-y-4">
        <SettingsPageHeader title={t("web.admin.settings.downloads.title")} />
        <p role="alert">{t("web.admin.settings.downloads.loadError")}</p>
        <Button variant="outline" onClick={() => void form.retryLoad()}>
          {t("common.retry")}
        </Button>
      </div>
    );
  }

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader
        title={t("web.admin.settings.downloads.title")}
        className="mb-8"
        actions={
          <Link to="/admin/downloads" className="text-sm underline underline-offset-3">
            {t("web.admin.settings.downloads.storageLink")}
          </Link>
        }
      />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label={t("web.admin.settings.downloads.group")}
          restartAll={allRestart(KEYS)}
          dirty={anyDirty(KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.downloads.enabled.label")}
            type="toggle"
            description={t("web.admin.settings.downloads.enabled.description")}
            value={form.getValue("download.enabled")}
            onChange={(v) => form.setValue("download.enabled", v)}
            restartRequired={restartKeys.has("download.enabled")}
          />
          <SettingField
            label={t("web.admin.settings.downloads.transcode.label")}
            type="toggle"
            description={t("web.admin.settings.downloads.transcode.description")}
            value={form.getValue("download.transcode_enabled")}
            onChange={(v) => form.setValue("download.transcode_enabled", v)}
            restartRequired={restartKeys.has("download.transcode_enabled")}
          />
          {/* Not a LimitField: the server reads 0 as "use the built-in
              worker count" (2), not as unlimited. */}
          <SettingField
            label={t("web.admin.settings.downloads.prepares.label")}
            type="number"
            description={t("web.admin.settings.downloads.prepares.description")}
            value={form.getValue("download.max_concurrent_prepares")}
            onChange={(v) => form.setValue("download.max_concurrent_prepares", v)}
            restartRequired={restartKeys.has("download.max_concurrent_prepares")}
          />
          <PathSettingField
            label={t("web.admin.settings.downloads.artifactDir.label")}
            defaultValue={derivedArtifactDir}
            description={t("web.admin.settings.downloads.artifactDir.description")}
            value={form.getValue("download.artifact_dir")}
            onChange={(v) => form.setValue("download.artifact_dir", v)}
            restartRequired={restartKeys.has("download.artifact_dir")}
          />
          {/* Stored as raw bytes (download.artifact_max_bytes); typed in GB
              because nobody sizes a disk budget in bytes. Unlimited stays
              the default and still writes the 0 sentinel. */}
          <LimitField
            label={t("web.admin.settings.downloads.budget.label")}
            unit="GB"
            scale={BYTES_PER_GB}
            hint={t("web.admin.settings.downloads.budget.hint")}
            value={form.getValue("download.artifact_max_bytes")}
            onChange={(v) => form.setValue("download.artifact_max_bytes", v)}
            restartRequired={restartKeys.has("download.artifact_max_bytes")}
          />
          <SettingField
            label={t("web.admin.settings.downloads.diskCeiling.label")}
            type="number"
            description={t("web.admin.settings.downloads.diskCeiling.description")}
            value={form.getValue("download.artifact_disk_ceiling_percent")}
            onChange={(v) => form.setValue("download.artifact_disk_ceiling_percent", v)}
            restartRequired={restartKeys.has("download.artifact_disk_ceiling_percent")}
          />
          <LimitField
            label={t("web.admin.settings.downloads.serverBandwidth.label")}
            unit="Mbps"
            hint={t("web.admin.settings.downloads.serverBandwidth.hint")}
            value={form.getValue("download.server_bandwidth_mbps")}
            onChange={(v) => form.setValue("download.server_bandwidth_mbps", v)}
            restartRequired={restartKeys.has("download.server_bandwidth_mbps")}
          />

          <AdvancedSection
            id="downloads"
            count={ADVANCED_KEYS.length}
            forceOpen={anyDirty(ADVANCED_KEYS)}
          >
            <SettingsSubheading>
              {t("web.admin.settings.downloads.perUser.heading")}
            </SettingsSubheading>
            <LimitField
              label={t("web.admin.settings.downloads.perUser.bandwidth.label")}
              unit="Mbps"
              value={form.getValue("download.user_bandwidth_mbps")}
              onChange={(v) => form.setValue("download.user_bandwidth_mbps", v)}
              restartRequired={restartKeys.has("download.user_bandwidth_mbps")}
            />
            <LimitField
              label={t("web.admin.settings.downloads.perUser.concurrent.label")}
              hint={t("web.admin.settings.downloads.perUser.concurrent.hint")}
              value={form.getValue("download.max_concurrent_per_user")}
              onChange={(v) => form.setValue("download.max_concurrent_per_user", v)}
              restartRequired={restartKeys.has("download.max_concurrent_per_user")}
            />
            <LimitField
              label={t("web.admin.settings.downloads.perUser.perPeriod.label")}
              hint={t("web.admin.settings.downloads.perUser.perPeriod.hint")}
              value={form.getValue("download.max_per_period")}
              onChange={(v) => form.setValue("download.max_per_period", v)}
              restartRequired={restartKeys.has("download.max_per_period")}
            />
            <SettingField
              label={t("web.admin.settings.downloads.perUser.period.label")}
              type="duration"
              description={t("web.admin.settings.downloads.perUser.period.description")}
              value={form.getValue("download.period_duration")}
              onChange={(v) => form.setValue("download.period_duration", v)}
              restartRequired={restartKeys.has("download.period_duration")}
            />

            <SettingsSubheading>
              {t("web.admin.settings.downloads.server.heading")}
            </SettingsSubheading>
            <SettingField
              label={t("web.admin.settings.downloads.server.localFallback.label")}
              type="toggle"
              description={t("web.admin.settings.downloads.server.localFallback.description")}
              value={form.getValue("download.local_transcode_fallback") || "true"}
              onChange={(v) => form.setValue("download.local_transcode_fallback", v)}
              restartRequired={restartKeys.has("download.local_transcode_fallback")}
            />
            <SettingField
              label={t("web.admin.settings.downloads.server.cacheHours.label")}
              type="number"
              description={t("web.admin.settings.downloads.server.cacheHours.description")}
              value={form.getValue("download.artifact_cache_hours")}
              onChange={(v) => form.setValue("download.artifact_cache_hours", v)}
              restartRequired={restartKeys.has("download.artifact_cache_hours")}
            />
          </AdvancedSection>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        canSave={form.loaded}
      />
    </div>
  );
}
