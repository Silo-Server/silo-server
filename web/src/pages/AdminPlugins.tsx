import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  Blocks,
  CircleDot,
  Download,
  ExternalLink,
  Loader2,
  Package,
  Plus,
  Settings2,
  Shield,
  Trash2,
  Upload,
  X,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
  Accordion,
  AccordionContent,
  AccordionItem,
  AccordionTrigger,
} from "@/components/ui/accordion";
import { PluginConfigForm } from "@/components/admin/plugins/PluginConfigForm";
import type { PluginCatalogEntry, PluginInstallation } from "@/api/types";
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
  useUpdatePluginInstallation,
  useUpdatePluginRepository,
} from "@/hooks/queries/admin/plugins";
import { useTask } from "@/hooks/queries/admin/tasks";
import { adminKeys } from "@/hooks/queries/keys";
import { pluginRouteHref } from "@/lib/pluginRouteHref";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";

function capabilityLabel(type: string): string {
  const labels: Record<string, string> = {
    "metadata_provider.v1": "Metadata",
    "auth_provider.v1": "Auth",
    "scheduled_task.v1": "Task",
    "media_analyzer.v1": "Analyzer",
  };
  return labels[type] ?? type.split(".")[0] ?? type;
}

/* ─── Installed plugin card ─────────────────────────────────────── */

function InstalledPluginCard({
  installation,
  onConfigure,
}: {
  installation: PluginInstallation;
  onConfigure: (installation: PluginInstallation) => void;
}) {
  const updateInstallation = useUpdatePluginInstallation();
  const deleteInstallation = useDeletePluginInstallation();
  const applyUpdate = useApplyPluginUpdate();
  const capabilities = installation.capabilities ?? [];
  const routes = installation.routes ?? [];
  const adminRoutes = routes.filter(
    (route) => route.navigable && route.navigation_kind === "admin",
  );

  return (
    <div className="surface-panel-subtle group relative overflow-hidden rounded-xl transition-all">
      <div className="flex flex-col gap-4 p-5 sm:flex-row sm:items-start sm:justify-between">
        {/* Left: icon + info */}
        <div className="flex items-start gap-4">
          <div
            className={`flex h-11 w-11 shrink-0 items-center justify-center rounded-xl text-sm font-bold ${
              installation.enabled ? "bg-primary/15 text-primary" : "bg-muted text-muted-foreground"
            }`}
          >
            <Blocks className="h-5 w-5" />
          </div>
          <div className="space-y-1.5">
            <div className="flex flex-wrap items-center gap-2">
              <h3 className="text-[15px] leading-tight font-semibold">{installation.plugin_id}</h3>
              <Badge variant="secondary" className="font-mono text-[11px]">
                {installation.version}
              </Badge>
              {installation.available_version && (
                <Badge variant="outline" className="border-amber-500/40 text-[11px] text-amber-500">
                  {installation.version} &rarr; {installation.available_version} available
                </Badge>
              )}
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
            onClick={() => deleteInstallation.mutate(installation.id)}
          >
            <Trash2 className="h-3.5 w-3.5" />
          </Button>
        </div>
      </div>
    </div>
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
                      isSaving={saveConfig.isPending}
                      onSave={(key, nextValue) =>
                        saveConfig.mutate({ id: installation.id, body: { key, value: nextValue } })
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
                        </div>
                        <Button
                          size="sm"
                          variant="outline"
                          onClick={() =>
                            saveTaskBinding.mutate({
                              id: installation.id,
                              capabilityId: capability.id,
                              body: {
                                enabled: binding?.enabled ?? true,
                                trigger: binding?.trigger ?? { type: "startup" },
                              },
                            })
                          }
                        >
                          Save binding
                        </Button>
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

  return (
    <div className="surface-panel-subtle flex flex-col justify-between gap-4 rounded-xl p-5">
      <div className="flex items-start gap-4">
        <div className="bg-muted flex h-11 w-11 shrink-0 items-center justify-center rounded-xl">
          <Blocks className="text-muted-foreground h-5 w-5" />
        </div>
        <div className="space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-[15px] leading-tight font-semibold">{entry.plugin_id}</h3>
            <Badge variant="secondary" className="font-mono text-[11px]">
              {entry.version}
            </Badge>
          </div>
          {entry.capabilities?.length > 0 && (
            <div className="flex flex-wrap gap-1.5">
              {entry.capabilities.map((cap) => (
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
      <div className="flex justify-end">
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
  const { installations, catalog, isLoading } = useAdminPlugins();
  const queryClient = useQueryClient();
  const checkPluginUpdates = useCheckPluginUpdates();
  const { data: pluginUpdateTask } = useTask(CHECK_PLUGIN_UPDATES_TASK_KEY);
  const [configuring, setConfiguring] = useState<PluginInstallation | null>(null);
  const previousTaskState = useRef<string | null>(null);

  const installedIds = new Set(installations.map((i) => i.plugin_id));
  const isCheckingUpdates =
    pluginUpdateTask?.state === "running" || pluginUpdateTask?.state === "cancelling";

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

      <Tabs defaultValue="installed">
        <TabsList variant="line" className="mb-2">
          <TabsTrigger value="installed">
            Installed
            {installations.length > 0 && (
              <Badge variant="secondary" className="ml-1.5 text-[10px]">
                {installations.length}
              </Badge>
            )}
          </TabsTrigger>
          <TabsTrigger value="available">
            Available
            {catalog.length > 0 && (
              <Badge variant="secondary" className="ml-1.5 text-[10px]">
                {catalog.length}
              </Badge>
            )}
          </TabsTrigger>
        </TabsList>

        {/* ── Installed ── */}
        <TabsContent value="installed" className="space-y-3">
          {installations.length === 0 ? (
            <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl py-16">
              <Blocks className="text-muted-foreground h-10 w-10" />
              <div className="text-center">
                <p className="text-sm font-medium">No plugins installed</p>
                <p className="text-muted-foreground text-xs">
                  Browse the Available tab to find and install plugins.
                </p>
              </div>
            </div>
          ) : (
            <div className="space-y-2">
              {installations.map((installation) => (
                <InstalledPluginCard
                  key={installation.id}
                  installation={installation}
                  onConfigure={setConfiguring}
                />
              ))}
            </div>
          )}
        </TabsContent>

        {/* ── Available ── */}
        <TabsContent value="available" className="space-y-8">
          {/* Catalog grid */}
          {catalog.length > 0 ? (
            <div className="space-y-4">
              <h3 className="text-sm font-semibold">Catalog</h3>
              <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {catalog.map((entry) => (
                  <CatalogCard
                    key={`${entry.plugin_id}:${entry.version}`}
                    entry={entry}
                    isInstalled={installedIds.has(entry.plugin_id)}
                  />
                ))}
              </div>
            </div>
          ) : (
            <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl py-12">
              <Package className="text-muted-foreground h-10 w-10" />
              <div className="text-center">
                <p className="text-sm font-medium">No plugins available</p>
                <p className="text-muted-foreground text-xs">
                  Add a repository or upload a plugin archive.
                </p>
              </div>
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
