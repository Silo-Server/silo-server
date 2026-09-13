import { Navigate } from "react-router";

import { SiloBrand } from "@/components/SiloBrand";
import { useAuth } from "@/hooks/useAuth";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useHasUnsavedChanges } from "@/hooks/useUnsavedChanges";
import "@/styles/admin-settings.css";
import "@/styles/setup-wizard.css";

import { WizardProvider, useWizardContext } from "./setup-wizard/WizardContext";
import { useWizardSteps } from "./setup-wizard/useWizardSteps";
import type { WizardStepId } from "./setup-wizard/useWizardSteps";
import type { SkippableStep } from "./setup-wizard/setupStorage";
import { StepRail } from "./setup-wizard/StepRail";
import { AccountStep } from "./setup-wizard/steps/AccountStep";
import { ServerStep } from "./setup-wizard/steps/ServerStep";
import { PlaybackStep } from "./setup-wizard/steps/PlaybackStep";
import { StorageStep } from "./setup-wizard/steps/StorageStep";
import { LibraryStep } from "./setup-wizard/steps/LibraryStep";
import { SubtitlesStep } from "./setup-wizard/steps/SubtitlesStep";
import { ConnectStep } from "./setup-wizard/steps/ConnectStep";
import { FeaturesStep } from "./setup-wizard/steps/FeaturesStep";
import { DoneStep } from "./setup-wizard/steps/DoneStep";

function StepContent({ step }: { step: WizardStepId }) {
  switch (step) {
    case "account":
      return <AccountStep />;
    case "server":
      return <ServerStep />;
    case "playback":
      return <PlaybackStep />;
    case "storage":
      return <StorageStep />;
    case "library":
      return <LibraryStep />;
    case "subtitles":
      return <SubtitlesStep />;
    case "connect":
      return <ConnectStep />;
    case "features":
      return <FeaturesStep />;
    case "done":
      return <DoneStep />;
  }
}

function WizardContent() {
  const { summaries, visit } = useWizardContext();
  const { steps, currentStep } = useWizardSteps();
  // The setup route mounts no router guard, so unsaved edits on the current
  // step would be lost silently on a rail click. Ask first, the way the admin
  // pages do through their guard.
  const hasUnsavedChanges = useHasUnsavedChanges();
  function visitStep(id: SkippableStep) {
    if (
      hasUnsavedChanges &&
      !window.confirm("Leave this step? Your unsaved changes will be lost.")
    ) {
      return;
    }
    visit(id);
  }

  return (
    <div className="setup-shell">
      <div className="setup-frame">
        <aside className="setup-side">
          <div className="setup-brand">
            <SiloBrand variant="mark" className="size-9" />
            <span>
              <span className="setup-brand-name">Silo</span>
              <span className="setup-brand-sub">First-run setup</span>
            </span>
          </div>
          <StepRail steps={steps} summaries={summaries} onVisit={visitStep} />
        </aside>
        <main className="setup-panel">
          {/* Keyed on the step so the entrance runs once per step change. */}
          <div key={currentStep} className="setup-panel-enter">
            <StepContent step={currentStep} />
          </div>
        </main>
      </div>
    </div>
  );
}

export default function SetupWizard() {
  const { user, loading, setupLoading, setupRequired, setupCompleted } = useAuth();
  useDocumentTitle("Setup");

  if (loading || setupLoading) {
    return <div className="text-muted-foreground p-8 text-sm">Loading...</div>;
  }

  if (!setupRequired && !user) {
    return <Navigate to="/login" replace />;
  }

  if (user && user.role !== "admin") {
    return <Navigate to="/profiles" replace />;
  }

  // The wizard is a one-time entrance. Once the server has recorded it as
  // finished, the same settings live in the admin area and this route stays
  // closed even to an admin who types it in.
  if (setupCompleted) {
    return <Navigate to="/admin/settings/general" replace />;
  }

  return (
    <WizardProvider>
      <WizardContent />
    </WizardProvider>
  );
}
