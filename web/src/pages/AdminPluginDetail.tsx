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
import { useT, type MessageKey, type Translate } from "@/i18n";
import { navigateToPluginRoute } from "@/lib/buildPluginHref";
import { formatDateTime } from "@/lib/datetime";
import { capabilityListLabel } from "@/lib/pluginCapabilities";
import { missingRequiredConfig } from "@/lib/pluginConfigReady";
import {
  configPanelId,
  installationTier,
  pendingUpdateVersion,
  pluginDisplayName,
  sourceLabel,
  tierNotice,
} from "@/lib/pluginPresentation";
import { pluginRouteHref } from "@/lib/pluginRouteHref";
import { pluginStatus } from "@/lib/pluginStatus";
import { cn } from "@/lib/utils";

const UPDATE_POLICY_LABELS: Record<string, MessageKey> = {
  auto: "web.admin.plugins.detail.policyAuto",
  notify: "web.admin.plugins.detail.policyNotify",
  off: "web.admin.plugins.detail.policyOff",
};

const bold = (chunks: ReactNode[]) => <strong className="font-semibold">{chunks}</strong>;

/** "a, b and c" from already rendered items, joined through the catalog. */
function joinList(t: Translate, items: ReactNode[]): ReactNode {
  return items.reduce((joined, item, index) =>
    t(
      index === items.length - 1
        ? "web.admin.plugins.detail.missingListLast"
        : "web.admin.plugins.detail.missingListNext",
      { items: joined, item },
    ),
  );
}

/**
 * `/admin/plugins/:pluginId`: one plugin's settings, status, and details, or a
 * preview of a catalog plugin that isn't installed yet. The route is keyed by
 * the manifest plugin ID, which deep links from other admin pages use too.
 */
export default function AdminPluginDetail() {
  const t = useT();
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
        title={t("web.admin.plugins.detail.installationsErrorTitle")}
        description={t("web.admin.plugins.detail.installationsErrorDescription")}
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
        title={t("web.admin.plugins.detail.catalogErrorTitle")}
        description={t("web.admin.plugins.detail.catalogErrorDescription")}
        onRetry={() => void catalogQuery.refetch()}
        retrying={catalogQuery.isFetching}
      >
        <AllPluginsLink />
      </PageUnavailable>
    );
  }

  return (
    <PageUnavailable
      title={t("web.admin.plugins.detail.notFoundTitle")}
      description={t("web.admin.plugins.detail.notFoundDescription")}
    >
      <AllPluginsLink />
    </PageUnavailable>
  );
}

function PageLoading() {
  const t = useT();
  return (
    <div className="text-muted-foreground pb-16 text-center text-sm">
      {t("web.admin.plugins.detail.loading")}
    </div>
  );
}

function AllPluginsLink() {
  const t = useT();
  return (
    <Button asChild variant="outline">
      <ViewTransitionLink to="/admin/plugins" up>
        {t("web.admin.plugins.detail.allPlugins")}
      </ViewTransitionLink>
    </Button>
  );
}

function BackLink({ fallback }: { fallback: string }) {
  const t = useT();
  const location = useLocation();
  const from = (location.state as PluginPageLinkState | null)?.from;
  const to = from && from.startsWith("?") ? `/admin/plugins${from}` : fallback;
  return (
    <ViewTransitionLink
      to={to}
      up
      className="text-muted-foreground hover:text-foreground -ms-1 inline-flex items-center gap-1 text-sm"
    >
      <ChevronLeft aria-hidden="true" className="size-4" />
      {t("web.admin.plugins.detail.back")}
    </ViewTransitionLink>
  );
}

