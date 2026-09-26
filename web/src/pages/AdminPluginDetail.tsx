import { useState } from "react";
import type { ReactNode } from "react";
import { useLocation, useNavigate, useParams, useSearchParams } from "react-router";
import {
  AlertTriangle,
  ChevronLeft,
  Download,
  Ellipsis,
  ExternalLink,
  Info,
  RotateCw,
  Trash2,
} from "lucide-react";

import type { PluginCatalogEntry, PluginInstallation } from "@/api/types";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import {
  PluginDeclaredSettings,
  PluginDetailRail,
  PluginSettingsPanels,
} from "@/components/admin/plugins/PluginDetailSections";
import type { PluginPageLinkState } from "@/components/admin/plugins/PluginTile";
import { PluginMonogram, PluginStatusLabel } from "@/components/admin/plugins/PluginVisuals";
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
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import {
  useAdminPluginCatalog,
  useAdminPluginInstallations,
  useApplyPluginUpdate,
  useDeletePluginInstallation,
  useInstallPlugin,
  useRestartPluginInstallation,
  useUpdatePluginInstallation,
} from "@/hooks/queries/admin/plugins";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { formatDateTime } from "@/lib/datetime";
import { capabilityListLabel } from "@/lib/pluginCapabilities";
import { missingRequiredConfig } from "@/lib/pluginConfigReady";
import {
  configPanelId,
  pluginDisplayName,
  sourceLabel,
  tierNotice,
} from "@/lib/pluginPresentation";
import { pluginRouteHref } from "@/lib/pluginRouteHref";
import { pluginStatus } from "@/lib/pluginStatus";
import { cn } from "@/lib/utils";

const UPDATE_POLICY_LABELS: Record<string, string> = {
  auto: "Update automatically",
  notify: "Ask before updating",
  off: "Don't check for updates",
};

/**
 * `/admin/plugins/:pluginId`: one plugin's settings, status, and details, or a
 * preview of a catalog plugin that isn't installed yet. The route is keyed by
 * the manifest plugin ID, which deep links from other admin pages use too.
 */
export default function AdminPluginDetail() {
  const { pluginId = "" } = useParams<{ pluginId: string }>();
  const [searchParams] = useSearchParams();
  const selectedRepository = searchParams.get("repository");
  const selectedVersion = searchParams.get("version");
  const installationsQuery = useAdminPluginInstallations();
  // The service keeps one installation per plugin ID (install replaces in place).
  const installation = installationsQuery.data?.find((entry) => entry.plugin_id === pluginId);
  // The catalog is a live fetch of every repository index, so only ask for it
  // when this page needs it: a preview, or an older manifest without presentation.
  const catalogQuery = useAdminPluginCatalog({
    enabled: installationsQuery.isSuccess && (!installation || !installation.presentation),
  });
  const catalogEntry = catalogQuery.data?.find(
    (entry) =>
      entry.plugin_id === pluginId &&
      (selectedRepository === null || String(entry.repository_id) === selectedRepository) &&
      (selectedVersion === null || entry.version === selectedVersion),
  );

  if (installationsQuery.isLoading) return <PageLoading />;

  if (installation) {
    // Keyed by installation so drafts in one plugin's forms never carry into another's.
    return (
      <InstalledPluginPage
        key={installation.id}
        installation={installation}
        catalogEntry={catalogEntry}
      />
    );
  }

  if (installationsQuery.isError) {
    return (
      <PageUnavailable
        title="Couldn't load plugins"
        description="Silo couldn't read the installed plugins. Try again in a moment."
        onRetry={() => void installationsQuery.refetch()}
        retrying={installationsQuery.isFetching}
      >
        <AllPluginsLink />
      </PageUnavailable>
    );
  }

  if (catalogQuery.isLoading) return <PageLoading />;
  if (catalogEntry) return <CatalogPluginPreview entry={catalogEntry} />;

  if (catalogQuery.isError) {
    return (
      <PageUnavailable
        title="Couldn't load the plugin catalog"
        description="This plugin isn't installed, and Silo couldn't read the catalog to look it up."
        onRetry={() => void catalogQuery.refetch()}
        retrying={catalogQuery.isFetching}
      >
        <AllPluginsLink />
      </PageUnavailable>
    );
  }

  return (
    <PageUnavailable
      title="Plugin not found"
      description="This plugin isn't installed and isn't in any catalog Silo can see."
    >
      <AllPluginsLink />
    </PageUnavailable>
  );
}

