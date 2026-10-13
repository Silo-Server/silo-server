import { useMemo } from "react";

import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Skeleton } from "@/components/ui/skeleton";
import { SettingField } from "./SettingField";
import { SaveBar } from "@/components/SaveBar";
import { FieldGroup } from "./FieldGroup";

// Identity (server name, login subtitle) used to live on the Branding tab; it is
// a plain server-wide value an admin looks for under General, so it saves with
// everything else on this page. Public signups and password reset live on
// Sign-in, since both only work with Silo passwords, and the log level on
// Logging.
const IDENTITY_KEYS = ["branding.server_name", "branding.login_subtitle"];

// Trusted proxies sit beside the public URL, the way the setup wizard asks for
// them: both are what an install behind a reverse proxy needs to get right.
const TRUSTED_PROXIES_KEY = "clientip.trusted_proxies";
const NETWORK_KEYS = ["server.public_url", "server.lan_discovery", TRUSTED_PROXIES_KEY];
const KEYS = [...NETWORK_KEYS, ...IDENTITY_KEYS];

export default function GeneralSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();

  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));
  const trustedProxiesManaged = form.sensitiveManagedByEnv.includes(TRUSTED_PROXIES_KEY);

  if (form.isLoading)
    return (
      <div className="space-y-6" role="status" aria-label="Loading settings">
        <Skeleton className="h-8 w-48" />
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <div className="space-y-4">
          <Skeleton className="h-10 w-full" />
          <Skeleton className="h-10 w-full" />
        </div>
        <span className="sr-only">Loading settings</span>
      </div>
    );

  return (
    <div className="flex h-full flex-col">
      <SettingsPageHeader title="General" />

      <div className="flex-1 space-y-5">
        <FieldGroup
          label="Identity"
          restartAll={allRestart(IDENTITY_KEYS)}
          dirty={anyDirty(IDENTITY_KEYS)}
        >
          <SettingField
            label="Server name"
            settingKey="branding.server_name"
            dirty={form.isDirty("branding.server_name")}
            hint="Silo"
            value={form.getValue("branding.server_name")}
            onChange={(v) => form.setValue("branding.server_name", v)}
            restartRequired={restartKeys.has("branding.server_name")}
          />
          <SettingField
            label="Login subtitle"
            settingKey="branding.login_subtitle"
            dirty={form.isDirty("branding.login_subtitle")}
            hint="Sign in with an existing account."
            description="Shown under the server name on the sign-in page."
            value={form.getValue("branding.login_subtitle")}
            onChange={(v) => form.setValue("branding.login_subtitle", v)}
            restartRequired={restartKeys.has("branding.login_subtitle")}
          />
        </FieldGroup>

        <FieldGroup
          label="Network"
          restartAll={allRestart(NETWORK_KEYS)}
          dirty={anyDirty(NETWORK_KEYS)}
        >
          <SettingField
            label="Silo public URL"
            settingKey="server.public_url"
            dirty={form.isDirty("server.public_url")}
            description="The HTTPS address users open. Used for websocket origins and links in email notifications."
            hint="https://silo.example.com"
            value={form.getValue("server.public_url")}
            onChange={(v) => form.setValue("server.public_url", v)}
            restartRequired={restartKeys.has("server.public_url")}
          />
          <SettingField
            label="Show on the local network"
            settingKey="server.lan_discovery"
            dirty={form.isDirty("server.lan_discovery")}
            type="toggle"
            description="Silo apps on the same network list this server when adding one, so nobody has to type its address. Needs a network that passes multicast (Docker host networking, not the default bridge)."
            value={form.getValue("server.lan_discovery")}
            onChange={(v) => form.setValue("server.lan_discovery", v)}
            restartRequired={restartKeys.has("server.lan_discovery")}
          />
          <AdvancedSection
            id="general.reverse-proxy"
            title="Behind a reverse proxy?"
            count={1}
            forceOpen={form.isDirty(TRUSTED_PROXIES_KEY)}
          >
            <SettingField
              label="Trusted proxies"
              settingKey={TRUSTED_PROXIES_KEY}
              dirty={form.isDirty(TRUSTED_PROXIES_KEY)}
              description={
                trustedProxiesManaged
                  ? "Managed by SILO_TRUSTED_PROXIES."
                  : "Comma-separated proxy ranges; empty keeps the private defaults."
              }
              hint="172.16.0.0/12, 203.0.113.7/32"
              value={form.getValue(TRUSTED_PROXIES_KEY)}
              onChange={(v) => form.setValue(TRUSTED_PROXIES_KEY, v)}
              disabled={trustedProxiesManaged}
              restartRequired={restartKeys.has(TRUSTED_PROXIES_KEY)}
            />
          </AdvancedSection>
        </FieldGroup>
      </div>

      <SaveBar
        dirtyCount={form.dirtyCount}
        onSave={form.save}
        onDiscard={form.discard}
        isSaving={form.isSaving}
      />
    </div>
  );
}
