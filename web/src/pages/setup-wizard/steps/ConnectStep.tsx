import { useMemo } from "react";

import { useSettingsForm } from "@/hooks/useSettingsForm";
import { useT } from "@/i18n";
import { SettingField } from "@/pages/admin-settings/SettingField";

import { StepFrame, StepSection, StepSkeleton } from "../StepFrame";
import { useStepSubmit, useStepSummary } from "../useStep";

const JELLYFIN_KEYS = ["jellyfin_compat.enabled", "jellyfin_compat.public_url"];

export function ConnectStep() {
  const t = useT();
  const form = useSettingsForm({ keys: useMemo(() => JELLYFIN_KEYS, []) });
  const { handleSubmit, busy, skip } = useStepSubmit(
    "connect",
    form,
    t("web.setup.wizard.saveFailed"),
  );

  // The snapshot always carries the effective value, so the default (on) is
  // never read as blank.
  const jellyfinEnabled = form.getValue("jellyfin_compat.enabled") === "true";
  useStepSummary(
    "connect",
    jellyfinEnabled
      ? t("web.setup.connect.summaryJellyfin")
      : t("web.setup.connect.summarySiloOnly"),
  );

  if (form.isPending) return <StepSkeleton rows={2} />;

  return (
    <StepFrame
      title={t("web.setup.connect.title")}
      lede={t("web.setup.connect.lede")}
      onSubmit={handleSubmit}
      busy={busy}
      onSkip={skip}
      footnote={t("web.setup.connect.footnote")}
    >
      <StepSection
        title={t("web.setup.connect.sectionTitle")}
        caption={t("web.setup.connect.sectionCaption")}
      >
        <SettingField
          label={t("web.setup.connect.enableLabel")}
          type="toggle"
          description={t("web.setup.connect.enableDescription")}
          value={jellyfinEnabled ? "true" : "false"}
          onChange={(v) => form.setValue("jellyfin_compat.enabled", v)}
        />
        {jellyfinEnabled ? (
          <SettingField
            label={t("web.setup.connect.addressLabel")}
            description={t("web.setup.connect.addressDescription")}
            hint="http://your-server:8096"
            value={form.getValue("jellyfin_compat.public_url")}
            onChange={(v) => form.setValue("jellyfin_compat.public_url", v)}
          />
        ) : null}
      </StepSection>
    </StepFrame>
  );
}
