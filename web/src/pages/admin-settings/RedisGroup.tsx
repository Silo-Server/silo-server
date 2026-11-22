import { useState } from "react";

import {
  ConnectionCheckAction,
  useConnectionCheck,
} from "@/components/admin/ConnectionCheckAction";
import { SecretField } from "@/components/settings/SecretField";
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
  setSecret: (key: string, value: string, options?: { stage?: boolean }) => void;
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
  // A result shows only beside the values it checked: a reload after a refused
  // save can undo the Redis edits without going through this group.
  const connectionCheck = useConnectionCheck("redis", form, REDIS_KEYS);
  const redisUrl = form.getValue("redis.url");
  const urlEnv = form.envVar("redis.url");
  const configured = form.sensitiveConfigured.includes("redis.url");
  const [enabledOverride, setEnabledOverride] = useState<boolean | null>(null);
  // A save, Discard, or a reload that undoes the drafts clears the toggle
  // override so it follows the stored URL again. Taking back an edit in place
  // doesn't: the admin is still editing. Adjusting during render (rather than
  // in an effect) keeps the override alive while the admin is still editing.
  const [lastResetRevision, setLastResetRevision] = useState(form.resetRevision);
  if (lastResetRevision !== form.resetRevision) {
    setLastResetRevision(form.resetRevision);
    setEnabledOverride(null);
  }
  // Once REDIS_URL sets the connection the switch is locked, so a choice
  // made before it took over no longer applies.
  if (urlEnv && enabledOverride !== null) setEnabledOverride(null);
  const enabled = enabledOverride ?? (redisUrl.trim() !== "" || configured);
  // URL edits go through the page's editors, which drop a keystroke that
  // arrives while a save is in flight.
  const urlDraft = { ...form, setValue: secrets.setSecret, resetValue: secrets.keepSaved };

  return (
    <FieldGroup
      label={REDIS}
      restartAll={REDIS_KEYS.every((key) => restartKeys.has(key))}
      dirty={REDIS_KEYS.some((key) => form.isDirty(key))}
    >
      <SettingField
        label={t("web.admin.settings.redis.enabled.label")}
        type="toggle"
        description={t("web.admin.settings.redis.enabled.description")}
        value={enabled ? "true" : "false"}
        onChange={(value) => {
          // The switch stages or takes back the credential, so it holds still
          // while a save is in flight, like the URL field below.
          if (secrets.disabled) return;
          if (value === "true") {
            // With a URL saved, taking back the switch-off leaves the switch
            // following the stored URL, so a URL removed elsewhere later shows.
            // With none saved, only the override keeps the empty fields open.
            setEnabledOverride(configured ? null : true);
            // Takes back a staged switch-off, which cleared the number too.
            form.resetValue("redis.url");
            form.resetValue("redis.db");
            return;
          }
          // With no saved URL there is nothing to switch off: drop what was
          // typed, since the fields that showed it are about to hide. The
          // switch then follows what is saved again, so a URL saved elsewhere
          // later still shows.
          if (!configured) {
            setEnabledOverride(null);
            form.resetValue("redis.url");
            form.resetValue("redis.db");
            return;
          }
          setEnabledOverride(false);
          stageRedisUrl(form, "");
        }}
        disabled={secrets.disabled}
        restartRequired={restartKeys.has("redis.url")}
        managedByEnv={urlEnv}
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
            hint={REDIS_URL_EXAMPLE}
            disabled={secrets.disabled}
            restartRequired={restartKeys.has("redis.url")}
            managedByEnv={urlEnv}
          />
          {/*
            The server reports the number in use here while none is saved: the
            one in the URL, or in REDIS_URL when the environment supplies it.
          */}
          <SettingField
            label={t("web.admin.settings.redis.db.label")}
            dirty={form.isDirty("redis.db")}
            type="number"
            description={t("web.admin.settings.redis.db.description")}
            value={form.getValue("redis.db")}
            onChange={(v) => {
              if (!secrets.disabled) form.setValue("redis.db", v);
            }}
            disabled={secrets.disabled}
            restartRequired={restartKeys.has("redis.db")}
            managedByEnv={form.envVar("redis.db")}
          />
          {/*
            Env-managed URLs stay checkable: the field is read-only so nothing
            is dirty, and the server checks the effective value it merged from
            REDIS_URL. Only writes are refused for env-managed keys.
          */}
          <ConnectionCheckAction
            onClick={() => void connectionCheck.run()}
            result={connectionCheck.result}
            isPending={connectionCheck.isPending}
            disabled={form.isSaving}
          />
        </>
      )}
    </FieldGroup>
  );
}
