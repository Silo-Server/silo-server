import type { ReactNode } from "react";
import { Link, useNavigate } from "react-router";

import type { PluginInstallation } from "@/api/types";
import { ProviderTile, ProviderTileGrid } from "@/components/settings/ProviderTile";
import { installationConfigReady } from "@/lib/pluginConfigReady";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { providerMonogram } from "@/lib/monogram";
import { pluginPagePath } from "@/lib/pluginPresentation";
import { useT } from "@/i18n";

/**
 * The capability type watch-provider plugins declare (see
 * `internal/pluginhost`'s `watch_sync_provider.v1`). Matched by family so a
 * future `.v2` still shows up here.
 */
const WATCH_SYNC_CAPABILITY = "watch_sync_provider";

interface PluginWatchProvider {
  installationId: number;
  capabilityId: string;
  pluginId: string;
  name: string;
  enabled: boolean;
  /** Whether the plugin's declared global config is actually filled in. */
  configReady: boolean;
}

/**
 * One entry per watch-sync capability across the installed plugins. Every
 * watch provider, Simkl and MDBList included, is a plugin, so this is
 * what the server can actually sync with.
 */
function pluginWatchProviders(installations: PluginInstallation[]): PluginWatchProvider[] {
  const providers: PluginWatchProvider[] = [];
  for (const installation of installations) {
    for (const capability of installation.capabilities ?? []) {
      const type = capability.type ?? "";
      if (type !== WATCH_SYNC_CAPABILITY && !type.startsWith(`${WATCH_SYNC_CAPABILITY}.`)) {
        continue;
      }
      providers.push({
        installationId: installation.id,
        capabilityId: capability.id,
        pluginId: installation.plugin_id,
        name: capability.display_name || installation.plugin_id,
        enabled: installation.enabled,
        // "Connected" must mean the plugin could serve a configured request,
        // not merely that the installation is switched on.
        configReady: installationConfigReady(installation),
      });
    }
  }
  return providers.sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * Watch providers are plugins with their own config pages, so these tiles
 * only report status and link out; nothing here is staged or saved.
 */
export function WatchProviderTiles() {
  const t = useT();
  const navigate = useNavigate();
  const { data: installations, isLoading, isError, refetch } = useAdminPluginInstallations();
  const providers = pluginWatchProviders(installations ?? []);

  if (isLoading) {
    return (
      <div role="status" aria-label={t("web.admin.settings.watchProviders.loading")}>
        <Skeleton className="h-24 w-full" />
        <span className="sr-only">{t("web.admin.settings.watchProviders.loading")}</span>
      </div>
    );
  }

  const catalogLink = (chunks: ReactNode) => (
    <Link
      to="/admin/plugins?tab=catalog"
      className="hover:text-foreground font-medium underline underline-offset-2 transition-colors"
    >
      {chunks}
    </Link>
  );

  return (
    <>
      {isError ? (
        <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
          <span>{t("web.admin.settings.watchProviders.loadError")}</span>
          <Button type="button" size="sm" variant="outline" onClick={() => void refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : providers.length > 0 ? (
        <ProviderTileGrid>
          {providers.map((provider) => (
            <ProviderTile
              key={`plugin-${provider.installationId}-${provider.capabilityId}`}
              name={provider.name}
              tagline={t("web.admin.settings.watchProviders.tagline")}
              monogram={providerMonogram(provider.name)}
              monogramClass="bg-violet-500/20 text-violet-700 dark:text-violet-300"
              state={provider.enabled && provider.configReady ? "connected" : "not_connected"}
              statePill={
                !provider.enabled
                  ? t("web.admin.settings.watchProviders.disabled")
                  : provider.configReady
                    ? t("web.admin.settings.watchProviders.enabled")
                    : t("web.admin.settings.watchProviders.needsSetup")
              }
              primaryAction={{
                label: t("web.admin.settings.watchProviders.configure"),
                onClick: () => navigate(pluginPagePath(provider.pluginId)),
              }}
            />
          ))}
        </ProviderTileGrid>
      ) : (
        <p className="text-sm">{t("web.admin.settings.watchProviders.empty")}</p>
      )}
      <p className="text-muted-foreground mt-3 text-xs">
        {t("web.admin.settings.watchProviders.footnote", { link: catalogLink })}
      </p>
    </>
  );
}
