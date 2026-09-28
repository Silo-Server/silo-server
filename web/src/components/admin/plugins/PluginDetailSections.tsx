import type { ReactNode } from "react";
import { ExternalLink } from "lucide-react";

import type {
  PluginCatalogEntry,
  PluginConfigSchema,
  PluginInstallation,
  PluginPresentation,
} from "@/api/types";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Badge } from "@/components/ui/badge";
import { Switch } from "@/components/ui/switch";
import {
  useSavePluginAuthBinding,
  useSavePluginConfig,
  useSavePluginTaskBinding,
  useTestPluginConfig,
} from "@/hooks/queries/admin/plugins";
import { formatDate, formatDateTime } from "@/lib/datetime";
import {
  configPanelId,
  licenseLabel,
  pluginResourceLinks,
  safeExternalURL,
  sourceLabel,
} from "@/lib/pluginPresentation";
import { describeTrigger, pluginTaskPath, pluginTaskTrigger } from "@/lib/taskTrigger";

import { humanizeConfigKey } from "./configSchemaAdminForm";
import { PluginConfigForm } from "./PluginConfigForm";
import { PluginMarkdown } from "./PluginMarkdown";

export function DetailPanel({
  id,
  title,
  description,
  badge,
  children,
}: {
  id?: string;
  title: string;
  description?: ReactNode;
  badge?: ReactNode;
  children?: ReactNode;
}) {
  return (
    <section id={id} className="bg-card scroll-mt-4 rounded-2xl border px-5 py-5">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-semibold tracking-tight">{title}</h2>
        {badge}
      </div>
      {description ? (
        <div className="text-muted-foreground mt-1 max-w-prose text-[13.5px]">{description}</div>
      ) : null}
      {children ? <div className="mt-4">{children}</div> : null}
    </section>
  );
}

function configTitle(schema: PluginConfigSchema): string {
  return schema.title?.trim() || humanizeConfigKey(schema.key);
}

/** Every settings panel an installed plugin declares, or a note when it has none. */
export function PluginSettingsPanels({
  installation,
  missingKeys,
}: {
  installation: PluginInstallation;
  missingKeys: Set<string>;
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
  const authCapabilities = capabilities.filter((c) => c.type === "auth_provider.v1");
  const taskCapabilities = capabilities.filter((c) => c.type === "scheduled_task.v1");

  if (
    globalConfigSchema.length === 0 &&
    authCapabilities.length === 0 &&
    taskCapabilities.length === 0
  ) {
    return (
      <DetailPanel
        title="Settings"
        description="This plugin has no settings. It works as soon as it's installed and turned on."
      />
    );
  }

  return (
    <>
      {globalConfigSchema.map((schema) => {
        const saved = globalConfigs.find((entry) => entry.key === schema.key);
        return (
          <DetailPanel
            key={schema.key}
            id={configPanelId(schema.key)}
            title={configTitle(schema)}
            description={schema.description}
            badge={
              missingKeys.has(schema.key) ? (
                <Badge variant="outline" className="border-warning/40 text-warning text-[11px]">
                  Required
                </Badge>
              ) : null
            }
          >
            <PluginConfigForm
              bare
              schema={schema}
              value={saved?.value}
              configuredSecrets={saved?.configured_secrets}
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
                  body: { key, value: nextValue, clear_secrets: clearSecrets },
                })
              }
            />
          </DetailPanel>
        );
      })}

      {authCapabilities.length > 0 ? (
        <DetailPanel
          title="Sign-in"
          description="Sign-in providers are registered when the server starts, so saved changes take effect after a restart."
        >
          <ul className="divide-y rounded-xl border">
            {authCapabilities.map((capability, index) => {
              const binding = authBindings.find((e) => e.capability_id === capability.id);
              const label = capability.display_name || capability.id;
              return (
                <li key={capability.id} className="flex items-center justify-between gap-4 p-3.5">
                  <div className="min-w-0">
                    <p className="text-sm font-medium">{label}</p>
                    {capability.description ? (
                      <p className="text-muted-foreground text-[13px]">{capability.description}</p>
                    ) : null}
                  </div>
                  <Switch
                    aria-label={`Offer ${label} on the sign-in screen`}
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
                </li>
              );
            })}
          </ul>
        </DetailPanel>
      ) : null}

      {taskCapabilities.length > 0 ? (
        <DetailPanel
          title="Scheduled tasks"
          description="Tasks are registered when the server starts, so turning one on or off takes effect after a restart. Once a task exists, change its schedule on its task page."
        >
          <ul className="divide-y rounded-xl border">
            {taskCapabilities.map((capability) => {
              const binding = taskBindings.find((e) => e.capability_id === capability.id);
              const label = capability.display_name || capability.id;
              const trigger = pluginTaskTrigger(binding?.trigger);
              const registered = installation.enabled && (binding?.enabled ?? true);
              return (
                <li key={capability.id} className="flex items-center justify-between gap-4 p-3.5">
                  <div className="min-w-0 space-y-0.5">
                    <p className="text-sm font-medium">{label}</p>
                    {capability.description ? (
                      <p className="text-muted-foreground text-[13px]">{capability.description}</p>
                    ) : null}
                    <p className="text-muted-foreground text-[13px]">
                      Default schedule: {describeTrigger(trigger)}
                    </p>
                    {registered ? (
                      <ViewTransitionLink
                        to={pluginTaskPath(installation.id, capability.id)}
                        className="text-foreground/85 hover:text-foreground text-[13px] underline underline-offset-4"
                      >
                        Schedule and history
                      </ViewTransitionLink>
                    ) : null}
                  </div>
                  <Switch
                    aria-label={`Run ${label}`}
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
                </li>
              );
            })}
          </ul>
        </DetailPanel>
      ) : null}
    </>
  );
}

