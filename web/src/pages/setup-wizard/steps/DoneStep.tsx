import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { ChevronRight } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useUpdateServerSettings } from "@/hooks/queries/admin/settings";
import { useT, type MessageKey } from "@/i18n";

import { librarySummary } from "../librarySummary";
import { WIZARD_STEP_LABELS } from "../useWizardSteps";
import { useWizardContext } from "../WizardContext";
import { SKIPPABLE_STEPS } from "../setupStorage";

const AFTER_SETUP_LINKS: { to: string; label: MessageKey }[] = [
  { to: "/admin/libraries", label: "web.setup.done.linkLibraries" },
  { to: "/admin/users", label: "web.setup.done.linkUsers" },
  { to: "/admin/nodes", label: "web.setup.done.linkNodes" },
  { to: "/admin/settings/general", label: "web.setup.done.linkSettings" },
];

export function DoneStep() {
  const navigate = useNavigate();
  const t = useT();
  const {
    clearProgress,
    profile,
    profiles,
    selectProfile,
    summaries,
    libraries,
    refreshSetupStatus,
  } = useWizardContext();
  const updateSettings = useUpdateServerSettings();
  const [finishing, setFinishing] = useState(false);

  async function completeSetup(destination: string) {
    setFinishing(true);
    // Record on the server that setup finished before throwing away the
    // local step markers: if the write fails the admin stays here with their
    // progress intact and can try again (the mutation shows the error).
    try {
      await updateSettings.mutateAsync({ "setup.completed": "true" });
    } catch {
      setFinishing(false);
      return;
    }
    const chosenProfile = profile ?? profiles[0] ?? null;
    if (chosenProfile && !profile) {
      selectProfile(chosenProfile);
    }
    clearProgress();
    navigate(destination);
    // Re-read the public status once the route has changed so the guard on
    // /setup sees the marker without racing this navigation.
    void refreshSetupStatus();
  }

  // Only what this visit actually recorded: summaries live in memory, so after
  // a reload the recap shrinks rather than guessing at what a step chose.
  const recap = SKIPPABLE_STEPS.flatMap((step) => {
    const value = step === "library" ? librarySummary(libraries) : summaries[step];
    return value ? [{ step, label: t(WIZARD_STEP_LABELS[step]), value }] : [];
  });

  return (
    <div className="setup-step">
      <header className="setup-step-header">
        <h1 className="setup-step-title">{t("web.setup.done.title")}</h1>
        <p className="setup-step-lede">
          {libraries.length > 0 ? t("web.setup.done.ledeLibraries") : t("web.setup.done.ledeEmpty")}
        </p>
      </header>

      {recap.length > 0 ? (
        <dl className="setup-recap">
          {recap.map((item) => (
            <div key={item.step} className="setup-recap-item">
              <dt className="setup-recap-label">{item.label}</dt>
              <dd className="setup-recap-value" title={item.value}>
                {item.value}
              </dd>
            </div>
          ))}
        </dl>
      ) : null}

      <div className="setup-links">
        {AFTER_SETUP_LINKS.map((link) => (
          <Link
            key={link.to}
            to={link.to}
            onClick={(e) => {
              e.preventDefault();
              void completeSetup(link.to);
            }}
          >
            {t(link.label)}
          </Link>
        ))}
      </div>

      <div className="setup-actions">
        <Button onClick={() => void completeSetup("/")} disabled={finishing} className="min-w-40">
          {finishing ? t("web.setup.done.starting") : t("web.setup.done.start")}
          <ChevronRight className="ms-1 size-4" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={() => void completeSetup("/admin/libraries")}
          disabled={finishing}
        >
          {t("web.setup.done.goToAdmin")}
        </Button>
      </div>
    </div>
  );
}
