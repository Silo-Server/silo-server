import { useState } from "react";
import type { FormEvent } from "react";
import { PluginConfigForm } from "@/components/admin/plugins/PluginConfigForm";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import type { PluginInstallation } from "@/api/types";
import { pluginRouteHref } from "@/lib/pluginRouteHref";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import {
  useAdminPlugins,
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
  useUpdatePluginRepository,
} from "@/hooks/queries/admin/plugins";

function PluginInstallationCard({ installation }: { installation: PluginInstallation }) {
  const updateInstallation = useUpdatePluginInstallation();
  const deleteInstallation = useDeletePluginInstallation();
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
  const [enabledOverride, setEnabledOverride] = useState<boolean | null>(null);

  const adminRoutes = routes.filter(
    (route) => route.navigable && route.navigation_kind === "admin",
  );
  const authCapabilities = capabilities.filter(
    (capability) => capability.type === "auth_provider.v1",
  );
  const taskCapabilities = capabilities.filter(
    (capability) => capability.type === "scheduled_task.v1",
  );
  const supportsConnectionTest = capabilities.some(
    (capability) => capability.type === "metadata_provider.v1",
  );
  const enabled = enabledOverride ?? installation.enabled;

  return (
    <section className="surface-panel-subtle space-y-5 rounded-xl p-5">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="space-y-2">
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-lg font-semibold">{installation.plugin_id}</h3>
            <Badge variant="secondary">{installation.version}</Badge>
            <Badge variant={enabled ? "outline" : "destructive"}>
              {enabled ? "Enabled" : "Disabled"}
            </Badge>
          </div>
          <div className="text-muted-foreground flex flex-wrap gap-2 text-xs">
            {capabilities.map((capability) => (
              <Badge key={`${capability.type}:${capability.id}`} variant="secondary">
                {capability.display_name || capability.id}
              </Badge>
            ))}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <div className="flex items-center gap-2 rounded-md border px-3 py-2">
            <Switch checked={enabled} onCheckedChange={setEnabledOverride} />
            <Label>Enabled</Label>
          </div>
          <Button
            variant="outline"
            onClick={() => updateInstallation.mutate({ id: installation.id, body: { enabled } })}
          >
            Save
          </Button>
          <Button variant="destructive" onClick={() => deleteInstallation.mutate(installation.id)}>
            Remove
          </Button>
        </div>
      </div>

      {globalConfigSchema.length > 0 && (
        <div className="space-y-3">
          <h4 className="text-sm font-semibold">Global Config</h4>
          <div className="grid gap-4 xl:grid-cols-2">
            {globalConfigSchema.map((schema) => (
              <PluginConfigForm
                key={schema.key}
                schema={schema}
                value={globalConfigs.find((entry) => entry.key === schema.key)?.value}
                isSaving={saveConfig.isPending}
                isTesting={testConfig.isPending}
                onSave={(key, nextValue) =>
                  saveConfig.mutate({ id: installation.id, body: { key, value: nextValue } })
                }
                onTest={
                  supportsConnectionTest
                    ? (key, nextValue) =>
                        testConfig.mutateAsync({
                          id: installation.id,
                          body: { key, value: nextValue },
                        })
                    : undefined
                }
              />
            ))}
          </div>
        </div>
      )}

      {authCapabilities.length > 0 && (
        <div className="space-y-3">
          <h4 className="text-sm font-semibold">Auth Providers</h4>
          {authCapabilities.map((capability, index) => {
            const binding = authBindings.find((entry) => entry.capability_id === capability.id);
            return (
              <div
                key={capability.id}
                className="flex flex-col gap-3 rounded-md border p-3 lg:flex-row lg:items-center lg:justify-between"
              >
                <div>
                  <p className="font-medium">{capability.display_name || capability.id}</p>
                  <p className="text-muted-foreground text-xs">{capability.id}</p>
                </div>
                <div className="flex flex-wrap gap-3">
                  <label className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      defaultChecked={binding?.enabled ?? false}
                      onChange={(event) =>
                        saveAuthBinding.mutate({
                          id: installation.id,
                          body: {
                            capability_id: capability.id,
                            enabled: event.target.checked,
                            display_order: binding?.display_order ?? index + 1,
                            auto_provision: binding?.auto_provision ?? true,
                            default_login: binding?.default_login ?? false,
                          },
                        })
                      }
                    />
                    Enabled
                  </label>
                </div>
              </div>
            );
          })}
        </div>
      )}

      {taskCapabilities.length > 0 && (
        <div className="space-y-3">
          <h4 className="text-sm font-semibold">Scheduled Tasks</h4>
          {taskCapabilities.map((capability) => {
            const binding = taskBindings.find((entry) => entry.capability_id === capability.id);
            return (
              <div key={capability.id} className="space-y-3 rounded-md border p-3">
                <div>
                  <p className="font-medium">{capability.display_name || capability.id}</p>
                  <p className="text-muted-foreground text-xs">{capability.id}</p>
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
                  Save task binding
                </Button>
              </div>
            );
          })}
        </div>
      )}

      {adminRoutes.length > 0 && (
        <div className="space-y-2">
          <h4 className="text-sm font-semibold">Plugin-hosted Admin Pages</h4>
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
                  className="inline-flex items-center rounded-md border px-3 py-2 text-sm"
                >
                  {route.navigation_label || route.path}
                </a>
              );
            })}
          </div>
        </div>
      )}
    </section>
  );
}

