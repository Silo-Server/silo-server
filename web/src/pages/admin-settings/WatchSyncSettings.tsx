import { Link, useNavigate } from "react-router";

import type { PluginInstallation } from "@/api/types";
import { ProviderTile, ProviderTileGrid } from "@/components/settings/ProviderTile";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { installationConfigReady } from "@/lib/pluginConfigReady";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";

import { FieldGroup } from "./FieldGroup";
import { providerMonogram } from "@/lib/monogram";
import { pluginPagePath } from "@/lib/pluginPresentation";

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
 * watch provider, Trakt, Simkl, and MDBList included, is a plugin, so this is
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

export default function WatchSyncSettings() {
  const navigate = useNavigate();
  const { data: installations, isLoading, isError, refetch } = useAdminPluginInstallations();
  const providers = pluginWatchProviders(installations ?? []);

  if (isLoading) {
    return (
      <div className="max-w-5xl space-y-6" role="status" aria-label="Loading watch sync">
        <Skeleton className="h-9 w-64" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-40 w-full" />
        <span className="sr-only">Loading watch sync</span>
      </div>
    );
  }

  const catalogLink = (
    <Link
      to="/admin/plugins?tab=catalog"
      className="hover:text-foreground font-medium underline underline-offset-2 transition-colors"
    >
      plugin catalog
    </Link>
  );

  return (
    <div className="flex h-full max-w-5xl flex-col gap-7">
      <SettingsPageHeader title="Watch Providers" />

      <FieldGroup label="Watch providers">
        <div className="py-3.5">
          {isError ? (
            <div role="alert" className="flex flex-wrap items-center gap-3 text-sm">
              <span>Couldn&apos;t load the installed watch provider plugins.</span>
              <Button type="button" size="sm" variant="outline" onClick={() => void refetch()}>
                Retry
              </Button>
            </div>
          ) : providers.length > 0 ? (
            <ProviderTileGrid>
              {providers.map((provider) => (
                <ProviderTile
                  key={`plugin-${provider.installationId}-${provider.capabilityId}`}
                  name={provider.name}
                  tagline="Watch provider plugin"
                  monogram={providerMonogram(provider.name)}
                  monogramClass="bg-violet-500/20 text-violet-700 dark:text-violet-300"
                  state={provider.enabled && provider.configReady ? "connected" : "not_connected"}
                  statePill={
                    !provider.enabled
                      ? "Disabled"
                      : provider.configReady
                        ? "Enabled"
                        : "Needs setup"
                  }
                  primaryAction={{
                    label: "Configure",
                    onClick: () => navigate(pluginPagePath(provider.pluginId)),
                  }}
                />
              ))}
            </ProviderTileGrid>
          ) : (
            <p className="text-sm">No watch provider plugins are installed.</p>
          )}
          <p className="text-muted-foreground mt-3 text-xs">
            Trakt, Simkl, MDBList, and other watch providers install from the {catalogLink}. A
            provider&apos;s app credentials live on its plugin page, and viewers link their own
            accounts from profile settings.
          </p>
        </div>
      </FieldGroup>
    </div>
  );
}
