import { useState } from "react";
import { useAdminSensitiveStatus, useUpdateServerSetting } from "@/hooks/queries/admin/settings";

import { Button } from "@/components/ui/button";
import { CredentialStatus } from "./CredentialStatus";
import { SettingField } from "./SettingField";

interface WatchProviderCredentials {
  key: string;
  displayName: string;
}

const WATCH_PROVIDER_CREDENTIALS: WatchProviderCredentials[] = [
  { key: "trakt", displayName: "Trakt" },
  { key: "simkl", displayName: "Simkl" },
];

function WatchProviderCredentialCard({ provider }: { provider: WatchProviderCredentials }) {
  const { data: sensitive } = useAdminSensitiveStatus();
  const updateSetting = useUpdateServerSetting();
  const [clientId, setClientId] = useState("");
  const [clientSecret, setClientSecret] = useState("");
  const configured = new Set(sensitive?.configured ?? []);
  const clientIdKey = `watchsync.${provider.key}.client_id`;
  const clientSecretKey = `watchsync.${provider.key}.client_secret`;

  function save() {
    const updates = [];
    if (clientId.trim() !== "") {
      updates.push(updateSetting.mutateAsync({ key: clientIdKey, value: clientId }));
    }
    if (clientSecret.trim() !== "") {
      updates.push(
        updateSetting.mutateAsync({
          key: clientSecretKey,
          value: clientSecret,
        }),
      );
    }
    void Promise.all(updates).then(() => {
      setClientId("");
      setClientSecret("");
    });
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">{provider.displayName}</h3>
          <p className="text-muted-foreground text-xs">
            OAuth credentials for profile connections.
          </p>
        </div>
        <CredentialStatus
          configured={configured.has(clientIdKey) && configured.has(clientSecretKey)}
        />
      </div>
      <SettingField
        label="Client ID"
        type="password"
        value={clientId}
        onChange={setClientId}
        sensitiveConfigured={configured.has(clientIdKey)}
        hint="Leave blank to keep the current value."
      />
      <SettingField
        label="Client Secret"
        type="password"
        value={clientSecret}
        onChange={setClientSecret}
        sensitiveConfigured={configured.has(clientSecretKey)}
        hint="Leave blank to keep the current value."
      />
      <Button type="button" onClick={save} disabled={updateSetting.isPending}>
        {updateSetting.isPending ? "Saving..." : `Save ${provider.displayName} Credentials`}
      </Button>
    </div>
  );
}

export default function WatchProvidersSettings() {
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Watch Providers</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          OAuth credentials for watch history and scrobbling services. Users connect their own
          accounts from their profile settings once a provider is configured here.
        </p>
      </div>

      <div className="space-y-4">
        {WATCH_PROVIDER_CREDENTIALS.map((provider) => (
          <WatchProviderCredentialCard key={provider.key} provider={provider} />
        ))}
      </div>
    </div>
  );
}
