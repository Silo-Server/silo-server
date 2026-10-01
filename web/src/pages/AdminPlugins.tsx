import { useEffect, useMemo, useRef } from "react";
import { Navigate, useSearchParams } from "react-router";
import { Blocks, Download, Package } from "lucide-react";
import { useQueryClient } from "@tanstack/react-query";

import type { PluginCatalogEntry, PluginInstallation } from "@/api/types";
import { CommunityCatalogControl } from "@/components/admin/plugins/CommunityCatalogControl";
import { FilterChips, PluginSearchField } from "@/components/admin/plugins/PluginListControls";
import {
  PluginRepositoriesPanel,
  PluginUploadPanel,
} from "@/components/admin/plugins/PluginSourcePanels";
import {
  CatalogPluginTile,
  InstalledPluginTile,
  PluginTileGrid,
} from "@/components/admin/plugins/PluginTile";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
  CHECK_PLUGIN_UPDATES_TASK_KEY,
  useAdminPlugins,
  useCheckPluginUpdates,
} from "@/hooks/queries/admin/plugins";
import { useTask } from "@/hooks/queries/admin/tasks";
import { adminKeys } from "@/hooks/queries/keys";
import { catalogJobs, hasCapabilityJob } from "@/lib/pluginCapabilities";
import {
  CATALOG_GROUPS,
  pluginDisplayName,
  pluginMatchesSearch,
  pluginPagePath,
} from "@/lib/pluginPresentation";
import {
  matchesInstalledFilter,
  parseInstalledFilter,
  sortInstalledPlugins,
  type InstalledFilter,
} from "@/lib/pluginStatus";

/**
 * `/admin/plugins`. Configuration used to be a dialog opened by
 * `?configure=<plugin_id>`; each plugin now has its own page, and old links
 * (bookmarks, other admin pages) are forwarded there.
 */
export default function AdminPlugins() {
  const [searchParams] = useSearchParams();
  const legacyConfigure = searchParams.get("configure");
  if (legacyConfigure) return <Navigate to={pluginPagePath(legacyConfigure)} replace />;
  return <PluginsBoard />;
}

function EmptyState({
  icon: Icon,
  title,
  description,
}: {
  icon: typeof Blocks;
  title: string;
  description: string;
}) {
  return (
    <div className="surface-panel-subtle flex flex-col items-center gap-3 rounded-xl py-14">
      <Icon className="text-muted-foreground h-10 w-10" aria-hidden />
      <div className="text-center">
        <p className="text-sm font-medium">{title}</p>
        <p className="text-muted-foreground text-xs">{description}</p>
      </div>
    </div>
  );
}

function NoMatches({ label, onClear }: { label: string; onClear: () => void }) {
  return (
    <div className="py-12 text-center">
      <p className="text-sm font-medium">{label}</p>
      <button
        type="button"
        className="text-muted-foreground hover:text-foreground mt-1 text-xs transition-colors"
        onClick={onClear}
      >
        Clear search and filters
      </button>
    </div>
  );
}

function ResultCount({ shown, total }: { shown: number; total: number }) {
  return (
    <span className="text-muted-foreground ml-auto text-xs tabular-nums" aria-live="polite">
      {shown === total ? `${total} plugins` : `${shown} of ${total} plugins`}
    </span>
  );
}

