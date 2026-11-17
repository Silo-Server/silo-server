import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { toast } from "sonner";

import type { SkippableStep } from "./setupStorage";
import { useWizardContext } from "./WizardContext";

interface SaveableForm {
  dirtyCount: number;
  isSaving: boolean;
  save: () => Promise<void>;
}

/**
 * The submit protocol every settings step shares: nothing dirty means the step
 * is simply done; otherwise save first and only then mark it done, so a failed
 * write keeps the admin on the step with their edits intact. `afterSave` runs
 * whether or not anything was dirty, and the step is done once it resolves true.
 */
export function useStepSubmit(
  step: SkippableStep,
  form: SaveableForm,
  errorMessage: string,
  // Resolving false keeps the step open without an error, as when it unmounted.
  afterSave?: () => Promise<boolean>,
) {
  const { markDone } = useWizardContext();
  const [submitting, setSubmitting] = useState(false);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    if (form.dirtyCount === 0 && !afterSave) {
      markDone(step);
      return;
    }
    setSubmitting(true);
    try {
      if (form.dirtyCount > 0) await form.save();
      if ((await afterSave?.()) === false) return;
      markDone(step);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : errorMessage);
    } finally {
      setSubmitting(false);
    }
  }

  return { handleSubmit, busy: submitting || form.isSaving, skip: () => markDone(step) };
}

/** Publishes a step's one-line summary to the rail whenever it changes. */
export function useStepSummary(step: SkippableStep, summary: string | undefined) {
  const { setSummary } = useWizardContext();
  useEffect(() => {
    setSummary(step, summary);
  }, [step, summary, setSummary]);
}
