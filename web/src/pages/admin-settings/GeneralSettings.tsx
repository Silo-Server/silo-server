import { useMemo } from "react";
import { Link } from "react-router";
import { ArrowRight } from "lucide-react";

import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useRestartKeys } from "@/hooks/useRestartKeys";
import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { SettingsPageHeader } from "@/components/settings/SettingsPageHeader";
import { Skeleton } from "@/components/ui/skeleton";
import { SettingField } from "./SettingField";
import { SaveBar } from "./SaveBar";
import { FieldGroup } from "./FieldGroup";

// Identity (server name, login subtitle) used to live on the Branding tab and
// public signups on the Invite Codes tab; both are plain server-wide switches an
// admin looks for under General, so they save with everything else on this page.
const IDENTITY_KEYS = ["branding.server_name", "branding.login_subtitle"];
const ACCESS_KEYS = ["signup.enabled"];
const LOGGING_ADVANCED_KEYS = ["server.log_quiet"];
const LOGGING_KEYS = ["server.log_level", ...LOGGING_ADVANCED_KEYS];

const KEYS = [...IDENTITY_KEYS, ...ACCESS_KEYS, ...LOGGING_KEYS];

export default function GeneralSettings() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const restartKeys = useRestartKeys();

  const allRestart = (keys: string[]) => keys.every((key) => restartKeys.has(key));
  const anyDirty = (keys: string[]) => keys.some((key) => form.isDirty(key));

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
      <SettingsPageHeader title="General" className="mb-7" />

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
          label="Access"
          restartAll={allRestart(ACCESS_KEYS)}
          dirty={anyDirty(ACCESS_KEYS)}
          actions={
            <Link
              to="/admin/users?tab=invite-codes"
              className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-xs font-medium transition-colors"
            >
              Manage invite codes
              <ArrowRight className="h-3 w-3" aria-hidden="true" />
            </Link>
          }
        >
          <SettingField
            label="Public signups"
            settingKey="signup.enabled"
            dirty={form.isDirty("signup.enabled")}
            type="toggle"
            description="Anyone with a valid invite code can create an account."
            value={form.getValue("signup.enabled")}
            onChange={(v) => form.setValue("signup.enabled", v)}
            restartRequired={restartKeys.has("signup.enabled")}
          />
        </FieldGroup>

        <FieldGroup
          label="Logging"
          restartAll={allRestart(LOGGING_KEYS)}
          dirty={anyDirty(LOGGING_KEYS)}
        >
          <SettingField
            label="Log level"
            settingKey="server.log_level"
            dirty={form.isDirty("server.log_level")}
            type="select"
            description="Debug is loud; use it while chasing a problem."
            value={form.getValue("server.log_level")}
            onChange={(v) => form.setValue("server.log_level", v)}
            restartRequired={restartKeys.has("server.log_level")}
            options={[
              { value: "debug", label: "Debug" },
              { value: "info", label: "Info" },
              { value: "warn", label: "Warn" },
              { value: "error", label: "Error" },
            ]}
          />
          <AdvancedSection
            id="general.logging"
            count={LOGGING_ADVANCED_KEYS.length}
            forceOpen={form.isDirty("server.log_quiet")}
          >
            <SettingField
              label="Quiet log prefixes"
              settingKey="server.log_quiet"
              dirty={form.isDirty("server.log_quiet")}
              hint="metadata, scanner"
              description="Drops log lines starting with any of these words."
              value={form.getValue("server.log_quiet")}
              onChange={(v) => form.setValue("server.log_quiet", v)}
              restartRequired={restartKeys.has("server.log_quiet")}
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