function PageLoading() {
  return (
    <div className="page-shell text-muted-foreground py-16 text-center text-sm">
      Loading plugin...
    </div>
  );
}

function AllPluginsLink() {
  return (
    <Button asChild variant="outline">
      <ViewTransitionLink to="/admin/plugins" up>
        All plugins
      </ViewTransitionLink>
    </Button>
  );
}

function BackLink({ fallback }: { fallback: string }) {
  const location = useLocation();
  const from = (location.state as PluginPageLinkState | null)?.from;
  const to = from && from.startsWith("?") ? `/admin/plugins${from}` : fallback;
  return (
    <ViewTransitionLink
      to={to}
      up
      className="text-muted-foreground hover:text-foreground -ml-1 inline-flex items-center gap-1 text-sm"
    >
      <ChevronLeft aria-hidden="true" className="size-4" />
      Plugins
    </ViewTransitionLink>
  );
}

function PageHeader({
  name,
  meta,
  actions,
}: {
  name: string;
  meta: ReactNode;
  actions: ReactNode;
}) {
  return (
    <div className="flex flex-wrap items-center gap-4">
      <PluginMonogram name={name} size="lg" />
      <div className="min-w-[220px] flex-1">
        <h1 className="page-title text-3xl">{name}</h1>
        <div className="text-muted-foreground mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-[13.5px]">
          {meta}
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2.5">{actions}</div>
    </div>
  );
}

function Banner({
  tone,
  icon,
  children,
}: {
  tone: "warning" | "info";
  icon: ReactNode;
  children: ReactNode;
}) {
  return (
    <div
      role={tone === "warning" ? "alert" : "note"}
      className={cn(
        "flex items-start gap-3 rounded-xl border px-4 py-3 text-sm leading-relaxed",
        tone === "warning" ? "border-warning/30 bg-warning/10" : "bg-card",
      )}
    >
      {icon}
      <div className="min-w-0 flex-1 space-y-2">{children}</div>
    </div>
  );
}

function TierNotice({ sourceKind }: { sourceKind: string }) {
  const notice = tierNotice(sourceKind);
  if (!notice) return null;
  return (
    <Banner
      tone="info"
      icon={<Info aria-hidden="true" className="text-muted-foreground mt-0.5 size-4 shrink-0" />}
    >
      <p>
        <strong className="font-semibold">{sourceLabel(sourceKind)}.</strong> {notice}
      </p>
    </Banner>
  );
}

