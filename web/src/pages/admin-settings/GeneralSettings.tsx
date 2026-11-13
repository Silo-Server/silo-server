import { useMemo, useRef, useState } from "react";
import { AlertTriangle } from "lucide-react";

import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { useT } from "@/i18n";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Skeleton } from "@/components/ui/skeleton";
import { SettingField } from "./SettingField";
import { SaveBar } from "@/components/SaveBar";
import { FieldGroup } from "./FieldGroup";
import { RedisGroup, type RedisUrlEditors } from "./RedisGroup";
import { REDIS_KEYS } from "./redisDraft";

// Identity (server name, login subtitle) used to live on the Branding tab; it is
// a plain server-wide value an admin looks for under General, so it saves with
// everything else on this page. Public signups and password reset live on
// Sign-in, since both only work with Silo passwords, and the log level on
// Logging.
const IDENTITY_KEYS = ["branding.server_name", "branding.login_subtitle"];

// Trusted proxies sit beside the public URL, the way the setup wizard asks for
// them: both are what an install behind a reverse proxy needs to get right.
const TRUSTED_PROXIES_KEY = "clientip.trusted_proxies";
// The local address covers what LAN discovery cannot reach: a Docker bridge
// network, where the advert never leaves the container and names the wrong port.
const LOCAL_URL_KEY = "server.local_url";
const NETWORK_KEYS = [
  "server.public_url",
  "server.lan_discovery",
  LOCAL_URL_KEY,
  TRUSTED_PROXIES_KEY,
];

// Redis and the Postgres pool are server-wide plumbing, not storage, so they
// moved here from the Storage page. userdb.backend, userdb.pool_max_open and
// userdb.idle_timeout are Hidden on purpose: PostgreSQL is the only usable
// per-user store, and the two SQLite tuning fields could never show. They stay
// ordinary settings through the API.
const DATABASE_KEYS = ["database.max_connections"];

const KEYS = [...NETWORK_KEYS, ...IDENTITY_KEYS, ...DATABASE_KEYS, ...REDIS_KEYS];

// The placeholder is the name a server without one shows: the product name.
const DEFAULT_SERVER_NAME = "Silo";

