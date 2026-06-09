import { useId, useMemo, useState } from "react";
import { AlertCircle, CheckCircle2, ChevronRight, Download, Loader2, Trash2 } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  useInstallJellyfinCompatWeb,
  useJellyfinCompatStatus,
  useRemoveJellyfinCompatWeb,
} from "@/hooks/queries/admin/settings";
import { hasPinnedJellyfinWebInstalled } from "@/lib/jellyfinCompat";
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

function operationTitle(kind?: string): string {
  return kind === "remove" ? "Removing Jellyfin Web UI" : "Installing Jellyfin Web UI";
}

function formatTimestamp(value?: string): string {
  if (!value) return "Unknown";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return parsed.toLocaleString();
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

function LayerDescription({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="border-border/60 bg-muted/30 rounded-lg border px-3 py-2">
      <p className="text-sm font-medium">{title}</p>
      <p className="text-muted-foreground mt-1 text-xs leading-relaxed">{children}</p>
    </div>
  );
}

function CollapsibleFieldGroup({
  label,
  expanded,
  onToggle,
  children,
}: {
  label: string;
  expanded: boolean;
  onToggle: () => void;
  children: React.ReactNode;
}) {
  const labelId = useId();
  const contentId = useId();

  return (
    <div
      role="group"
      aria-labelledby={labelId}
      className="surface-panel rounded-2xl border-0 p-4 sm:p-5"
    >
      <button
        type="button"
        aria-expanded={expanded}
        aria-controls={contentId}
        onClick={onToggle}
        className={`text-muted-foreground hover:text-foreground flex w-full items-center gap-2 text-left transition-colors ${expanded ? "mb-3" : ""}`}
      >
        <ChevronRight
          className={`h-3.5 w-3.5 shrink-0 transition-transform duration-200 ${expanded ? "rotate-90" : ""}`}
        />
        <span id={labelId} className="text-xs font-semibold tracking-[0.22em] uppercase">
          {label}
        </span>
      </button>

      {expanded && (
        <div id={contentId} className="divide-border divide-y">
          {children}
        </div>
      )}
    </div>
  );
}

export default function CompatibilityProxiesSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const statusQuery = useJellyfinCompatStatus();
  const installWeb = useInstallJellyfinCompatWeb();
  const removeWeb = useRemoveJellyfinCompatWeb();
  const status = statusQuery.data;
  const [jellyfinExpanded, setJellyfinExpanded] = useState(false);

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
    ["jellyfin_compat.web_version", "jellyfin_compat.web_install_dir"].includes(key),
  );
  const operationRunning =
    status?.operation?.state === "running" ||
    status?.web_state === "installing" ||
    status?.web_state === "removing";
  const missingPrerequisites = status?.prerequisites?.filter((item) => !item.available) ?? [];
  const jellyfinEnabledValue = form.getValue("jellyfin_compat.enabled");
  const jellyfinEnabledChecked =
    jellyfinEnabledValue === "" ? Boolean(status?.enabled) : jellyfinEnabledValue === "true";
  const jellyfinEnabledDirty = form.dirtyKeys.includes("jellyfin_compat.enabled");
  const pinnedJellyfinWebInstalled = hasPinnedJellyfinWebInstalled(status);

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Compatibility Proxies</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure protocol-compatible listener surfaces for external client apps.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <CollapsibleFieldGroup
          label="Jellyfin"
          expanded={jellyfinExpanded}
          onToggle={() => setJellyfinExpanded((current) => !current)}
        >
          <div className="space-y-4 py-3">
            <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-center">
              <div className="space-y-0.5">
                <Label htmlFor="jellyfin-compat-enabled" className="text-sm font-medium">
                  Enable Jellyfin Proxy
                </Label>
                <p className="text-muted-foreground text-xs">
                  Starts the Jellyfin-compatible API listener for external Jellyfin clients.
                </p>
              </div>
              <Switch
                id="jellyfin-compat-enabled"
                checked={jellyfinEnabledChecked}
                disabled={form.isSaving}
                onCheckedChange={(enabled) =>
                  form.setValue("jellyfin_compat.enabled", enabled ? "true" : "false")
                }
              />
            </div>

            <div className="grid gap-3 md:grid-cols-2">
              <LayerDescription title="API Layer">
                Provides the Jellyfin-compatible API surface used by most third-party apps for
                discovery, authentication, browsing, metadata, and playback.
              </LayerDescription>
              <LayerDescription title="Web Component Layer">
                Provides the Jellyfin Web UI assets required by Jellyfin native apps and some other
                clients that expect Jellyfin Web to exist at the server's web route.
              </LayerDescription>
            </div>

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
              {jellyfinEnabledDirty && <Badge variant="outline">Enablement pending save</Badge>}
              {status?.restart_required && <Badge variant="outline">Restart required</Badge>}
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

          <div className="space-y-4 py-3">
            <h3 className="text-sm font-medium">Web Component</h3>
            <p className="text-muted-foreground text-sm leading-relaxed">
              The Web Component is separate from the API layer. Leave the pinned version and install
              directory blank to use Silo's managed defaults.
            </p>

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

            {status?.operation?.state === "running" && (
              <div className="border-border/70 bg-muted/30 flex items-start gap-3 rounded-lg border px-3 py-3 text-sm">
                <Loader2 className="text-muted-foreground mt-0.5 h-4 w-4 flex-shrink-0 animate-spin" />
                <div className="space-y-1">
                  <p className="font-medium">{operationTitle(status.operation.kind)}</p>
                  <p className="text-muted-foreground leading-relaxed">
                    Silo is{" "}
                    {status.operation.kind === "remove"
                      ? "removing managed Jellyfin Web assets"
                      : "downloading Jellyfin Web, installing dependencies, and building production assets"}
                    . This can take several minutes; status refreshes automatically while the
                    operation is running.
                  </p>
                  <p className="text-muted-foreground text-xs">
                    Started {formatTimestamp(status.operation.started_at)}
                  </p>
                </div>
              </div>
            )}

            <div className="flex flex-wrap items-center gap-2">
              {!pinnedJellyfinWebInstalled && (
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
                  Save Web settings before installing or removing assets.
                </span>
              )}
              {missingPrerequisites.length > 0 && (
                <span className="text-muted-foreground text-sm">
                  Missing installer prerequisites:{" "}
                  {missingPrerequisites.map((item) => item.command).join(", ")}
                </span>
              )}
              {pinnedJellyfinWebInstalled && (
                <span className="text-muted-foreground inline-flex items-center gap-1 text-sm">
                  <CheckCircle2 className="h-4 w-4" />
                  Pinned Web UI version installed
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

            <div className="divide-border divide-y">
              <SettingField
                label="Pinned Web Version (Optional)"
                hint="Optional. Defaults to Silo's pinned Jellyfin Web version."
                value={form.getValue("jellyfin_compat.web_version")}
                onChange={(v) => form.setValue("jellyfin_compat.web_version", v)}
              />
              <SettingField
                label="Web Install Directory (Optional)"
                hint="Optional. Defaults to Silo's managed Jellyfin Web install directory."
                value={form.getValue("jellyfin_compat.web_install_dir")}
                onChange={(v) => form.setValue("jellyfin_compat.web_install_dir", v)}
              />
            </div>
          </div>

          <div className="space-y-4 py-3">
            <h3 className="text-sm font-medium">Server Identity</h3>

            <div className="divide-border divide-y">
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
            </div>
          </div>
        </CollapsibleFieldGroup>

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