export default function PluginsSettings() {
  const { repositories, catalog, installations, isLoading } = useAdminPlugins();
  const createRepository = useCreatePluginRepository();
  const updateRepository = useUpdatePluginRepository();
  const deleteRepository = useDeletePluginRepository();
  const installPlugin = useInstallPlugin();
  const uploadPlugin = usePluginUpload();

  const [repositoryName, setRepositoryName] = useState("");
  const [repositoryURL, setRepositoryURL] = useState("");
  const [uploadFile, setUploadFile] = useState<File | null>(null);

  function handleRepositorySubmit(event: FormEvent) {
    event.preventDefault();
    if (!repositoryName.trim() || !repositoryURL.trim()) {
      return;
    }
    createRepository.mutate({
      display_name: repositoryName.trim(),
      url: repositoryURL.trim(),
      enabled: true,
    });
    setRepositoryName("");
    setRepositoryURL("");
  }

  function handleUploadSubmit(event: FormEvent) {
    event.preventDefault();
    if (!uploadFile) {
      return;
    }
    uploadPlugin.upload(uploadFile, { onSuccess: () => setUploadFile(null) });
  }

  if (isLoading) {
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-24 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-24 w-full" />
        </div>
        <span className="sr-only">Loading settings</span>
      </div>
    );
  }

  return (
    <div className="space-y-8">
      <section className="surface-panel-subtle space-y-4 rounded-xl p-5">
        <div className="flex items-center justify-between">
          <h3 className="text-base font-semibold">Repositories</h3>
          <Badge variant="secondary">{repositories.length}</Badge>
        </div>
        <form onSubmit={handleRepositorySubmit} className="grid gap-3 lg:grid-cols-[1fr_1fr_auto]">
          <Input
            value={repositoryName}
            onChange={(event) => setRepositoryName(event.target.value)}
            placeholder="Repository name"
          />
          <Input
            value={repositoryURL}
            onChange={(event) => setRepositoryURL(event.target.value)}
            placeholder="https://plugins.example.test/index.json"
          />
          <Button type="submit">Add repository</Button>
        </form>
        <div className="space-y-2">
          {repositories.map((repository) => (
            <div
              key={repository.id}
              className="flex flex-col gap-3 rounded-md border p-3 lg:flex-row lg:items-center lg:justify-between"
            >
              <div className="space-y-1">
                <p className="font-medium">{repository.display_name}</p>
                <p className="text-muted-foreground text-xs">{repository.url}</p>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    updateRepository.mutate({
                      id: repository.id,
                      body: { enabled: !repository.enabled },
                    })
                  }
                >
                  {repository.enabled ? "Disable" : "Enable"}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => deleteRepository.mutate(repository.id)}
                >
                  Remove
                </Button>
              </div>
            </div>
          ))}
        </div>
      </section>

      <section className="surface-panel-subtle space-y-4 rounded-xl p-5">
        <div className="flex items-center justify-between">
          <h3 className="text-base font-semibold">Catalog</h3>
          <Badge variant="secondary">{catalog.length}</Badge>
        </div>
        <form
          onSubmit={handleUploadSubmit}
          className="flex flex-col gap-3 rounded-md border p-3 lg:flex-row lg:items-center"
        >
          <input
            type="file"
            disabled={uploadPlugin.isPending}
            onChange={(event) => setUploadFile(event.target.files?.[0] ?? null)}
          />
          <Button type="submit" variant="outline" disabled={!uploadFile || uploadPlugin.isPending}>
            {uploadPlugin.isPending ? "Uploading..." : "Upload package"}
          </Button>
        </form>
        {uploadPlugin.progress !== null && (
          <Progress value={uploadPlugin.progress} aria-label="Plugin upload progress" />
        )}
        <div className="space-y-2">
          {catalog.map((entry) => (
            <div
              key={`${entry.plugin_id}:${entry.version}`}
              className="flex flex-col gap-3 rounded-md border p-3 lg:flex-row lg:items-center lg:justify-between"
            >
              <div className="space-y-1">
                <p className="font-medium">{entry.plugin_id}</p>
                <p className="text-muted-foreground text-xs">{entry.version}</p>
              </div>
              <Button
                size="sm"
                onClick={() =>
                  installPlugin.mutate({
                    repository_id: entry.repository_id,
                    plugin_id: entry.plugin_id,
                    version: entry.version,
                  })
                }
              >
                Install
              </Button>
            </div>
          ))}
        </div>
      </section>

      <section className="space-y-4">
        <div className="flex items-center justify-between">
          <h3 className="text-base font-semibold">Installed Plugins</h3>
          <Badge variant="secondary">{installations.length}</Badge>
        </div>
        <div className="space-y-4">
          {installations.map((installation) => (
            <PluginInstallationCard key={installation.id} installation={installation} />
          ))}
        </div>
      </section>
    </div>
  );
}
