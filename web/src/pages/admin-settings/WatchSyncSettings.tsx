import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { toast } from "sonner";

import type { PluginInstallation } from "@/api/types";
import {
  ProviderTile,
  ProviderTileGrid,
  providerMonogram,
} from "@/components/settings/ProviderTile";
import { RestartBadge } from "@/components/settings/RestartBadge";
import { SecretField } from "@/components/settings/SecretField";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
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
import { RestartServerButton } from "@/components/admin/RestartServerButton";
import { installationConfigReady } from "@/lib/pluginConfigReady";
import { useReportUnsavedChanges } from "@/hooks/useUnsavedChanges";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { useUpdateServerSettings } from "@/hooks/queries/admin/settings";
import { useRestartKeys, type RestartKeyMatcher } from "@/hooks/useRestartKeys";
import { useSettingsForm } from "@/hooks/useSettingsForm";

import { FieldGroup } from "./FieldGroup";

/**
 * App credentials only. A viewer's own Trakt or Simkl account is linked from
 * their profile settings, never here.
 */
const KEYS = [
  "watchsync.trakt.client_id",
  "watchsync.trakt.client_secret",
  "watchsync.simkl.client_id",
  "watchsync.simkl.client_secret",
];

interface WatchProvider {
  key: string;
  title: string;
  monogram: string;
  monogramClass: string;
}

const PROVIDERS: WatchProvider[] = [
  {
    key: "trakt",
    title: "Trakt",
    monogram: "TR",
    monogramClass: "bg-red-500/20 text-red-700 dark:text-red-300",
  },
  {
    key: "simkl",
    title: "Simkl",
    monogram: "SK",
    monogramClass: "bg-amber-500/20 text-amber-700 dark:text-amber-300",
  },
];

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
 * One entry per watch-sync capability across the installed plugins. Trakt and
 * Simkl are built in; every other watch provider arrives as a plugin, so this
 * is what keeps the page honest about what the server can actually sync with.
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

function credentialKeys(providerKey: string) {
  return [
    { key: `watchsync.${providerKey}.client_id`, label: "Client ID" },
    { key: `watchsync.${providerKey}.client_secret`, label: "Client secret" },
  ];
}

function WatchProviderTile({
  provider,
  sensitiveConfigured,
  restartKeys,
  expanded,
  onExpand,
  onCollapse,
}: {
  provider: WatchProvider;
  sensitiveConfigured: string[];
  restartKeys: RestartKeyMatcher;
  expanded: boolean;
  onExpand: () => void;
  onCollapse: () => void;
}) {
  const updateSettings = useUpdateServerSettings();
  const [drafts, setDrafts] = useState<Record<string, string>>({});
  const [needsRestart, setNeedsRestart] = useState(false);
  const [confirmClear, setConfirmClear] = useState(false);
  // Tile drafts live outside useSettingsForm, so the navigation guard and the
  // reload prompt only see them if the tile reports them itself.
  useReportUnsavedChanges(Object.values(drafts).some((value) => value !== ""));

  const { title } = provider;
  const fields = credentialKeys(provider.key);
  const configuredKeys = new Set(sensitiveConfigured);
  const anyConfigured = fields.some((field) => configuredKeys.has(field.key));
  const allConfigured = fields.every((field) => configuredKeys.has(field.key));
  const draftOf = (key: string) => drafts[key] ?? "";
  const restartRequired = fields.some((field) => restartKeys.has(field.key));

  async function save() {
    const updates: Record<string, string> = {};
    for (const field of fields) {
      if (draftOf(field.key).trim() !== "") updates[field.key] = draftOf(field.key);
    }
    if (Object.keys(updates).length === 0) {
      toast.info(`Nothing to save for ${title}.`);
      return;
    }
    try {
      const result = await updateSettings.mutateAsync(updates);
      setDrafts({});
      setNeedsRestart((current) => current || result.restart_required);
      toast.success(`${title} credentials saved`);
    } catch {
      // The mutation surfaces the API error.
    }
  }

  async function clearAll() {
    try {
      const result = await updateSettings.mutateAsync(
        Object.fromEntries(fields.map((field) => [field.key, ""])),
      );
      setDrafts({});
      setNeedsRestart((current) => current || result.restart_required);
      toast.success(`${title} credentials cleared`);
    } catch {
      // The mutation surfaces the API error.
    }
  }

  return (
    <ProviderTile
      name={title}
      tagline="Watch history sync"
      monogram={provider.monogram}
      monogramClass={provider.monogramClass}
      state={expanded ? "editing" : allConfigured ? "connected" : "not_connected"}
      statePill={!expanded && !allConfigured && anyConfigured ? "Partly set up" : undefined}
      badge={restartRequired ? <RestartBadge /> : undefined}
      busy={updateSettings.isPending}
      expanded={expanded}
      primaryAction={{
        label: anyConfigured ? "Manage" : "Connect",
        onClick: onExpand,
      }}
    >
      <p className="text-muted-foreground mb-1 text-xs">
        Viewers link their own {title} account from profile settings.
      </p>
      {fields.map((field) => (
        <SecretField
          key={field.key}
          label={field.label}
          value={draftOf(field.key)}
          configured={configuredKeys.has(field.key)}
          onChange={(next) => setDrafts((prev) => ({ ...prev, [field.key]: next }))}
          restartRequired={restartKeys.has(field.key)}
        />
      ))}
      {/*
        Every action carries a border at rest. A `ghost` button reads as plain
        text until it is hovered, which hid Clear credentials and Close from
        admins who never hovered them.
      */}
      <div className="mt-3.5 flex flex-wrap items-center gap-2">
        <Button
          type="button"
          size="sm"
          onClick={() => void save()}
          disabled={updateSettings.isPending}
        >
          {updateSettings.isPending ? "Saving..." : "Save"}
        </Button>
        {anyConfigured ? (
          <Button type="button" size="sm" variant="outline" onClick={() => setConfirmClear(true)}>
            Clear credentials
          </Button>
        ) : null}
        <Button type="button" size="sm" variant="outline" onClick={onCollapse}>
          Close
        </Button>
      </div>
      {needsRestart && (
        <div className="border-warning/30 bg-warning/10 text-warning mt-3 flex flex-wrap items-center justify-between gap-3 rounded-xl border px-3 py-2 text-xs">
          <span>Restart the server to pick up the new {title} credentials.</span>
          <RestartServerButton />
        </div>
      )}
      <AlertDialog open={confirmClear} onOpenChange={setConfirmClear}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Clear {title} credentials?</AlertDialogTitle>
            <AlertDialogDescription>
              Viewers can no longer connect a {title} account, and existing connections stop
              syncing.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
              onClick={() => {
                void clearAll();
                setConfirmClear(false);
              }}
            >
              Clear
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </ProviderTile>
  );
}

