import { useState } from "react";

import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { SecretField } from "@/components/settings/SecretField";
import { useCheckAdminSettingsConnection } from "@/hooks/queries/admin/settings";
import type { RestartKeyMatcher } from "@/hooks/useRestartKeys";
import type { useSettingsForm } from "@/hooks/useSettingsForm";

import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";
import { REDIS_KEYS, keepSavedRedisUrl, stageRedisUrl } from "./redisDraft";

/**
 * How the page stages the connection URL. The page freezes both while a save
 * is in flight, so a late keystroke cannot ride along with it; emptying the
 * input reverts to the saved URL (`keepSaved`).
 */
export interface RedisUrlEditors {
  keepSaved: (key: string) => void;
  setSecret: (key: string, value: string) => void;
  disabled: boolean;
}

export function RedisGroup({
  form,
  restartKeys,
  secrets,
}: {
  form: ReturnType<typeof useSettingsForm>;
  restartKeys: RestartKeyMatcher;
  secrets: RedisUrlEditors;
}) {
  const checkConnection = useCheckAdminSettingsConnection();
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);
  const redisUrl = form.getValue("redis.url");
  const managedByEnv = form.sensitiveManagedByEnv.includes("redis.url");
  const configured = form.sensitiveConfigured.includes("redis.url");
  const [enabledOverride, setEnabledOverride] = useState<boolean | null>(null);
  // Saving or discarding clears the toggle override so it follows the stored
  // URL again. Adjusting during render (rather than in an effect) keeps the
  // override alive while the admin is still editing.
  const [lastDirtyCount, setLastDirtyCount] = useState(form.dirtyCount);
  if (lastDirtyCount !== form.dirtyCount) {
    setLastDirtyCount(form.dirtyCount);
    if (form.dirtyCount === 0) setEnabledOverride(null);
  }
  const enabled = enabledOverride ?? (redisUrl.trim() !== "" || configured);
  // URL edits go through the page's editors, which drop a keystroke that
  // arrives while a save is in flight.
  const urlDraft = { ...form, setValue: secrets.setSecret, resetValue: secrets.keepSaved };

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

  return (
    <FieldGroup
      label="Redis"
      restartAll={REDIS_KEYS.every((key) => restartKeys.has(key))}
      dirty={REDIS_KEYS.some((key) => form.isDirty(key))}
    >
      <SettingField
        label="Use Redis"
        type="toggle"
        description={
          managedByEnv ? "Set by REDIS_URL" : "Needed when running more than one server."
        }
        value={enabled ? "true" : "false"}
        onChange={(value) => {
          // The switch stages or takes back the credential, so it holds still
          // while a save is in flight, like the URL field below.
          if (secrets.disabled) return;
          if (value === "true") {
            setEnabledOverride(true);
            // Takes back a staged switch-off, which cleared the number too.
            form.resetValue("redis.url");
            form.resetValue("redis.db");
            return;
          }
          setEnabledOverride(false);
          stageRedisUrl(form, "");
        }}
        disabled={managedByEnv || secrets.disabled}
        restartRequired={restartKeys.has("redis.url")}
      />
      {enabled && (
        <>
          {/*
            No `onClear` here: the Use Redis switch above already stages the
            empty URL, and one clear per surface is the rule.
          */}
          <SecretField
            label="Connection URL"
            value={redisUrl}
            configured={configured}
            onKeep={() => keepSavedRedisUrl(urlDraft)}
            onChange={(v) => stageRedisUrl(urlDraft, v)}
            hint={managedByEnv ? "Value supplied by REDIS_URL" : "redis://host:6379"}
            disabled={managedByEnv || secrets.disabled}
            restartRequired={restartKeys.has("redis.url")}
          />
          {/*
            The server reports the number in use here while none is saved: the
            one in the URL, or in REDIS_URL when the environment supplies it.
          */}
          <SettingField
            label="Database number"
            dirty={form.isDirty("redis.db")}
            type="number"
            description={
              managedByEnv
                ? "Set by REDIS_URL"
                : "Replaces the number in the URL. Installs sharing one Redis each need their own."
            }
            value={form.getValue("redis.db")}
            onChange={(v) => {
              if (!secrets.disabled) form.setValue("redis.db", v);
            }}
            disabled={managedByEnv || secrets.disabled}
            restartRequired={restartKeys.has("redis.db")}
          />
          {/*
            Env-managed URLs stay checkable: the field is read-only so nothing
            is dirty, and the server checks the effective value it merged from
            REDIS_URL. Only writes are refused for env-managed keys.
          */}
          <ConnectionCheckAction
            onClick={handleCheckConnection}
            result={connectionResult}
            isPending={checkConnection.isPending}
            disabled={form.isSaving}
          />
        </>
      )}
    </FieldGroup>
  );
}
