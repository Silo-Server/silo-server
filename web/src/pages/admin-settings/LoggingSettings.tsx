import { useMemo } from "react";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { SettingsSubheading } from "@/components/settings/SettingsSubheading";
import { Skeleton } from "@/components/ui/skeleton";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT } from "@/i18n";
import { SaveBar } from "@/components/SaveBar";

import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";

const OPSLOG_RETENTION_DAYS_KEY = "opslog.retention_days";
const OPSLOG_MAX_ROWS_KEY = "opslog.max_rows";
const OPSLOG_MAX_SIZE_MB_KEY = "opslog.max_size_mb";

const SERVER_LOG_ADVANCED_KEYS = ["server.log_quiet"];
const SERVER_LOG_KEYS = ["server.log_level", ...SERVER_LOG_ADVANCED_KEYS];

// How long and how large the log may grow is what an admin comes here to
// change. The row cap is a backstop whose default suits almost every server,
// and the policy decision log is a debugging tool; both sit behind Advanced.
//
// Hidden tier: still saved and readable through the settings API, without a
// control here — policy.decision_log_scope_sample_rate and
// opslog.bucket_policies (per-area limits).
const RETENTION_ESSENTIAL_KEYS = [OPSLOG_RETENTION_DAYS_KEY, OPSLOG_MAX_SIZE_MB_KEY];

const RETENTION_ADVANCED_KEYS = [
  OPSLOG_MAX_ROWS_KEY,
  "policy.decision_log_retention_days",
  "policy.decision_log_verbosity",
];

const RETENTION_KEYS = [...RETENTION_ESSENTIAL_KEYS, ...RETENTION_ADVANCED_KEYS];

const KEYS = [...SERVER_LOG_KEYS, ...RETENTION_KEYS];

// Example prefixes are the server's own log component names, not prose.
const QUIET_PREFIXES_EXAMPLE = "metadata, scanner";

/**
 * Settings → Logging: what the server writes to its log, and how much log
 * history the database keeps. Every field is a plain batched setting, so the
 * page saves through the ordinary save bar.
 */
export default function LoggingSettings() {
  const t = useT();
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();

  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));

  if (form.isLoading)
    return (
      <div
        className="space-y-6"
        role="status"
        aria-label={t("web.admin.settings.fields.loadingSettings")}
      >
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">{t("web.admin.settings.fields.loadingSettings")}</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.logging.title")} />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label={t("web.admin.settings.logging.server.group")}
          restartAll={allRestart(SERVER_LOG_KEYS)}
          dirty={anyDirty(SERVER_LOG_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.logging.server.level.label")}
            dirty={form.isDirty("server.log_level")}
            type="select"
            description={t("web.admin.settings.logging.server.level.description")}
            value={form.getValue("server.log_level")}
            onChange={(v) => form.setValue("server.log_level", v)}
            restartRequired={restartKeys.has("server.log_level")}
            options={[
              { value: "debug", label: t("web.admin.settings.logging.server.level.debug") },
              { value: "info", label: t("web.admin.settings.logging.server.level.info") },
              { value: "warn", label: t("web.admin.settings.logging.server.level.warn") },
              { value: "error", label: t("web.admin.settings.logging.server.level.error") },
            ]}
          />
          <AdvancedSection
            id="logging.server"
            count={SERVER_LOG_ADVANCED_KEYS.length}
            forceOpen={anyDirty(SERVER_LOG_ADVANCED_KEYS)}
          >
            <SettingField
              label={t("web.admin.settings.logging.server.quiet.label")}
              dirty={form.isDirty("server.log_quiet")}
              hint={QUIET_PREFIXES_EXAMPLE}
              description={t("web.admin.settings.logging.server.quiet.description")}
              value={form.getValue("server.log_quiet")}
              onChange={(v) => form.setValue("server.log_quiet", v)}
              restartRequired={restartKeys.has("server.log_quiet")}
            />
          </AdvancedSection>
        </FieldGroup>

        <FieldGroup
          label={t("web.admin.settings.logging.retention.group")}
          description={t("web.admin.settings.logging.retention.description")}
          restartAll={allRestart(RETENTION_KEYS)}
          dirty={anyDirty(RETENTION_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.logging.retention.days.label")}
            dirty={form.isDirty(OPSLOG_RETENTION_DAYS_KEY)}
            type="number"
            unit={t("web.admin.settings.logging.retention.unitDays")}
            value={form.getValue(OPSLOG_RETENTION_DAYS_KEY)}
            onChange={(v) => form.setValue(OPSLOG_RETENTION_DAYS_KEY, v)}
            restartRequired={restartKeys.has(OPSLOG_RETENTION_DAYS_KEY)}
          />
          <SettingField
            label={t("web.admin.settings.logging.retention.maxSize.label")}
            dirty={form.isDirty(OPSLOG_MAX_SIZE_MB_KEY)}
            type="number"
            unit="MB"
            value={form.getValue(OPSLOG_MAX_SIZE_MB_KEY)}
            onChange={(v) => form.setValue(OPSLOG_MAX_SIZE_MB_KEY, v)}
            restartRequired={restartKeys.has(OPSLOG_MAX_SIZE_MB_KEY)}
          />

          <AdvancedSection
            id="logging.retention"
            count={RETENTION_ADVANCED_KEYS.length}
            forceOpen={anyDirty(RETENTION_ADVANCED_KEYS)}
          >
            <SettingField
              label={t("web.admin.settings.logging.retention.maxRows.label")}
              dirty={form.isDirty(OPSLOG_MAX_ROWS_KEY)}
              type="number"
              value={form.getValue(OPSLOG_MAX_ROWS_KEY)}
              onChange={(v) => form.setValue(OPSLOG_MAX_ROWS_KEY, v)}
              restartRequired={restartKeys.has(OPSLOG_MAX_ROWS_KEY)}
            />

            <SettingsSubheading>
              {t("web.admin.settings.logging.retention.permissions.heading")}
            </SettingsSubheading>
            <SettingField
              label={t("web.admin.settings.logging.retention.permissions.days.label")}
              dirty={form.isDirty("policy.decision_log_retention_days")}
              type="number"
              unit={t("web.admin.settings.logging.retention.unitDays")}
              value={form.getValue("policy.decision_log_retention_days")}
              onChange={(v) => form.setValue("policy.decision_log_retention_days", v)}
              restartRequired={restartKeys.has("policy.decision_log_retention_days")}
            />
            <SettingField
              label={t("web.admin.settings.logging.retention.permissions.verbosity.label")}
              dirty={form.isDirty("policy.decision_log_verbosity")}
              type="select"
              description={t(
                "web.admin.settings.logging.retention.permissions.verbosity.description",
              )}
              value={form.getValue("policy.decision_log_verbosity") || "digest"}
              onChange={(v) => form.setValue("policy.decision_log_verbosity", v)}
              options={[
                {
                  value: "digest",
                  label: t("web.admin.settings.logging.retention.permissions.verbosity.digest"),
                },
                {
                  value: "verbose",
                  label: t("web.admin.settings.logging.retention.permissions.verbosity.verbose"),
                },
              ]}
              restartRequired={restartKeys.has("policy.decision_log_verbosity")}
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
