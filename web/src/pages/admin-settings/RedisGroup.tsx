import { useState } from "react";

import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import { SecretField } from "@/components/settings/SecretField";
import { useCheckAdminSettingsConnection } from "@/hooks/queries/admin/settings";
import type { RestartKeyMatcher } from "@/hooks/useRestartKeys";
import type { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT } from "@/i18n";

import { FieldGroup } from "./FieldGroup";
import { SettingField } from "./SettingField";
import { REDIS_KEYS, keepSavedRedisUrl, stageRedisUrl } from "./redisDraft";

/**
 * How the page stages the connection URL. The page freezes both while a save
 * is in flight, so a late keystroke cannot ride along with it; emptying the
 * input reverts to the saved URL (`keepSaved`).
 */
const REDIS = "Redis";
const REDIS_URL_EXAMPLE = "redis://host:6379";

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
  const t = useT();
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
        message:
          error instanceof Error
            ? error.message
            : t("web.admin.settings.fields.connectionCheckFailed"),
      });
    }
  }

  return (
    <FieldGroup
      label={REDIS}
      restartAll={REDIS_KEYS.every((key) => restartKeys.has(key))}
      dirty={REDIS_KEYS.some((key) => form.isDirty(key))}
    >
      <SettingField
        label={t("web.admin.settings.redis.enabled.label")}
        type="toggle"
        description={
          managedByEnv
            ? t("web.admin.settings.redis.setByEnv")
            : t("web.admin.settings.redis.enabled.description")
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
            label={t("web.admin.settings.redis.url.label")}
            value={redisUrl}
            configured={configured}
            onKeep={() => keepSavedRedisUrl(urlDraft)}
            onChange={(v) => stageRedisUrl(urlDraft, v)}
            hint={managedByEnv ? t("web.admin.settings.redis.url.managed") : REDIS_URL_EXAMPLE}
            disabled={managedByEnv || secrets.disabled}
            restartRequired={restartKeys.has("redis.url")}
          />
          {/*
            The server reports the number in use here while none is saved: the
            one in the URL, or in REDIS_URL when the environment supplies it.
          */}
          <SettingField
            label={t("web.admin.settings.redis.db.label")}
            dirty={form.isDirty("redis.db")}
            type="number"
            description={
              managedByEnv
                ? t("web.admin.settings.redis.setByEnv")
                : t("web.admin.settings.redis.db.description")
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