export default function WatchSyncSettings() {
  const form = useSettingsForm({ keys: KEYS });
  const restartKeys = useRestartKeys();
  const navigate = useNavigate();
  const [expandedTile, setExpandedTile] = useState<string | null>(null);
  // Plugin providers render as they arrive rather than blocking the page:
  // the built-in tiles are useful on their own.
  const { data: installations } = useAdminPluginInstallations();
  const pluginProviders = pluginWatchProviders(installations ?? []);

  if (form.isLoading) {
    return (
      <div className="max-w-5xl space-y-6" role="status" aria-label="Loading watch sync">
        <Skeleton className="h-9 w-64" />
        <Skeleton className="h-12 w-full" />
        <Skeleton className="h-40 w-full" />
        <span className="sr-only">Loading watch sync</span>
      </div>
    );
  }

  return (
    <div className="flex h-full max-w-5xl flex-col gap-7">
      <SettingsPageHeader title="Watch Providers" />

      <FieldGroup label="Watch providers">
        <div className="py-3.5">
          <ProviderTileGrid>
            {PROVIDERS.map((provider) => (
              <WatchProviderTile
                key={provider.key}
                provider={provider}
                sensitiveConfigured={form.sensitiveConfigured}
                restartKeys={restartKeys}
                expanded={expandedTile === provider.key}
                onExpand={() => setExpandedTile(provider.key)}
                onCollapse={() => setExpandedTile(null)}
              />
            ))}
            {pluginProviders.map((provider) => (
              <ProviderTile
                key={`plugin-${provider.installationId}-${provider.capabilityId}`}
                name={provider.name}
                tagline="Watch provider plugin"
                monogram={providerMonogram(provider.name)}
                monogramClass="bg-violet-500/20 text-violet-700 dark:text-violet-300"
                state={provider.enabled && provider.configReady ? "connected" : "not_connected"}
                statePill={
                  !provider.enabled ? "Disabled" : provider.configReady ? "Enabled" : "Needs setup"
                }
                primaryAction={{
                  label: "Configure",
                  onClick: () =>
                    navigate(
                      `/admin/plugins?installed_q=${encodeURIComponent(provider.pluginId)}&configure=${encodeURIComponent(provider.pluginId)}`,
                    ),
                }}
              />
            ))}
          </ProviderTileGrid>
          <p className="text-muted-foreground mt-3 text-xs">
            Watch providers are pluggable. More install from the{" "}
            <Link
              to="/admin/plugins?tab=catalog"
              className="hover:text-foreground font-medium underline underline-offset-2 transition-colors"
            >
              plugin catalog
            </Link>
            ; a plugin provider's credentials live on its plugin page.
          </p>
        </div>
      </FieldGroup>
    </div>
  );
}