function PluginPageHeader({
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
        <h1 className="text-[1.75rem] leading-[1.15] font-bold tracking-[-0.035em] text-balance sm:text-[2rem]">
          {name}
        </h1>
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

function TierNotice({ tier }: { tier: string }) {
  const t = useT();
  const notice = tierNotice(tier);
  if (!notice) return null;
  return (
    <Banner
      tone="info"
      icon={<Info aria-hidden="true" className="text-muted-foreground mt-0.5 size-4 shrink-0" />}
    >
      <p>
        {t("web.admin.plugins.detail.tierNotice", { source: sourceLabel(tier), notice, b: bold })}
      </p>
    </Banner>
  );
}

/** Stands in for the Updates menu when no update policy can apply. */
function UpdatesState({ children }: { children: ReactNode }) {
  return (
    <span className="text-muted-foreground inline-flex h-8 items-center text-[13px]">
      {children}
    </span>
  );
}

function InstalledPluginPage({
  installation,
  catalogEntry,
}: {
  installation: PluginInstallation;
  catalogEntry?: PluginCatalogEntry;
}) {
  const t = useT();
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
  const tier = installationTier(installation);
  const updateVersion = pendingUpdateVersion(installation);
  // Updates pause when the plugin's repository is disabled; the approved
  // community catalog is disabled by hiding it.
  const pausedKey: MessageKey =
    tier === "approved_community"
      ? "web.admin.plugins.detail.pausedCommunity"
      : installation.repository_name
        ? "web.admin.plugins.detail.pausedRepository"
        : "web.admin.plugins.detail.pausedRepositoryUnnamed";
  const hasMenuActions = canRestart || Boolean(updateVersion) || adminRoutes.length > 0;
  const policy = installation.update_policy || "auto";
  const policyOptions = UPDATE_POLICY_LABELS[policy]
    ? Object.keys(UPDATE_POLICY_LABELS)
    : [...Object.keys(UPDATE_POLICY_LABELS), policy];
  const publisher = presentation?.publisher_name?.trim();
  const jobs = capabilityListLabel(capabilities);
  const runtimeProblem =
    installation.enabled &&
    (runtime.state === "failed" || (runtime.resident && runtime.state === "backoff"));

  const restartButton = (
    <Button
      variant="outline"
      size="sm"
      disabled={restartDisabled}
      onClick={() => restartInstallation.mutate(installation.id)}
    >
      <RotateCw aria-hidden="true" />
      {restartInstallation.isPending
        ? t("web.admin.plugins.detail.restarting")
        : t("web.admin.plugins.detail.restartNow")}
    </Button>
  );

  return (
    <div className="space-y-5 pb-4 sm:pb-6">
      <BackLink fallback="/admin/plugins" />

      <PluginPageHeader
        name={name}
        meta={
          <>
            {publisher ? (
              <span>{t("web.admin.plugins.detail.byPublisher", { publisher })}</span>
            ) : null}
            <span>{t("web.admin.plugins.detail.version", { version: installation.version })}</span>
            {jobs ? <span>{jobs}</span> : null}
            <span>{sourceLabel(tier)}</span>
            <PluginStatusLabel {...status} />
          </>
        }
        actions={
          <>
            {/* Without a repository there is nothing to check; with the
                repository disabled or the approved community catalog hidden,
                checks are paused. Either way the stored policy does nothing,
                so it is not offered. */}
            {tier === "unverified" ? (
              <UpdatesState>{t("web.admin.plugins.detail.updatedByUpload")}</UpdatesState>
            ) : installation.updates_paused ? (
              <UpdatesState>{t("web.admin.plugins.detail.updatesPaused")}</UpdatesState>
            ) : (
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
                <SelectTrigger
                  size="sm"
                  aria-label={t("web.admin.plugins.detail.updatesLabel")}
                  className="h-8 text-[13px]"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {policyOptions.map((value) => (
                    <SelectItem key={value} value={value}>
                      {UPDATE_POLICY_LABELS[value] ? t(UPDATE_POLICY_LABELS[value]) : value}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            )}
            <label className="text-foreground/85 flex items-center gap-2 text-[13.5px]">
              <Switch
                checked={installation.enabled}
                aria-label={t("web.admin.plugins.detail.enabledLabel")}
                disabled={updateInstallation.isPending}
                onCheckedChange={(checked) =>
                  updateInstallation.mutate({ id: installation.id, body: { enabled: checked } })
                }
              />
              {installation.enabled
                ? t("web.admin.plugins.detail.on")
                : t("web.admin.plugins.detail.off")}
            </label>
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="outline"
                  size="icon-sm"
                  aria-label={t("web.admin.plugins.detail.moreActions")}
                >
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
                    {t("web.admin.plugins.detail.restart")}
                  </DropdownMenuItem>
                ) : null}
                {updateVersion ? (
                  <DropdownMenuItem
                    disabled={applyUpdate.isPending}
                    onSelect={() => applyUpdate.mutate(installation.id)}
                  >
                    <Download aria-hidden="true" />
                    {t("web.admin.plugins.detail.updateTo", { version: updateVersion })}
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
                  {t("web.admin.plugins.detail.uninstallMenu")}
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
          {runtime.resident ? (
            <p>
              {runtime.state === "failed"
                ? t("web.admin.plugins.detail.runtimeFailed", {
                    name,
                    count: runtime.restart_count,
                    count_text: runtime.restart_count,
                    b: bold,
                  })
                : runtime.state === "backoff" && runtime.next_restart_at
                  ? t("web.admin.plugins.detail.runtimeBackoffNext", {
                      name,
                      count: runtime.restart_count,
                      count_text: runtime.restart_count,
                      time: formatDateTime(runtime.next_restart_at),
                      b: bold,
                    })
                  : t("web.admin.plugins.detail.runtimeBackoff", {
                      name,
                      count: runtime.restart_count,
                      count_text: runtime.restart_count,
                      b: bold,
                    })}
            </p>
          ) : (
            <p>{t("web.admin.plugins.detail.startFailed", { name, b: bold })}</p>
          )}
          {runtime.last_error ? (
            <pre className="bg-background/60 rounded-lg border px-3 py-2 font-mono text-xs [overflow-wrap:anywhere] whitespace-pre-wrap">
              {runtime.last_error}
            </pre>
          ) : null}
          {canRestart ? <div>{restartButton}</div> : null}
        </Banner>
      ) : null}

      {installation.update_failure ? (
        <Banner
          tone="warning"
          icon={
            <AlertTriangle aria-hidden="true" className="text-warning mt-0.5 size-4 shrink-0" />
          }
        >
          <p>
            {t("web.admin.plugins.detail.updateFailed", {
              name,
              b: bold,
              version: installation.update_failure.version,
              reason: installation.update_failure.reason,
            })}
          </p>
          <p className="text-muted-foreground">
            {t("web.admin.plugins.detail.updateFailedStays", {
              version: installation.version,
              date: formatDateTime(installation.update_failure.failed_at),
            })}
          </p>
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
            {t("web.admin.plugins.detail.missingConfig", {
              name,
              b: bold,
              fields: joinList(
                t,
                missing.map((entry) => (
                  <a
                    key={entry.key}
                    href={`#${configPanelId(entry.key)}`}
                    className="underline underline-offset-4"
                  >
                    {entry.title?.trim() || entry.key}
                  </a>
                )),
              ),
            })}
          </p>
        </Banner>
      ) : null}

      {installation.updates_paused ? (
        <Banner
          tone="info"
          icon={
            <Info aria-hidden="true" className="text-muted-foreground mt-0.5 size-4 shrink-0" />
          }
        >
          <p>
            {t(pausedKey, {
              name,
              repository: installation.repository_name ?? "",
              policy: UPDATE_POLICY_LABELS[policy] ? t(UPDATE_POLICY_LABELS[policy]) : "",
              has_policy: UPDATE_POLICY_LABELS[policy] ? "yes" : "no",
              b: bold,
              link: (chunks: ReactNode[]) => (
                <ViewTransitionLink
                  to="/admin/plugins?tab=catalog"
                  className="underline underline-offset-4"
                >
                  {chunks}
                </ViewTransitionLink>
              ),
            })}
          </p>
        </Banner>
      ) : null}

      <TierNotice tier={tier} />

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
            <AlertDialogTitle>
              {t("web.admin.plugins.detail.uninstallTitle", { name })}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("web.admin.plugins.detail.uninstallDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              onClick={() =>
                deleteInstallation.mutate(installation.id, {
                  onSuccess: () => navigate("/admin/plugins", { replace: true }),
                })
              }
              disabled={deleteInstallation.isPending}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              {deleteInstallation.isPending
                ? t("web.admin.plugins.detail.uninstalling")
                : t("web.admin.plugins.detail.uninstallConfirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}

function CatalogPluginPreview({ entry }: { entry: PluginCatalogEntry }) {
  const t = useT();
  const installPlugin = useInstallPlugin();
  const name = pluginDisplayName(entry.plugin_id, entry.presentation);
  const publisher = entry.presentation?.publisher_name?.trim();
  const jobs = capabilityListLabel(entry.capabilities ?? []);

  return (
    <div className="space-y-5 pb-4 sm:pb-6">
      <BackLink fallback="/admin/plugins?tab=catalog" />
      <PluginPageHeader
        name={name}
        meta={
          <>
            {publisher ? (
              <span>{t("web.admin.plugins.detail.byPublisher", { publisher })}</span>
            ) : null}
            <span>{t("web.admin.plugins.detail.version", { version: entry.version })}</span>
            {jobs ? <span>{jobs}</span> : null}
            <span>{sourceLabel(entry.source_kind)}</span>
            <PluginStatusLabel
              label={t("web.admin.plugins.detail.notInstalled")}
              dotClass="bg-muted-foreground/60"
            />
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
            {installPlugin.isPending
              ? t("web.admin.plugins.detail.installing")
              : t("web.admin.plugins.detail.install")}
          </Button>
        }
      />
      <TierNotice tier={entry.source_kind} />
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
