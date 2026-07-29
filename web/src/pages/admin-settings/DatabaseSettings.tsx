import { useEffect, useMemo, useState } from "react";
import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { Badge } from "@/components/ui/badge";
import { useCheckAdminSettingsConnection } from "@/hooks/queries/admin/settings";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";

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
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);
  const redisUrl = form.getValue("redis.url");
  const redisManagedByEnv = form.sensitiveManagedByEnv.includes("redis.url");
  const redisConfigured = redisUrl.trim() !== "" || form.sensitiveConfigured.includes("redis.url");
  const [redisEnabledOverride, setRedisEnabledOverride] = useState<boolean | null>(null);
  const effectiveRedisEnabled = redisEnabledOverride ?? redisConfigured;

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
            options={[
              { value: "postgres", label: "PostgreSQL" },
              { value: "sqlite", label: "SQLite" },
            ]}
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