function PluginsBoard() {
  const { installations, catalog, catalogSettings, repositories, repositoriesError, isLoading } =
    useAdminPlugins();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const checkPluginUpdates = useCheckPluginUpdates();
  const { data: pluginUpdateTask } = useTask(CHECK_PLUGIN_UPDATES_TASK_KEY);
  const previousTaskState = useRef<string | null>(null);

  const activeTab = searchParams.get("tab") === "catalog" ? "catalog" : "installed";
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

  const header = (
    <div className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
      <div className="space-y-3">
        <h1 className="page-title text-[clamp(2rem,4vw,3rem)]">Plugins</h1>
        <p className="page-subtitle text-sm sm:text-base">
          Add metadata sources, playback markers, request routing, and watch sync to Silo.
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
  );

  if (isLoading) {
    return (
      <div className="page-shell space-y-6 py-4 sm:py-6">
        {header}
        <div className="text-muted-foreground py-12 text-center text-sm">Loading plugins...</div>
      </div>
    );
  }

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      {header}

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

        <TabsContent value="installed" className="space-y-4">
          <InstalledTab
            installations={installations}
            catalog={catalog}
            query={searchParams.get("installed_q") ?? ""}
            filter={parseInstalledFilter(searchParams.get("installed_filter"))}
            onChange={(updates) => updatePluginView(updates)}
          />
        </TabsContent>

        <TabsContent value="catalog" className="space-y-5">
          {catalogSettings ? <CommunityCatalogControl settings={catalogSettings} /> : null}
          <CatalogTab
            installations={installations}
            catalog={catalog}
            query={searchParams.get("catalog_q") ?? ""}
            job={searchParams.get("catalog_job") ?? ""}
            onChange={(updates) => updatePluginView(updates)}
          />
          <div className="grid gap-2.5 xl:grid-cols-2">
            <PluginUploadPanel />
            <PluginRepositoriesPanel
              repositories={repositories}
              repositoriesError={repositoriesError}
            />
          </div>
        </TabsContent>
      </Tabs>
    </div>
  );
}

type ViewUpdate = (updates: Record<string, string | undefined>) => void;

function InstalledTab({
  installations,
  catalog,
  query,
  filter,
  onChange,
}: {
  installations: PluginInstallation[];
  catalog: PluginCatalogEntry[];
  query: string;
  filter: InstalledFilter;
  onChange: ViewUpdate;
}) {
  const catalogByPluginID = useMemo(
    () => new Map(catalog.map((entry) => [entry.plugin_id, entry])),
    [catalog],
  );
  const searched = useMemo(
    () =>
      installations.filter((installation) =>
        pluginMatchesSearch({
          query,
          pluginID: installation.plugin_id,
          presentation:
            installation.presentation ??
            catalogByPluginID.get(installation.plugin_id)?.presentation,
          capabilities: installation.capabilities ?? [],
          sourceKind: installation.source_kind,
          repositoryName: installation.repository_name,
        }),
      ),
    [catalogByPluginID, installations, query],
  );
  const visible = useMemo(
    () =>
      sortInstalledPlugins(
        searched.filter((installation) => matchesInstalledFilter(installation, filter)),
        (installation) =>
          installation.presentation ?? catalogByPluginID.get(installation.plugin_id)?.presentation,
      ),
    [catalogByPluginID, filter, searched],
  );

  if (installations.length === 0) {
    return (
      <EmptyState
        icon={Blocks}
        title="No plugins installed"
        description="Browse the Catalog tab to find and install plugins."
      />
    );
  }

  const count = (value: InstalledFilter) =>
    searched.filter((installation) => matchesInstalledFilter(installation, value)).length;

  return (
    <>
      <div className="flex flex-col gap-2.5 sm:flex-row sm:flex-wrap sm:items-center">
        <PluginSearchField
          query={query}
          placeholder="Search installed plugins"
          onQueryChange={(next) => onChange({ installed_q: next || undefined })}
        />
        <FilterChips
          label="Filter installed plugins"
          value={filter}
          onChange={(next) => onChange({ installed_filter: next === "all" ? undefined : next })}
          chips={[
            { value: "all", label: "All", count: searched.length },
            { value: "attention", label: "Needs attention", count: count("attention") },
            { value: "update", label: "Update available", count: count("update") },
            { value: "off", label: "Off", count: count("off") },
          ]}
        />
        <ResultCount shown={visible.length} total={installations.length} />
      </div>
      {visible.length === 0 ? (
        <NoMatches
          label="No installed plugins match"
          onClear={() => onChange({ installed_q: undefined, installed_filter: undefined })}
        />
      ) : (
        <PluginTileGrid>
          {visible.map((installation) => (
            <InstalledPluginTile
              key={installation.id}
              installation={installation}
              catalogEntry={catalogByPluginID.get(installation.plugin_id)}
            />
          ))}
        </PluginTileGrid>
      )}
    </>
  );
}

