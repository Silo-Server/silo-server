import { useEffect, useMemo } from "react";
import { AlertCircle, CheckCircle2, Download, Trash2 } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  useInstallJellyfinCompatWeb,
  useJellyfinCompatStatus,
  useRemoveJellyfinCompatWeb,
  useUpdateJellyfinCompatSettings,
} from "@/hooks/queries/admin/settings";
import { markAdminRestartRequired } from "@/hooks/useAdminRestartRequired";
import { useSettingsForm } from "@/hooks/useSettingsForm";

import { FieldGroup } from "./FieldGroup";
import { SaveBar } from "./SaveBar";
import { SettingField } from "./SettingField";

const JELLYFIN_KEYS = [
  "jellyfin_compat.enabled",
  "jellyfin_compat.public_url",
  "jellyfin_compat.server_name",
  "jellyfin_compat.server_id",
  "jellyfin_compat.emulated_server_version",
  "jellyfin_compat.web_version",
  "jellyfin_compat.web_dir",
  "jellyfin_compat.web_install_dir",
  "jellyfin_compat.session_ttl",
  "jellyfin_compat.playback_session_ttl",
];

const AUDIOBOOKSHELF_KEYS = ["audiobookshelf_compat.enabled"];

const KEYS = [...JELLYFIN_KEYS, ...AUDIOBOOKSHELF_KEYS];

