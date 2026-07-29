import { useEffect, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { useSearchParams } from "react-router";
import {
  Blocks,
  CircleDot,
  Download,
  ExternalLink,
  Loader2,
  Package,
  Plus,
  Search,
  Settings2,
  Shield,
  Trash2,
  Upload,
  X,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { TablePagination } from "@/components/ui/pagination";
import { Progress } from "@/components/ui/progress";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import {
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { PluginConfigForm } from "@/components/admin/plugins/PluginConfigForm";
import type {
  PluginCatalogEntry,
  PluginCatalogSettings,
  PluginInstallation,
  PluginPresentation,
} from "@/api/types";
import { useQueryClient } from "@tanstack/react-query";
import {
  CHECK_PLUGIN_UPDATES_TASK_KEY,
  useAdminPlugins,
  useApplyPluginUpdate,
  useCheckPluginUpdates,
  useCreatePluginRepository,
  useDeletePluginInstallation,
  useDeletePluginRepository,
  useInstallPlugin,
  usePluginUpload,
  useSavePluginAuthBinding,
  useSavePluginConfig,
  useSavePluginTaskBinding,
  useTestPluginConfig,
  useUpdatePluginInstallation,
  useUpdatePluginCatalogSettings,
  useUpdatePluginRepository,
} from "@/hooks/queries/admin/plugins";
import { useTask } from "@/hooks/queries/admin/tasks";
import { adminKeys } from "@/hooks/queries/keys";
import { pluginRouteHref } from "@/lib/pluginRouteHref";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";

const INSTALLED_PAGE_SIZE = 10;
const CATALOG_PAGE_SIZE = 12;

function capabilityLabel(type: string): string {
  const labels: Record<string, string> = {
    "metadata_provider.v1": "Metadata",
    "auth_provider.v1": "Auth",
    "scheduled_task.v1": "Task",
    "media_analyzer.v1": "Analyzer",
  };
  return labels[type] ?? type.split(".")[0] ?? type;
}

function sourceLabel(sourceKind: string): string {
  switch (sourceKind) {
    case "silo":
      return "Silo maintained";
    case "approved_community":
      return "Approved community";
    default:
      return "External source";
  }
}

function pluginDisplayName(pluginID: string, presentation?: PluginPresentation): string {
  const displayName = presentation?.display_name.trim();
  if (displayName) return displayName;

  return pluginID
    .replace(/^silo[._-]?/, "")
    .split(/[._-]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}

function pluginSummary(
  presentation: PluginPresentation | undefined,
  capabilities: PluginInstallation["capabilities"],
): string {
  const summary = presentation?.summary.trim();
  if (summary) return summary;

  return (
    capabilities.find((capability) => capability.description?.trim())?.description?.trim() ??
    "No description provided."
  );
}

function safeExternalURL(rawURL?: string): string | undefined {
  if (!rawURL) return undefined;
  try {
    const url = new URL(rawURL);
    return url.protocol === "http:" || url.protocol === "https:" ? url.toString() : undefined;
  } catch {
    return undefined;
  }
}

function PluginResourceLinks({
  presentation,
  repoURL,
}: {
  presentation?: PluginPresentation;
  repoURL?: string;
}) {
  const links = [
    { label: "Source", url: safeExternalURL(presentation?.source_url || repoURL) },
    { label: "Changelog", url: safeExternalURL(presentation?.changelog_url) },
    { label: "Support", url: safeExternalURL(presentation?.support_url) },
  ].filter((link): link is { label: string; url: string } => Boolean(link.url));

  if (links.length === 0) return null;

  return (
    <div className="text-muted-foreground flex flex-wrap gap-x-3 gap-y-1 text-xs">
      {links.map((link) => (
        <a
          key={link.label}
          href={link.url}
          target="_blank"
          rel="noopener noreferrer"
          className="hover:text-foreground inline-flex items-center gap-1 transition-colors"
        >
          {link.label}
          <ExternalLink className="h-3 w-3" aria-hidden />
        </a>
      ))}
    </div>
  );
}

function parsePluginPage(rawPage: string | null): number {
  const page = Number.parseInt(rawPage ?? "1", 10);
  return Number.isFinite(page) && page > 0 ? page - 1 : 0;
}

function pluginMatchesSearch({
  query,
  pluginID,
  presentation,
  capabilities,
  sourceKind,
  repositoryName,
}: {
  query: string;
  pluginID: string;
  presentation?: PluginPresentation;
  capabilities: PluginInstallation["capabilities"];
  sourceKind: string;
  repositoryName?: string;
}): boolean {
  const normalizedQuery = query.trim().toLocaleLowerCase();
  if (!normalizedQuery) return true;

  const searchableText = [
    pluginID,
    pluginDisplayName(pluginID, presentation),
    presentation?.summary,
    presentation?.description_markdown,
    presentation?.publisher_name,
    repositoryName,
    sourceLabel(sourceKind),
    ...capabilities.flatMap((capability) => [
      capability.display_name,
      capability.description,
      capability.type,
    ]),
  ]
    .filter(Boolean)
    .join("\n")
    .toLocaleLowerCase();

  return searchableText.includes(normalizedQuery);
}

function PluginListToolbar({
  query,
  total,
  matchingTotal,
  placeholder,
  onQueryChange,
}: {
  query: string;
  total: number;
  matchingTotal: number;
  placeholder: string;
  onQueryChange: (query: string) => void;
}) {
  return (
    <div className="flex flex-col gap-2 border-b pb-3 sm:flex-row sm:items-center sm:justify-between">
      <div className="relative w-full sm:max-w-sm">
        <Search
          className="text-muted-foreground pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2"
          aria-hidden
        />
        <Input
          type="search"
          value={query}
          onChange={(event) => onQueryChange(event.target.value)}
          placeholder={placeholder}
          aria-label={placeholder}
          className="pr-9 pl-9"
        />
        {query ? (
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={() => onQueryChange("")}
            aria-label="Clear plugin search"
            className="absolute top-1/2 right-1 -translate-y-1/2"
          >
            <X className="h-3.5 w-3.5" />
          </Button>
        ) : null}
      </div>
      <span className="text-muted-foreground shrink-0 text-xs tabular-nums" aria-live="polite">
        {query.trim() ? `${matchingTotal} of ${total} plugins` : `${total} plugins`}
      </span>
    </div>
  );
}

/* ─── Installed plugin card ─────────────────────────────────────── */

function InstalledPluginCard({
  installation,
  catalogEntry,
  onConfigure,
}: {
  installation: PluginInstallation;
  catalogEntry?: PluginCatalogEntry;
  onConfigure: (installation: PluginInstallation) => void;
}) {
  const updateInstallation = useUpdatePluginInstallation();
  const deleteInstallation = useDeletePluginInstallation();
  const applyUpdate = useApplyPluginUpdate();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const capabilities = installation.capabilities ?? [];
  const presentation = installation.presentation ?? catalogEntry?.presentation;
  const repoURL = installation.repo_url || catalogEntry?.repo_url;
  const routes = installation.routes ?? [];
  const adminRoutes = routes.filter(
    (route) => route.navigable && route.navigation_kind === "admin",
  );

  return (
    <>
      <div className="surface-panel-subtle group relative overflow-hidden rounded-xl transition-all">
        <div className="flex flex-col gap-4 p-5 sm:flex-row sm:items-start sm:justify-between">
          {/* Left: icon + info */}
          <div className="flex items-start gap-4">
            <div
              className={`flex h-11 w-11 shrink-0 items-center justify-center rounded-xl text-sm font-bold ${
                installation.enabled
                  ? "bg-primary/15 text-primary"
                  : "bg-muted text-muted-foreground"
              }`}
            >
              <Blocks className="h-5 w-5" />
            </div>
            <div className="space-y-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <h3 className="text-[15px] leading-tight font-semibold">
                  {pluginDisplayName(installation.plugin_id, presentation)}
                </h3>
                <Badge variant="secondary" className="font-mono text-[11px]">
                  {installation.version}
                </Badge>
                <Badge variant="outline" className="text-[11px]">
                  {sourceLabel(installation.source_kind)}
                </Badge>
                {installation.available_version && (
                  <Badge
                    variant="outline"
                    className="border-amber-500/40 text-[11px] text-amber-500"
                  >
                    {installation.version} &rarr; {installation.available_version} available
                  </Badge>
                )}
                {installation.updates_paused ? (
                  <Badge variant="outline" className="text-muted-foreground text-[11px]">
                    Updates paused
                  </Badge>
                ) : null}
                {installation.available_version && (
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-6 px-2 text-[11px]"
                    disabled={applyUpdate.isPending}
                    onClick={(e) => {
                      e.stopPropagation();
                      applyUpdate.mutate(installation.id);
                    }}
                  >
                    {applyUpdate.isPending ? (
                      <>
                        <Loader2 className="mr-1 h-3 w-3 animate-spin" />
                        Updating...
                      </>
                    ) : (
                      "Update"
                    )}
                  </Button>
                )}
                <span className="flex items-center gap-1.5">
                  <span
                    className={`inline-block h-2 w-2 rounded-full ${installation.enabled ? "bg-success" : "bg-muted-foreground"}`}
                  />
                  <span className="text-muted-foreground text-[11px] font-medium">
                    {installation.enabled ? "Active" : "Inactive"}
                  </span>
                </span>
              </div>
              <p className="text-muted-foreground font-mono text-[11px]">
                {installation.plugin_id}
              </p>
              <p className="text-muted-foreground max-w-3xl text-xs leading-relaxed">
                {pluginSummary(presentation, capabilities)}
              </p>
              {capabilities.length > 0 && (
                <div className="flex flex-wrap gap-1.5">
                  {capabilities.map((cap) => (
                    <span
                      key={`${cap.type}:${cap.id}`}
                      className="bg-muted text-muted-foreground inline-flex items-center rounded-md px-2 py-0.5 text-[11px] font-medium"
                    >
                      {cap.display_name || capabilityLabel(cap.type)}
                    </span>
                  ))}
                </div>
              )}
              <PluginResourceLinks presentation={presentation} repoURL={repoURL} />
            </div>
          </div>

          {/* Right: actions */}
          <div className="flex shrink-0 flex-wrap items-center gap-2 sm:ml-4">
            {adminRoutes.length > 0 ? (
              <>
                {adminRoutes.map((route) => {
                  const href = pluginRouteHref(installation.id, route.path);
                  return (
                    <Button
                      key={route.id}
                      variant="outline"
                      size="sm"
                      onClick={() => void navigateToPluginRoute(href)}
                    >
                      <ExternalLink className="mr-1.5 h-3.5 w-3.5" />
                      {route.navigation_label || route.path}
                    </Button>
                  );
                })}
                <Button
                  variant="ghost"
                  size="icon-sm"
                  title="Plugin settings"
                  aria-label="Plugin settings"
                  onClick={() => onConfigure(installation)}
                >
                  <Settings2 className="h-3.5 w-3.5" />
                </Button>
              </>
            ) : (
              <Button variant="outline" size="sm" onClick={() => onConfigure(installation)}>
                <Settings2 className="mr-1.5 h-3.5 w-3.5" />
                Configure
              </Button>
            )}
            <Select
              value={installation.update_policy ?? "auto"}
              onValueChange={(value) =>
                updateInstallation.mutate({
                  id: installation.id,
                  body: { update_policy: value },
                })
              }
            >
              <SelectTrigger size="sm" className="h-8 text-xs">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="auto">Auto</SelectItem>
                <SelectItem value="notify">Notify</SelectItem>
                <SelectItem value="off">Off</SelectItem>
              </SelectContent>
            </Select>
            <div className="flex items-center gap-2 rounded-lg border px-2.5 py-1.5">
              <Switch
                size="sm"
                checked={installation.enabled}
                onCheckedChange={(checked) =>
                  updateInstallation.mutate({ id: installation.id, body: { enabled: checked } })
                }
              />
            </div>
            <Button
              variant="ghost"
              size="icon-sm"
              className="text-muted-foreground hover:text-destructive"
              onClick={() => setConfirmDelete(true)}
              aria-label={`Uninstall ${pluginDisplayName(installation.plugin_id, presentation)}`}
            >
              <Trash2 className="h-3.5 w-3.5" />
            </Button>
          </div>
        </div>
      </div>
      <AlertDialog open={confirmDelete} onOpenChange={setConfirmDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              Uninstall {pluginDisplayName(installation.plugin_id, presentation)}?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Silo will stop the plugin, then remove its installation, configuration, and installed
              files. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => deleteInstallation.mutate(installation.id)}
              disabled={deleteInstallation.isPending}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleteInstallation.isPending ? "Uninstalling..." : "Uninstall plugin"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

/* ─── Configure dialog ──────────────────────────────────────────── */

function ConfigureDialog({
  installation,
  onClose,
}: {
  installation: PluginInstallation;
  onClose: () => void;
}) {
  const saveConfig = useSavePluginConfig();
  const testConfig = useTestPluginConfig();
  const saveAuthBinding = useSavePluginAuthBinding();
  const saveTaskBinding = useSavePluginTaskBinding();
  const capabilities = installation.capabilities ?? [];
  const globalConfigs = installation.global_configs ?? [];
  const globalConfigSchema = installation.global_config_schema ?? [];
  const authBindings = installation.auth_bindings ?? [];
  const taskBindings = installation.task_bindings ?? [];
  const routes = installation.routes ?? [];
  const authCapabilities = capabilities.filter((c) => c.type === "auth_provider.v1");
  const taskCapabilities = capabilities.filter((c) => c.type === "scheduled_task.v1");
  const adminRoutes = routes.filter((r) => r.navigable && r.navigation_kind === "admin");
  const hasSections =
    globalConfigSchema.length > 0 ||
    authCapabilities.length > 0 ||
    taskCapabilities.length > 0 ||
    adminRoutes.length > 0;

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Blocks className="h-5 w-5" />
            {installation.plugin_id}
            <Badge variant="secondary" className="font-mono text-[11px]">
              {installation.version}
            </Badge>
          </DialogTitle>
          <DialogDescription>
            Configure bindings, credentials, and runtime settings.
          </DialogDescription>
        </DialogHeader>

        <Accordion type="multiple" className="w-full" defaultValue={["config", "analyzers"]}>
          {/* Global Config */}
          {globalConfigSchema.length > 0 && (
            <AccordionItem value="config">
              <AccordionTrigger>
                <span className="flex items-center gap-2 text-sm font-semibold">
                  <Settings2 className="h-4 w-4" />
                  Global Configuration
                </span>
              </AccordionTrigger>
              <AccordionContent>
                <div className="space-y-4">
                  {globalConfigSchema.map((schema) => (
                    <PluginConfigForm
                      key={schema.key}
                      schema={schema}
                      value={globalConfigs.find((entry) => entry.key === schema.key)?.value}
                      configuredSecrets={
                        globalConfigs.find((entry) => entry.key === schema.key)?.configured_secrets
                      }
                      isSaving={saveConfig.isPending}
                      isTesting={testConfig.isPending}
                      onTest={(key, nextValue, clearSecrets) =>
                        testConfig.mutateAsync({
                          id: installation.id,
                          body: { key, value: nextValue, clear_secrets: clearSecrets },
                        })
                      }
                      onSave={(key, nextValue, clearSecrets) =>
                        saveConfig.mutate({
                          id: installation.id,
                          body: {
                            key,
                            value: nextValue,
                            clear_secrets: clearSecrets,
                          },
                        })
                      }
                    />
                  ))}
                </div>
              </AccordionContent>
            </AccordionItem>
          )}

          {/* Auth Providers */}
          {authCapabilities.length > 0 && (
            <AccordionItem value="auth">
              <AccordionTrigger>
                <span className="flex items-center gap-2 text-sm font-semibold">
                  <Shield className="h-4 w-4" />
                  Auth Providers
                </span>
              </AccordionTrigger>
              <AccordionContent>
                <div className="space-y-3">
                  <p className="text-muted-foreground text-xs">
                    Auth-provider bindings are registered at server startup. Saved changes require a
                    restart.
                  </p>
                  {authCapabilities.map((capability, index) => {
                    const binding = authBindings.find((e) => e.capability_id === capability.id);
                    return (
                      <div
                        key={capability.id}
                        className="flex items-center justify-between rounded-lg border p-3"
                      >
                        <div>
                          <p className="text-sm font-medium">
                            {capability.display_name || capability.id}
                          </p>
                          <p className="text-muted-foreground font-mono text-xs">{capability.id}</p>
                        </div>
                        <Switch
                          checked={binding?.enabled ?? false}
                          disabled={saveAuthBinding.isPending}
                          onCheckedChange={(checked) =>
                            saveAuthBinding.mutate({
                              id: installation.id,
                              body: {
                                capability_id: capability.id,
                                enabled: checked,
                                display_order: binding?.display_order ?? index + 1,
                                auto_provision: binding?.auto_provision ?? true,
                                default_login: binding?.default_login ?? false,
                              },
                            })
                          }
                        />
                      </div>
                    );
                  })}
                </div>
              </AccordionContent>
            </AccordionItem>
          )}

          {/* Scheduled Tasks */}
          {taskCapabilities.length > 0 && (
            <AccordionItem value="tasks">
              <AccordionTrigger>
                <span className="flex items-center gap-2 text-sm font-semibold">
                  <CircleDot className="h-4 w-4" />
                  Scheduled Tasks
                </span>
              </AccordionTrigger>
              <AccordionContent>
                <div className="space-y-3">
                  <p className="text-muted-foreground text-xs">
                    Enable or disable each declared task binding. Task registration is rebuilt at
                    server startup, so saved changes require a restart.
                  </p>
                  {taskCapabilities.map((capability) => {
                    const binding = taskBindings.find((e) => e.capability_id === capability.id);
                    return (
                      <div
                        key={capability.id}
                        className="flex items-center justify-between rounded-lg border p-3"
                      >
                        <div>
                          <p className="text-sm font-medium">
                            {capability.display_name || capability.id}
                          </p>
                          <p className="text-muted-foreground font-mono text-xs">{capability.id}</p>
                          <p className="text-muted-foreground mt-1 text-xs">
                            Trigger: {JSON.stringify(binding?.trigger ?? { type: "startup" })}
                          </p>
                        </div>
                        <Switch
                          checked={binding?.enabled ?? true}
                          disabled={saveTaskBinding.isPending}
                          onCheckedChange={(checked) =>
                            saveTaskBinding.mutate({
                              id: installation.id,
                              capabilityId: capability.id,
                              body: {
                                enabled: checked,
                                trigger: binding?.trigger ?? { type: "startup" },
                              },
                            })
                          }
                        />
                      </div>
                    );
                  })}
                </div>
              </AccordionContent>
            </AccordionItem>
          )}

          {/* Admin Pages / Assets */}
          {adminRoutes.length > 0 && (
            <AccordionItem value="pages">
              <AccordionTrigger>
                <span className="flex items-center gap-2 text-sm font-semibold">
                  <ExternalLink className="h-4 w-4" />
                  Plugin Pages
                </span>
              </AccordionTrigger>
              <AccordionContent>
                <div className="flex flex-wrap gap-2">
                  {adminRoutes.map((route) => {
                    const href = pluginRouteHref(installation.id, route.path);
                    return (
                      <a
                        key={route.id}
                        href={href}
                        onClick={(e) => {
                          e.preventDefault();
                          void navigateToPluginRoute(href);
                        }}
                        className="hover:bg-accent inline-flex items-center gap-1.5 rounded-lg border px-3 py-2 text-sm transition-colors"
                      >
                        <ExternalLink className="h-3 w-3" />
                        {route.navigation_label || route.path}
                      </a>
                    );
                  })}
                </div>
              </AccordionContent>
            </AccordionItem>
          )}
        </Accordion>

        {!hasSections && (
          <p className="text-muted-foreground py-4 text-center text-sm">
            This plugin has no additional configuration.
          </p>
        )}

        <DialogFooter showCloseButton />
      </DialogContent>
    </Dialog>
  );
}

/* ─── Available plugin card ─────────────────────────────────────── */

function CatalogCard({ entry, isInstalled }: { entry: PluginCatalogEntry; isInstalled: boolean }) {
  const installPlugin = useInstallPlugin();
  const capabilities = entry.capabilities ?? [];

  return (
    <div className="surface-panel-subtle flex flex-col justify-between gap-4 rounded-xl p-5">
      <div className="flex items-start gap-4">
        <div className="bg-muted flex h-11 w-11 shrink-0 items-center justify-center rounded-xl">
          <Blocks className="text-muted-foreground h-5 w-5" />
        </div>
        <div className="space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-[15px] leading-tight font-semibold">
              {pluginDisplayName(entry.plugin_id, entry.presentation)}
            </h3>
            <Badge variant="secondary" className="font-mono text-[11px]">
              {entry.version}
            </Badge>
            <Badge variant="outline" className="text-[11px]">
              {sourceLabel(entry.source_kind)}
            </Badge>
          </div>
          <p className="text-muted-foreground font-mono text-[11px]">{entry.plugin_id}</p>
          <p className="text-muted-foreground line-clamp-2 text-xs leading-relaxed">
            {pluginSummary(entry.presentation, capabilities)}
          </p>
          {capabilities.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {capabilities.map((cap) => (
                <span
                  key={`${cap.type}:${cap.id}`}
                  className="bg-muted text-muted-foreground inline-flex items-center rounded-md px-2 py-0.5 text-[11px] font-medium"
                >
                  {cap.display_name || capabilityLabel(cap.type)}
                </span>
              ))}
            </div>
          )}
        </div>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-3 border-t pt-3">
        <PluginResourceLinks presentation={entry.presentation} repoURL={entry.repo_url} />
        {isInstalled ? (
          <Badge variant="outline" className="text-muted-foreground text-xs">
            Installed
          </Badge>
        ) : (
          <Button
            size="sm"
            onClick={() =>
              installPlugin.mutate({
                repository_id: entry.repository_id,
                plugin_id: entry.plugin_id,
                version: entry.version,
              })
            }
            disabled={installPlugin.isPending}
          >
            <Download className="mr-1.5 h-3.5 w-3.5" />
            Install
          </Button>
        )}
      </div>
    </div>
  );
}

function CommunityCatalogControl({ settings }: { settings: PluginCatalogSettings }) {
  const updateSettings = useUpdatePluginCatalogSettings();
  const [confirmDisable, setConfirmDisable] = useState(false);

  function setIncluded(include: boolean) {
    if (!include && settings.installed_community_plugin_count > 0) {
      setConfirmDisable(true);
      return;
    }
    updateSettings.mutate({ include_approved_community_plugins: include });
  }

  function disableCommunityCatalog() {
    updateSettings.mutate({ include_approved_community_plugins: false });
    setConfirmDisable(false);
  }

  return (
    <>
      <div className="flex flex-col gap-3 border-b pb-5 sm:flex-row sm:items-start sm:justify-between">
        <div className="max-w-2xl space-y-1">
          <div className="flex items-center gap-2">
            <Shield className="text-muted-foreground h-4 w-4" />
            <label htmlFor="approved-community-plugins" className="text-sm font-medium">
              Include approved community plugins
            </label>
          </div>
          <p className="text-muted-foreground text-xs leading-relaxed">
            Reviewed by Silo maintainers to work as described and be safe for their documented use.
            These plugins remain maintained and supported by community contributors.
          </p>
          {settings.migrated_plugin_count > 0 ? (
            <p className="text-muted-foreground text-xs">
              {settings.migrated_plugin_count} existing{" "}
              {settings.migrated_plugin_count === 1 ? "installation was" : "installations were"}{" "}
              moved here without changing configuration.
            </p>
          ) : null}
        </div>
        <Switch
          id="approved-community-plugins"
          checked={settings.include_approved_community_plugins}
          disabled={updateSettings.isPending}
          onCheckedChange={setIncluded}
          aria-label="Include approved community plugins"
        />
      </div>

      <AlertDialog open={confirmDisable} onOpenChange={setConfirmDisable}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Hide approved community plugins?</AlertDialogTitle>
            <AlertDialogDescription>
              {settings.installed_community_plugin_count}{" "}
              {settings.installed_community_plugin_count === 1
                ? "installed plugin will"
                : "installed plugins will"}{" "}
              keep running, but update discovery will pause until this catalog is included again.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={disableCommunityCatalog}
              disabled={updateSettings.isPending}
            >
              Hide and pause updates
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  );
}

/* ─── Repository management ─────────────────────────────────────── */

function RepositorySection() {
  const { repositories } = useAdminPlugins();
  const createRepository = useCreatePluginRepository();
  const updateRepository = useUpdatePluginRepository();
  const deleteRepository = useDeletePluginRepository();

  const [name, setName] = useState("");
  const [url, setUrl] = useState("");
  const [showForm, setShowForm] = useState(false);

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (!name.trim() || !url.trim()) return;
    createRepository.mutate({ display_name: name.trim(), url: url.trim(), enabled: true });
    setName("");
    setUrl("");
    setShowForm(false);
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-semibold">Repositories</h3>
          <p className="text-muted-foreground text-xs">Sources for discovering plugins.</p>
        </div>
        <Button variant="outline" size="sm" onClick={() => setShowForm(!showForm)}>
          {showForm ? (
            <X className="mr-1.5 h-3.5 w-3.5" />
          ) : (
            <Plus className="mr-1.5 h-3.5 w-3.5" />
          )}
          {showForm ? "Cancel" : "Add"}
        </Button>
      </div>

      {showForm && (
        <form
          onSubmit={handleSubmit}
          className="flex flex-col gap-3 rounded-lg border p-4 sm:flex-row"
        >
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="Repository name"
            className="sm:flex-1"
          />
          <Input
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            placeholder="https://plugins.example.test/index.json"
            className="sm:flex-[2]"
          />
          <Button type="submit" size="sm">
            Add
          </Button>
        </form>
      )}

      {repositories.length > 0 && (
        <div className="divide-border surface-panel-subtle divide-y overflow-hidden rounded-xl">
          {repositories.map((repo) => (
            <div
              key={repo.id}
              className="flex flex-col gap-3 px-5 py-3.5 sm:flex-row sm:items-center sm:justify-between"
            >
              <div className="min-w-0 space-y-0.5">
                <div className="flex items-center gap-2">
                  <p className="truncate text-sm font-medium">{repo.display_name}</p>
                  <span
                    className={`inline-block h-1.5 w-1.5 rounded-full ${repo.enabled ? "bg-success" : "bg-muted-foreground"}`}
                  />
                </div>
                <p className="text-muted-foreground truncate font-mono text-xs">{repo.url}</p>
              </div>
              <div className="flex shrink-0 gap-2">
                {repo.managed ? (
                  <span className="text-muted-foreground self-center text-xs">Managed by Silo</span>
                ) : (
                  <>
                    <Button
                      variant="outline"
                      size="xs"
                      onClick={() =>
                        updateRepository.mutate({ id: repo.id, body: { enabled: !repo.enabled } })
                      }
                    >
                      {repo.enabled ? "Disable" : "Enable"}
                    </Button>
                    <Button
                      variant="ghost"
                      size="xs"
                      className="text-muted-foreground hover:text-destructive"
                      onClick={() => deleteRepository.mutate(repo.id)}
                    >
                      <Trash2 className="h-3 w-3" />
                    </Button>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      {repositories.length === 0 && !showForm && (
        <p className="text-muted-foreground py-4 text-center text-sm">
          No repositories configured. Add one to browse available plugins.
        </p>
      )}
    </div>
  );
}

/* ─── Upload section ────────────────────────────────────────────── */

function UploadSection() {
  const { upload, progress, isPending } = usePluginUpload();
  const [file, setFile] = useState<File | null>(null);

  function handleSubmit(event: FormEvent) {
    event.preventDefault();
    if (!file) return;
    upload(file, { onSuccess: () => setFile(null) });
  }

  return (
    <div className="space-y-3">
      <div>
        <h3 className="text-sm font-semibold">Manual Install</h3>
        <p className="text-muted-foreground text-xs">Upload a plugin binary directly.</p>
      </div>
      <form
        onSubmit={handleSubmit}
        className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl p-5 sm:flex-row"
      >
        <label className="border-border hover:border-foreground/20 flex h-10 flex-1 cursor-pointer items-center gap-2 rounded-lg border border-dashed px-4 text-sm transition-colors">
          <Upload className="text-muted-foreground h-4 w-4 shrink-0" />
          <span className="text-muted-foreground truncate">
            {file ? file.name : "Choose plugin file..."}
          </span>
          <input
            type="file"
            className="hidden"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
        </label>
        <Button type="submit" variant="outline" size="sm" disabled={!file || isPending}>
          <Upload className="mr-1.5 h-3.5 w-3.5" />
          {isPending ? "Uploading..." : "Upload"}
        </Button>
      </form>
      {progress !== null && <Progress value={progress} aria-label="Plugin upload progress" />}
    </div>
  );
}

/* ─── Main page ─────────────────────────────────────────────────── */

export default function AdminPlugins() {
  const { installations, catalog, catalogSettings, isLoading } = useAdminPlugins();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const checkPluginUpdates = useCheckPluginUpdates();
  const { data: pluginUpdateTask } = useTask(CHECK_PLUGIN_UPDATES_TASK_KEY);
  const [configuring, setConfiguring] = useState<PluginInstallation | null>(null);
  const previousTaskState = useRef<string | null>(null);

  const installedIds = useMemo(
    () => new Set(installations.map((installation) => installation.plugin_id)),
    [installations],
  );
  const catalogByPluginID = useMemo(
    () => new Map(catalog.map((entry) => [entry.plugin_id, entry])),
    [catalog],
  );
  const activeTab = searchParams.get("tab") === "catalog" ? "catalog" : "installed";
  const installedQuery = searchParams.get("installed_q") ?? "";
  const catalogQuery = searchParams.get("catalog_q") ?? "";
  const filteredInstallations = useMemo(
    () =>
      installations.filter((installation) => {
        const catalogEntry = catalogByPluginID.get(installation.plugin_id);
        return pluginMatchesSearch({
          query: installedQuery,
          pluginID: installation.plugin_id,
          presentation: installation.presentation ?? catalogEntry?.presentation,
          capabilities: installation.capabilities ?? [],
          sourceKind: installation.source_kind,
          repositoryName: installation.repository_name,
        });
      }),
    [catalogByPluginID, installations, installedQuery],
  );
  const filteredCatalog = useMemo(
    () =>
      catalog.filter((entry) =>
        pluginMatchesSearch({
          query: catalogQuery,
          pluginID: entry.plugin_id,
          presentation: entry.presentation,
          capabilities: entry.capabilities ?? [],
          sourceKind: entry.source_kind,
          repositoryName: entry.repository_name,
        }),
      ),
    [catalog, catalogQuery],
  );
  const installedPageCount = Math.max(
    1,
    Math.ceil(filteredInstallations.length / INSTALLED_PAGE_SIZE),
  );
  const catalogPageCount = Math.max(1, Math.ceil(filteredCatalog.length / CATALOG_PAGE_SIZE));
  const installedPage = Math.min(
    parsePluginPage(searchParams.get("installed_page")),
    installedPageCount - 1,
  );
  const catalogPage = Math.min(
    parsePluginPage(searchParams.get("catalog_page")),
    catalogPageCount - 1,
  );
  const visibleInstallations = useMemo(
    () =>
      filteredInstallations.slice(
        installedPage * INSTALLED_PAGE_SIZE,
        (installedPage + 1) * INSTALLED_PAGE_SIZE,
      ),
    [filteredInstallations, installedPage],
  );
  const visibleCatalog = useMemo(
    () =>
      filteredCatalog.slice(catalogPage * CATALOG_PAGE_SIZE, (catalogPage + 1) * CATALOG_PAGE_SIZE),
    [catalogPage, filteredCatalog],
  );
  const isCheckingUpdates =
    pluginUpdateTask?.state === "running" || pluginUpdateTask?.state === "cancelling";

  function updatePluginView(
    updates: Record<string, string | undefined>,
    options: { replace?: boolean } = { replace: true },
  ) {
    const next = new URLSearchParams(searchParams);
    for (const [key, value] of Object.entries(updates)) {
      if (value) next.set(key, value);
      else next.delete(key);
    }
    setSearchParams(next, { replace: options.replace ?? true });
  }

  useEffect(() => {
    const currentState = pluginUpdateTask?.state ?? null;
    const previousState = previousTaskState.current;

    if (
      previousState !== null &&
      (previousState === "running" || previousState === "cancelling") &&
      currentState === "idle"
    ) {
      queryClient.invalidateQueries({ queryKey: adminKeys.pluginRepositories() });
      queryClient.invalidateQueries({ queryKey: adminKeys.pluginCatalog() });
      queryClient.invalidateQueries({ queryKey: adminKeys.pluginInstallations() });
    }

    previousTaskState.current = currentState;
  }, [pluginUpdateTask?.state, queryClient]);

  if (isLoading) {
    return (
      <div className="space-y-6">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Plugins</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Extend Silo with community and first-party plugins.
          </p>
        </div>
        <div className="text-muted-foreground py-12 text-center text-sm">Loading plugins...</div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Plugins</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Extend Silo with community and first-party plugins.
          </p>
        </div>
        <Button
          variant="outline"
          onClick={() => checkPluginUpdates.mutate()}
          disabled={checkPluginUpdates.isPending || isCheckingUpdates}
        >
          <Download className="mr-1.5 h-3.5 w-3.5" />
          {isCheckingUpdates ? "Checking updates..." : "Check for updates"}
        </Button>
      </div>

      <Tabs
        value={activeTab}
        onValueChange={(value) =>
          updatePluginView({ tab: value === "catalog" ? "catalog" : undefined }, { replace: false })
        }
      >
        <TabsList variant="line" className="mb-2">
          <TabsTrigger value="installed">
            Installed
            {installations.length > 0 && (
              <Badge variant="secondary" className="ml-1.5 text-[10px]">
                {installations.length}
              </Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value="catalog">
            Catalog
            {catalog.length > 0 && (
              <Badge variant="secondary" className="ml-1.5 text-[10px]">
                {catalog.length}
              </Badge>
            )}
          </TabsTrigger>
        </TabsList>

        {/* ── Installed ── */}
        <TabsContent value="installed" className="space-y-3">
          {installations.length > 0 ? (
            <PluginListToolbar
              query={installedQuery}
              total={installations.length}
              matchingTotal={filteredInstallations.length}
              placeholder="Search installed plugins"
              onQueryChange={(query) =>
                updatePluginView({ installed_q: query || undefined, installed_page: undefined })
              }
            />
          ) : null}
          {installations.length === 0 ? (
            <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl py-16">
              <Blocks className="text-muted-foreground h-10 w-10" />
              <div className="text-center">
                <p className="text-sm font-medium">No plugins installed</p>
                <p className="text-muted-foreground text-xs">
                  Browse the Catalog tab to find and install plugins.
                </p>
              </div>
            </div>
          ) : filteredInstallations.length === 0 ? (
            <div className="py-12 text-center">
              <p className="text-sm font-medium">No installed plugins match your search</p>
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground mt-1 text-xs transition-colors"
                onClick={() =>
                  updatePluginView({ installed_q: undefined, installed_page: undefined })
                }
              >
                Clear search
              </button>
            </div>
          ) : (
            <>
              <div className="space-y-2">
                {visibleInstallations.map((installation) => (
                  <InstalledPluginCard
                    key={installation.id}
                    installation={installation}
                    catalogEntry={catalogByPluginID.get(installation.plugin_id)}
                    onConfigure={setConfiguring}
                  />
                ))}
              </div>
              {filteredInstallations.length > INSTALLED_PAGE_SIZE ? (
                <TablePagination
                  page={installedPage}
                  pageSize={INSTALLED_PAGE_SIZE}
                  total={filteredInstallations.length}
                  itemNoun="plugin"
                  onPageChange={(page) =>
                    updatePluginView({ installed_page: page === 0 ? undefined : String(page + 1) })
                  }
                  className="border-t pt-3"
                />
              ) : null}
            </>
          )}
        </TabsContent>

        {/* ── Catalog ── */}
        <TabsContent value="catalog" className="space-y-8">
          {catalogSettings ? <CommunityCatalogControl settings={catalogSettings} /> : null}
          {catalog.length > 0 ? (
            <PluginListToolbar
              query={catalogQuery}
              total={catalog.length}
              matchingTotal={filteredCatalog.length}
              placeholder="Search the plugin catalog"
              onQueryChange={(query) =>
                updatePluginView({ catalog_q: query || undefined, catalog_page: undefined })
              }
            />
          ) : null}
          {/* Catalog grid */}
          {catalog.length === 0 ? (
            <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl py-12">
              <Package className="text-muted-foreground h-10 w-10" />
              <div className="text-center">
                <p className="text-sm font-medium">No plugins available</p>
                <p className="text-muted-foreground text-xs">
                  Add a repository or upload a plugin archive.
                </p>
              </div>
            </div>
          ) : filteredCatalog.length === 0 ? (
            <div className="py-12 text-center">
              <p className="text-sm font-medium">No catalog plugins match your search</p>
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground mt-1 text-xs transition-colors"
                onClick={() => updatePluginView({ catalog_q: undefined, catalog_page: undefined })}
              >
                Clear search
              </button>
            </div>
          ) : (
            <div className="space-y-4">
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {visibleCatalog.map((entry) => (
                  <CatalogCard
                    key={`${entry.plugin_id}:${entry.version}`}
                    entry={entry}
                    isInstalled={installedIds.has(entry.plugin_id)}
                  />
                ))}
              </div>
              {filteredCatalog.length > CATALOG_PAGE_SIZE ? (
                <TablePagination
                  page={catalogPage}
                  pageSize={CATALOG_PAGE_SIZE}
                  total={filteredCatalog.length}
                  itemNoun="plugin"
                  onPageChange={(page) =>
                    updatePluginView({ catalog_page: page === 0 ? undefined : String(page + 1) })
                  }
                  className="border-t pt-3"
                />
              ) : null}
            </div>
          )}

          <UploadSection />
          <RepositorySection />
        </TabsContent>
      </Tabs>

      {/* Configure dialog */}
      {configuring && (
        <ConfigureDialog installation={configuring} onClose={() => setConfiguring(null)} />
      )}
    </div>
  );
}
