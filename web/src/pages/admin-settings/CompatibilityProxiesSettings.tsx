import { useMemo } from "react";
import {
  AlertCircle,
  CheckCircle2,
  Download,
  Loader2,
  Power,
  PowerOff,
  Trash2,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import {
  useInstallJellyfinCompatWeb,
  useJellyfinCompatStatus,
  useRemoveJellyfinCompatWeb,
  useUpdateJellyfinCompatSettings,
} from "@/hooks/queries/admin/settings";
import { hasPinnedJellyfinWebInstalled } from "@/lib/jellyfinCompat";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SaveBar } from "@/components/SaveBar";

import { FieldGroup } from "./FieldGroup";
import { SettingField, SettingFieldRow, SettingFieldStatus } from "./SettingField";
import { formatDateTime } from "@/lib/datetime";

/**
 * Rows in the Jellyfin Advanced section. The server ID is among them but is
 * shown read-only: it is generated on first start, and every Jellyfin app
 * keys its saved server on it.
 */
const JELLYFIN_ADVANCED_KEYS = [
  "jellyfin_compat.server_name",
  "jellyfin_compat.server_id",
  "jellyfin_compat.emulated_server_version",
  "jellyfin_compat.session_ttl",
  "jellyfin_compat.playback_session_ttl",
  "jellyfin_compat.web_version",
  "jellyfin_compat.web_install_dir",
];

const JELLYFIN_KEYS = [
  "jellyfin_compat.enabled",
  "jellyfin_compat.public_url",
  "jellyfin_compat.web_enabled",
  ...JELLYFIN_ADVANCED_KEYS,
];

const AUDIOBOOKSHELF_KEYS = ["audiobookshelf_compat.enabled"];

const KEYS = [...JELLYFIN_KEYS, ...AUDIOBOOKSHELF_KEYS];

// Installing or removing the Jellyfin Web files uses the saved values, so the
// buttons stay disabled while either of these is only staged in the form.
const WEB_INSTALL_KEYS = ["jellyfin_compat.web_version", "jellyfin_compat.web_install_dir"];

// Product names that head their groups, and an example address.
const JELLYFIN = "Jellyfin";
const AUDIOBOOKSHELF = "Audiobookshelf";
const PUBLIC_URL_HINT = "https://media.example.com";