function CatalogTab({
  installations,
  catalog,
  query,
  job,
  onChange,
}: {
  installations: PluginInstallation[];
  catalog: PluginCatalogEntry[];
  query: string;
  job: string;
  onChange: ViewUpdate;
}) {
  const installedIds = useMemo(
    () => new Set(installations.map((installation) => installation.plugin_id)),
    [installations],
  );
  const searched = useMemo(
    () =>
      catalog.filter((entry) =>
        pluginMatchesSearch({
          query,
          pluginID: entry.plugin_id,
          presentation: entry.presentation,
          capabilities: entry.capabilities ?? [],
          sourceKind: entry.source_kind,
          repositoryName: entry.repository_name,
        }),
      ),
    [catalog, query],
  );
  // Chips come from the whole catalog so a job filter still applies (and can
  // show no results) when the search narrows the list.
  const jobs = useMemo(
    () => catalogJobs(catalog.map((entry) => entry.capabilities ?? [])),
    [catalog],
  );
  const activeJob = jobs.some((kind) => kind.job === job) ? job : "";
  const visible = activeJob
    ? searched.filter((entry) => hasCapabilityJob(entry.capabilities ?? [], activeJob))
    : searched;

  if (catalog.length === 0) {
    return (
      <EmptyState
        icon={Package}
        title="No plugins available"
        description="Add a repository or upload a plugin file."
      />
    );
  }

  const groups = CATALOG_GROUPS.map((group) => ({
    ...group,
    entries: visible
      .filter((entry) => entry.source_kind === group.kind)
      .sort(
        (a, b) =>
          Number(installedIds.has(a.plugin_id)) - Number(installedIds.has(b.plugin_id)) ||
          pluginDisplayName(a.plugin_id, a.presentation).localeCompare(
            pluginDisplayName(b.plugin_id, b.presentation),
            undefined,
            { sensitivity: "base" },
          ),
      ),
  })).filter((group) => group.entries.length > 0);

  return (
    <>
      <div className="flex flex-col gap-2.5 sm:flex-row sm:flex-wrap sm:items-center">
        <PluginSearchField
          query={query}
          placeholder="Search the plugin catalog"
          onQueryChange={(next) => onChange({ catalog_q: next || undefined })}
        />
        {jobs.length > 1 ? (
          <FilterChips
            label="Filter by what plugins do"
            value={activeJob}
            onChange={(next) => onChange({ catalog_job: next || undefined })}
            chips={[
              { value: "", label: "All" },
              ...jobs.map((kind) => ({ value: kind.job, label: kind.label })),
            ]}
          />
        ) : null}
        <ResultCount shown={visible.length} total={catalog.length} />
      </div>
      {groups.length === 0 ? (
        <NoMatches
          label="No catalog plugins match"
          onClear={() => onChange({ catalog_q: undefined, catalog_job: undefined })}
        />
      ) : (
        groups.map((group) => (
          <section key={group.kind} className="space-y-2.5" aria-label={group.title}>
            <h2 className="flex items-baseline gap-2 text-[15px] font-semibold">
              {group.title}
              <span className="text-muted-foreground text-[13.5px] font-medium tabular-nums">
                {group.entries.length}
              </span>
            </h2>
            <PluginTileGrid>
              {group.entries.map((entry) => (
                <CatalogPluginTile
                  key={`${entry.plugin_id}:${entry.version}`}
                  entry={entry}
                  isInstalled={installedIds.has(entry.plugin_id)}
                />
              ))}
            </PluginTileGrid>
          </section>
        ))
      )}
    </>
  );
}