export default function GeneralSettings() {
  const t = useT();
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();

  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  const trustedProxiesManaged = form.sensitiveManagedByEnv.includes(TRUSTED_PROXIES_KEY);
  const [saveInProgress, setSaveInProgress] = useState(false);
  const saveInProgressRef = useRef(false);

  // The Redis URL is a credential: its editors are frozen while a save is in
  // flight so a late keystroke cannot ride along with it.
  const redisUrlEditors: RedisUrlEditors = {
    keepSaved: (key) => {
      if (saveInProgressRef.current) return;
      form.resetValue(key);
    },
    setSecret: (key, value) => {
      if (saveInProgressRef.current) return;
      form.setValue(key, value);
    },
    disabled: form.isSaving || saveInProgress,
  };

  async function handleSave() {
    if (saveInProgressRef.current) return;
    saveInProgressRef.current = true;
    setSaveInProgress(true);
    try {
      // While credential status is unavailable the Redis group is replaced by
      // "Reload before editing", so its retained draft stays unsent.
      await form.save(
        form.sensitiveStatusError
          ? form.dirtyKeys.filter((key) => !REDIS_KEYS.includes(key))
          : undefined,
      );
    } finally {
      saveInProgressRef.current = false;
      setSaveInProgress(false);
    }
  }

  function handleDiscard() {
    if (saveInProgressRef.current) return;
    form.discard();
  }

  // Redis reads whether a URL is saved from the protected-credential status,
  // so the page waits for it; if it fails, only the Redis group is held.
  if (form.isLoading || (!form.sensitiveStatusReady && !form.sensitiveStatusError))
    return (
      <div
        className="space-y-6"
        role="status"
        aria-label={t("web.admin.settings.fields.loadingSettings")}
      >
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">{t("web.admin.settings.fields.loadingSettings")}</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title={t("web.admin.settings.general.title")} />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label={t("web.admin.settings.general.identity.group")}
          restartAll={allRestart(IDENTITY_KEYS)}
          dirty={anyDirty(IDENTITY_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.general.identity.serverName.label")}
            dirty={form.isDirty("branding.server_name")}
            hint={DEFAULT_SERVER_NAME}
            value={form.getValue("branding.server_name")}
            onChange={(v) => form.setValue("branding.server_name", v)}
            restartRequired={restartKeys.has("branding.server_name")}
          />
          <SettingField
            label={t("web.admin.settings.general.identity.loginSubtitle.label")}
            dirty={form.isDirty("branding.login_subtitle")}
            hint={t("web.admin.settings.general.identity.loginSubtitle.placeholder")}
            description={t("web.admin.settings.general.identity.loginSubtitle.description")}
            value={form.getValue("branding.login_subtitle")}
            onChange={(v) => form.setValue("branding.login_subtitle", v)}
            restartRequired={restartKeys.has("branding.login_subtitle")}
          />
        </FieldGroup>

        <FieldGroup
          label={t("web.admin.settings.general.network.group")}
          restartAll={allRestart(NETWORK_KEYS)}
          dirty={anyDirty(NETWORK_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.general.network.publicUrl.label")}
            dirty={form.isDirty("server.public_url")}
            description={t("web.admin.settings.general.network.publicUrl.description")}
            // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
            hint="https://silo.example.com"
            value={form.getValue("server.public_url")}
            onChange={(v) => form.setValue("server.public_url", v)}
            restartRequired={restartKeys.has("server.public_url")}
          />
          <SettingField
            label={t("web.admin.settings.general.network.lanDiscovery.label")}
            dirty={form.isDirty("server.lan_discovery")}
            type="toggle"
            description={t("web.admin.settings.general.network.lanDiscovery.description")}
            value={form.getValue("server.lan_discovery")}
            onChange={(v) => form.setValue("server.lan_discovery", v)}
            restartRequired={restartKeys.has("server.lan_discovery")}
          />
          <SettingField
            label={t("web.admin.settings.general.network.localUrl.label")}
            dirty={form.isDirty(LOCAL_URL_KEY)}
            description={t("web.admin.settings.general.network.localUrl.description")}
            // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
            hint="http://192.168.1.20:8080"
            value={form.getValue(LOCAL_URL_KEY)}
            onChange={(v) => form.setValue(LOCAL_URL_KEY, v)}
            restartRequired={restartKeys.has(LOCAL_URL_KEY)}
          />
          <SettingField
            label={t("web.admin.settings.general.network.trustedProxies.label")}
            dirty={form.isDirty(TRUSTED_PROXIES_KEY)}
            description={
              trustedProxiesManaged
                ? t("web.admin.settings.general.network.trustedProxies.managed")
                : t("web.admin.settings.general.network.trustedProxies.description")
            }
            // eslint-disable-next-line formatjs/no-literal-string-in-jsx -- an example value, not text
            hint="172.16.0.0/12, 203.0.113.7/32"
            value={form.getValue(TRUSTED_PROXIES_KEY)}
            onChange={(v) => form.setValue(TRUSTED_PROXIES_KEY, v)}
            disabled={trustedProxiesManaged}
            restartRequired={restartKeys.has(TRUSTED_PROXIES_KEY)}
          />
        </FieldGroup>

        <FieldGroup
          label={t("web.admin.settings.general.database.group")}
          restartAll={allRestart(DATABASE_KEYS)}
          dirty={anyDirty(DATABASE_KEYS)}
        >
          <SettingField
            label={t("web.admin.settings.general.database.maxConnections.label")}
            dirty={form.isDirty("database.max_connections")}
            type="number"
            description={t("web.admin.settings.general.database.maxConnections.description")}
            value={form.getValue("database.max_connections")}
            onChange={(v) => form.setValue("database.max_connections", v)}
            restartRequired={restartKeys.has("database.max_connections")}
          />
        </FieldGroup>

        {form.sensitiveStatusError ? (
          <div
            className="flex items-start gap-3 rounded-xl border border-red-500/20 bg-red-500/5 p-4"
            role="alert"
          >
            <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-red-500" />
            <div>
              <p className="text-sm font-medium">
                {t("web.admin.settings.redis.unavailable.title")}
              </p>
              <p className="text-muted-foreground mt-1 text-[13px]">
                {t("web.admin.settings.redis.unavailable.description")}
              </p>
            </div>
          </div>
        ) : (
          <RedisGroup form={form} restartKeys={restartKeys} secrets={redisUrlEditors} />
        )}
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={handleSave}
        onDiscard={handleDiscard}
        isSaving={form.isSaving || saveInProgress}
      />
    </div>
  );
}
