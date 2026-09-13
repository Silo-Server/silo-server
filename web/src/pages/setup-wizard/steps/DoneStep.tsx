import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { ChevronRight } from "lucide-react";

import { Button } from "@/components/ui/button";
import { useUpdateServerSettings } from "@/hooks/queries/admin/settings";

import { librarySummary } from "../librarySummary";
import { WIZARD_STEP_LABELS } from "../useWizardSteps";
import { useWizardContext } from "../WizardContext";
import { SKIPPABLE_STEPS } from "../setupStorage";

const AFTER_SETUP_LINKS = [
  { to: "/admin/libraries", label: "Scan and manage libraries" },
  { to: "/admin/users", label: "Invite people" },
  { to: "/admin/nodes", label: "Add transcode or proxy nodes" },
  { to: "/admin/settings/general", label: "All settings" },
];

export function DoneStep() {
  const navigate = useNavigate();
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

  function completeSetup(destination: string) {
    setFinishing(true);
    const chosenProfile = profile ?? profiles[0] ?? null;
    if (chosenProfile && !profile) {
      selectProfile(chosenProfile);
    }
    clearProgress();
    navigate(destination);
    // Record on the server that setup finished so /setup will not reopen,
    // then re-read the public status so the route guard sees it. Leaving is
    // never blocked on this write: a failure only means the route stays open
    // until the admin comes back through it, and the mutation shows a toast.
    void updateSettings
      .mutateAsync({ "setup.completed": "true" })
      .then(() => refreshSetupStatus())
      .catch(() => {});
  }

  // Only what this visit actually recorded: summaries live in memory, so after
  // a reload the recap shrinks rather than guessing at what a step chose.
  const recap = SKIPPABLE_STEPS.flatMap((step) => {
    const value = step === "library" ? librarySummary(libraries) : summaries[step];
    return value ? [{ step, label: WIZARD_STEP_LABELS[step], value }] : [];
  });

  return (
    <div className="setup-step">
      <header className="setup-step-header">
        <h1 className="setup-step-title">Silo is ready</h1>
        <p className="setup-step-lede">
          {libraries.length > 0
            ? "Your libraries are scanning in the background. Start browsing, or head to the admin area to keep going."
            : "Add a library from the admin area when you're ready and Silo will start scanning."}
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
              completeSetup(link.to);
            }}
          >
            {link.label}
          </Link>
        ))}
      </div>

      <div className="setup-actions">
        <Button onClick={() => completeSetup("/")} disabled={finishing} className="min-w-40">
          {finishing ? "Starting…" : "Start using Silo"}
          <ChevronRight className="ml-1 size-4" />
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={() => completeSetup("/admin/libraries")}
          disabled={finishing}
        >
          Go to admin
        </Button>
      </div>
    </div>
  );
}
