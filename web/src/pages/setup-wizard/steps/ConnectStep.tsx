import { useMemo } from "react";

import { useSettingsForm } from "@/hooks/useSettingsForm";
import { SettingField } from "@/pages/admin-settings/SettingField";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSubmit, useStepSummary } from "../useStep";

const JELLYFIN_KEYS = ["jellyfin_compat.enabled", "jellyfin_compat.public_url"];

export function ConnectStep() {
  const form = useSettingsForm({ keys: useMemo(() => JELLYFIN_KEYS, []) });
  const { handleSubmit, busy, skip } = useStepSubmit("connect", form, "Failed to save");

  // The snapshot always carries the effective value, so the default (on) is
  // never read as blank.
  const jellyfinEnabled = form.getValue("jellyfin_compat.enabled") === "true";
  useStepSummary("connect", jellyfinEnabled ? "Jellyfin apps" : "Vio apps only");

  if (form.isPending) return <StepSkeleton rows={2} />;

  return (
    <StepFrame
      title="Connect apps"
      lede="Vio's own apps for iOS, Android, and TV work out of the box. It can also answer to apps built for Jellyfin."
      onSubmit={handleSubmit}
      busy={busy}
      onSkip={skip}
      footnote="The Jellyfin web UI and more are in Admin › Settings › Compatibility."
    >
      <StepSection
        title="Jellyfin-compatible apps"
        caption="Infuse, Findroid, VidHub, Swiftfin, and others sign in and play through Vio's compatibility API."
      >
        <SettingField
          label="Answer Jellyfin apps"
          type="toggle"
          description="Runs a Jellyfin-protocol listener alongside the native API."
          value={jellyfinEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("jellyfin_compat.enabled", v)}
          managedByEnv={form.envVar("jellyfin_compat.enabled")}
        />
        {jellyfinEnabled ? (
          <SettingField
            label="Address for Jellyfin apps"
            description="What those apps connect to. Usually your public URL with the Jellyfin port."
            hint="http://your-server:8096"
            value={form.getValue("jellyfin_compat.public_url")}
            onChange={(v) => form.setValue("jellyfin_compat.public_url", v)}
            managedByEnv={form.envVar("jellyfin_compat.public_url")}
          />
        ) : null}
      </StepSection>
    </StepFrame>
  );
}
