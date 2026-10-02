import { useMemo, useState } from "react";
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
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";

import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField, SettingFieldStatus } from "./SettingField";
import { formatDateTime } from "@/lib/datetime";

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

function statusLabel(value: string): string {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

// `web_state` is an internal enum; admins get plain wording instead.
const WEB_STATE_LABELS: Record<string, string> = {
  missing: "Not installed",
  installed: "Installed",
  update_available: "Update available",
  installing: "Installing",
  removing: "Removing",
  failed: "Install failed",
};

function webStateLabel(value?: string): string {
  if (!value) return "Unknown";
  return WEB_STATE_LABELS[value] ?? statusLabel(value);
}

function operationTitle(kind?: string): string {
  return kind === "remove" ? "Removing Jellyfin Web UI" : "Installing Jellyfin Web UI";
}

function formatTimestamp(value?: string): string {
  if (!value) return "Unknown";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return formatDateTime(parsed);
}

function formatOperationPhase(value?: string): string {
  if (!value) return "Working";
  return statusLabel(value);
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
  return (
    <div className="flex min-h-9 items-center justify-between gap-4 py-2 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className={mono ? "max-w-[60%] truncate font-mono text-xs" : "text-right"}>
        {typeof value === "boolean" ? (value ? "Yes" : "No") : value || "Not set"}
      </span>
    </div>
  );
}

export default function CompatibilityProxiesSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();
  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  const statusQuery = useJellyfinCompatStatus();
  const installWeb = useInstallJellyfinCompatWeb();
  const removeWeb = useRemoveJellyfinCompatWeb();
  const updateCompatSettings = useUpdateJellyfinCompatSettings();
  const status = statusQuery.data;
  const [showDiagnostics, setShowDiagnostics] = useState(false);

  if (form.isLoading || statusQuery.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
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
        <span className="sr-only">Loading settings</span>
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
      <SettingsPageHeader title="Compatibility" className="mb-8" />

      <div className="flex-1 space-y-5">
        <FieldGroup label="Jellyfin" restartAll={allRestart(JELLYFIN_KEYS)}>
          <SettingField
            label="Allow Jellyfin apps to connect"
            type="toggle"
            value={jellyfinEnabledChecked ? "true" : "false"}
            onChange={setJellyfinAPIEnabled}
            disabled={form.isSaving}
            restartRequired={restartKeys.has("jellyfin_compat.enabled")}
          />

          <SettingField
            label="Address Jellyfin apps should use"
            hint="https://media.example.com"
            value={form.getValue("jellyfin_compat.public_url")}
            onChange={(v) => form.setValue("jellyfin_compat.public_url", v)}
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
              <h4 className="text-sm font-medium">Jellyfin web player</h4>
              <p className="text-muted-foreground mt-1 text-xs leading-relaxed">
                Jellyfin mobile and TV apps expect to find it on the server.
              </p>
              <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1">
                <SettingFieldStatus tone={installedWebAssetsPresent ? "ok" : "muted"}>
                  {installedWebAssetsPresent
                    ? `Version ${status?.installed_version} installed`
                    : webStateLabel(status?.web_state)}
                </SettingFieldStatus>
                {jellyfinProxyRunning && installedWebAssetsPresent ? (
                  <SettingFieldStatus tone={jellyfinWebServing ? "ok" : "muted"}>
                    {jellyfinWebServing ? "Served to clients" : "Not served to clients"}
                  </SettingFieldStatus>
                ) : null}
                {status?.installer_ready === false ? (
                  <SettingFieldStatus tone="warn">
                    Downloader is missing required tools
                  </SettingFieldStatus>
                ) : null}
              </div>
            </div>

            {status?.operation?.state === "running" &&
              (() => {
                const progress = clampProgressPercent(status.operation.progress_percent);
                const phase = formatOperationPhase(status.operation.phase);
                const message =
                  status.operation.message ||
                  (status.operation.kind === "remove"
                    ? "Removing the downloaded Jellyfin web player"
                    : "Downloading the Jellyfin web player and building it");

                return (
                  <div className="border-border/70 bg-muted/30 flex items-start gap-3 rounded-lg border px-3 py-3 text-sm">
                    <Loader2 className="text-muted-foreground mt-0.5 h-4 w-4 flex-shrink-0 animate-spin" />
                    <div className="min-w-0 flex-1 space-y-2">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <p className="font-medium">{operationTitle(status.operation.kind)}</p>
                        {progress !== null && (
                          <span className="text-muted-foreground text-xs font-medium">
                            {progress}%
                          </span>
                        )}
                      </div>
                      <div className="space-y-1">
                        <p className="text-muted-foreground leading-relaxed">{message}</p>
                        <p className="text-muted-foreground text-xs">{phase}</p>
                      </div>
                      {progress !== null && (
                        <Progress value={progress} aria-label="Jellyfin Web install progress" />
                      )}
                      <p className="text-muted-foreground text-xs">
                        Started {formatTimestamp(status.operation.started_at)}
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
                  <Download className="mr-2 h-4 w-4" />
                  {status?.web_state === "update_available"
                    ? "Update Web UI"
                    : operationRunning
                      ? "Web UI Busy"
                      : "Install Web UI"}
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
                    <PowerOff className="mr-2 h-4 w-4" />
                  ) : (
                    <Power className="mr-2 h-4 w-4" />
                  )}
                  {jellyfinWebServing ? "Disable Web UI" : "Enable Web UI"}
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
                <Trash2 className="mr-2 h-4 w-4" />
                Remove Web UI
              </Button>
              {hasDirtyWebConfig && (
                <span className="text-muted-foreground text-sm">
                  Save your changes before installing or removing the web player.
                </span>
              )}
              {missingPrerequisites.length > 0 && (
                <span className="text-muted-foreground text-sm">
                  Silo cannot download it until these commands are installed on the server:{" "}
                  {missingPrerequisites.map((item) => item.command).join(", ")}
                </span>
              )}
              {pinnedJellyfinWebInstalled && (
                <span className="text-muted-foreground inline-flex items-center gap-1 text-sm">
                  <CheckCircle2 className="h-4 w-4" />
                  The chosen version is installed
                </span>
              )}
              {status?.license_present && status?.provenance_present ? (
                <span className="text-muted-foreground inline-flex items-center gap-1 text-sm">
                  <CheckCircle2 className="h-4 w-4" />
                  License and download record present
                </span>
              ) : null}
            </div>

            <div>
              <Button
                type="button"
                size="xs"
                variant="ghost"
                aria-expanded={showDiagnostics}
                onClick={() => setShowDiagnostics((current) => !current)}
              >
                {showDiagnostics ? "Hide download details" : "Show download details"}
              </Button>
              {showDiagnostics && (
                <div className="grid gap-x-8 pt-2 md:grid-cols-2">
                  <StatusLine
                    label="API state"
                    value={status ? statusLabel(status.api_state) : ""}
                  />
                  <StatusLine label="Listen address" value={status?.listen} mono />
                  <StatusLine label="Public URL in use" value={status?.public_url} mono />
                  <StatusLine
                    label="Jellyfin version reported"
                    value={status?.emulated_server_version}
                  />
                  <StatusLine label="Version chosen" value={status?.pinned_version} />
                  <StatusLine
                    label="Current job"
                    value={
                      status?.operation
                        ? `${statusLabel(status.operation.kind)} ${statusLabel(status.operation.state)}`
                        : "None"
                    }
                  />
                  <StatusLine label="Downloaded from" value={status?.source_url} mono />
                  <StatusLine label="Source commit" value={status?.commit_sha} mono />
                  <StatusLine label="Checksum" value={status?.checksum} mono />
                  <StatusLine label="Installed at" value={status?.install_path} mono />
                  <StatusLine label="License file present" value={status?.license_present} />
                  <StatusLine label="Download record present" value={status?.provenance_present} />
                </div>
              )}
            </div>
          </div>

          <AdvancedSection
            id="compatibility.jellyfin"
            count={JELLYFIN_ADVANCED_KEYS.length}
            forceOpen={jellyfinAdvancedDirty.length > 0}
          >
            <SettingField
              label="Name shown to Jellyfin apps"
              description="Defaults to your Silo server name."
              value={form.getValue("jellyfin_compat.server_name")}
              onChange={(v) => form.setValue("jellyfin_compat.server_name", v)}
              restartRequired={restartKeys.has("jellyfin_compat.server_name")}
            />
            <SettingField
              label="Server ID"
              description="Changing it makes saved clients treat Silo as a new server."
              value={form.getValue("jellyfin_compat.server_id")}
              onChange={(v) => form.setValue("jellyfin_compat.server_id", v)}
              restartRequired={restartKeys.has("jellyfin_compat.server_id")}
            />
            <SettingField
              label="Jellyfin version to report"
              description="Leave as is unless an app refuses to connect."
              value={form.getValue("jellyfin_compat.emulated_server_version")}
              onChange={(v) => form.setValue("jellyfin_compat.emulated_server_version", v)}
              restartRequired={restartKeys.has("jellyfin_compat.emulated_server_version")}
            />
            <SettingField
              label="Stay signed in for"
              type="duration"
              description="For example 24h."
              value={form.getValue("jellyfin_compat.session_ttl")}
              onChange={(v) => form.setValue("jellyfin_compat.session_ttl", v)}
              restartRequired={restartKeys.has("jellyfin_compat.session_ttl")}
            />
            <SettingField
              label="Forget idle playback after"
              type="duration"
              description="For example 6h."
              value={form.getValue("jellyfin_compat.playback_session_ttl")}
              onChange={(v) => form.setValue("jellyfin_compat.playback_session_ttl", v)}
              restartRequired={restartKeys.has("jellyfin_compat.playback_session_ttl")}
            />
            <SettingField
              label="Web player version to install"
              description="Leave blank to match the reported Jellyfin version."
              value={form.getValue("jellyfin_compat.web_version")}
              onChange={(v) => form.setValue("jellyfin_compat.web_version", v)}
              restartRequired={restartKeys.has("jellyfin_compat.web_version")}
            />
            <SettingField
              label="Web player install folder"
              description="Leave blank to use the folder Silo manages."
              value={form.getValue("jellyfin_compat.web_install_dir")}
              onChange={(v) => form.setValue("jellyfin_compat.web_install_dir", v)}
              restartRequired={restartKeys.has("jellyfin_compat.web_install_dir")}
            />
          </AdvancedSection>
        </FieldGroup>

        <FieldGroup label="Audiobookshelf" restartAll={allRestart(AUDIOBOOKSHELF_KEYS)}>
          <SettingField
            label="Allow Audiobookshelf apps to connect"
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