function statusLabel(value: string): string {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

// `web_state` is an internal enum; admins get plain wording instead.
const WEB_STATE_LABELS: Record<string, MessageKey> = {
  missing: "web.admin.settings.compatibilityProxies.webState.missing",
  installed: "web.admin.settings.compatibilityProxies.webState.installed",
  update_available: "web.admin.settings.compatibilityProxies.webState.updateAvailable",
  installing: "web.admin.settings.compatibilityProxies.webState.installing",
  removing: "web.admin.settings.compatibilityProxies.webState.removing",
  failed: "web.admin.settings.compatibilityProxies.webState.failed",
};

function webStateLabel(t: Translate, value?: string): string {
  if (!value) return t("web.admin.settings.compatibilityProxies.unknown");
  const key = WEB_STATE_LABELS[value];
  return key ? t(key) : statusLabel(value);
}

function operationTitle(t: Translate, kind?: string): string {
  return kind === "remove"
    ? t("web.admin.settings.compatibilityProxies.operation.removing")
    : t("web.admin.settings.compatibilityProxies.operation.installing");
}

function formatTimestamp(t: Translate, value?: string): string {
  if (!value) return t("web.admin.settings.compatibilityProxies.unknown");
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return formatDateTime(parsed);
}

function formatOperationPhase(t: Translate, value?: string): string {
  if (!value) return t("web.admin.settings.compatibilityProxies.operation.working");
  return statusLabel(value);
}

/**
 * Whether an address points back at the server itself (127.0.0.0/8,
 * localhost, ::1). Silo hands this address to Jellyfin apps, and from any
 * other device a loopback address is that device, not the server.
 */
function isLoopbackAddress(raw: string): boolean {
  const trimmed = raw.trim();
  if (!trimmed) return false;
  let hostname: string;
  try {
    hostname = new URL(trimmed.includes("://") ? trimmed : `http://${trimmed}`).hostname;
  } catch {
    return false;
  }
  hostname = hostname.toLowerCase().replace(/^\[|\]$/g, "");
  return (
    hostname === "localhost" ||
    hostname.endsWith(".localhost") ||
    hostname === "::1" ||
    /^127(\.\d{1,3}){3}$/.test(hostname)
  );
}

/** Marks a feature that ships outside the 1.0 support promise. */
function BetaChip() {
  const t = useT();
  return (
    <span className="border-border text-muted-foreground inline-flex shrink-0 items-center rounded-md border px-1.5 py-0.5 text-xs font-medium tracking-wide">
      {t("web.admin.settings.compatibilityProxies.beta")}
    </span>
  );
}

function clampProgressPercent(value?: number): number | null {
  if (typeof value !== "number" || !Number.isFinite(value)) return null;
  return Math.min(100, Math.max(0, Math.round(value)));
}

function StatusLine({
  label,
  value,
  mono = false,
}: {
  label: string;
  value?: string | boolean;
  mono?: boolean;
}) {
  const t = useT();
  return (
    <div className="flex min-h-9 items-center justify-between gap-4 py-2 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className={mono ? "max-w-[60%] truncate font-mono text-xs" : "text-end"}>
        {typeof value === "boolean"
          ? value
            ? t("web.admin.settings.compatibilityProxies.details.yes")
            : t("web.admin.settings.compatibilityProxies.details.no")
          : value || t("web.admin.settings.compatibilityProxies.details.notSet")}
      </span>
    </div>
  );
}

export default function CompatibilityProxiesSettings() {
  const t = useT();
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();
  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  const statusQuery = useJellyfinCompatStatus();
  const installWeb = useInstallJellyfinCompatWeb();
  const removeWeb = useRemoveJellyfinCompatWeb();
  const updateCompatSettings = useUpdateJellyfinCompatSettings();
  const status = statusQuery.data;

  if (form.isLoading || statusQuery.isLoading)
    return (
      <div
        className="space-y-6"
        role="status"
        aria-label={t("web.admin.settings.compatibilityProxies.loading")}
      >
        <Skeleton className="h-8 w-56" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">{t("web.admin.settings.compatibilityProxies.loading")}</span>
      </div>
    );

  const dirtyKeys: string[] = form.dirtyKeys ?? [];
  const isDirty = (key: string) => dirtyKeys.includes(key);
  const hasDirtyWebConfig = dirtyKeys.some((key) => WEB_INSTALL_KEYS.includes(key));
  const jellyfinAdvancedDirty = JELLYFIN_ADVANCED_KEYS.filter((key) => isDirty(key));
  const operationRunning =
    status?.operation?.state === "running" ||
    status?.web_state === "installing" ||
    status?.web_state === "removing";
  const missingPrerequisites = status?.prerequisites?.filter((item) => !item.available) ?? [];
  const jellyfinEnabledValue = form.getValue("jellyfin_compat.enabled");
  const jellyfinEnabledChecked =
    jellyfinEnabledValue === "" ? Boolean(status?.enabled) : jellyfinEnabledValue === "true";
  const jellyfinProxyRunning = Boolean(status?.enabled);
  const jellyfinWebServing = jellyfinProxyRunning && status?.web_enabled !== false;
  const installedWebAssetsPresent = Boolean(status?.installed_version);
  const pinnedJellyfinWebInstalled = hasPinnedJellyfinWebInstalled(status);
  const publicURL = form.getValue("jellyfin_compat.public_url");
  const serverID = form.getValue("jellyfin_compat.server_id");

  const setJellyfinAPIEnabled = (value: string) => {
    form.setValue("jellyfin_compat.enabled", value);
    if (value === "false") {
      form.setValue("jellyfin_compat.web_enabled", "false");
    }
  };
  const installJellyfinWeb = () => {
    const version = form.getValue("jellyfin_compat.web_version").trim();
    installWeb.mutate(version ? { version } : {});
  };

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.compatibilityProxies.title")} />

      <div className="flex-1 space-y-5">
        <FieldGroup label={JELLYFIN} restartAll={allRestart(JELLYFIN_KEYS)}>
          <SettingField
            label={t("web.admin.settings.compatibilityProxies.jellyfin.enable")}
            type="toggle"
            value={jellyfinEnabledChecked ? "true" : "false"}
            onChange={setJellyfinAPIEnabled}
            disabled={form.isSaving}
            restartRequired={restartKeys.has("jellyfin_compat.enabled")}
          />

          <SettingField
            label={t("web.admin.settings.compatibilityProxies.jellyfin.address")}
            hint={PUBLIC_URL_HINT}
            value={publicURL}
            onChange={(v) => form.setValue("jellyfin_compat.public_url", v)}
            status={
              jellyfinEnabledChecked && isLoopbackAddress(publicURL) ? (
                <SettingFieldStatus tone="warn">
                  {t("web.admin.settings.compatibilityProxies.jellyfin.loopbackWarning")}
                </SettingFieldStatus>
              ) : undefined
            }
            restartRequired={restartKeys.has("jellyfin_compat.public_url")}
          />

          {status?.last_error && (
            <div className="bg-destructive/10 text-destructive my-3 flex items-start gap-2 rounded-lg px-3 py-2 text-sm">
              <AlertCircle className="mt-0.5 h-4 w-4 flex-shrink-0" />
              <span>{status.last_error}</span>
            </div>
          )}

          <div className="space-y-4 py-3.5">
            <div>
              <h4 className="text-sm font-medium">
                {t("web.admin.settings.compatibilityProxies.web.heading")}
              </h4>
              <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
                {t("web.admin.settings.compatibilityProxies.web.description")}
              </p>
              <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1">
                <SettingFieldStatus tone={installedWebAssetsPresent ? "ok" : "muted"}>
                  {installedWebAssetsPresent
                    ? t("web.admin.settings.compatibilityProxies.web.versionInstalled", {
                        version: String(status?.installed_version),
                      })
                    : webStateLabel(t, status?.web_state)}
                </SettingFieldStatus>
                {jellyfinProxyRunning && installedWebAssetsPresent ? (
                  <SettingFieldStatus tone={jellyfinWebServing ? "ok" : "muted"}>
                    {jellyfinWebServing
                      ? t("web.admin.settings.compatibilityProxies.web.served")
                      : t("web.admin.settings.compatibilityProxies.web.notServed")}
                  </SettingFieldStatus>
                ) : null}
                {status?.installer_ready === false ? (
                  <SettingFieldStatus tone="warn">
                    {t("web.admin.settings.compatibilityProxies.web.installerMissingTools")}
                  </SettingFieldStatus>
                ) : null}
              </div>
            </div>

            {status?.operation?.state === "running" &&
              (() => {
                const progress = clampProgressPercent(status.operation.progress_percent);
                const phase = formatOperationPhase(t, status.operation.phase);
                const message =
                  status.operation.message ||
                  (status.operation.kind === "remove"
                    ? t("web.admin.settings.compatibilityProxies.operation.removeMessage")
                    : t("web.admin.settings.compatibilityProxies.operation.installMessage"));

                return (
                  <div className="border-border/70 bg-muted/30 flex items-start gap-3 rounded-lg border px-3 py-3 text-sm">
                    <Loader2 className="text-muted-foreground mt-0.5 h-4 w-4 flex-shrink-0 animate-spin" />
                    <div className="min-w-0 flex-1 space-y-2">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <p className="font-medium">{operationTitle(t, status.operation.kind)}</p>
                        {progress !== null && (
                          <span className="text-muted-foreground text-xs font-medium">
                            {t("web.admin.settings.compatibilityProxies.operation.percent", {
                              percent: String(progress),
                            })}
                          </span>
                        )}
                      </div>
                      <div className="space-y-1">
                        <p className="text-muted-foreground leading-relaxed">{message}</p>
                        <p className="text-muted-foreground text-xs">{phase}</p>
                      </div>
                      {progress !== null && (
                        <Progress
                          value={progress}
                          aria-label={t(
                            "web.admin.settings.compatibilityProxies.operation.progressLabel",
                          )}
                        />
                      )}
                      <p className="text-muted-foreground text-xs">
                        {t("web.admin.settings.compatibilityProxies.operation.started", {
                          time: formatTimestamp(t, status.operation.started_at),
                        })}
                      </p>
                    </div>
                  </div>
                );
              })()}

            <div className="flex flex-wrap items-center gap-2">
              {!pinnedJellyfinWebInstalled && (
                <Button
                  type="button"
                  size="sm"
                  onClick={installJellyfinWeb}
                  disabled={
                    hasDirtyWebConfig ||
                    installWeb.isPending ||
                    operationRunning ||
                    status?.installer_ready === false
                  }
                >
                  <Download className="me-2 h-4 w-4" />
                  {status?.web_state === "update_available"
                    ? t("web.admin.settings.compatibilityProxies.web.update")
                    : operationRunning
                      ? t("web.admin.settings.compatibilityProxies.web.busy")
                      : t("web.admin.settings.compatibilityProxies.web.install")}
                </Button>
              )}
              {installedWebAssetsPresent && (
                <Button
                  type="button"
                  size="sm"
                  variant={jellyfinWebServing ? "outline" : "default"}
                  onClick={() => updateCompatSettings.mutate({ web_enabled: !jellyfinWebServing })}
                  disabled={
                    !jellyfinProxyRunning || updateCompatSettings.isPending || operationRunning
                  }
                >
                  {jellyfinWebServing ? (
                    <PowerOff className="me-2 h-4 w-4" />
                  ) : (
                    <Power className="me-2 h-4 w-4" />
                  )}
                  {jellyfinWebServing
                    ? t("web.admin.settings.compatibilityProxies.web.disable")
                    : t("web.admin.settings.compatibilityProxies.web.enable")}
                </Button>
              )}
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => removeWeb.mutate()}
                disabled={
                  hasDirtyWebConfig ||
                  removeWeb.isPending ||
                  operationRunning ||
                  status?.web_state === "missing"
                }
              >
                <Trash2 className="me-2 h-4 w-4" />
                {t("web.admin.settings.compatibilityProxies.web.remove")}
              </Button>
              {hasDirtyWebConfig && (
                <span className="text-muted-foreground text-sm">
                  {t("web.admin.settings.compatibilityProxies.web.saveFirst")}
                </span>
              )}
              {missingPrerequisites.length > 0 && (
                <span className="text-muted-foreground text-sm">
                  {t("web.admin.settings.compatibilityProxies.web.missingCommands", {
                    commands: missingPrerequisites.map((item) => item.command).join(", "),
                  })}
                </span>
              )}
              {pinnedJellyfinWebInstalled && (
                <span className="text-muted-foreground inline-flex items-center gap-1 text-sm">
                  <CheckCircle2 className="h-4 w-4" />
                  {t("web.admin.settings.compatibilityProxies.web.pinnedInstalled")}
                </span>
              )}
              {status?.license_present && status?.provenance_present ? (
                <span className="text-muted-foreground inline-flex items-center gap-1 text-sm">
                  <CheckCircle2 className="h-4 w-4" />
                  {t("web.admin.settings.compatibilityProxies.web.licensePresent")}
                </span>
              ) : null}
            </div>

            <AdvancedSection
              id="compatibility.jellyfin.web-details"
              title={t("web.admin.settings.compatibilityProxies.details.title")}
            >
              <div className="grid gap-x-8 py-2 md:grid-cols-2">
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.apiState")}
                  value={status ? statusLabel(status.api_state) : ""}
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.listen")}
                  value={status?.listen}
                  mono
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.publicUrl")}
                  value={status?.public_url}
                  mono
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.reportedVersion")}
                  value={status?.emulated_server_version}
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.chosenVersion")}
                  value={status?.pinned_version}
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.currentJob")}
                  value={
                    status?.operation
                      ? `${statusLabel(status.operation.kind)} ${statusLabel(status.operation.state)}`
                      : t("web.admin.settings.compatibilityProxies.details.none")
                  }
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.downloadedFrom")}
                  value={status?.source_url}
                  mono
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.sourceCommit")}
                  value={status?.commit_sha}
                  mono
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.checksum")}
                  value={status?.checksum}
                  mono
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.installedAt")}
                  value={status?.install_path}
                  mono
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.licenseFile")}
                  value={status?.license_present}
                />
                <StatusLine
                  label={t("web.admin.settings.compatibilityProxies.details.downloadRecord")}
                  value={status?.provenance_present}
                />
              </div>
            </AdvancedSection>
          </div>

          <AdvancedSection
            id="compatibility.jellyfin"
            count={JELLYFIN_ADVANCED_KEYS.length}
            forceOpen={jellyfinAdvancedDirty.length > 0}
          >
            <SettingField
              label={t("web.admin.settings.compatibilityProxies.advanced.serverName")}
              // jellycompat reads only its own key, defaulting to "Silo"; the
              // server name on General never reaches Jellyfin apps.
              description={t(
                "web.admin.settings.compatibilityProxies.advanced.serverNameDescription",
              )}
              value={form.getValue("jellyfin_compat.server_name")}
              onChange={(v) => form.setValue("jellyfin_compat.server_name", v)}
              restartRequired={restartKeys.has("jellyfin_compat.server_name")}
            />
            <SettingFieldRow
              label={t("web.admin.settings.compatibilityProxies.advanced.serverId")}
              description={t(
                "web.admin.settings.compatibilityProxies.advanced.serverIdDescription",
              )}
            >
              <span className="text-muted-foreground font-mono text-xs break-all">
                {serverID || t("web.admin.settings.compatibilityProxies.advanced.serverIdPending")}
              </span>
            </SettingFieldRow>
            <SettingField
              label={t("web.admin.settings.compatibilityProxies.advanced.reportVersion")}
              description={t(
                "web.admin.settings.compatibilityProxies.advanced.reportVersionDescription",
              )}
              value={form.getValue("jellyfin_compat.emulated_server_version")}
              onChange={(v) => form.setValue("jellyfin_compat.emulated_server_version", v)}
              restartRequired={restartKeys.has("jellyfin_compat.emulated_server_version")}
            />
            <SettingField
              label={t("web.admin.settings.compatibilityProxies.advanced.sessionTtl")}
              type="duration"
              description={t(
                "web.admin.settings.compatibilityProxies.advanced.sessionTtlDescription",
              )}
              value={form.getValue("jellyfin_compat.session_ttl")}
              onChange={(v) => form.setValue("jellyfin_compat.session_ttl", v)}
              restartRequired={restartKeys.has("jellyfin_compat.session_ttl")}
            />
            <SettingField
              label={t("web.admin.settings.compatibilityProxies.advanced.playbackTtl")}
              type="duration"
              description={t(
                "web.admin.settings.compatibilityProxies.advanced.playbackTtlDescription",
              )}
              value={form.getValue("jellyfin_compat.playback_session_ttl")}
              onChange={(v) => form.setValue("jellyfin_compat.playback_session_ttl", v)}
              restartRequired={restartKeys.has("jellyfin_compat.playback_session_ttl")}
            />
            <SettingField
              label={t("web.admin.settings.compatibilityProxies.advanced.webVersion")}
              // The effective default (DefaultJellyfinWebVersion) refills an
              // emptied field, and Install sends what the field shows, so
              // "leave blank to match" could never happen.
              description={t(
                "web.admin.settings.compatibilityProxies.advanced.webVersionDescription",
              )}
              value={form.getValue("jellyfin_compat.web_version")}
              onChange={(v) => form.setValue("jellyfin_compat.web_version", v)}
              restartRequired={restartKeys.has("jellyfin_compat.web_version")}
            />
            <SettingField
              label={t("web.admin.settings.compatibilityProxies.advanced.webDir")}
              description={t("web.admin.settings.compatibilityProxies.advanced.webDirDescription")}
              value={form.getValue("jellyfin_compat.web_install_dir")}
              onChange={(v) => form.setValue("jellyfin_compat.web_install_dir", v)}
              restartRequired={restartKeys.has("jellyfin_compat.web_install_dir")}
            />
          </AdvancedSection>
        </FieldGroup>

        {/* Audiobookshelf compatibility ships as beta, outside the 1.0
            support promise (docs/architecture/v1-scope.md). */}
        <FieldGroup
          label={AUDIOBOOKSHELF}
          actions={<BetaChip />}
          restartAll={allRestart(AUDIOBOOKSHELF_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.compatibilityProxies.audiobookshelf.enable")}
            type="toggle"
            value={form.getValue("audiobookshelf_compat.enabled")}
            onChange={(v) => form.setValue("audiobookshelf_compat.enabled", v)}
            restartRequired={restartKeys.has("audiobookshelf_compat.enabled")}
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