/** Read-only list of the settings an uninstalled plugin will ask for. */
export function PluginDeclaredSettings({ schemas }: { schemas: PluginConfigSchema[] }) {
  if (schemas.length === 0) {
    return (
      <DetailPanel
        title="Settings"
        description="This plugin has no settings. It works as soon as it's installed."
      />
    );
  }
  return (
    <DetailPanel title="Settings" description="You can change these after installing.">
      <ul className="divide-y">
        {schemas.map((schema) => (
          <li key={schema.key} className="space-y-0.5 py-2.5 first:pt-0 last:pb-0">
            <p className="flex items-center gap-2 text-sm font-medium">
              {configTitle(schema)}
              {schema.required ? (
                <Badge variant="outline" className="text-[11px]">
                  Required
                </Badge>
              ) : null}
            </p>
            {schema.description ? (
              <p className="text-muted-foreground text-[13px]">{schema.description}</p>
            ) : null}
          </li>
        ))}
      </ul>
    </DetailPanel>
  );
}

function RailPanel({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="bg-card rounded-2xl border px-4 py-4">
      <h2 className="mb-2.5 text-sm font-semibold">{title}</h2>
      {children}
    </section>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="min-w-0 [overflow-wrap:anywhere]">{children}</dd>
    </>
  );
}

const RUNTIME_STATE_LABELS: Record<string, string> = {
  stopped: "Stopped",
  starting: "Starting",
  running: "Running",
  backoff: "Waiting to restart",
  failed: "Failed",
};

/** The plugin page's side column: setup notes, description, process status, and details. */
export function PluginDetailRail({
  pluginID,
  presentation,
  installation,
  catalogEntry,
}: {
  pluginID: string;
  presentation?: PluginPresentation;
  installation?: PluginInstallation;
  catalogEntry?: PluginCatalogEntry;
}) {
  const sourceKind = installation?.source_kind ?? catalogEntry?.source_kind ?? "external";
  const repositoryName = installation?.repository_name || catalogEntry?.repository_name;
  const repoURL = installation?.repo_url || catalogEntry?.repo_url;
  const links = pluginResourceLinks(presentation, repoURL);
  const publisher = presentation?.publisher_name?.trim();
  const publisherURL = safeExternalURL(presentation?.publisher_url);
  const runtime = installation?.runtime;

  return (
    <aside className="flex flex-col gap-3.5" aria-label="About this plugin">
      {presentation?.setup_markdown?.trim() ? (
        <RailPanel title="Setup">
          <PluginMarkdown source={presentation.setup_markdown} className="text-foreground/85" />
        </RailPanel>
      ) : null}
      {presentation?.description_markdown?.trim() ? (
        <RailPanel title="About">
          <PluginMarkdown
            source={presentation.description_markdown}
            className="text-foreground/85"
          />
        </RailPanel>
      ) : null}
      {runtime?.resident ? (
        <RailPanel title="Process">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-[13.5px]">
            <Fact label="State">{RUNTIME_STATE_LABELS[runtime.state] ?? runtime.state}</Fact>
            <Fact label="Restarts">{runtime.restart_count}</Fact>
            {runtime.last_started_at ? (
              <Fact label="Last started">{formatDateTime(runtime.last_started_at)}</Fact>
            ) : null}
            {runtime.next_restart_at ? (
              <Fact label="Next restart">{formatDateTime(runtime.next_restart_at)}</Fact>
            ) : null}
          </dl>
        </RailPanel>
      ) : null}
      <RailPanel title="Details">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-[13.5px]">
          <Fact label="Plugin ID">
            <span className="font-mono text-[12.5px]">{pluginID}</span>
          </Fact>
          {publisher ? (
            <Fact label="Publisher">
              {publisherURL ? (
                <a
                  href={publisherURL}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="underline underline-offset-4"
                >
                  {publisher}
                </a>
              ) : (
                publisher
              )}
            </Fact>
          ) : null}
          <Fact label="Source">{sourceLabel(sourceKind)}</Fact>
          {repositoryName && repositoryName !== sourceLabel(sourceKind) ? (
            <Fact label="Repository">{repositoryName}</Fact>
          ) : null}
          <Fact label="License">{licenseLabel(presentation?.license_spdx)}</Fact>
          {installation?.created_at ? (
            <Fact label="Installed">{formatDate(installation.created_at, "medium")}</Fact>
          ) : null}
        </dl>
        {links.length > 0 ? (
          <div className="mt-3.5 flex flex-wrap gap-x-4 gap-y-1.5 text-[13.5px]">
            {links.map((link) => (
              <a
                key={link.label}
                href={link.url}
                target="_blank"
                rel="noopener noreferrer"
                className="text-foreground/85 hover:text-foreground inline-flex items-center gap-1"
              >
                {link.label}
                <ExternalLink aria-hidden="true" className="text-muted-foreground size-3" />
              </a>
            ))}
          </div>
        ) : null}
      </RailPanel>
    </aside>
  );
}
