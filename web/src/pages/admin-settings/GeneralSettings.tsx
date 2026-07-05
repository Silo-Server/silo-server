import { useMemo } from "react";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";
import { Skeleton } from "@/components/ui/skeleton";

const KEYS = [
  "auth.access_token_expiry",
  "auth.refresh_token_expiry",
  "server.log_level",
  "server.log_quiet",
  "clientip.trusted_proxies",
];

export default function GeneralSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });

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
      <div className="mb-6 space-y-2">
        <h2 className="text-xl font-semibold tracking-tight">General</h2>
        <p className="text-muted-foreground text-sm leading-relaxed">
          Authentication, token lifetimes, networking, and server logging behavior.
        </p>
      </div>

      <div className="flex-1 space-y-6">
        <FieldGroup label="Authentication">
          <SettingField
            label="Access Token Expiry"
            type="duration"
            hint="e.g. 1h, 30m"
            value={form.getValue("auth.access_token_expiry")}
            onChange={(v) => form.setValue("auth.access_token_expiry", v)}
          />
          <SettingField
            label="Refresh Token Expiry"
            type="duration"
            hint="e.g. 30d, 720h"
            value={form.getValue("auth.refresh_token_expiry")}
            onChange={(v) => form.setValue("auth.refresh_token_expiry", v)}
          />
        </FieldGroup>

        <FieldGroup label="Logging">
          <SettingField
            label="Log Level"
            type="select"
            value={form.getValue("server.log_level")}
            onChange={(v) => form.setValue("server.log_level", v)}
            options={[
              { value: "debug", label: "Debug" },
              { value: "info", label: "Info" },
              { value: "warn", label: "Warn" },
              { value: "error", label: "Error" },
            ]}
          />
          <SettingField
            label="Quiet Subsystems"
            hint="Comma-separated subsystem prefixes to silence"
            value={form.getValue("server.log_quiet")}
            onChange={(v) => form.setValue("server.log_quiet", v)}
          />
        </FieldGroup>

        <FieldGroup label="Network">
          <SettingField
            label="Trusted Proxies"
            hint="Comma-separated CIDRs of reverse proxies whose X-Forwarded-For is trusted, e.g. 172.16.0.0/12, 203.0.113.7/32. Leave empty for the built-in private-network defaults. Applies without a restart."
            value={form.getValue("clientip.trusted_proxies")}
            onChange={(v) => form.setValue("clientip.trusted_proxies", v)}
          />
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
