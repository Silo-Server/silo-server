import { useEffect, useMemo, useRef } from "react";

import { AdvancedSection } from "@/components/settings/AdvancedSection";
import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "@/pages/admin-settings/SettingField";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSubmit, useStepSummary } from "../useStep";

const KEYS = ["branding.server_name", "server.public_url", "clientip.trusted_proxies"];

/** The address the browser opened the wizard on, offered as the public URL default. */
function currentOrigin(): string {
  if (typeof window === "undefined") return "";
  return window.location.origin;
}

export function ServerStep() {
  const form = useSettingsForm({ keys: useMemo(() => KEYS, []) });
  const { handleSubmit, busy, skip } = useStepSubmit(
    "server",
    form,
    "Failed to save server settings",
  );
  const prefilled = useRef(false);

  const proxiesManaged = form.sensitiveManagedByEnv.includes("clientip.trusted_proxies");
  const serverName = form.getValue("branding.server_name");

  // Offer the URL the admin is looking at as the public URL. It is staged,
  // not saved, so an install behind a reverse proxy can still clear it.
  const { loaded, getPersistedValue, setValue } = form;
  useEffect(() => {
    if (prefilled.current || !loaded) return;
    prefilled.current = true;
    if (getPersistedValue("server.public_url") === "" && currentOrigin() !== "") {
      setValue("server.public_url", currentOrigin());
    }
  }, [loaded, getPersistedValue, setValue]);

  useStepSummary("server", serverName.trim() || undefined);

  if (form.isPending) return <StepSkeleton rows={3} />;

  return (
    <StepFrame
      title="Name your server"
      lede="What people see when they sign in, and the address their apps use to reach you."
      onSubmit={handleSubmit}
      busy={busy}
      onSkip={skip}
      footnote="Everything here is in Admin › Settings › General."
    >
      <StepSection>
        <SettingField
          label="Server name"
          description="Shown on the sign-in page and in the apps."
          hint="Silo"
          value={serverName}
          onChange={(v) => form.setValue("branding.server_name", v)}
        />
        <SettingField
          label="Public URL"
          description="The address people open in a browser. Also used for links in emails and notifications."
          hint="https://silo.example.com"
          value={form.getValue("server.public_url")}
          onChange={(v) => form.setValue("server.public_url", v)}
        />
        <AdvancedSection
          id="setup.server"
          title="Behind a reverse proxy?"
          count={1}
          forceOpen={form.isDirty("clientip.trusted_proxies")}
        >
          <SettingField
            label="Trusted proxies"
            description={
              proxiesManaged
                ? "Set by SILO_TRUSTED_PROXIES in the environment."
                : "Silo reads the real client address from these proxy ranges. Leave blank to trust private networks only."
            }
            hint="172.16.0.0/12, 203.0.113.7/32"
            value={form.getValue("clientip.trusted_proxies")}
            onChange={(v) => form.setValue("clientip.trusted_proxies", v)}
            disabled={proxiesManaged}
          />
        </AdvancedSection>
      </StepSection>
    </StepFrame>
  );
}
