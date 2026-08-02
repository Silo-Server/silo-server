import { useEffect, useMemo, useState } from "react";
import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { usePurgeVirtualPlaybackItems } from "@/hooks/queries/admin/collections";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminPluginInstallations } from "@/hooks/queries/admin/plugins";
import { useCheckAdminSettingsConnection } from "@/hooks/queries/admin/settings";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";
import { USER_DATABASE_BACKEND_OPTIONS } from "./databaseSettingOptions";

const REDIS_KEYS = ["redis.url"];

const KEYS = [
  "database.max_connections",
  ...REDIS_KEYS,
  "userdb.backend",
  "userdb.pool_max_open",
  "userdb.idle_timeout",
];

export default function DatabaseSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const checkConnection = useCheckAdminSettingsConnection();
  const purgeVirtual = usePurgeVirtualPlaybackItems();
  const { data: librariesData } = useAdminLibraries();
  const { data: pluginInstallations } = useAdminPluginInstallations();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);
  const [purgeLibraryID, setPurgeLibraryID] = useState("all");
  const [purgeInstallationID, setPurgeInstallationID] = useState("all");
  const redisUrl = form.getValue("redis.url");
  const redisManagedByEnv = form.sensitiveManagedByEnv.includes("redis.url");
  const redisConfigured = redisUrl.trim() !== "" || form.sensitiveConfigured.includes("redis.url");
  const [redisEnabledOverride, setRedisEnabledOverride] = useState<boolean | null>(null);
  const effectiveRedisEnabled = redisEnabledOverride ?? redisConfigured;

  const libraryOptions = useMemo(() => {
    const opts = [{ value: "all", label: "All Libraries" }];
    if (librariesData) {
      for (const lib of librariesData) {
        opts.push({ value: String(lib.id), label: `${lib.name} (ID: ${lib.id})` });
      }
    }
    return opts;
  }, [librariesData]);

  const pluginOptions = useMemo(() => {
    const opts = [{ value: "all", label: "All Plugins" }];
    if (pluginInstallations) {
      for (const plugin of pluginInstallations) {
        opts.push({
          value: String(plugin.id),
          label: `${plugin.plugin_id} v${plugin.version} (ID: ${plugin.id})`,
        });
      }
    }
    return opts;
  }, [pluginInstallations]);

  useEffect(() => {
    if (form.dirtyCount === 0) {
      setRedisEnabledOverride(null);
    }
  }, [form.dirtyCount]);

  async function handleCheckConnection() {
    try {
      setConnectionResult(
        await checkConnection.mutateAsync({
          kind: "redis",
          body: form.buildConnectionCheckRequest(REDIS_KEYS),
        }),
      );
    } catch (error) {
      setConnectionResult({
        success: false,
        message: error instanceof Error ? error.message : "Connection check failed.",
      });
    }
  }

  if (form.isLoading) return <div>Loading...</div>;

  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Database</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Configure connection pooling, Redis, and user database replication behavior.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Main Database">
          <SettingField
            label="Max Connections"
            type="number"
            value={form.getValue("database.max_connections")}
            onChange={(v) => form.setValue("database.max_connections", v)}
          />
        </FieldGroup>

        <FieldGroup label="Redis">
          {redisManagedByEnv && (
            <div className="border-border/70 flex flex-col gap-2 border-b py-3">
              <div className="flex items-center gap-2">
                <Badge variant="outline">Managed by environment</Badge>
              </div>
              <p className="text-muted-foreground text-sm">
                Redis is configured by the <code>REDIS_URL</code> environment variable. Change your
                deployment configuration and restart the server to update or disable Redis.
              </p>
            </div>
          )}
          <SettingField
            label="Enable Redis"
            type="toggle"
            hint={
              redisManagedByEnv
                ? "This setting is controlled by REDIS_URL"
                : "Leave disabled to run without Redis"
            }
            value={effectiveRedisEnabled ? "true" : "false"}
            onChange={(value) => {
              if (value === "true") {
                setRedisEnabledOverride(true);
                form.resetValue("redis.url");
                return;
              }
              setRedisEnabledOverride(false);
              form.setValue("redis.url", "");
            }}
            disabled={redisManagedByEnv}
          />
          {effectiveRedisEnabled && (
            <>
              <SettingField
                label="Connection URL"
                type="password"
                hint={redisManagedByEnv ? "Value supplied by REDIS_URL" : "redis://host:6379"}
                value={redisUrl}
                onChange={(v) => form.setValue("redis.url", v)}
                sensitiveConfigured={form.sensitiveConfigured.includes("redis.url")}
                disabled={redisManagedByEnv}
              />
              <ConnectionCheckAction
                onClick={handleCheckConnection}
                result={connectionResult}
                isPending={checkConnection.isPending}
                disabled={form.isSaving || redisManagedByEnv}
              />
            </>
          )}
        </FieldGroup>

        <FieldGroup label="User Database">
          <SettingField
            label="User DB Backend"
            type="select"
            options={USER_DATABASE_BACKEND_OPTIONS}
            value={form.getValue("userdb.backend")}
            onChange={(v) => form.setValue("userdb.backend", v)}
          />
          {form.getValue("userdb.backend") === "sqlite" && (
            <>
              <SettingField
                label="Pool Max Open"
                type="number"
                value={form.getValue("userdb.pool_max_open")}
                onChange={(v) => form.setValue("userdb.pool_max_open", v)}
              />
              <SettingField
                label="Idle Timeout"
                type="duration"
                hint="How long an inactive per-user SQLite connection remains open, e.g. 12h"
                value={form.getValue("userdb.idle_timeout")}
                onChange={(v) => form.setValue("userdb.idle_timeout", v)}
              />
            </>
          )}
        </FieldGroup>
      </div>

      <FieldGroup label="Danger Zone">
        <div className="flex flex-col justify-between gap-4 py-3 sm:flex-row sm:items-center">
          <div className="space-y-1">
            <span className="text-sm font-medium">Purge Virtual Library</span>
            <p className="text-muted-foreground text-xs">
              Remove all zero-storage virtual files and their orphaned catalog items.
            </p>
          </div>
          <div className="flex shrink-0 items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={purgeVirtual.isPending}
              onClick={() => {
                const libraryId = Number.parseInt(purgeLibraryID, 10);
                const installationId = Number.parseInt(purgeInstallationID, 10);
                purgeVirtual.mutate({
                  dryRun: true,
                  libraryId: libraryId > 0 ? libraryId : undefined,
                  installationId: installationId > 0 ? installationId : undefined,
                });
              }}
            >
              {purgeVirtual.isPending && purgeVirtual.variables?.dryRun
                ? "Previewing..."
                : "Preview Purge"}
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              disabled={purgeVirtual.isPending}
              onClick={() => {
                const libraryId = Number.parseInt(purgeLibraryID, 10);
                const installationId = Number.parseInt(purgeInstallationID, 10);
                const scope =
                  libraryId > 0 || installationId > 0 ? "the selected scope" : "all virtual items";
                if (
                  window.confirm(
                    `Purge all zero-storage virtual library items for ${scope}? This cannot be undone.`,
                  )
                ) {
                  purgeVirtual.mutate({
                    dryRun: false,
                    libraryId: libraryId > 0 ? libraryId : undefined,
                    installationId: installationId > 0 ? installationId : undefined,
                  });
                }
              }}
            >
              {purgeVirtual.isPending && !purgeVirtual.variables?.dryRun
                ? "Purging..."
                : "Purge Virtual Items"}
            </Button>
          </div>
        </div>
        <div className="grid gap-3 pt-2 pb-1 md:grid-cols-2">
          <SettingField
            label="Library Scope"
            type="select"
            options={libraryOptions}
            value={purgeLibraryID}
            onChange={setPurgeLibraryID}
          />
          <SettingField
            label="Plugin Installation Scope"
            type="select"
            options={pluginOptions}
            value={purgeInstallationID}
            onChange={setPurgeInstallationID}
          />
        </div>
      </FieldGroup>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
        restartRequired={form.restartRequired}
      />
    </div>
  );
}
