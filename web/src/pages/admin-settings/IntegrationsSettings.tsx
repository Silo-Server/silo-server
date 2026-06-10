import { useState } from "react";
import { useAdminSensitiveStatus, useUpdateServerSetting } from "@/hooks/queries/admin/settings";

import { Button } from "@/components/ui/button";
import { CredentialStatus } from "./CredentialStatus";
import { SettingField } from "./SettingField";

function MDBListCredentialCard() {
  const { data: sensitive } = useAdminSensitiveStatus();
  const updateSetting = useUpdateServerSetting();
  const [apiKey, setApiKey] = useState("");
  const configured = new Set(sensitive?.configured ?? []).has("mdblist.api_key");

  function save() {
    if (apiKey.trim() === "") return;
    void updateSetting.mutateAsync({ key: "mdblist.api_key", value: apiKey }).then(() => {
      setApiKey("");
    });
  }

  return (
    <div className="border-border bg-surface max-w-2xl rounded-lg border px-5 py-4">
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
      <Button type="button" onClick={save} disabled={updateSetting.isPending}>
        {updateSetting.isPending ? "Saving..." : "Save MDBList API Key"}
      </Button>
    </div>
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
