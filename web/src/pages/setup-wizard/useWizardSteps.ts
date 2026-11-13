import type { MessageKey } from "@/i18n";

import { useWizardContext } from "./WizardContext";
import { SKIPPABLE_STEPS, type SkippableStep } from "./setupStorage";

export type WizardStepId = "account" | SkippableStep | "done";

export interface StepDef {
  id: WizardStepId;
  /** Translated where it renders. */
  label: MessageKey;
  complete: boolean;
  active: boolean;
  /** A completed skippable step the admin can jump back to. */
  reachable: boolean;
}

// Wizard order. Everything after the account is optional and skippable; the
// admin can revisit any finished step from the rail until they leave the
// wizard on the final screen.
export const WIZARD_STEP_ORDER: readonly WizardStepId[] = ["account", ...SKIPPABLE_STEPS, "done"];

export const WIZARD_STEP_LABELS: Record<WizardStepId, MessageKey> = {
  account: "web.setup.rail.account",
  server: "web.setup.rail.server",
  playback: "web.setup.rail.playback",
  storage: "web.setup.rail.storage",
  library: "web.setup.rail.library",
  subtitles: "web.setup.rail.subtitles",
  connect: "web.setup.rail.connect",
  features: "web.setup.rail.features",
  done: "web.setup.rail.done",
};

export function useWizardSteps() {
  const { user, profile, settingsReady, stepDone, visiting } = useWizardContext();

  // The account counts as done once a user and profile exist and the next
  // step has its data, so the swap from the account form is a single frame.
  const complete: Record<WizardStepId, boolean> = {
    account: !!user && !!profile && settingsReady,
    ...stepDone,
    done: false,
  };

  // The first unfinished step in order is where a fresh visit lands. A
  // completed step can be reopened from the rail without unmarking it.
  const nextStep = WIZARD_STEP_ORDER.find((id) => !complete[id]) ?? "done";
  const currentStep: WizardStepId = visiting && complete[visiting] ? visiting : nextStep;

  const steps: StepDef[] = WIZARD_STEP_ORDER.map((id) => ({
    id,
    label: WIZARD_STEP_LABELS[id],
    complete: complete[id],
    active: currentStep === id,
    reachable: complete[id] && id !== "account",
  }));

  return { steps, currentStep };
}
