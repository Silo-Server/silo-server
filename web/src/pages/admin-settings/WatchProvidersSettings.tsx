import { useState } from "react";
import { AlertTriangle } from "lucide-react";
import { toast } from "sonner";
import {
  useAdminSensitiveStatus,
  useUpdateServerSettings as useUpdateServerSetting,
} from "@/hooks/queries/admin/settings";

import { Button } from "@/components/ui/button";
import { CredentialStatus } from "./CredentialStatus";
import { RestartServerButton } from "./RestartServerButton";
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
  const [confirmClear, setConfirmClear] = useState(false);
  const [restartRequired, setRestartRequired] = useState(false);
  const configured = new Set(sensitive?.configured ?? []);
  const clientIdKey = `watchsync.${provider.key}.client_id`;
  const clientSecretKey = `watchsync.${provider.key}.client_secret`;

  async function save() {
    const updates: Record<string, string> = {};
    if (clientId.trim() !== "") {
      updates[clientIdKey] = clientId;
    }
    if (clientSecret.trim() !== "") {
      updates[clientSecretKey] = clientSecret;
    }
    if (Object.keys(updates).length === 0) {
      toast.info(`No ${provider.displayName} credentials changed.`);
      return;
    }
    try {
      const result = await updateSetting.mutateAsync(updates);
      setClientId("");
      setClientSecret("");
      setRestartRequired((current) => current || result.restart_required);
      toast.success(`${provider.displayName} credentials saved`);
    } catch {
      // The mutation reports the API error.
    }
  }

  async function clearCredentials() {
    try {
      const result = await updateSetting.mutateAsync({
        [clientIdKey]: "",
        [clientSecretKey]: "",
      });
      setClientId("");
      setClientSecret("");
      setConfirmClear(false);
      setRestartRequired((current) => current || result.restart_required);
      toast.success(`${provider.displayName} credentials cleared`);
    } catch {
      // The mutation reports the API error.
    }
  }

  return (
    <fieldset
      disabled={updateSetting.isPending}
      className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4"
    >
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
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" onClick={() => void save()} disabled={updateSetting.isPending}>
          {updateSetting.isPending ? "Saving..." : `Save ${provider.displayName} Credentials`}
        </Button>
        {(configured.has(clientIdKey) || configured.has(clientSecretKey)) && !confirmClear && (
          <Button type="button" variant="outline" onClick={() => setConfirmClear(true)}>
            Clear credentials
          </Button>
        )}
        {confirmClear && (
          <>
            <span className="text-muted-foreground text-xs">
              Disconnect this server credential?
            </span>
            <Button
              type="button"
              variant="destructive"
              onClick={() => void clearCredentials()}
              disabled={updateSetting.isPending}
            >
              Confirm clear
            </Button>
            <Button type="button" variant="ghost" onClick={() => setConfirmClear(false)}>
              Cancel
            </Button>
          </>
        )}
      </div>
      {restartRequired && (
        <div className="border-warning/30 bg-warning/10 text-warning mt-3 flex items-center justify-between gap-3 rounded-xl border px-3 py-2 text-xs">
          <span className="flex items-center gap-2">
            <AlertTriangle className="h-3.5 w-3.5" />
            Restart required for {provider.displayName} collection browsing to use this credential
            change.
          </span>
          <RestartServerButton />
        </div>
      )}
    </fieldset>
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