function statusLabel(value: string): string {
  return value
    .split("_")
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
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
  const statusQuery = useJellyfinCompatStatus();
  const updateCompat = useUpdateJellyfinCompatSettings();
  const installWeb = useInstallJellyfinCompatWeb();
  const removeWeb = useRemoveJellyfinCompatWeb();
  const status = statusQuery.data;

  useEffect(() => {
    if (status?.restart_required) {
      markAdminRestartRequired();
    }
  }, [status?.restart_required]);

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

  const hasDirtyWebConfig = form.dirtyKeys.some((key) =>
    [
      "jellyfin_compat.web_version",
      "jellyfin_compat.web_dir",
      "jellyfin_compat.web_install_dir",
    ].includes(key),
  );
  const operationRunning =
    status?.operation?.state === "running" ||
    status?.web_state === "installing" ||
    status?.web_state === "removing";
  const missingPrerequisites = status?.prerequisites?.filter((item) => !item.available) ?? [];

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Compatibility Proxies</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure protocol-compatible listener surfaces for external client apps.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Jellyfin Status">
          <div className="py-3">
            <div className="flex flex-col gap-4 sm:flex-row sm:items-start sm:justify-between">
              <div className="space-y-2">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant={status?.enabled ? "default" : "outline"}>
                    {status?.enabled ? "API enabled" : "API disabled"}
                  </Badge>
                  <Badge
                    variant={
                      status?.web_state === "installed" || status?.web_state === "update_available"
                        ? "secondary"
                        : status?.web_state === "failed"
                          ? "destructive"
                          : "outline"
                    }
                  >
                    Web UI {status ? statusLabel(status.web_state) : "Unknown"}
                  </Badge>
                  {status?.operation?.state === "running" && (
                    <Badge variant="secondary">{statusLabel(status.operation.kind)} running</Badge>
                  )}
                  {status?.restart_required && <Badge variant="outline">Restart required</Badge>}
                </div>
                <p className="text-muted-foreground max-w-3xl text-sm leading-relaxed">
                  Jellyfin-compatible API support is optional. Silo is not affiliated with or
                  endorsed by the Jellyfin project.
                </p>
              </div>

              <div className="flex items-center gap-2">
                <Switch
                  id="jellyfin-compat-enabled"
                  checked={status?.enabled ?? form.getValue("jellyfin_compat.enabled") === "true"}
                  disabled={updateCompat.isPending}
                  onCheckedChange={(enabled) => updateCompat.mutate({ enabled })}
                />
                <Label htmlFor="jellyfin-compat-enabled" className="text-sm">
                  Enabled
                </Label>
              </div>
            </div>

            {status?.last_error && (
              <div className="bg-destructive/10 text-destructive mt-4 flex items-start gap-2 rounded-lg px-3 py-2 text-sm">
                <AlertCircle className="mt-0.5 h-4 w-4 flex-shrink-0" />
                <span>{status.last_error}</span>
              </div>
            )}
          </div>

          <div className="grid gap-x-8 py-3 md:grid-cols-2">
            <StatusLine label="API state" value={status ? statusLabel(status.api_state) : ""} />
            <StatusLine label="Listen address" value={status?.listen} mono />
            <StatusLine label="Public URL" value={status?.public_url} mono />
            <StatusLine label="Emulated version" value={status?.emulated_server_version} />
          </div>
        </FieldGroup>

        <FieldGroup label="Jellyfin Web Component">
          <div className="space-y-4 py-3">
            <div className="grid gap-x-8 md:grid-cols-2">
              <StatusLine label="Pinned version" value={status?.pinned_version} />
              <StatusLine label="Installed version" value={status?.installed_version} />
              <StatusLine
                label="Installer"
                value={status?.installer_ready ? "Ready" : "Missing prerequisites"}
              />
              <StatusLine
                label="Operation"
                value={
                  status?.operation
                    ? `${statusLabel(status.operation.kind)} ${statusLabel(status.operation.state)}`
                    : "Idle"
                }
              />
              <StatusLine label="Source" value={status?.source_url} mono />
              <StatusLine label="Commit" value={status?.commit_sha} mono />
              <StatusLine label="Checksum" value={status?.checksum} mono />
              <StatusLine label="Install path" value={status?.install_path} mono />
              <StatusLine label="License present" value={status?.license_present} />
              <StatusLine label="Provenance present" value={status?.provenance_present} />
            </div>

            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                size="sm"
                onClick={() =>
                  installWeb.mutate({ version: form.getValue("jellyfin_compat.web_version") })
                }
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
                  Save Web settings before installing or removing assets.
                </span>
              )}
              {missingPrerequisites.length > 0 && (
                <span className="text-muted-foreground text-sm">
                  Missing installer prerequisites:{" "}
                  {missingPrerequisites.map((item) => item.command).join(", ")}
                </span>
              )}
              {(status?.license_present && status?.provenance_present && (
                <span className="text-muted-foreground inline-flex items-center gap-1 text-sm">
                  <CheckCircle2 className="h-4 w-4" />
                  License and provenance files found
                </span>
              )) ||
                null}
            </div>
          </div>

          <SettingField
            label="Pinned Web Version"
            value={form.getValue("jellyfin_compat.web_version")}
            onChange={(v) => form.setValue("jellyfin_compat.web_version", v)}
          />
          <SettingField
            label="Web Install Directory"
            value={form.getValue("jellyfin_compat.web_install_dir")}
            onChange={(v) => form.setValue("jellyfin_compat.web_install_dir", v)}
          />
          <SettingField
            label="Active Web Directory"
            value={form.getValue("jellyfin_compat.web_dir")}
            onChange={(v) => form.setValue("jellyfin_compat.web_dir", v)}
          />
        </FieldGroup>

        <FieldGroup label="Jellyfin Server Identity">
          <SettingField
            label="Public URL"
            value={form.getValue("jellyfin_compat.public_url")}
            onChange={(v) => form.setValue("jellyfin_compat.public_url", v)}
          />
          <SettingField
            label="Server Name"
            value={form.getValue("jellyfin_compat.server_name")}
            onChange={(v) => form.setValue("jellyfin_compat.server_name", v)}
          />
          <SettingField
            label="Server ID"
            value={form.getValue("jellyfin_compat.server_id")}
            onChange={(v) => form.setValue("jellyfin_compat.server_id", v)}
          />
          <SettingField
            label="Emulated Server Version"
            value={form.getValue("jellyfin_compat.emulated_server_version")}
            onChange={(v) => form.setValue("jellyfin_compat.emulated_server_version", v)}
          />
          <SettingField
            label="Session TTL"
            type="duration"
            hint="e.g. 24h"
            value={form.getValue("jellyfin_compat.session_ttl")}
            onChange={(v) => form.setValue("jellyfin_compat.session_ttl", v)}
          />
          <SettingField
            label="Playback Session TTL"
            type="duration"
            hint="e.g. 6h"
            value={form.getValue("jellyfin_compat.playback_session_ttl")}
            onChange={(v) => form.setValue("jellyfin_compat.playback_session_ttl", v)}
          />
        </FieldGroup>

        <FieldGroup label="Audiobookshelf">
          <SettingField
            label="Enable Audiobookshelf Proxy"
            type="toggle"
            hint="Starts the ABS-compatible API listener for external Audiobookshelf clients."
            value={form.getValue("audiobookshelf_compat.enabled")}
            onChange={(v) => form.setValue("audiobookshelf_compat.enabled", v)}
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
