import { useState } from "react";
import { toast } from "sonner";
import type { ConnectionCheckResponse } from "@/api/types";
import { ConnectionCheckAction } from "@/components/admin/ConnectionCheckAction";
import {
  useAdminSensitiveStatus,
  useCheckAdminSettingsConnection,
  useUpdateServerSettings,
} from "@/hooks/queries/admin/settings";

import { Button } from "@/components/ui/button";
import { CredentialStatus } from "./CredentialStatus";
import { SettingField } from "./SettingField";

function MDBListCredentialCard() {
  const { data: sensitive } = useAdminSensitiveStatus();
  const updateSettings = useUpdateServerSettings();
  const checkConnection = useCheckAdminSettingsConnection();
  const [apiKey, setApiKey] = useState("");
  const [confirmClear, setConfirmClear] = useState(false);
  const [connectionResult, setConnectionResult] = useState<ConnectionCheckResponse | null>(null);
  const configured = new Set(sensitive?.configured ?? []).has("mdblist.api_key");

  async function save() {
    if (apiKey.trim() === "") {
      toast.info("No MDBList API key change to save.");
      return;
    }
    try {
      await updateSettings.mutateAsync({ "mdblist.api_key": apiKey });
      setApiKey("");
      setConnectionResult(null);
      toast.success("MDBList API key saved");
    } catch {
      // The mutation reports the API error.
    }
  }

  async function clearKey() {
    try {
      await updateSettings.mutateAsync({ "mdblist.api_key": "" });
      setApiKey("");
      setConfirmClear(false);
      setConnectionResult(null);
      toast.success("MDBList API key cleared");
    } catch {
      // The mutation reports the API error.
    }
  }

  async function testKey() {
    try {
      setConnectionResult(
        await checkConnection.mutateAsync({
          kind: "mdblist",
          body: {
            values: { "mdblist.api_key": apiKey },
            dirty_keys: apiKey.trim() === "" ? [] : ["mdblist.api_key"],
          },
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
    <fieldset
      disabled={updateSettings.isPending || checkConnection.isPending}
      className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4"
    >
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h3 className="text-sm font-semibold">MDBList</h3>
          <p className="text-muted-foreground text-xs">
            Enables list search/browse when users add MDBList collections. Importing a list by URL
            works without a key — only discovery requires one. Get a free key at{" "}
            <a
              href="https://mdblist.com/preferences/#api"
              target="_blank"
              rel="noreferrer"
              className="underline"
            >
              mdblist.com/preferences
            </a>
            .
          </p>
        </div>
        <CredentialStatus configured={configured} />
      </div>
      <SettingField
        label="API Key"
        type="password"
        value={apiKey}
        onChange={setApiKey}
        sensitiveConfigured={configured}
        hint="Leave blank to keep the current value."
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button type="button" onClick={() => void save()} disabled={updateSettings.isPending}>
          {updateSettings.isPending ? "Saving..." : "Save MDBList API Key"}
        </Button>
        {configured && !confirmClear && (
          <Button type="button" variant="outline" onClick={() => setConfirmClear(true)}>
            Clear API key
          </Button>
        )}
        {confirmClear && (
          <>
            <span className="text-muted-foreground text-xs">Disable MDBList discovery?</span>
            <Button
              type="button"
              variant="destructive"
              onClick={() => void clearKey()}
              disabled={updateSettings.isPending}
            >
              Confirm clear
            </Button>
            <Button type="button" variant="ghost" onClick={() => setConfirmClear(false)}>
              Cancel
            </Button>
          </>
        )}
      </div>
      <ConnectionCheckAction
        onClick={() => void testKey()}
        result={connectionResult}
        isPending={checkConnection.isPending}
        disabled={updateSettings.isPending || (!configured && apiKey.trim() === "")}
      />
      <p className="text-muted-foreground text-xs">
        Test Connection uses the key entered above, or the saved key when the field is blank.
      </p>
    </fieldset>
  );
}

export default function IntegrationsSettings() {
  return (
    <div className="flex h-full flex-col">
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">Integrations</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          API keys for external services. Watch provider and subtitle credentials have their own
          pages in the sidebar.
        </p>
      </div>

      <MDBListCredentialCard />
    </div>
  );
}
