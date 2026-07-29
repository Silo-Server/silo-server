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
  const trustedProxiesManaged = form.sensitiveManagedByEnv.includes("clientip.trusted_proxies");

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
            label="Quiet Log Prefixes"
            hint="Comma-separated message prefixes to silence, such as metadata or scanner. A trailing colon is optional."
            value={form.getValue("server.log_quiet")}
            onChange={(v) => form.setValue("server.log_quiet", v)}
          />
        </FieldGroup>

        <FieldGroup label="Network">
          <SettingField
            label="Trusted Proxies"
            hint={
              (trustedProxiesManaged
                ? "Managed by SILO_TRUSTED_PROXIES. Remove that environment variable to edit here. "
                : "") +
              "Comma-separated CIDRs of reverse proxies whose X-Forwarded-For is trusted, e.g. " +
              "172.16.0.0/12, 203.0.113.7/32. Applies without a restart."
            }
            value={form.getValue("clientip.trusted_proxies")}
            onChange={(v) => form.setValue("clientip.trusted_proxies", v)}
            disabled={trustedProxiesManaged}
          />
          <div className="border-border/60 bg-muted/30 my-3 rounded-lg border px-3 py-2">
            <p className="text-sm font-medium">Choosing trusted proxy ranges</p>
            <ul className="text-muted-foreground mt-1 list-disc space-y-1 pl-4 text-xs leading-relaxed">
              <li>
                Setting this replaces the defaults (private ranges 10.0.0.0/8, 172.16.0.0/12,
                192.168.0.0/16 and loopback). Leave it empty to keep them.
              </li>
              <li>
                Recommended: keep the defaults, and only add your proxy&apos;s public address as a
                /32 (e.g. 203.0.113.7/32) if it reaches Silo from outside those ranges.
              </li>
              <li>
                CDNs such as Cloudflare connect from many published IP ranges — you must list all of
                their CIDRs and keep the list up to date as they change.
              </li>
              <li>
                Avoid 0.0.0.0/0 (trust everything): any client could then spoof its IP with a forged
                X-Forwarded-For header, affecting rate limits and audit logs.
              </li>
            </ul>
          </div>
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