function InstalledPluginPage({
  installation,
  catalogEntry,
}: {
  installation: PluginInstallation;
  catalogEntry?: PluginCatalogEntry;
}) {
  const navigate = useNavigate();
  const updateInstallation = useUpdatePluginInstallation();
  const applyUpdate = useApplyPluginUpdate();
  const restartInstallation = useRestartPluginInstallation();
  const deleteInstallation = useDeletePluginInstallation();
  const [confirmUninstall, setConfirmUninstall] = useState(false);

  const presentation = installation.presentation ?? catalogEntry?.presentation;
  const name = pluginDisplayName(installation.plugin_id, presentation);
  const status = pluginStatus(installation);
  const missing = missingRequiredConfig(installation);
  const runtime = installation.runtime;
  const capabilities = installation.capabilities ?? [];
  const adminRoutes = (installation.routes ?? []).filter(
    (route) => route.navigable && route.navigation_kind === "admin",
  );
  const canRestart = runtime.resident && installation.enabled;
  const restartDisabled = restartInstallation.isPending || runtime.state === "starting";
  const hasMenuActions =
    canRestart || Boolean(installation.available_version) || adminRoutes.length > 0;
  const policy = installation.update_policy || "auto";
  const policyOptions = UPDATE_POLICY_LABELS[policy]
    ? Object.keys(UPDATE_POLICY_LABELS)
    : [...Object.keys(UPDATE_POLICY_LABELS), policy];
  const publisher = presentation?.publisher_name?.trim();
  const jobs = capabilityListLabel(capabilities);
  const runtimeProblem =
    installation.enabled &&
    runtime.resident &&
    (runtime.state === "failed" || runtime.state === "backoff");

  const restartButton = (
    <Button
      variant="outline"
      size="sm"
      disabled={restartDisabled}
      onClick={() => restartInstallation.mutate(installation.id)}
    >
      <RotateCw aria-hidden="true" />
      {restartInstallation.isPending ? "Restarting..." : "Restart now"}
    </Button>
  );

  return (
    <div className="page-shell space-y-5 py-4 sm:py-6">
      <BackLink fallback="/admin/plugins" />

      <PageHeader
        name={name}
        meta={
          <>
            {publisher ? <span>by {publisher}</span> : null}
            <span>Version {installation.version}</span>
            {jobs ? <span>{jobs}</span> : null}
            <span>{sourceLabel(installation.source_kind)}</span>
            <PluginStatusLabel {...status} />
          </>
        }
        actions={
          <>
            <Select
              value={policy}
              disabled={updateInstallation.isPending}
              onValueChange={(value) =>
                updateInstallation.mutate({
                  id: installation.id,
                  body: { update_policy: value },
                })
              }
            >
              <SelectTrigger size="sm" aria-label="Updates" className="h-8 text-[13px]">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {policyOptions.map((value) => (
                  <SelectItem key={value} value={value}>
                    {UPDATE_POLICY_LABELS[value] ?? value}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <label className="text-foreground/85 flex items-center gap-2 text-[13.5px]">
              <Switch
                checked={installation.enabled}
                aria-label="Enabled"
                disabled={updateInstallation.isPending}
                onCheckedChange={(checked) =>
                  updateInstallation.mutate({ id: installation.id, body: { enabled: checked } })
                }
              />
              {installation.enabled ? "On" : "Off"}
            </label>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="icon-sm" aria-label="More actions">
                  <Ellipsis aria-hidden="true" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="min-w-48">
                {canRestart ? (
                  <DropdownMenuItem
                    disabled={restartDisabled}
                    onSelect={() => restartInstallation.mutate(installation.id)}
                  >
                    <RotateCw aria-hidden="true" />
                    Restart
                  </DropdownMenuItem>
                ) : null}
                {installation.available_version ? (
                  <DropdownMenuItem
                    disabled={applyUpdate.isPending}
                    onSelect={() => applyUpdate.mutate(installation.id)}
                  >
                    <Download aria-hidden="true" />
                    Update to {installation.available_version}
                  </DropdownMenuItem>
                ) : null}
                {adminRoutes.map((route) => (
                  <DropdownMenuItem
                    key={route.id}
                    onSelect={() =>
                      void navigateToPluginRoute(pluginRouteHref(installation.id, route.path))
                    }
                  >
                    <ExternalLink aria-hidden="true" />
                    {route.navigation_label || route.path}
                  </DropdownMenuItem>
                ))}
                {hasMenuActions ? <DropdownMenuSeparator /> : null}
                <DropdownMenuItem variant="destructive" onSelect={() => setConfirmUninstall(true)}>
                  <Trash2 aria-hidden="true" />
                  Uninstall...
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </>
        }
      />

      {runtimeProblem ? (
        <Banner
          tone="warning"
          icon={
            <AlertTriangle aria-hidden="true" className="text-warning mt-0.5 size-4 shrink-0" />
          }
        >
          <p>
            <strong className="font-semibold">
              {runtime.state === "failed"
                ? `${name} stopped and won't restart on its own.`
                : `${name} keeps stopping.`}
            </strong>{" "}
            Silo has restarted it {runtime.restart_count}{" "}
            {runtime.restart_count === 1 ? "time" : "times"}
            {runtime.state === "backoff" && runtime.next_restart_at
              ? `. The next attempt is at ${formatDateTime(runtime.next_restart_at)}.`
              : "."}
          </p>
          {runtime.last_error ? (
            <pre className="bg-background/60 rounded-lg border px-3 py-2 font-mono text-xs [overflow-wrap:anywhere] whitespace-pre-wrap">
              {runtime.last_error}
            </pre>
          ) : null}
          <div>{restartButton}</div>
        </Banner>
      ) : null}

      {installation.enabled && missing.length > 0 ? (
        <Banner
          tone="warning"
          icon={
            <AlertTriangle aria-hidden="true" className="text-warning mt-0.5 size-4 shrink-0" />
          }
        >
          <p>
            <strong className="font-semibold">Finish setting up {name}.</strong> Add{" "}
            {missing.map((entry, index) => (
              <span key={entry.key}>
                {index > 0 ? (index === missing.length - 1 ? " and " : ", ") : null}
                <a href={`#${configPanelId(entry.key)}`} className="underline underline-offset-4">
                  {entry.title?.trim() || entry.key}
                </a>
              </span>
            ))}
            . Until then it can't do its job.
          </p>
        </Banner>
      ) : null}

      <TierNotice sourceKind={installation.source_kind} />

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_300px] xl:items-start">
        <div className="flex min-w-0 flex-col gap-3.5">
          <PluginSettingsPanels
            installation={installation}
            missingKeys={new Set(missing.map((entry) => entry.key))}
          />
        </div>
        <PluginDetailRail
          pluginID={installation.plugin_id}
          presentation={presentation}
          installation={installation}
          catalogEntry={catalogEntry}
        />
      </div>

      <AlertDialog open={confirmUninstall} onOpenChange={setConfirmUninstall}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Uninstall {name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Silo will stop the plugin, then remove its installation, configuration, and installed
              files. This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                deleteInstallation.mutate(installation.id, {
                  onSuccess: () => navigate("/admin/plugins", { replace: true }),
                })
              }
              disabled={deleteInstallation.isPending}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleteInstallation.isPending ? "Uninstalling..." : "Uninstall plugin"}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function CatalogPluginPreview({ entry }: { entry: PluginCatalogEntry }) {
  const installPlugin = useInstallPlugin();
  const name = pluginDisplayName(entry.plugin_id, entry.presentation);
  const publisher = entry.presentation?.publisher_name?.trim();
  const jobs = capabilityListLabel(entry.capabilities ?? []);

  return (
    <div className="page-shell space-y-5 py-4 sm:py-6">
      <BackLink fallback="/admin/plugins?tab=catalog" />
      <PageHeader
        name={name}
        meta={
          <>
            {publisher ? <span>by {publisher}</span> : null}
            <span>Version {entry.version}</span>
            {jobs ? <span>{jobs}</span> : null}
            <span>{sourceLabel(entry.source_kind)}</span>
            <PluginStatusLabel label="Not installed" dotClass="bg-muted-foreground/60" />
          </>
        }
        actions={
          <Button
            disabled={installPlugin.isPending}
            onClick={() =>
              installPlugin.mutate({
                repository_id: entry.repository_id,
                plugin_id: entry.plugin_id,
                version: entry.version,
              })
            }
          >
            <Download aria-hidden="true" />
            {installPlugin.isPending ? "Installing..." : "Install"}
          </Button>
        }
      />
      <TierNotice sourceKind={entry.source_kind} />
      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_300px] xl:items-start">
        <div className="flex min-w-0 flex-col gap-3.5">
          <PluginDeclaredSettings schemas={entry.global_config_schema ?? []} />
        </div>
        <PluginDetailRail
          pluginID={entry.plugin_id}
          presentation={entry.presentation}
          catalogEntry={entry}
        />
      </div>
    </div>
  );
}
